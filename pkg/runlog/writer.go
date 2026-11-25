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

// DefaultToolOutputHalf keeps this many bytes from each end of a tool's
// output. Errors usually sit at the end, so the tail is kept too.
const DefaultToolOutputHalf = 8 << 10

// DefaultRunLogMaxBytes caps one project run's log. It guards the data root's
// disk, which also holds the database, against a run that keeps printing;
// readers do not need it, as they locate offsets in the file rather than hold
// it. Ordinary runs stay far below it.
const DefaultRunLogMaxBytes int64 = 256 << 20

// Uncapped, as Options.MaxBytes, writes a log with no size cap.
const Uncapped int64 = -1

// maxLogLineBytes bounds one run log line, so that a single oversized entry —
// however it is produced — cannot make the whole log unreadable: the writer
// shrinks an entry that would not fit (see fitLine) and the reader skips lines
// past it.
const maxLogLineBytes int64 = 16 << 20

// Options configures a Writer. Zero values use the defaults.
type Options struct {
	// MaxBytes caps the log: zero applies DefaultRunLogMaxBytes and
	// Uncapped (any negative value) turns the cap off. The cap is soft:
	// past it only essential entries (user input, turn ends, truncation
	// markers) are written, and they may take the file beyond it.
	MaxBytes       int64
	ToolOutputHalf int
	Now            func() time.Time
	// OnAppend runs after each entry is appended, with the entry's rendered
	// text. It is called with the writer's lock held, in order.
	//
	// Once the log has a checkpoint, an entry only counts once the next
	// Checkpoint commits it: until then a crash rolls it back. OnAppend is
	// then deferred to that Checkpoint, and entries appended after the last
	// one are never reported, not even by Close; the next Open rolls them
	// back and replaying the journal appends and reports them again.
	OnAppend func(entry Entry, rendered string)
}

// Writer appends to one run log. It is safe for concurrent use; entries are
// assigned sequence numbers and text offsets in the order they are appended.
//
// Once a capped log reaches its cap, later entries are dropped, except the
// small ones the run's state depends on: user input, turn ends, tool calls and
// results, usage and errors. Each stretch of dropped entries is closed by one
// truncated marker counting the rendered bytes it dropped, written before the
// next kept entry or when the writer closes.
type Writer struct {
	mu     sync.Mutex
	opts   Options
	file   *os.File
	merger *Merger

	seq        uint64
	textOffset uint64
	// textEndsLine reports whether the text view so far is empty or ends in
	// a newline, which decides whether user input needs one before it.
	textEndsLine bool
	turn         uint32
	size         int64
	truncated    bool
	// dropped counts the rendered bytes dropped since the last marker.
	dropped       uint64
	sourceOffset  uint64
	recovery      *Recovery
	notifications []Entry
	// changed reports whether merger or writer state moved since the last
	// checkpoint, including changes that wrote no bytes: text held for
	// coalescing, or entries the cap dropped.
	changed bool
}

// Open opens the run log at path for appending, creating it if needed. An
// existing log is scanned so sequence numbers, text offsets, the current
// turn and the tool calls already recorded continue where they stopped,
// which is what a re-attached run needs after a daemon restart. A line torn
// by a crash mid-write is cut off.
func Open(path string, opts Options) (*Writer, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.MaxBytes == 0 {
		opts.MaxBytes = DefaultRunLogMaxBytes
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create run log dir: %w", err)
	}
	w := &Writer{opts: opts, merger: NewMerger(opts.ToolOutputHalf), textEndsLine: true}
	recovery, err := recoverCheckpoint(path)
	if err != nil {
		return nil, err
	}
	w.recovery = recovery
	if recovery != nil {
		w.sourceOffset = recovery.SourceOffset
	}
	if err := w.recover(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open run log %s: %w", path, err)
	}
	w.file = file
	if recovery != nil && recovery.merger != nil {
		w.merger.restoreCheckpoint(recovery.merger)
	}
	if recovery != nil && recovery.writer != nil {
		// Entries dropped by the cap before the checkpoint leave no line
		// until their marker is written; only the checkpoint remembers them.
		w.truncated = w.truncated || recovery.writer.Truncated
		w.dropped = recovery.writer.Dropped
	}
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
		line, readErr := readLogLine(reader)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return fmt.Errorf("read run log %s: %w", path, readErr)
		}
		if !line.complete {
			if line.bytes > 0 {
				if err := os.Truncate(path, good); err != nil {
					return fmt.Errorf("truncate torn run log line: %w", err)
				}
			}
			break
		}
		good += line.bytes
		w.size = good
		entry, ok := decodeEntry(line.data)
		// Sequence numbers only grow; a line that would move them back did
		// not come from this writer and must not reset its position.
		if !ok || entry.Seq <= w.seq {
			continue
		}
		w.seq = entry.Seq
		rendered := Render(entry)
		w.textOffset = entry.TextOffset + uint64(len(rendered))
		if rendered != "" {
			w.textEndsLine = strings.HasSuffix(rendered, "\n")
		}
		if entry.Turn > w.turn {
			w.turn = entry.Turn
		}
		if entry.Type == EntryTruncated && !entry.SourceGap {
			w.truncated = true
		}
		w.merger.Recover(entry)
	}
	return nil
}

