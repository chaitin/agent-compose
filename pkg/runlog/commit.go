package runlog

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// A journaled log is one whose consumer reads a guest journal and may replay
// part of it after a daemon restart. Its commits say how far the log is
// durable: a crash rolls the log back to its last commit, and the consumer
// resumes the journal from the offset that commit names, so replaying never
// duplicates an entry and never loses one.
//
// A commit is a line of its own, appended after the entries it covers; no
// entry is ever rewritten. A log without commits is not journaled and all of
// its complete lines count.
//
// A commit is only written where everything the consumer needs to continue
// can be rebuilt from the committed prefix: the merger holds no text waiting
// for a newline and no tool call waiting for its arguments, and the consumer
// says the same of its own state. The few facts that cannot be rebuilt from
// entries, such as a process exit, are kept as records (AppendRecord).

// idleCommitBytes is how far a journal may advance past the last commit
// without the log changing before a commit is written anyway. Past the size
// cap a run can keep printing without adding a line; this keeps its replay
// window bounded at the cost of one small line per such stretch.
const idleCommitBytes = 1 << 20

// Resume is where the consumer of a journaled log continues after a restart.
type Resume struct {
	// Offset is the journal offset to resume reading from.
	Offset uint64
	// State is the consumer state the last commit carried.
	State json.RawMessage
	// Records holds, by name, the last record in the committed prefix.
	Records map[string]json.RawMessage
}

// Resume returns where the consumer of this log continues: nil when the log
// has no commit, either because it is not journaled or because nothing was
// committed before the daemon stopped.
func (w *Writer) Resume() *Resume {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.resume == nil {
		return nil
	}
	resume := Resume{Offset: w.resume.Offset, State: bytes.Clone(w.resume.State), Records: make(map[string]json.RawMessage, len(w.resume.Records))}
	for name, data := range w.resume.Records {
		resume.Records[name] = bytes.Clone(data)
	}
	return &resume
}

// SetSourceOffset associates subsequent entries with the end of the journal
// record being processed. The consumer must serialize record processing and
// Commit calls.
func (w *Writer) SetSourceOffset(offset uint64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.sourceOffset = offset
}

// Commit makes everything appended so far durable and records that the
// consumer resumes reading its journal from src, with state as its own
// state there. It reports whether everything appended is now covered by a
// commit: it is not while the merger still holds input that has not become
// an entry, since a restart could not rebuild it from the log.
//
// Entries dropped by the size cap since the last marker get their marker
// first, so a restart does not need to know how much was dropped.
//
// Nothing is fsynced: a commit is written for every journal record, and a
// daemon restart, the failure it exists for, keeps what was written in the
// page cache. After a machine crash the log is rolled back to the last
// complete commit that reached the disk.
func (w *Writer) Commit(src uint64, state json.RawMessage) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if len(state) > 0 && !json.Valid(state) {
		return false, errors.New("invalid run log commit state")
	}
	if w.file == nil {
		return false, errors.New("run log is closed")
	}
	if !w.merger.Quiescent() {
		return false, nil
	}
	if last := w.resume; last != nil && !w.uncommitted && bytes.Equal(state, last.State) && src >= last.Offset {
		if src == last.Offset {
			return true, nil
		}
		if src-last.Offset < idleCommitBytes {
			// No line was appended since the last commit: everything is
			// covered, and replaying from it rebuilds the same state, so this
			// record needs no line of its own.
			return true, nil
		}
	}
	if err := w.commitLocked(src, state); err != nil {
		return false, err
	}
	return true, nil
}

func (w *Writer) commitLocked(src uint64, state json.RawMessage) error {
	if err := w.writeDroppedLocked(w.turn); err != nil {
		return err
	}
	if err := w.writeControlLocked(Entry{Type: EntryCommit, SourceOffset: src, Capped: w.truncated, Control: state}); err != nil {
		return err
	}
	records := map[string]json.RawMessage{}
	if w.resume != nil {
		records = w.resume.Records
	}
	for name, data := range w.pendingRecords {
		records[name] = data
	}
	w.pendingRecords = nil
	w.resume = &Resume{Offset: src, State: bytes.Clone(state), Records: records}
	w.sourceOffset, w.uncommitted = src, false
	if w.opts.OnAppend != nil {
		for _, entry := range w.notifications {
			w.opts.OnAppend(entry, Render(entry))
		}
	}
	w.notifications = nil
	return nil
}

