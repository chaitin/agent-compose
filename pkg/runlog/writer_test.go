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
		// One byte past the bound, the entry is kept with its text cut down
		// rather than dropped.
		if extra == 1 && (entry.Seq != 2 || len(entries) != 2 || len(entries[1].Text) >= size ||
			!strings.Contains(entries[1].Text, "bytes omitted")) {
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

func TestFitLineShrinksEveryLongField(t *testing.T) {
	big := strings.Repeat("\x01", int(maxLogLineBytes)/4) // escapes to six bytes each
	changes := make([]FileChange, 100000)
	for i := range changes {
		changes[i] = FileChange{Path: "dir/" + strings.Repeat("p", 200), Kind: "update"}
	}
	original := &Tool{ID: "t", Name: "Bash", Command: big, Output: big, Error: big,
		Input: json.RawMessage(`"` + strings.Repeat("i", int(maxLogLineBytes)) + `"`), Changes: changes}
	line, err := fitLine(Entry{Type: EntryToolCall, Tool: original, Usage: &Usage{Model: big}})
	if err != nil {
		t.Fatal(err)
	}
	var entry Entry
	if int64(len(line))+1 > maxLogLineBytes || json.Unmarshal(line, &entry) != nil {
		t.Fatalf("line: %d bytes, valid=%v", len(line), json.Valid(line))
	}
	if len(original.Command) != len(big) || len(original.Changes) != len(changes) {
		t.Fatal("fitLine modified the caller's tool")
	}
	if !strings.Contains(entry.Tool.Command, "bytes omitted") || len(entry.Tool.Changes) >= len(changes) {
		t.Fatalf("not shrunk: command %d bytes, %d changes", len(entry.Tool.Command), len(entry.Tool.Changes))
	}
}

func TestSplitTextKeepsRunesWhole(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want []string
	}{
		{"abc", 5, []string{"abc"}},
		{"abcdef", 2, []string{"ab", "cd", "ef"}},
		{"aé", 2, []string{"a", "é"}},
		{"日本", 4, []string{"日", "本"}},
		// No rune start within reach: cut at the byte bound.
		{"\x80\x80\x80\x80\x80", 2, []string{"\x80\x80", "\x80\x80", "\x80"}},
	} {
		got := splitText(c.in, c.n)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Fatalf("splitText(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
	if got := runePrefix("abc", 0); got != "" {
		t.Fatalf("runePrefix n=0 = %q", got)
	}
}
