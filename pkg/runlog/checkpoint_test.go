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
	entries, err = ReadEntries(path, 0)
	if err != nil || len(entries) != 3 || entries[0].Type != EntryText || entries[0].Text != "hello world" || entries[1].Tool == nil || entries[1].Tool.Name != "shell" || entries[2].Type != EntryToolResult {
		t.Fatalf("lost merger state: %+v err=%v", entries, err)
	}
}
