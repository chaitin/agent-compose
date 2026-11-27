package runlog

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

const textFlushBytes = 4 << 10

// maxToolInputBytes caps one tool call's arguments. Tool output is trimmed
// from both ends, but input had no guard; a very large argument would grow a
// single essential line past the log's per-line bound and make it unreadable.
const maxToolInputBytes = 1 << 20

// maxEntryTextBytes bounds the text one prose or output entry carries. Longer
// text is split across consecutive entries rather than cut, so the text view
// and a stream's bytes stay complete; even fully escaped, one such entry stays
// far below the per-line bound.
const maxEntryTextBytes = 1 << 20

// Pending is an entry a Merger produced, before a Writer gives it a position.
// Essential entries survive the log cap; see Writer.
type Pending struct {
	Entry     Entry
	Essential bool
}

// Merger turns a run's raw inputs into entries without touching storage, so
// the same rules feed both the run log and anything that only needs the text
// view. It is not safe for concurrent use.
//
// Deltas are coalesced into paragraphs. A tool call is recorded once, when
// its input is known: claude announces a call before its arguments finish
// streaming and re-emits it afterwards, and codex resends its status. Its
// outcome is recorded once, from the first tool_result. Reasoning, steps,
// retries, compaction and todo lists are not recorded.
type Merger struct {
	toolOutputHalf int
	pendingText    strings.Builder
	tools          map[string]*toolState
	turn           uint32
}

type toolState struct {
	tool          Tool
	callWritten   bool
	resultWritten bool
}

// NewMerger returns a Merger keeping toolOutputHalf bytes from each end of a
// tool's output (DefaultToolOutputHalf when not positive).
func NewMerger(toolOutputHalf int) *Merger {
	if toolOutputHalf <= 0 {
		toolOutputHalf = DefaultToolOutputHalf
	}
	return &Merger{toolOutputHalf: toolOutputHalf, tools: map[string]*toolState{}}
}

// Recover seeds the merger from an entry already in the log, so a reopened
// log does not record a tool call or result twice.
func (m *Merger) Recover(entry Entry) {
	if entry.Turn != m.turn || entry.Type == EntryUser {
		m.tools = map[string]*toolState{}
		m.turn = entry.Turn
	}
	if entry.Tool == nil || entry.Tool.ID == "" {
		return
	}
	state := m.tool(entry.Tool.ID)
	switch entry.Type {
	case EntryToolCall:
		state.callWritten = true
		state.tool = writtenCall(*entry.Tool)
	case EntryToolResult:
		state.resultWritten = true
	}
}

// User records user input; the caller starts a new turn.
func (m *Merger) User(text string) []Pending {
	out := m.FlushText()
	m.tools = map[string]*toolState{}
	return append(out, Pending{Entry: Entry{Type: EntryUser, Text: text}, Essential: true})
}

// Output records process output, byte for byte.
func (m *Merger) Output(stream, data string) []Pending {
	if data == "" {
		return nil
	}
	out := m.FlushText()
	for _, part := range splitText(data, maxEntryTextBytes) {
		out = append(out, Pending{Entry: Entry{Type: EntryOutput, Stream: stream, Data: part}})
	}
	return out
}

// EndTurn closes the turn. Its final text is recorded on the turn_end entry
// only, never as prose: runners stream their answer as text deltas, and a
// structured answer as a tool call, so the log already holds it. Older guests
// printed it as process output. A call still waiting for its arguments never
// gets them once the turn is over, so it is forgotten.
func (m *Merger) EndTurn(stopReason, finalText string) []Pending {
	m.forgetUnannounced()
	return append(m.FlushText(), Pending{Entry: Entry{Type: EntryTurnEnd, StopReason: stopReason, FinalText: finalText}, Essential: true})
}

// Quiescent reports whether every input the merger took in has become an
// entry: no text waits for a newline and no tool call for its arguments. Only
// then can its state be rebuilt from the log alone (see Recover), which a
// commit requires.
func (m *Merger) Quiescent() bool {
	if m.pendingText.Len() > 0 {
		return false
	}
	for _, state := range m.tools {
		if state.awaitingArguments() {
			return false
		}
	}
	return true
}

// forgetUnannounced drops calls that will never get their arguments.
func (m *Merger) forgetUnannounced() {
	for id, state := range m.tools {
		if state.awaitingArguments() {
			delete(m.tools, id)
		}
	}
}

// awaitingArguments reports a call announced before its arguments, which has
// no entry yet.
func (s *toolState) awaitingArguments() bool {
	return !s.callWritten && !s.resultWritten
}

// FlushText emits text still held for coalescing.
func (m *Merger) FlushText() []Pending {
	if m.pendingText.Len() == 0 {
		return nil
	}
	text := m.pendingText.String()
	m.pendingText.Reset()
	return m.text(text)
}

