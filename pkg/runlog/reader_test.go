package runlog

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadEntryBatchAdvancesPastDiscardedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	data := strings.Repeat("x", int(maxLogLineBytes)+1) + "\n" +
		"invalid JSON\n" + `{"seq":2,"type":"text","text":"after"}` + "\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, next, err := ReadEntryBatch(path, 0, 0, 0)
	if err != nil || len(entries) != 1 || entries[0].Text != "after" || next != int64(len(data)) {
		t.Fatalf("entries=%v next=%d want=%d err=%v", entries, next, len(data), err)
	}
	entries, same, err := ReadEntryBatch(path, next, 0, 0)
	if err != nil || len(entries) != 0 || same != next {
		t.Fatalf("unchanged read: entries=%v next=%d err=%v", entries, same, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"seq":3,"type":"text","text":"new"}` + "\n"); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	entries, _, err = ReadEntryBatch(path, next, 0, 0)
	if err != nil || len(entries) != 1 || entries[0].Seq != 3 {
		t.Fatalf("appended read: entries=%v err=%v", entries, err)
	}
}

func TestDecodeEntriesLeavesTornLinesUnconsumed(t *testing.T) {
	for _, torn := range []string{`{"seq":2`, strings.Repeat("x", int(maxLogLineBytes)+1)} {
		prefix := `{"seq":1,"type":"text","text":"before"}` + "\n"
		_, next, err := decodeEntries(strings.NewReader(prefix+torn), 0, 100)
		if err != nil || next != int64(100+len(prefix)) {
			t.Fatalf("torn length=%d next=%d err=%v", len(torn), next, err)
		}
	}
}

type failingLogReader struct{ err error }

func (r failingLogReader) Read([]byte) (int, error) { return 0, r.err }

func TestDecodeEntriesPropagatesIOFailure(t *testing.T) {
	failure := errors.New("read failed")
	_, _, err := decodeEntries(io.MultiReader(strings.NewReader("partial"), failingLogReader{failure}), 0, 0)
	if !errors.Is(err, failure) {
		t.Fatalf("error=%v, want read failure", err)
	}
}
