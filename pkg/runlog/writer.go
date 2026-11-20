package runlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultMaxBytes caps one run log. The guest detach journal keeps up to
	// 8 MiB, so the host cap must not be smaller.
	DefaultMaxBytes int64 = 16 << 20
	// DefaultToolOutputHalf keeps this many bytes from each end of a tool's
	// output. Errors usually sit at the end, so the tail is kept too.
	DefaultToolOutputHalf = 8 << 10

	textFlushBytes = 4 << 10
)

// Options configures a Writer. Zero values use the defaults.
type Options struct {
	MaxBytes       int64
	ToolOutputHalf int
	Now            func() time.Time
	// OnAppend runs after each entry is durably appended, with the entry's
	// rendered text. It is called with the writer's lock held, in order.
	OnAppend func(entry Entry, rendered string)
}

// Writer appends to one run log. It is safe for concurrent use; entries are
// assigned sequence numbers and text offsets in the order they are appended.
type Writer struct {
	mu   sync.Mutex
	opts Options
	file *os.File

	seq        uint64
	textOffset uint64
	turn       uint32
	size       int64
	truncated  bool

	pendingText strings.Builder
	turnProse   string
	tools       map[string]*toolState
}

type toolState struct {
	tool          Tool
	callWritten   bool
	resultWritten bool
}

// Open opens the run log at path for appending, creating it if needed. An
// existing log is scanned so sequence numbers, text offsets, the current
// turn and the tool calls already recorded continue where they stopped,
// which is what a re-attached run needs after a daemon restart.
func Open(path string, opts Options) (*Writer, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.ToolOutputHalf <= 0 {
		opts.ToolOutputHalf = DefaultToolOutputHalf
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create run log dir: %w", err)
	}
	w := &Writer{opts: opts, tools: map[string]*toolState{}}
	if err := w.recover(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run log %s: %w", path, err)
	}
	w.file = file
	return w, nil
}

func (w *Writer) recover(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open run log %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	reader := bufio.NewReaderSize(file, 64<<10)
	var good int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read run log %s: %w", path, readErr)
		}
		complete := len(line) > 0 && line[len(line)-1] == '\n'
		if !complete {
			// A torn last line from a crash mid-write. Cut it off so the
			// next append starts on a line of its own.
			if len(line) > 0 {
				if err := os.Truncate(path, good); err != nil {
					return fmt.Errorf("truncate torn run log line: %w", err)
				}
			}
			break
		}
		good += int64(len(line))
		w.size = good
		var entry Entry
		if json.Unmarshal(line, &entry) != nil {
			continue
		}
		w.seq = entry.Seq
		w.textOffset = entry.TextOffset + uint64(len(Render(entry)))
		if entry.Turn > w.turn {
			w.turn = entry.Turn
		}
		switch entry.Type {
		case EntryTruncated:
			w.truncated = true
		case EntryToolCall, EntryToolResult:
			if entry.Tool != nil && entry.Tool.ID != "" {
				state := w.tool(entry.Tool.ID)
				if entry.Type == EntryToolCall {
					state.callWritten = true
					state.tool = *entry.Tool
				} else {
					state.resultWritten = true
				}
			}
		}
	}
	return nil
}

// AppendUser records user input and starts a new turn.
func (w *Writer) AppendUser(text string) (Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.flushTextLocked(); err != nil {
		return Entry{}, err
	}
	w.turn++
	w.turnProse = ""
	return w.appendLocked(Entry{Type: EntryUser, Text: text}, true)
}

// AppendOutput records process output. stream is stdout, stderr or tty.
// data is kept byte for byte, so filtering a log by stream reproduces it.
func (w *Writer) AppendOutput(stream, data string) error {
	if data == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.flushTextLocked(); err != nil {
		return err
	}
	_, err := w.appendLocked(Entry{Type: EntryOutput, Stream: stream, Data: data}, false)
	return err
}

// EndTurn closes the current turn. Text the provider delivered only in its
// final answer, never as deltas, is recorded before the turn end.
func (w *Writer) EndTurn(stopReason, finalText string) (Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.flushTextLocked(); err != nil {
		return Entry{}, err
	}
	if rest := finalText[streamedFinalTextOverlap(w.turnProse, finalText):]; rest != "" {
		if _, err := w.appendLocked(Entry{Type: EntryText, Text: rest}, false); err != nil {
			return Entry{}, err
		}
		w.turnProse += rest
	}
	return w.appendLocked(Entry{Type: EntryTurnEnd, StopReason: stopReason, FinalText: finalText}, true)
}

// Flush writes text still buffered for coalescing.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.flushTextLocked()
}

// Close flushes buffered text and closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	flushErr := w.flushTextLocked()
	closeErr := w.file.Close()
	w.file = nil
	return errors.Join(flushErr, closeErr)
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

