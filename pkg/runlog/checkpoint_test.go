package runlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckpointRecoversOnlyCommittedJournalEffects(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendInitialPrompt("go"); err != nil {
		t.Fatal(err)
	}
	w.SetSourceOffset(40)
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"call","name":"shell","command":"pwd"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(40, json.RawMessage(`{"turn":1}`)); err != nil {
		t.Fatal(err)
	}
	seq := w.Seq()
	// A crash can leave several complete entries, not just a torn last line.
	if err := w.AppendAgentEvent([]byte(`{"kind":"usage","scope":"turn","inputTokens":9}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"answer"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EndTurn("completed", "answer"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	recovery := w.Recovery()
	if recovery == nil || recovery.SourceOffset != 40 || string(recovery.State) != `{"turn":1}` || w.Seq() != seq {
		t.Fatalf("recovery=%+v seq=%d", recovery, w.Seq())
	}
	// The caller cannot mutate the stored snapshot.
	recovery.State[0] = 'x'
	if !json.Valid(w.Recovery().State) {
		t.Fatal("shared recovery state")
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"call","name":"shell","command":"pwd"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"usage","scope":"turn","inputTokens":9}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"answer"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EndTurn("completed", "answer"); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(80, json.RawMessage(`{"turn":1}`)); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(entries)
	if summary.InputTokens != 9 || summary.ToolCalls != 1 || strings.Count(RenderText(entries), "answer") != 1 {
		t.Fatalf("summary=%+v log=%+v", summary, entries)
	}
	for _, e := range entries[1:] {
		if e.SourceOffset != 40 {
			t.Fatalf("missing source offset: %+v", e)
		}
	}
}

func TestCheckpointRejectsInvalidOrMissingLogPrefix(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(0, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(9, json.RawMessage(`broken`)); err == nil {
		t.Fatal("accepted invalid state")
	}
	if w.Recovery().SourceOffset != 0 {
		t.Fatal("failed checkpoint advanced cursor")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpointPath(path), []byte("{\"v\":1,\"src_offset\":9,\"log_bytes\":100}\n{\"state\":{},\"merger\":{}}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Options{}); err == nil {
		t.Fatal("accepted checkpoint beyond log")
	}
}

func TestJournalGapDoesNotActivateHostLogCap(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	w.SetSourceOffset(100)
	if err := w.AppendJournalGap(10, 100); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if err := w.AppendOutput("stdout", "after gap"); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !entries[0].SourceGap || entries[0].DroppedBytes != 90 || entries[1].Data != "after gap" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestCheckpointPublishesOnlyCommittedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	var published []Entry
	w, err := Open(path, Options{OnAppend: func(e Entry, _ string) { published = append(published, e) }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if err := w.Checkpoint(0, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "first"); err != nil {
		t.Fatal(err)
	}
	entries, next, err := ReadEntryBatch(path, 0, 0, 0)
	if err != nil || len(entries) != 0 || next != 0 || len(published) != 0 {
		t.Fatalf("uncommitted entries visible: %+v next=%d published=%+v err=%v", entries, next, published, err)
	}
	if err := w.Checkpoint(5, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	entries, next, err = ReadEntryBatch(path, next, 0, 0)
	if err != nil || len(entries) != 1 || len(published) != 1 || entries[0].Data != "first" {
		t.Fatalf("commit invisible: %+v published=%+v err=%v", entries, published, err)
	}
	if err := w.AppendOutput("stdout", "uncommitted tail"); err != nil {
		t.Fatal(err)
	}
	tail, after, err := ReadEntryBatch(path, next, 0, 0)
	if err != nil || len(tail) != 0 || after != next || len(published) != 1 {
		t.Fatalf("tail visible: %+v next=%d after=%d err=%v", tail, next, after, err)
	}
}

func TestCheckpointPreservesCoalescedTextAndUnfinishedTool(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"hello "}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"pending","name":"shell","status":"in_progress"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(100, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil || len(entries) != 0 {
		t.Fatalf("journal checkpoint broke paragraph coalescing: %+v err=%v", entries, err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"world"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_result","id":"pending","output":"done"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(150, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// Close commits the text it flushes, so the paragraph continues in a new
	// entry; the announced tool survives in the merger state.
	entries, err = ReadEntries(path, 0)
	if err != nil || len(entries) != 4 || entries[0].Text+entries[1].Text != "hello world" || entries[2].Tool == nil || entries[2].Tool.Name != "shell" || entries[3].Type != EntryToolResult {
		t.Fatalf("lost merger state: %+v err=%v", entries, err)
	}
}

func TestCloseCommitsWhatItWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{MaxBytes: 4 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"partial"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", strings.Repeat("x", 8<<10)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"tail"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(10, json.RawMessage(`{"n":1}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Readers see the held text and the cap's marker, and a reopen keeps them.
	summary, err := SummarizeLog(path)
	if err != nil || !summary.Truncated {
		t.Fatalf("summary after close = %+v err=%v", summary, err)
	}
	before, _ := ReadEntries(path, 0)
	w, err = Open(path, Options{MaxBytes: 4 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if r := w.Recovery(); r == nil || r.SourceOffset != 10 || string(r.State) != `{"n":1}` {
		t.Fatalf("recovery after close = %+v", r)
	}
	_ = w.Close()
	after, _ := ReadEntries(path, 0)
	if len(before) == 0 || len(after) != len(before) || after[len(after)-1].Type != EntryTruncated {
		t.Fatalf("entries before reopen = %d, after = %d", len(before), len(after))
	}
}

func TestCloseLeavesTheConsumersUncommittedWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Checkpoint(1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// A record the consumer never committed is replayed after a restart, so
	// Close must not commit it.
	if err := w.AppendOutput("stdout", "uncommitted\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if entries, _ := ReadEntries(path, 0); len(entries) != 0 {
		t.Fatalf("uncommitted entries became visible: %+v", entries)
	}
}

func TestCloseLeavesUncommittedStateThatWroteNoBytes(t *testing.T) {
	for name, c := range map[string]struct {
		opts        Options
		uncommitted func(w *Writer) error
	}{
		// A delta without a newline stays in the merger.
		"held text": {Options{}, func(w *Writer) error {
			return w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"held"}`))
		}},
		// Past the cap, output only counts toward the next marker.
		"dropped output": {Options{MaxBytes: 1}, func(w *Writer) error { return w.AppendOutput("stdout", "dropped\n") }},
	} {
		path := filepath.Join(t.TempDir(), FileName)
		opts, uncommitted := c.opts, c.uncommitted
		w, err := Open(path, opts)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.AppendInitialPrompt("go"); err != nil {
			t.Fatal(err)
		}
		if err := w.Checkpoint(1, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if err := uncommitted(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		// The consumer replays the record from the committed offset.
		w, err = Open(path, opts)
		if err != nil {
			t.Fatal(err)
		}
		if err := uncommitted(w); err != nil {
			t.Fatal(err)
		}
		if err := w.Checkpoint(2, json.RawMessage(`{}`)); err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		entries, err := ReadEntries(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		text := RenderText(entries)
		if name == "held text" && strings.Count(text, "held") != 1 {
			t.Fatalf("%s: replayed text repeated: %q", name, text)
		}
		var dropped uint64
		for _, e := range entries {
			if e.Type == EntryTruncated {
				dropped += e.DroppedBytes
			}
		}
		if name == "dropped output" && dropped != uint64(len("dropped\n")) {
			t.Fatalf("%s: markers count %d dropped bytes, want %d", name, dropped, len("dropped\n"))
		}
	}
}
