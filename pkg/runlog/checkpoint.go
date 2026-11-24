package runlog

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Recovery binds a journal position and its consumer state to a durable prefix
// of the log. State belongs to the consumer; runlog does not interpret it.
type Recovery struct {
	merger       *mergerCheckpoint
	writer       *writerCheckpoint
	SourceOffset uint64          `json:"src_offset"`
	State        json.RawMessage `json:"state"`
	LogBytes     int64           `json:"log_bytes"`
}

func checkpointPath(path string) string { return path + ".checkpoint.json" }

// recoverCheckpoint discards effects written after the last committed journal
// record. Replaying that record can then never duplicate a partially written
// group of text, tool, usage and turn entries.
func recoverCheckpoint(path string) (*Recovery, error) {
	file, err := os.Open(checkpointPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read run log checkpoint: %w", err)
	}
	defer func() { _ = file.Close() }() // Read-only snapshot.
	reader := bufio.NewReader(file)
	header, err := readCheckpointHeader(reader)
	if err != nil {
		return nil, err
	}
	state, err := io.ReadAll(reader)
	if err != nil {
		return nil, fmt.Errorf("read run log checkpoint state: %w", err)
	}
	var saved checkpointState
	if err := json.Unmarshal(state, &saved); err != nil {
		return nil, fmt.Errorf("decode run log checkpoint state: %w", err)
	}
	recovery := Recovery{SourceOffset: header.SourceOffset, LogBytes: header.LogBytes, State: saved.State, merger: saved.Merger, writer: saved.Writer}
	if saved.Merger == nil {
		return nil, errors.New("invalid run log checkpoint: missing merger state")
	}
	if !json.Valid(recovery.State) {
		return nil, errors.New("invalid run log checkpoint: consumer state is not valid JSON")
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat checkpointed run log: %w", err)
	}
	if info.Size() < recovery.LogBytes {
		return nil, errors.New("run log is shorter than its checkpoint")
	}
	if info.Size() > recovery.LogBytes {
		if err := os.Truncate(path, recovery.LogBytes); err != nil {
			return nil, fmt.Errorf("restore run log checkpoint: %w", err)
		}
	}
	return &recovery, nil
}

// Recovery returns an owned copy of the last committed consumer state.
func (w *Writer) Recovery() *Recovery {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.recovery == nil {
		return nil
	}
	recovery := *w.recovery
	recovery.State = append(json.RawMessage(nil), recovery.State...)
	return &recovery
}

// SetSourceOffset associates subsequent entries with a journal record's end.
// The consumer must serialize record processing and Checkpoint calls.
func (w *Writer) SetSourceOffset(offset uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sourceOffset = offset
}

// Checkpoint commits all effects of a journal record before acknowledging its
// position. A crash before the atomic rename replays the previous checkpoint;
// a crash afterwards resumes past this record. The single replaced snapshot
// avoids growing the log with copies of the consumer's lifecycle state.
//
// Nothing is fsynced: a checkpoint runs for every journal record, and a
// daemon restart, the failure it exists for, keeps what was written in the
// page cache. A machine crash can leave the log shorter than its checkpoint;
// the next Open then reports it and the run cannot be resumed.
func (w *Writer) Checkpoint(offset uint64, state json.RawMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !json.Valid(state) {
		return errors.New("invalid run log checkpoint state")
	}
	if w.file == nil {
		return errors.New("run log is closed")
	}
	recovery := Recovery{SourceOffset: offset, State: state, LogBytes: w.size}
	header, err := json.Marshal(checkpointHeader{Version: 1, SourceOffset: offset, LogBytes: w.size})
	if err != nil {
		return fmt.Errorf("encode run log checkpoint: %w", err)
	}
	saved, err := json.Marshal(checkpointState{State: state, Merger: w.merger.checkpoint(),
		Writer: &writerCheckpoint{Truncated: w.truncated, Dropped: w.dropped}})
	if err != nil {
		return fmt.Errorf("encode run log merge checkpoint: %w", err)
	}
	data := append(header, '\n')
	data = append(data, saved...)
	data = append(data, '\n')
	if err := replaceCheckpoint(checkpointPath(w.file.Name()), data); err != nil {
		return err
	}
	recovery.State = append(json.RawMessage(nil), state...)
	w.recovery, w.sourceOffset = &recovery, offset
	if w.opts.OnAppend != nil {
		for _, entry := range w.notifications {
			w.opts.OnAppend(entry, Render(entry))
		}
	}
	w.notifications = nil
	return nil
}