// Settle writes the text the merger holds for coalescing and forgets tool
// calls still waiting for their arguments. It is for when no more input will
// come, such as after the process exited, so that the next commit can cover
// everything the consumer took in.
func (w *Writer) Settle() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.merger.forgetUnannounced()
	return w.appendAllLocked(w.merger.FlushText())
}

// AppendRecord keeps a fact under name that the consumer needs after a
// restart and cannot derive from entries. It becomes durable with the next
// commit, and Resume then reports the last one of each name.
func (w *Writer) AppendRecord(name string, data json.RawMessage) error {
	if name == "" || !json.Valid(data) {
		return errors.New("invalid run log record")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return errors.New("run log is closed")
	}
	if err := w.writeControlLocked(Entry{Type: EntryRecord, SourceOffset: w.sourceOffset, Name: name, Control: data}); err != nil {
		return err
	}
	if w.pendingRecords == nil {
		w.pendingRecords = map[string]json.RawMessage{}
	}
	w.pendingRecords[name] = bytes.Clone(data)
	return nil
}

// writeControlLocked appends a control line. It has no sequence number and no
// text, so it moves neither, and the cap does not apply to it.
func (w *Writer) writeControlLocked(entry Entry) error {
	entry.Time = w.opts.Now().UTC()
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encode run log %s: %w", entry.Type, err)
	}
	if int64(len(line))+1 > maxLogLineBytes {
		return fmt.Errorf("run log %s does not fit in one line", entry.Type)
	}
	line = append(line, '\n')
	if _, err := w.file.Write(line); err != nil {
		return fmt.Errorf("append run log %s: %w", entry.Type, err)
	}
	w.size += int64(len(line))
	w.uncommitted = entry.Type != EntryCommit
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

// controlHead is what locating the committed prefix decodes of a line.
type controlHead struct {
	Seq          uint64    `json:"seq"`
	Type         EntryType `json:"type"`
	SourceOffset uint64    `json:"src_offset"`
}

// committedLogBytes is how much of the open run log readers may see: up to
// the end of its last commit, or every complete line of a log that is not
// journaled. It walks back from the end, and stops at the first commit or at
// the first entry no journal record produced: a journaled log commits before
// it records anything a journal record produced, so an entry without a source
// offset is never part of an uncommitted tail.
func committedLogBytes(file *os.File) (int64, error) {
	end, _, err := committedEnd(file)
	return end, err
}

// committedLogReader reads the open run log from byte offset from, at which
// the file is positioned, up to the end of its committed prefix.
func committedLogReader(file *os.File, from int64) (io.Reader, error) {
	logBytes, err := committedLogBytes(file)
	if err != nil {
		return nil, err
	}
	return io.LimitReader(file, max(logBytes-from, 0)), nil
}

// committedEnd returns committedLogBytes and whether the log has a commit.
func committedEnd(file *os.File) (int64, bool, error) {
	info, err := file.Stat()
	if err != nil {
		return 0, false, fmt.Errorf("stat run log: %w", err)
	}
	complete, err := lastLineEnd(file, 0, info.Size())
	if err != nil {
		return 0, false, err
	}
	committed, journaled := complete, false
	err = scanLinesBackward(file, 0, complete, func(start int64, line []byte) bool {
		var head controlHead
		if line == nil || json.Unmarshal(line, &head) != nil {
			return true
		}
		if head.Seq == 0 && head.Type == EntryCommit {
			committed, journaled = start+int64(len(line))+1, true
			return false
		}
		return head.Seq == 0 || head.SourceOffset != 0
	})
	if err != nil {
		return 0, false, err
	}
	return committed, journaled, nil
}

// rollBack cuts a journaled log back to its last commit, discarding what a
// restart replays from the journal. It reports whether the log is journaled.
func rollBack(path string) (bool, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("open run log %s: %w", path, err)
	}
	end, journaled, err := committedEnd(file)
	info, statErr := file.Stat()
	_ = file.Close() // Read-only.
	if err != nil {
		return false, err
	}
	if statErr != nil {
		return false, fmt.Errorf("stat run log: %w", statErr)
	}
	if journaled && info.Size() > end {
		if err := os.Truncate(path, end); err != nil {
			return false, fmt.Errorf("roll run log back to its last commit: %w", err)
		}
	}
	return journaled, nil
}