// AppendAgentEvent merges one runtime agent event into the log.
//
// Deltas are coalesced into paragraphs. A tool call is recorded once, when
// its input is known: claude announces a call before its arguments finish
// streaming and re-emits it afterwards, and codex resends its status. Its
// outcome is recorded once, from the first tool_result. Reasoning, steps,
// retries, compaction and todo lists are not recorded.
func (w *Writer) AppendAgentEvent(raw []byte) error {
	var event agentEvent
	if err := json.Unmarshal(raw, &event); err != nil {
		return fmt.Errorf("decode agent event: %w", err)
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	switch event.Kind {
	case "text_delta":
		return w.appendTextLocked(event.Text)
	case "tool_call":
		return w.toolCallLocked(event)
	case "tool_result":
		return w.toolResultLocked(event)
	case "usage":
		if err := w.flushTextLocked(); err != nil {
			return err
		}
		_, err := w.appendLocked(Entry{Type: EntryUsage, Usage: &Usage{
			Scope: event.Scope, Model: event.Model,
			InputTokens: event.InputTokens, OutputTokens: event.OutputTokens, ReasoningTokens: event.ReasoningTokens,
			CachedTokens: event.CachedTokens, CacheWriteTokens: event.CacheWriteTokens, CostUSD: event.CostUSD,
		}}, true)
		return err
	case "error":
		if err := w.flushTextLocked(); err != nil {
			return err
		}
		_, err := w.appendLocked(Entry{Type: EntryError, Text: event.Message, Severity: event.Severity, Code: event.Code}, true)
		return err
	default:
		return nil
	}
}

func (w *Writer) appendTextLocked(text string) error {
	if text == "" {
		return nil
	}
	w.pendingText.WriteString(text)
	pending := w.pendingText.String()
	if len(pending) >= textFlushBytes {
		return w.flushTextLocked()
	}
	if cut := strings.LastIndexByte(pending, '\n'); cut >= 0 {
		head, tail := pending[:cut+1], pending[cut+1:]
		w.pendingText.Reset()
		w.pendingText.WriteString(tail)
		return w.writeTextLocked(head)
	}
	return nil
}

func (w *Writer) flushTextLocked() error {
	if w.pendingText.Len() == 0 {
		return nil
	}
	text := w.pendingText.String()
	w.pendingText.Reset()
	return w.writeTextLocked(text)
}

func (w *Writer) writeTextLocked(text string) error {
	if _, err := w.appendLocked(Entry{Type: EntryText, Text: text}, false); err != nil {
		return err
	}
	w.turnProse += text
	return nil
}

func (w *Writer) tool(id string) *toolState {
	state := w.tools[id]
	if state == nil {
		state = &toolState{tool: Tool{ID: id}}
		w.tools[id] = state
	}
	return state
}

func (w *Writer) toolCallLocked(event agentEvent) error {
	if event.ID == "" {
		return nil
	}
	state := w.tool(event.ID)
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
		tool.Input = append(json.RawMessage(nil), event.Input...)
	}
	if event.ExitCode != nil {
		code := *event.ExitCode
		tool.ExitCode = &code
	}
	if state.callWritten {
		return nil
	}
	if tool.Command == "" && toolInputText(tool.Input) == "" && event.Status == "in_progress" {
		// Arguments are still streaming; wait for the completed re-emit or
		// the result, whichever comes first.
		return nil
	}
	return w.writeToolCallLocked(state)
}

func (w *Writer) writeToolCallLocked(state *toolState) error {
	if err := w.flushTextLocked(); err != nil {
		return err
	}
	call := state.tool
	call.ExitCode = nil
	if _, err := w.appendLocked(Entry{Type: EntryToolCall, Tool: &call}, true); err != nil {
		return err
	}
	state.callWritten = true
	return nil
}

func (w *Writer) toolResultLocked(event agentEvent) error {
	if event.ID == "" {
		return nil
	}
	state := w.tool(event.ID)
	if state.resultWritten {
		return nil
	}
	if !state.callWritten {
		if err := w.writeToolCallLocked(state); err != nil {
			return err
		}
	}
	if err := w.flushTextLocked(); err != nil {
		return err
	}
	output, outputBytes := truncateMiddle(event.Output, w.opts.ToolOutputHalf)
	result := Tool{
		ID: state.tool.ID, ParentID: state.tool.ParentID, Name: state.tool.Name,
		OK: event.OK, ExitCode: state.tool.ExitCode,
		Output: output, OutputBytes: outputBytes, Error: event.Error,
	}
	if event.ExitCode != nil {
		code := *event.ExitCode
		result.ExitCode = &code
	}
	if _, err := w.appendLocked(Entry{Type: EntryToolResult, Tool: &result}, true); err != nil {
		return err
	}
	state.resultWritten = true
	return nil
}

// appendLocked assigns the entry its position and writes it. Once the log
// reaches its cap, one truncated marker is written and later entries are
// dropped, except the small ones the run's state depends on (essential):
// user input, turn ends, tool calls and results, usage and errors.
func (w *Writer) appendLocked(entry Entry, essential bool) (Entry, error) {
	if w.file == nil {
		return Entry{}, errors.New("run log is closed")
	}
	if w.turn == 0 {
		w.turn = 1
	}
	if entry.Turn == 0 {
		entry.Turn = w.turn
	}
	rendered := Render(entry)
	if w.truncated && !essential {
		return Entry{}, nil
	}
	if !w.truncated && w.size+int64(len(rendered))+256 > w.opts.MaxBytes {
		w.truncated = true
		if _, err := w.writeLocked(Entry{Type: EntryTruncated, Turn: entry.Turn, DroppedBytes: uint64(len(rendered))}); err != nil {
			return Entry{}, err
		}
		if !essential {
			return Entry{}, nil
		}
	}
	return w.writeLocked(entry)
}

func (w *Writer) writeLocked(entry Entry) (Entry, error) {
	entry.Seq = w.seq + 1
	entry.Time = w.opts.Now().UTC()
	entry.TextOffset = w.textOffset
	if entry.Turn == 0 {
		entry.Turn = w.turn
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return Entry{}, fmt.Errorf("encode run log entry: %w", err)
	}
	line = append(line, '\n')
	if _, err := w.file.Write(line); err != nil {
		return Entry{}, fmt.Errorf("append run log: %w", err)
	}
	rendered := Render(entry)
	w.seq = entry.Seq
	w.textOffset += uint64(len(rendered))
	w.size += int64(len(line))
	if w.opts.OnAppend != nil {
		w.opts.OnAppend(entry, rendered)
	}
	return entry, nil
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
	case lead&0x80 == 0:
		need = 1
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