// Seq is the sequence number of the last entry, 0 for an empty log.
func (w *Writer) Seq() uint64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.seq
}

// AppendUser records user input and starts a new turn.
func (w *Writer) AppendUser(text string) (Entry, error) {
	return w.appendUser(text, false)
}

// AppendInitialPrompt records the run's opening prompt as its first turn.
func (w *Writer) AppendInitialPrompt(text string) (Entry, error) {
	return w.appendUser(text, true)
}

func (w *Writer) appendUser(text string, initial bool) (Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	pending := w.merger.User(text)
	if err := w.appendAllLocked(pending[:len(pending)-1]); err != nil {
		return Entry{}, err
	}
	w.turn++
	user := pending[len(pending)-1]
	user.Entry.Initial = initial
	user.Entry.Newline = !initial && !w.textEndsLine
	return w.appendLocked(user)
}

// AppendOutput records process output. stream is stdout, stderr or tty.
// data must be whole UTF-8, as the drivers' stream decoders deliver it
// (pkg/driver/utf8_stream.go): JSON stores invalid UTF-8 as U+FFFD, so a
// character split across two calls would be lost.
// Valid UTF-8 is kept byte for byte, so filtering a log by stream reproduces
// it; a JSON string cannot carry invalid sequences, which become U+FFFD.
func (w *Writer) AppendOutput(stream, data string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendAllLocked(w.merger.Output(stream, data))
}

// AppendAgentEvent merges one runtime agent event into the log; see Merger.
func (w *Writer) AppendAgentEvent(raw []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	pending, err := w.merger.Event(raw)
	if err != nil {
		return err
	}
	return w.appendAllLocked(pending)
}

// EndTurn closes the current turn and returns its turn end entry.
func (w *Writer) EndTurn(stopReason, finalText string) (Entry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	pending := w.merger.EndTurn(stopReason, finalText)
	if err := w.appendAllLocked(pending[:len(pending)-1]); err != nil {
		return Entry{}, err
	}
	return w.appendLocked(pending[len(pending)-1])
}

// Flush writes text still buffered for coalescing.
func (w *Writer) Flush() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.appendAllLocked(w.merger.FlushText())
}

// Close flushes buffered text and closes the file. Entries a checkpointed
// log appended since its last Checkpoint are not reported to OnAppend; see
// Options.OnAppend.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	// Close writes the text still held for coalescing and the marker for
	// bytes the cap dropped. On a checkpointed log readers see only what the
	// last checkpoint committed. When nothing changed since then, these come
	// from state that checkpoint already held, so they are committed too, with
	// the consumer's state unchanged. Otherwise they may include input the
	// consumer will replay from the checkpoint, and stay uncommitted.
	committed := w.recovery != nil && !w.changed
	flushErr := w.appendAllLocked(w.merger.FlushText())
	if flushErr == nil {
		flushErr = w.writeDroppedLocked(w.turn)
	}
	if flushErr == nil && committed && w.size != w.recovery.LogBytes {
		flushErr = w.checkpointLocked(w.recovery.SourceOffset, w.recovery.State)
	}
	closeErr := w.file.Close()
	w.file = nil
	return errors.Join(flushErr, closeErr)
}

func (w *Writer) appendAllLocked(pending []Pending) error {
	w.changed = true
	for _, p := range pending {
		if _, err := w.appendLocked(p); err != nil {
			return err
		}
	}
	return nil
}

// appendLocked gives the entry its position and writes it, applying the cap.
func (w *Writer) appendLocked(p Pending) (Entry, error) {
	w.changed = true
	if w.file == nil {
		return Entry{}, errors.New("run log is closed")
	}
	if w.turn == 0 {
		w.turn = 1
	}
	entry := p.Entry
	if entry.Turn == 0 {
		entry.Turn = w.turn
	}
	if w.truncated && !p.Essential {
		w.dropped += uint64(len(Render(entry)))
		return Entry{}, nil
	}
	if !w.truncated && w.opts.MaxBytes > 0 {
		line, err := json.Marshal(entry)
		if err != nil {
			return Entry{}, fmt.Errorf("encode run log entry: %w", err)
		}
		// Leave room for the marker that will close the dropped stretch.
		if w.size+int64(len(line))+256 > w.opts.MaxBytes {
			w.truncated = true
			if !p.Essential {
				w.dropped += uint64(len(Render(entry)))
				return Entry{}, nil
			}
		}
	}
	if err := w.writeDroppedLocked(entry.Turn); err != nil {
		return Entry{}, err
	}
	return w.writeLocked(entry)
}

