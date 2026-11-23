package runlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// TextView reads a run log's text view by byte offset straight from the file,
// without rendering the whole log into memory.
//
// Entries record the text offset where their rendered text begins, and those
// offsets only grow in file order, so the file is its own index: a read
// binary-searches the file for the entry holding the offset and renders
// forward from there. What a view holds is independent of the log's size.
//
// A view covers the committed, complete lines present at its last Refresh; a
// follower calls Refresh to take in lines appended since. A TextView is not
// safe for concurrent use.
//
// Offsets are the entries' recorded text offsets. A log the writer produced
// has no gaps between them; were a line unreadable, its text would be missing
// from the view rather than shifting what follows.
type TextView struct {
	file *os.File
	// end is the file offset just past the last complete line covered.
	end int64
	// size is the length of the text view up to end.
	size uint64

	// The previous read stopped inside the entry starting at file offset
	// cursorLine, whose text begins at cursorText. A sequential read resumes
	// there instead of searching again.
	cursorLine int64
	cursorText uint64
	cursorOK   bool
}

// textViewResumeWindow bounds how far ahead of the cursor a read may start
// and still scan forward from it rather than binary-search.
const textViewResumeWindow = 1 << 20

// textViewProbeBuffer is the read buffer of one binary-search probe; a probe
// only needs to reach the next line boundary and decode one line.
const textViewProbeBuffer = 4 << 10

