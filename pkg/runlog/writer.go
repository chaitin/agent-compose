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
//
// Once the log reaches its cap, one truncated marker is written and later
// entries are dropped, except the small ones the run's state depends on: user
// input, turn ends, tool calls and results, usage and errors.
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
}

// Open opens the run log at path for appending, creating it if needed. An
// existing log is scanned so sequence numbers, text offsets, the current
// turn and the tool calls already recorded continue where they stopped,
// which is what a re-attached run needs after a daemon restart. A line torn
// by a crash mid-write is cut off.
func Open(path string, opts Options) (*Writer, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create run log dir: %w", err)
	}
	w := &Writer{opts: opts, merger: NewMerger(opts.ToolOutputHalf), textEndsLine: true}
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
		if len(line) == 0 || line[len(line)-1] != '\n' {
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
		rendered := Render(entry)
		w.textOffset = entry.TextOffset + uint64(len(rendered))
		if rendered != "" {
			w.textEndsLine = strings.HasSuffix(rendered, "\n")
		}
		if entry.Turn > w.turn {
			w.turn = entry.Turn
		}
		if entry.Type == EntryTruncated {
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
// data is kept byte for byte, so filtering a log by stream reproduces it.
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

// Close flushes buffered text and closes the file.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	flushErr := w.appendAllLocked(w.merger.FlushText())
	closeErr := w.file.Close()
	w.file = nil
	return errors.Join(flushErr, closeErr)
}

func (w *Writer) appendAllLocked(pending []Pending) error {
	for _, p := range pending {
		if _, err := w.appendLocked(p); err != nil {
			return err
		}
	}
	return nil
}

// appendLocked gives the entry its position and writes it, applying the cap.
func (w *Writer) appendLocked(p Pending) (Entry, error) {
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
		return Entry{}, nil
	}
	if !w.truncated {
		if line, err := json.Marshal(entry); err == nil && w.size+int64(len(line))+256 > w.opts.MaxBytes {
			w.truncated = true
			if _, err := w.writeLocked(Entry{Type: EntryTruncated, Turn: entry.Turn, DroppedBytes: uint64(len(Render(entry)))}); err != nil {
				return Entry{}, err
			}
			if !p.Essential {
				return Entry{}, nil
			}
		}
	}
	return w.writeLocked(entry)
}

func (w *Writer) writeLocked(entry Entry) (Entry, error) {
	entry.Seq = w.seq + 1
	entry.Time = w.opts.Now().UTC()
	entry.TextOffset = w.textOffset
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
	if rendered != "" {
		w.textEndsLine = strings.HasSuffix(rendered, "\n")
	}
	w.size += int64(len(line))
	if w.opts.OnAppend != nil {
		w.opts.OnAppend(entry, rendered)
	}
	return entry, nil
}
