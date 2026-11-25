package runlog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ReadEntries returns the entries of the run log at path with Seq > afterSeq.
// A torn trailing line, left by a crash mid-write, is skipped.
func ReadEntries(path string, afterSeq uint64) ([]Entry, error) {
	file, err := openLog(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	reader, err := committedLogReader(file, 0)
	if err != nil {
		return nil, err
	}
	entries, _, err := decodeEntries(reader, afterSeq, 0)
	return entries, err
}

// ReadEntryBatch reads the run log at path from byte offset from, which must
// be on a line boundary, keeping entries with Seq > afterSeq and stopping
// after limit of them (no limit when limit <= 0). It returns the offset just
// past the last line consumed, so a reader following a growing log, or
// walking a large one a batch at a time, resumes there without re-reading.
// A torn trailing line is not consumed; the next call reads it once complete.
func ReadEntryBatch(path string, from int64, afterSeq uint64, limit int) ([]Entry, int64, error) {
	file, err := openLog(path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(from, io.SeekStart); err != nil {
		return nil, 0, err
	}
	reader, err := committedLogReader(file, from)
	if err != nil {
		return nil, from, err
	}
	var entries []Entry
	next, err := scanEntries(reader, from, func(entry Entry) bool {
		if entry.Seq <= afterSeq {
			return true
		}
		entries = append(entries, entry)
		return limit <= 0 || len(entries) < limit
	})
	if err != nil {
		return nil, next, err
	}
	return entries, next, nil
}

// ScanEntries calls fn with each entry of the run log at path, in order,
// without holding the log in memory. Scanning stops early when fn returns
// false.
func ScanEntries(path string, fn func(Entry) bool) error {
	file, err := openLog(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	reader, err := committedLogReader(file, 0)
	if err != nil {
		return err
	}
	_, err = scanEntries(reader, 0, fn)
	return err
}

// decodeEntries reads run log lines from r starting at byte offset start and
// returns the entries with Seq > afterSeq and the offset just past the last
// complete line; see scanEntries.
func decodeEntries(r io.Reader, afterSeq uint64, start int64) ([]Entry, int64, error) {
	var entries []Entry
	pos, err := scanEntries(r, start, func(entry Entry) bool {
		if entry.Seq > afterSeq {
			entries = append(entries, entry)
		}
		return true
	})
	if err != nil {
		return nil, pos, err
	}
	return entries, pos, nil
}

// scanEntries reads run log lines from r starting at byte offset start,
// calling fn with each entry until fn returns false, and returns the offset
// just past the last line consumed. An unparsable or oversized line is
// skipped, a torn trailing line is left unconsumed, and only an I/O failure
// is an error.
func scanEntries(r io.Reader, start int64, fn func(Entry) bool) (int64, error) {
	reader := bufio.NewReaderSize(r, 64<<10)
	pos := start
	for {
		line, err := readLogLine(reader)
		if line.complete {
			pos += line.bytes
			if entry, ok := decodeEntry(line.data); ok && !fn(entry) {
				return pos, nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return pos, nil
			}
			return pos, fmt.Errorf("read run log: %w", err)
		}
	}
}

// logLine keeps physical consumption separate from retained content. Even a
// discarded oversized line advances the cursor, but a torn line never does.
type logLine struct {
	data     []byte
	bytes    int64
	complete bool
}

func readLogLine(r *bufio.Reader) (logLine, error) {
	var line logLine
	keep := true
	for {
		frag, err := r.ReadSlice('\n')
		line.bytes += int64(len(frag))
		line.complete = len(frag) > 0 && frag[len(frag)-1] == '\n'
		if keep {
			if line.bytes > maxLogLineBytes {
				keep = false
				line.data = nil
			} else {
				line.data = append(line.data, frag...)
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if line.complete && keep {
			line.data = line.data[:len(line.data)-1]
		}
		return line, err
	}
}

// RenderText concatenates the text view of entries.
func RenderText(entries []Entry) string {
	var b strings.Builder
	for _, entry := range entries {
		b.WriteString(Render(entry))
	}
	return b.String()
}
