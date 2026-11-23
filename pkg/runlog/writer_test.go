package runlog

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWriterAndReaderAgreeOnFinalLineBound(t *testing.T) {
	for _, extra := range []int{0, 1} {
		w, path := openTest(t, Options{})
		if _, err := w.AppendInitialPrompt("go"); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(Entry{Seq: 2, Time: time.Unix(100, 0).UTC(), Turn: 2, Type: EntryUser})
		if err != nil {
			t.Fatal(err)
		}
		// Include the text key and the physical newline.
		size := int(maxLogLineBytes) - len(encoded) - len(`,"text":""`) - 1 + extra
		entry, err := w.AppendUser(strings.Repeat("a", size))
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		entries, err := ReadEntries(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		if extra == 0 && (entry.Seq != 2 || len(entries) != 2 || len(entries[1].Text) != size) {
			t.Fatalf("exact bound: seq=%d entries=%d", entry.Seq, len(entries))
		}
		if extra == 1 && (entry.Seq != 0 || len(entries) != 1) {
			t.Fatalf("past bound: seq=%d entries=%d", entry.Seq, len(entries))
		}
	}
}

func TestRecoverySkipsOversizedLinesAndTruncatesTornTail(t *testing.T) {
	w, path := openTest(t, Options{})
	if err := w.AppendOutput("stdout", "before"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := f.WriteString(strings.Repeat("x", int(maxLogLineBytes)+1) + "\n" + `{"seq":99`)
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	w, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "after"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, next, err := ReadEntryBatch(path, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[1].Seq != 2 || next != info.Size() || RenderText(entries) != "beforeafter" {
		t.Fatalf("entries=%+v next=%d size=%d", entries, next, info.Size())
	}
}
