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
	turnProse      string
	// legacyOutput is the tail of the turn's process output, kept only while
	// the turn has no agent events; see rememberLegacyOutput.
	legacyOutput string
	// turnEvents distinguishes structured prose from older guests' mirrored
	// process output when reconciling a terminal frame's final text.
	turnEvents bool
	tools      map[string]*toolState
	turn       uint32
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
	if entry.Turn != m.turn {
		m.tools = map[string]*toolState{}
		m.legacyOutput = ""
		m.turn = entry.Turn
	}
	switch entry.Type {
	case EntryUser, EntryTurnEnd:
		m.turnProse, m.turnEvents = "", false
		m.legacyOutput = ""
		if entry.Type == EntryUser {
			m.tools = map[string]*toolState{}
		}
	case EntryText:
		m.turnProse += entry.Text
		m.sawTurnEvent()
	case EntryOutput:
		m.rememberLegacyOutput(entry.Data)
	case EntryToolCall, EntryToolResult, EntryUsage, EntryError:
		m.sawTurnEvent()
	}
	if entry.Tool == nil || entry.Tool.ID == "" {
		return
	}
	state := m.tool(entry.Tool.ID)
	switch entry.Type {
	case EntryToolCall:
		state.callWritten = true
		state.tool = *entry.Tool
	case EntryToolResult:
		state.resultWritten = true
	}
}

// User records user input; the caller starts a new turn.
func (m *Merger) User(text string) []Pending {
	out := m.FlushText()
	m.turnProse = ""
	m.turnEvents = false
	m.legacyOutput = ""
	m.tools = map[string]*toolState{}
	return append(out, Pending{Entry: Entry{Type: EntryUser, Text: text}, Essential: true})
}

// Output records process output, byte for byte.
func (m *Merger) Output(stream, data string) []Pending {
	if data == "" {
		return nil
	}
	m.rememberLegacyOutput(data)
	return append(m.FlushText(), Pending{Entry: Entry{Type: EntryOutput, Stream: stream, Data: data}})
}

// EndTurn closes the turn, recording final text that never reached the log as
// deltas. Older guests mirror answers as process output, which must not repeat.
func (m *Merger) EndTurn(stopReason, finalText string) []Pending {
	out := m.FlushText()
	if m.turnEvents || !strings.Contains(m.legacyOutput, finalText) {
		if rest := finalText[streamedFinalTextOverlap(m.turnProse, finalText):]; rest != "" {
			out = append(out, m.text(rest))
		}
	}
	m.turnProse = ""
	m.turnEvents = false
	m.legacyOutput = ""
	return append(out, Pending{Entry: Entry{Type: EntryTurnEnd, StopReason: stopReason, FinalText: finalText}, Essential: true})
}

// maxLegacyOutputBytes bounds the tail of a turn's process output the merger
// keeps to recognize a final answer an older guest already printed there.
// Such a guest prints its answer last, so the tail is what matters; an answer
// longer than this is recorded again rather than tracked without limit.
// The tail is part of every journal checkpoint, so it must stay small.
const maxLegacyOutputBytes = 64 << 10

// rememberLegacyOutput keeps the tail of the turn's process output. Once the
// turn has agent events its final text is never compared with the output, so
// nothing is kept.
func (m *Merger) rememberLegacyOutput(data string) {
	if m.turnEvents {
		return
	}
	m.legacyOutput = keepTail(m.legacyOutput+data, maxLegacyOutputBytes)
}

// sawTurnEvent marks the turn as carrying agent events and drops the output
// tail kept for older guests, which this turn no longer needs.
func (m *Merger) sawTurnEvent() {
	m.turnEvents = true
	m.legacyOutput = ""
}

func keepTail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// FlushText emits text still held for coalescing.
func (m *Merger) FlushText() []Pending {
	if m.pendingText.Len() == 0 {
		return nil
	}
	text := m.pendingText.String()
	m.pendingText.Reset()
	return []Pending{m.text(text)}
}

func (m *Merger) text(text string) Pending {
	m.turnProse += text
	return Pending{Entry: Entry{Type: EntryText, Text: text}}
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
	m.sawTurnEvent()
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
	return []Pending{m.text(pending[:cut+1])}
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
		tool.Input = clampToolInput(event.Input)
	}
	if len(event.Changes) > 0 {
		tool.Changes = append([]FileChange(nil), event.Changes...)
	}
	if event.ExitCode != nil {
		code := *event.ExitCode
		tool.ExitCode = &code
	}
	if state.callWritten {
		return nil
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
	return append(m.FlushText(), Pending{Entry: Entry{Type: EntryToolCall, Tool: &call}, Essential: true})
}

// clampToolInput keeps a tool call's arguments within maxToolInputBytes while
// staying valid JSON, so the entry always serializes. An oversized input
// degrades to a string that keeps its head and a truncation marker.
func clampToolInput(input json.RawMessage) json.RawMessage {
	if len(input) <= maxToolInputBytes {
		return input
	}
	head := string(input[:maxToolInputBytes])
	for len(head) > 0 && !utf8.ValidString(head) {
		head = head[:len(head)-1]
	}
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
	var out []Pending
	if !state.callWritten {
		out = m.emitToolCall(state)
	}
	out = append(out, m.FlushText()...)
	output, outputBytes := truncateMiddle(event.Output, m.toolOutputHalf)
	result := Tool{
		ID: state.tool.ID, ParentID: state.tool.ParentID, Name: state.tool.Name,
		OK: event.OK, ExitCode: state.tool.ExitCode,
		Output: output, OutputBytes: outputBytes, Error: event.Error,
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

// streamedFinalTextOverlap returns the length of the longest prefix of
// finalText that the prose logged so far ends with, so only the part of the
// final answer that was never streamed is appended.
func streamedFinalTextOverlap(logged, finalText string) int {
	if finalText == "" || logged == "" {
		return 0
	}
	if len(logged) > len(finalText) {
		logged = logged[len(logged)-len(finalText):]
	}
	combined := finalText + "\x00" + logged
	failure := make([]int, len(combined))
	for i := 1; i < len(combined); i++ {
		length := failure[i-1]
		for length > 0 && combined[i] != combined[length] {
			length = failure[length-1]
		}
		if combined[i] == combined[length] {
			length++
		}
		failure[i] = length
	}
	overlap := failure[len(combined)-1]
	if overlap > len(finalText) || !strings.HasSuffix(logged, finalText[:overlap]) {
		return 0
	}
	return overlap
}