// text records prose, split into entries of at most maxEntryTextBytes.
func (m *Merger) text(text string) []Pending {
	parts := splitText(text, maxEntryTextBytes)
	out := make([]Pending, 0, len(parts))
	for _, part := range parts {
		out = append(out, Pending{Entry: Entry{Type: EntryText, Text: part}})
	}
	return out
}

// splitText cuts s into pieces of at most n bytes (n > 0), at rune starts
// where the input is valid UTF-8, so no piece ends in half a character.
func splitText(s string, n int) []string {
	var parts []string
	for len(s) > n {
		head := runePrefix(s, n)
		if head == "" {
			// No rune start in reach: invalid input, cut at the byte bound.
			head = s[:n]
		}
		parts = append(parts, head)
		s = s[len(head):]
	}
	return append(parts, s)
}

// runePrefix returns the longest prefix of s of at most n bytes that does not
// end inside a UTF-8 sequence, backing off at most one sequence's length.
func runePrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	cut := n
	for back := 0; back < utf8.UTFMax-1 && cut > 0 && !utf8.RuneStart(s[cut]); back++ {
		cut--
	}
	if !utf8.RuneStart(s[cut]) {
		cut = n
	}
	return s[:cut]
}

// agentEvent mirrors the provider-neutral AgentEvent union of the runtime
// (runtime/javascript/src/agent-event.ts).
type agentEvent struct {
	Kind            string          `json:"kind"`
	Text            string          `json:"text"`
	ID              string          `json:"id"`
	ParentToolUseID string          `json:"parentToolUseId"`
	Name            string          `json:"name"`
	ToolKind        string          `json:"toolKind"`
	Status          string          `json:"status"`
	Input           json.RawMessage `json:"input"`
	Changes         []FileChange    `json:"changes"`
	Command         string          `json:"command"`
	ExitCode        *int            `json:"exitCode"`
	OK              *bool           `json:"ok"`
	Output          string          `json:"output"`
	Error           string          `json:"error"`

	Scope            string  `json:"scope"`
	Model            string  `json:"model"`
	InputTokens      int64   `json:"inputTokens"`
	OutputTokens     int64   `json:"outputTokens"`
	ReasoningTokens  int64   `json:"reasoningTokens"`
	CachedTokens     int64   `json:"cachedTokens"`
	CacheWriteTokens int64   `json:"cacheWriteTokens"`
	CostUSD          float64 `json:"costUsd"`

	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// Event merges one runtime agent event.
func (m *Merger) Event(raw []byte) ([]Pending, error) {
	var event agentEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return nil, fmt.Errorf("decode agent event: %w", err)
	}
	switch event.Kind {
	case "text_delta":
		return m.textDelta(event.Text), nil
	case "tool_call":
		return m.toolCall(event), nil
	case "tool_result":
		return m.toolResult(event), nil
	case "usage":
		return append(m.FlushText(), Pending{Entry: Entry{Type: EntryUsage, Usage: &Usage{
			Scope: event.Scope, Model: event.Model,
			InputTokens: event.InputTokens, OutputTokens: event.OutputTokens, ReasoningTokens: event.ReasoningTokens,
			CachedTokens: event.CachedTokens, CacheWriteTokens: event.CacheWriteTokens, CostUSD: event.CostUSD,
		}}, Essential: true}), nil
	case "error":
		return append(m.FlushText(), Pending{Entry: Entry{Type: EntryError, Text: event.Message, Severity: event.Severity, Code: event.Code}, Essential: true}), nil
	default:
		return nil, nil
	}
}

func (m *Merger) textDelta(text string) []Pending {
	if text == "" {
		return nil
	}
	m.pendingText.WriteString(text)
	pending := m.pendingText.String()
	if len(pending) >= textFlushBytes {
		return m.FlushText()
	}
	cut := strings.LastIndexByte(pending, '\n')
	if cut < 0 {
		return nil
	}
	m.pendingText.Reset()
	m.pendingText.WriteString(pending[cut+1:])
	return m.text(pending[:cut+1])
}

func (m *Merger) tool(id string) *toolState {
	state := m.tools[id]
	if state == nil {
		state = &toolState{tool: Tool{ID: id}}
		m.tools[id] = state
	}
	return state
}