// OpenTextView opens the text view of the run log at path.
func OpenTextView(path string) (*TextView, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	v := &TextView{file: file}
	if err := v.Refresh(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return v, nil
}

// Close closes the underlying file.
func (v *TextView) Close() error {
	return v.file.Close()
}

// Size is the length of the text view as of the last Refresh.
func (v *TextView) Size() uint64 {
	return v.size
}

// Refresh extends the view to the complete lines committed since the last
// Refresh. A log cut back by checkpoint recovery is taken in from scratch.
func (v *TextView) Refresh() error {
	limit, err := committedLogBytes(v.file)
	if err != nil {
		return err
	}
	if limit < v.end {
		v.end, v.size, v.cursorOK = 0, 0, false
	}
	if limit == v.end {
		return nil
	}
	newEnd, err := lastLineEnd(v.file, v.end, limit)
	if err != nil || newEnd == v.end {
		return err
	}
	err = scanLinesBackward(v.file, v.end, newEnd, func(_ int64, line []byte) bool {
		var entry Entry
		if line == nil || json.Unmarshal(line, &entry) != nil {
			return true
		}
		v.size = entry.TextOffset + uint64(len(Render(entry)))
		return false
	})
	if err != nil {
		return err
	}
	v.end = newEnd
	return nil
}

// ReadAt reads the text view at byte offset off. It returns io.EOF when p
// reaches past the end of the view.
func (v *TextView) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("run log text view: negative offset")
	}
	at := uint64(off)
	if at >= v.size {
		return 0, io.EOF
	}
	var start int64
	if v.cursorOK && v.cursorText <= at && at-v.cursorText <= textViewResumeWindow {
		start = v.cursorLine
	} else {
		var err error
		if start, err = v.seek(at); err != nil {
			return 0, err
		}
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(v.file, start, v.end-start), 64<<10)
	pos := start
	n := 0
	for n < len(p) {
		line, readErr := readLogLine(reader)
		lineStart := pos
		pos += line.bytes
		var entry Entry
		if line.complete && line.data != nil && json.Unmarshal(line.data, &entry) == nil {
			rendered := Render(entry)
			if rendered != "" && entry.TextOffset+uint64(len(rendered)) > at {
				skip := uint64(0)
				if entry.TextOffset < at {
					skip = at - entry.TextOffset
				}
				copied := copy(p[n:], rendered[skip:])
				n += copied
				at += uint64(copied)
				v.cursorLine, v.cursorText, v.cursorOK = lineStart, entry.TextOffset, true
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return n, fmt.Errorf("read run log: %w", readErr)
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// TailOffset returns the text offset where the last lines lines of the view
// begin, ignoring a trailing newline, or 0 when the view has fewer lines.
// It reads the log backward and stops once it has seen enough.
func (v *TextView) TailOffset(lines int) (uint64, error) {
	if lines <= 0 || v.size == 0 {
		return v.size, nil
	}
	seen := 0
	var found uint64
	ok := false
	err := scanLinesBackward(v.file, 0, v.end, func(_ int64, line []byte) bool {
		var entry Entry
		if line == nil || json.Unmarshal(line, &entry) != nil {
			return true
		}
		rendered := Render(entry)
		for i := len(rendered) - 1; i >= 0; i-- {
			position := entry.TextOffset + uint64(i)
			if rendered[i] != '\n' || position == v.size-1 {
				continue
			}
			seen++
			if seen == lines {
				found, ok = position+1, true
				return false
			}
		}
		return true
	})
	if err != nil || !ok {
		return 0, err
	}
	return found, nil
}

// seek returns the file offset of the last entry whose text starts at or
// before at, or 0 when there is none.
func (v *TextView) seek(at uint64) (int64, error) {
	lo, hi := int64(0), v.end
	best := int64(0)
	for lo < hi {
		mid := lo + (hi-lo)/2
		start, next, textOffset, ok, err := v.lineFrom(mid, hi)
		if err != nil {
			return 0, err
		}
		switch {
		case !ok:
			hi = mid
		case textOffset <= at:
			best, lo = start, next
		default:
			hi = mid
		}
	}
	return best, nil
}

// lineFrom finds the first readable entry line starting in [pos, hi) and
// returns its start, the offset just past it and its text offset.
func (v *TextView) lineFrom(pos, hi int64) (start, next int64, textOffset uint64, ok bool, err error) {
	from := max(pos-1, 0)
	reader := bufio.NewReaderSize(io.NewSectionReader(v.file, from, v.end-from), textViewProbeBuffer)
	at := from
	if pos > 0 {
		// The byte before pos tells whether pos starts a line; skip to the
		// end of the line holding it.
		for {
			frag, readErr := reader.ReadSlice('\n')
			at += int64(len(frag))
			if errors.Is(readErr, bufio.ErrBufferFull) {
				continue
			}
			if readErr != nil {
				return 0, 0, 0, false, ignoreEOF(readErr)
			}
			break
		}
	}
	for at < hi {
		line, readErr := readLogLine(reader)
		lineStart := at
		at += line.bytes
		var header struct {
			TextOffset uint64 `json:"text_offset"`
		}
		if line.complete && line.data != nil && json.Unmarshal(line.data, &header) == nil {
			return lineStart, at, header.TextOffset, true, nil
		}
		if readErr != nil {
			return 0, 0, 0, false, ignoreEOF(readErr)
		}
	}
	return 0, 0, 0, false, nil
}

func ignoreEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return nil
	}
	return fmt.Errorf("read run log: %w", err)
}

// lastLineEnd returns the offset just past the last newline in [lo, hi), or
// lo when there is none: a torn trailing line is not part of the log yet.
func lastLineEnd(r io.ReaderAt, lo, hi int64) (int64, error) {
	buf := make([]byte, 64<<10)
	for pos := hi; pos > lo; {
		blockStart := max(lo, pos-int64(len(buf)))
		block := buf[:pos-blockStart]
		if _, err := r.ReadAt(block, blockStart); err != nil && !errors.Is(err, io.EOF) {
			return lo, fmt.Errorf("read run log: %w", err)
		}
		if i := bytes.LastIndexByte(block, '\n'); i >= 0 {
			return blockStart + int64(i) + 1, nil
		}
		pos = blockStart
	}
	return lo, nil
}

// scanLinesBackward calls fn for each line in [lo, hi), last line first, with
// the line's start and content without its newline. lo must start a line and
// hi must be lo or just past a newline. A line longer than maxLogLineBytes is
// passed as nil. Scanning stops when fn returns false.
func scanLinesBackward(r io.ReaderAt, lo, hi int64, fn func(start int64, line []byte) bool) error {
	if hi <= lo {
		return nil
	}
	buf := make([]byte, 64<<10)
	// parts holds the pieces of the current line read so far, last piece
	// first; size counts its bytes even once they are no longer kept.
	var parts [][]byte
	var size int64
	add := func(piece []byte) {
		size += int64(len(piece))
		if size > maxLogLineBytes {
			parts = nil
			return
		}
		parts = append(parts, bytes.Clone(piece))
	}
	emit := func(start int64) bool {
		var line []byte
		if size <= maxLogLineBytes {
			line = make([]byte, 0, size)
			for i := len(parts) - 1; i >= 0; i-- {
				line = append(line, parts[i]...)
			}
		}
		parts, size = parts[:0], 0
		return fn(start, line)
	}
	// Leave out the newline ending the last line.
	pos := hi - 1
	for pos > lo {
		blockStart := max(lo, pos-int64(len(buf)))
		block := buf[:pos-blockStart]
		if _, err := r.ReadAt(block, blockStart); err != nil && !errors.Is(err, io.EOF) {
			return fmt.Errorf("read run log: %w", err)
		}
		end := len(block)
		for i := end - 1; i >= 0; i-- {
			if block[i] != '\n' {
				continue
			}
			add(block[i+1 : end])
			if !emit(blockStart + int64(i) + 1) {
				return nil
			}
			end = i
		}
		add(block[:end])
		pos = blockStart
	}
	emit(lo)
	return nil
}