// writeDroppedLocked closes a stretch of dropped entries with a truncated
// marker counting what it dropped. The marker goes where the text went
// missing: before the next kept entry, or at the end when the writer closes.
func (w *Writer) writeDroppedLocked(turn uint32) error {
	if w.dropped == 0 {
		return nil
	}
	if _, err := w.writeLocked(Entry{Type: EntryTruncated, Turn: turn, DroppedBytes: w.dropped}); err != nil {
		// Keep the count so the next marker still reports these bytes.
		return err
	}
	w.dropped = 0
	return nil
}

func (w *Writer) writeLocked(entry Entry) (Entry, error) {
	entry.SourceOffset = w.sourceOffset
	entry.Seq = w.seq + 1
	entry.Time = w.opts.Now().UTC()
	entry.TextOffset = w.textOffset
	line, err := fitLine(entry)
	if err != nil {
		return Entry{}, err
	}
	// Position the text view by the entry as readers will decode it. JSON
	// does not store every string as given: invalid UTF-8 becomes U+FFFD and
	// tool input is compacted and escaped. Rendering the in-memory entry
	// would shift every later text offset by the difference.
	var stored Entry
	if err := json.Unmarshal(line, &stored); err != nil {
		return Entry{}, fmt.Errorf("decode run log entry: %w", err)
	}
	entry = stored
	line = append(line, '\n')
	if _, err := w.file.Write(line); err != nil {
		return Entry{}, fmt.Errorf("append run log: %w", err)
	}
	rendered := Render(entry)
	w.seq = entry.Seq
	w.textOffset += uint64(len(rendered))
	if rendered != "" {
		w.textEndsLine = strings.HasSuffix(rendered, "\n")
	}
	w.size += int64(len(line))
	if w.opts.OnAppend != nil {
		if w.recovery != nil {
			w.notifications = append(w.notifications, entry)
		} else {
			w.opts.OnAppend(entry, rendered)
		}
	}
	return entry, nil
}

// fitLine encodes entry as one log line. The final encoding, including the
// position metadata and the newline, is checked against the same bound the
// reader applies; an entry past it keeps a shrinking head and tail of each of
// its long fields until it fits, instead of being dropped. Dropping it would
// lose the very entries a run's state depends on (a user message, a turn end)
// and hand the caller no sequence number to point at. What was cut is marked
// in the text itself.
func fitLine(entry Entry) ([]byte, error) {
	line, err := json.Marshal(entry)
	if err != nil {
		return nil, fmt.Errorf("encode run log entry: %w", err)
	}
	for budget := maxEntryTextBytes; int64(len(line))+1 > maxLogLineBytes; budget /= 4 {
		fitted := shrinkEntry(entry, budget)
		if line, err = json.Marshal(fitted); err != nil {
			return nil, fmt.Errorf("encode run log entry: %w", err)
		}
		if int64(len(line))+1 <= maxLogLineBytes {
			return line, nil
		}
		if budget == 0 {
			return nil, fmt.Errorf("run log %s entry does not fit in one line", entry.Type)
		}
	}
	return line, nil
}

// shrinkEntry keeps at most budget bytes of each long field of entry. It
// copies what it changes, as Tool is shared with the merger's state.
func shrinkEntry(entry Entry, budget int) Entry {
	entry.Text = clampText(entry.Text, budget)
	entry.FinalText = clampText(entry.FinalText, budget)
	entry.Data = clampText(entry.Data, budget)
	entry.Code = clampText(entry.Code, budget)
	if entry.Tool != nil {
		tool := *entry.Tool
		tool.Name = clampText(tool.Name, budget)
		tool.Command = clampText(tool.Command, budget)
		tool.Output = clampText(tool.Output, budget)
		tool.Error = clampText(tool.Error, budget)
		tool.Input = clampToolInput(tool.Input, budget)
		if len(tool.Changes) > 0 {
			keep := min(len(tool.Changes), budget/64)
			changes := make([]FileChange, keep)
			for i, change := range tool.Changes[:keep] {
				changes[i] = FileChange{Path: clampText(change.Path, budget), Kind: clampText(change.Kind, budget)}
			}
			tool.Changes = changes
		}
		entry.Tool = &tool
	}
	if entry.Usage != nil {
		usage := *entry.Usage
		usage.Model = clampText(usage.Model, budget)
		entry.Usage = &usage
	}
	return entry
}

// clampText keeps at most about limit bytes of s, half from each end, with a
// marker saying how much was omitted between them.
func clampText(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	if limit/2 <= 0 {
		return fmt.Sprintf("[… %d bytes omitted …]", len(s))
	}
	clamped, _ := truncateMiddle(s, limit/2)
	return clamped
}