func (m *Merger) toolCall(event agentEvent) []Pending {
	if event.ID == "" {
		return nil
	}
	state := m.tool(event.ID)
	tool := &state.tool
	if state.callWritten {
		// Only what the result entry repeats is still needed; the
		// arguments are already in the log.
		if event.ParentToolUseID != "" {
			tool.ParentID = event.ParentToolUseID
		}
		if event.ExitCode != nil {
			code := *event.ExitCode
			tool.ExitCode = &code
		}
		return nil
	}
	if event.Name != "" {
		tool.Name = event.Name
	}
	if event.ToolKind != "" {
		tool.Kind = event.ToolKind
	}
	if event.ParentToolUseID != "" {
		tool.ParentID = event.ParentToolUseID
	}
	if event.Command != "" {
		tool.Command = event.Command
	}
	if toolInputText(event.Input) != "" {
		tool.Input = clampToolInput(event.Input, maxToolInputBytes)
	}
	if len(event.Changes) > 0 {
		tool.Changes = append([]FileChange(nil), event.Changes...)
	}
	if event.ExitCode != nil {
		code := *event.ExitCode
		tool.ExitCode = &code
	}
	if tool.Command == "" && toolInputText(tool.Input) == "" && len(tool.Changes) == 0 && event.Status == "in_progress" {
		// Arguments are still streaming; wait for the completed re-emit or
		// the result, whichever comes first.
		return nil
	}
	return m.emitToolCall(state)
}

func (m *Merger) emitToolCall(state *toolState) []Pending {
	call := state.tool
	call.ExitCode = nil
	state.callWritten = true
	state.tool = writtenCall(state.tool)
	return append(m.FlushText(), Pending{Entry: Entry{Type: EntryToolCall, Tool: &call}, Essential: true})
}

// writtenCall keeps what a tool's result entry repeats from its call. Once
// the call is in the log its arguments, which may be large, are not needed;
// Recover rebuilds the same state from the call entry.
func writtenCall(tool Tool) Tool {
	return Tool{ID: tool.ID, ParentID: tool.ParentID, Name: tool.Name, ExitCode: tool.ExitCode}
}

// clampToolInput keeps a tool call's arguments within limit bytes while
// staying valid JSON, so the entry always serializes. An oversized input
// degrades to a string that keeps its head and a truncation marker.
func clampToolInput(input json.RawMessage, limit int) json.RawMessage {
	if len(input) <= limit {
		return input
	}
	head := runePrefix(string(input), limit)
	out, err := json.Marshal(head + "\n[… tool input truncated …]")
	if err != nil {
		return json.RawMessage(`"[tool input truncated]"`)
	}
	return out
}

func (m *Merger) toolResult(event agentEvent) []Pending {
	if event.ID == "" {
		return nil
	}
	state := m.tool(event.ID)
	if state.resultWritten {
		return nil
	}
	// claude reports a subagent's tool calls' parent on their results only.
	if event.ParentToolUseID != "" {
		state.tool.ParentID = event.ParentToolUseID
	}
	var out []Pending
	if !state.callWritten {
		out = m.emitToolCall(state)
	}
	out = append(out, m.FlushText()...)
	output, outputBytes := truncateMiddle(event.Output, m.toolOutputHalf)
	// A failing tool's error often carries its whole stderr; it is kept like
	// the output, from both ends, so the essential result line stays small.
	failure, _ := truncateMiddle(event.Error, m.toolOutputHalf)
	result := Tool{
		ID: state.tool.ID, ParentID: state.tool.ParentID, Name: state.tool.Name,
		OK: event.OK, ExitCode: state.tool.ExitCode,
		Output: output, OutputBytes: outputBytes, Error: failure,
	}
	if event.ExitCode != nil {
		code := *event.ExitCode
		result.ExitCode = &code
	}
	state.resultWritten = true
	return append(out, Pending{Entry: Entry{Type: EntryToolResult, Tool: &result}, Essential: true})
}

// truncateMiddle keeps half bytes from each end of s and reports the
// original length when it had to cut. Cuts land on rune boundaries.
func truncateMiddle(s string, half int) (string, int) {
	if half <= 0 || len(s) <= 2*half {
		return s, 0
	}
	head := s[:half]
	for len(head) > 0 && !validRuneEnd(head) {
		head = head[:len(head)-1]
	}
	tail := s[len(s)-half:]
	for len(tail) > 0 && (tail[0]&0xC0) == 0x80 {
		tail = tail[1:]
	}
	return head + fmt.Sprintf("\n[… %d bytes omitted …]\n", len(s)-len(head)-len(tail)) + tail, len(s)
}

func validRuneEnd(s string) bool {
	// Walk back over continuation bytes to the lead byte and check the
	// sequence it starts is complete.
	i := len(s) - 1
	for i > 0 && (s[i]&0xC0) == 0x80 {
		i--
	}
	lead := s[i]
	need := 1
	switch {
	case lead&0xE0 == 0xC0:
		need = 2
	case lead&0xF0 == 0xE0:
		need = 3
	case lead&0xF8 == 0xF0:
		need = 4
	}
	return len(s)-i >= need
}