func replaceCheckpoint(path string, data []byte) error {
	file, err := os.CreateTemp(filepath.Dir(path), ".run-log-checkpoint-*")
	if err != nil {
		return fmt.Errorf("create run log checkpoint: %w", err)
	}
	defer func() {
		// Close/remove are cleanup only; successful publication is checked below.
		_ = file.Close()
		_ = os.Remove(file.Name())
	}()
	if _, err := file.Write(data); err != nil {
		return fmt.Errorf("write run log checkpoint: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close run log checkpoint: %w", err)
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return fmt.Errorf("publish run log checkpoint: %w", err)
	}
	return nil
}

// AppendJournalGap records guest retention loss without disabling later text
// as the host log-size cap does.
func (w *Writer) AppendJournalGap(from, to uint64) error {
	if to <= from {
		return errors.New("invalid journal gap range")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.appendAllLocked(w.merger.FlushText()); err != nil {
		return err
	}
	_, err := w.appendLocked(Pending{Entry: Entry{Type: EntryTruncated, SourceGap: true, DroppedBytes: to - from}, Essential: true})
	return err
}

// A small first-line header lets live readers observe only the committed prefix
// without decoding the potentially large lifecycle/partial-frame state.
type checkpointHeader struct {
	Version      int    `json:"v"`
	SourceOffset uint64 `json:"src_offset"`
	LogBytes     int64  `json:"log_bytes"`
}

func readCheckpointHeader(reader *bufio.Reader) (checkpointHeader, error) {
	var header checkpointHeader
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return header, fmt.Errorf("read run log checkpoint header: %w", err)
	}
	if err := json.Unmarshal(line, &header); err != nil {
		return header, fmt.Errorf("decode run log checkpoint header: %w", err)
	}
	if header.Version != 1 || header.LogBytes < 0 {
		return header, errors.New("invalid run log checkpoint header")
	}
	return header, nil
}

func committedLogReader(file *os.File, from int64) (io.Reader, error) {
	logBytes, ok, err := checkpointLogBytes(file.Name())
	if err != nil {
		return nil, err
	}
	if !ok {
		return file, nil
	}
	return io.LimitReader(file, max(logBytes-from, 0)), nil
}

// committedLogBytes is how much of the open run log readers may see: the
// whole file, or the prefix its checkpoint has committed.
func committedLogBytes(file *os.File) (int64, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat run log: %w", err)
	}
	logBytes, ok, err := checkpointLogBytes(file.Name())
	if err != nil {
		return 0, err
	}
	if !ok {
		return info.Size(), nil
	}
	return min(info.Size(), logBytes), nil
}

// checkpointLogBytes reads the committed log length from the checkpoint of
// the run log at path, reporting false when there is no checkpoint.
func checkpointLogBytes(path string) (int64, bool, error) {
	checkpoint, err := os.Open(checkpointPath(path))
	if errors.Is(err, os.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("open run log checkpoint: %w", err)
	}
	defer func() { _ = checkpoint.Close() }() // Read-only snapshot.
	header, err := readCheckpointHeader(bufio.NewReader(checkpoint))
	if err != nil {
		return 0, false, err
	}
	return header.LogBytes, true, nil
}

type checkpointState struct {
	State  json.RawMessage   `json:"state"`
	Merger *mergerCheckpoint `json:"merger"`
	Writer *writerCheckpoint `json:"writer,omitempty"`
}

// writerCheckpoint is the cap state a log's lines do not show: whether the
// cap is reached when no marker has been written yet, and the rendered bytes
// dropped since the last marker.
type writerCheckpoint struct {
	Truncated bool   `json:"truncated,omitempty"`
	Dropped   uint64 `json:"dropped,omitempty"`
}
