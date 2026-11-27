package runlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// crash stops w the way a daemon dying does: nothing is flushed or
// committed, and the file keeps whatever was already written.
func crash(w *Writer) {
	_ = w.Abandon()
}

func mustCommit(t *testing.T, w *Writer, src uint64, state string) {
	t.Helper()
	var raw json.RawMessage
	if state != "" {
		raw = json.RawMessage(state)
	}
	ok, err := w.Commit(src, raw)
	if err != nil || !ok {
		t.Fatalf("commit at %d = %v, %v", src, ok, err)
	}
}

func TestCommitRollsBackWhatItDidNotCover(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendInitialPrompt("go"); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 0, "")
	w.SetSourceOffset(40)
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"call","name":"shell","command":"pwd"}`)); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 40, `{"plain":true}`)
	seq := w.Seq()
	// A crash can leave several complete entries, not just a torn last line.
	w.SetSourceOffset(80)
	if err := w.AppendAgentEvent([]byte(`{"kind":"usage","scope":"turn","inputTokens":9}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"answer\n"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EndTurn("completed", "answer", nil); err != nil {
		t.Fatal(err)
	}
	crash(w)

	w, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	resume := w.Resume()
	if resume == nil || resume.Offset != 40 || string(resume.State) != `{"plain":true}` || w.Seq() != seq {
		t.Fatalf("resume=%+v seq=%d, want offset 40 and seq %d", resume, w.Seq(), seq)
	}
	// The caller cannot mutate what the writer keeps.
	resume.State[0] = 'x'
	if !json.Valid(w.Resume().State) {
		t.Fatal("shared resume state")
	}
	// Replaying the journal from 40 appends the same effects once.
	w.SetSourceOffset(80)
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"call","name":"shell","command":"pwd"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"usage","scope":"turn","inputTokens":9}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"answer\n"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EndTurn("completed", "answer", nil); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 80, `{}`)
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	summary := Summarize(entries)
	if summary.InputTokens != 9 || summary.ToolCalls != 1 || strings.Count(RenderText(entries), "answer") != 1 {
		t.Fatalf("summary=%+v log=%+v", summary, entries)
	}
	for i, e := range entries {
		if e.Seq != uint64(i+1) {
			t.Fatalf("entry %d has seq %d: commits must not take sequence numbers", i, e.Seq)
		}
	}
}

func TestCommitRejectsInvalidState(t *testing.T) {
	w, _ := openTest(t, Options{})
	if _, err := w.Commit(9, json.RawMessage(`broken`)); err == nil {
		t.Fatal("accepted invalid state")
	}
	if w.Resume() != nil {
		t.Fatal("a failed commit was recorded")
	}
}

func TestCommitWaitsForInputThatIsNotAnEntryYet(t *testing.T) {
	for name, held := range map[string]string{
		"text without a newline":     `{"kind":"text_delta","text":"hello "}`,
		"call waiting for arguments": `{"kind":"tool_call","id":"pending","name":"shell","status":"in_progress"}`,
	} {
		w, path := openTest(t, Options{})
		mustCommit(t, w, 0, "")
		w.SetSourceOffset(10)
		if err := w.AppendAgentEvent([]byte(held)); err != nil {
			t.Fatal(err)
		}
		if ok, err := w.Commit(10, nil); err != nil || ok {
			t.Fatalf("%s: commit = %v, %v; a restart could not rebuild it", name, ok, err)
		}
		w.SetSourceOffset(20)
		for _, event := range []string{`{"kind":"text_delta","text":"world\n"}`, `{"kind":"tool_result","id":"pending","output":"done"}`} {
			if err := w.AppendAgentEvent([]byte(event)); err != nil {
				t.Fatal(err)
			}
		}
		mustCommit(t, w, 20, "")
		if entries, err := ReadEntries(path, 0); err != nil || len(entries) == 0 {
			t.Fatalf("%s: entries=%+v err=%v", name, entries, err)
		}
	}
}

func TestTurnEndForgetsCallsThatNeverGotArguments(t *testing.T) {
	w, _ := openTest(t, Options{})
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"pending","name":"shell","status":"in_progress"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.EndTurn("stop", "", nil); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 5, "")
}

func TestCommitPublishesOnlyWhatItCovers(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	var published []Entry
	w, err := Open(path, Options{OnAppend: func(e Entry, _ string) { published = append(published, e) }})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	mustCommit(t, w, 0, "")
	w.SetSourceOffset(5)
	if err := w.AppendOutput("stdout", "first"); err != nil {
		t.Fatal(err)
	}
	entries, next, err := ReadEntryBatch(path, 0, 0, 0)
	if err != nil || len(entries) != 0 || len(published) != 0 {
		t.Fatalf("uncommitted entries visible: %+v published=%+v err=%v", entries, published, err)
	}
	mustCommit(t, w, 5, "")
	entries, next, err = ReadEntryBatch(path, next, 0, 0)
	if err != nil || len(entries) != 1 || len(published) != 1 || entries[0].Data != "first" {
		t.Fatalf("commit invisible: %+v published=%+v err=%v", entries, published, err)
	}
	w.SetSourceOffset(9)
	if err := w.AppendOutput("stdout", "uncommitted tail"); err != nil {
		t.Fatal(err)
	}
	tail, after, err := ReadEntryBatch(path, next, 0, 0)
	if err != nil || len(tail) != 0 || after != next || len(published) != 1 {
		t.Fatalf("tail visible: %+v next=%d after=%d err=%v", tail, next, after, err)
	}
}

func TestLogWithoutCommitsIsWhollyVisible(t *testing.T) {
	w, path := openTest(t, Options{})
	if err := w.AppendOutput("stdout", "step output\n"); err != nil {
		t.Fatal(err)
	}
	if entries, err := ReadEntries(path, 0); err != nil || len(entries) != 1 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
	crash(w)
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.Resume() != nil || reopened.Seq() != 1 {
		t.Fatalf("resume=%+v seq=%d", reopened.Resume(), reopened.Seq())
	}
}

func TestCloseCommitsTheFinishedLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	w, err := Open(path, Options{MaxBytes: 4 << 10})
	if err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 0, `{"n":1}`)
	w.SetSourceOffset(10)
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"partial"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", strings.Repeat("x", 8<<10)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"tail"}`)); err != nil {
		t.Fatal(err)
	}
	// The run is over: Close covers the held text and the cap's marker.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	summary, err := SummarizeLog(path)
	if err != nil || !summary.Truncated {
		t.Fatalf("summary after close = %+v err=%v", summary, err)
	}
	before, _ := ReadEntries(path, 0)
	w, err = Open(path, Options{MaxBytes: 4 << 10})
	if err != nil {
		t.Fatal(err)
	}
	if r := w.Resume(); r == nil || r.Offset != 10 || string(r.State) != `{"n":1}` {
		t.Fatalf("resume after close = %+v", r)
	}
	_ = w.Close()
	after, _ := ReadEntries(path, 0)
	if len(before) == 0 || len(after) != len(before) || after[len(after)-1].Type != EntryTruncated {
		t.Fatalf("entries before reopen = %d, after = %d", len(before), len(after))
	}
}

func TestRecordsCountOnceCommitted(t *testing.T) {
	w, path := openTest(t, Options{})
	mustCommit(t, w, 0, "")
	w.SetSourceOffset(10)
	if err := w.AppendRecord("exit", json.RawMessage(`{"code":0}`)); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 10, "")
	w.SetSourceOffset(20)
	if err := w.AppendRecord("result", json.RawMessage(`{"ok":true}`)); err != nil {
		t.Fatal(err)
	}
	crash(w)
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	resume := reopened.Resume()
	if resume == nil || string(resume.Records["exit"]) != `{"code":0}` || resume.Records["result"] != nil {
		t.Fatalf("resume=%+v", resume)
	}
	// Records are invisible to readers.
	if entries, err := ReadEntries(path, 0); err != nil || len(entries) != 0 {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}

func TestIdleRecordsNeedNoCommit(t *testing.T) {
	w, path := openTest(t, Options{})
	mustCommit(t, w, 0, "")
	size := func() int64 {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		return info.Size()
	}
	start := size()
	// Records that change nothing replay to the same state from the last
	// commit, so they add no line until the journal has moved far enough.
	if ok, err := w.Commit(100, nil); err != nil || !ok || size() != start {
		t.Fatalf("idle commit = %v, %v, grew %d bytes", ok, err, size()-start)
	}
	mustCommit(t, w, idleCommitBytes, "")
	if size() == start {
		t.Fatal("a long idle stretch was not committed")
	}
	// Committing the same position again is a no-op.
	grown := size()
	mustCommit(t, w, idleCommitBytes, "")
	if size() != grown {
		t.Fatal("repeated commit wrote a line")
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
	mustCommit(t, w, 100, "")
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err = Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = w.Close() }()
	w.SetSourceOffset(110)
	if err := w.AppendOutput("stdout", "after gap"); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 110, "")
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || !entries[0].SourceGap || entries[0].DroppedBytes != 90 || entries[1].Data != "after gap" {
		t.Fatalf("entries=%+v", entries)
	}
}

func TestCapStaysReachedAcrossARestart(t *testing.T) {
	w, path := openTest(t, Options{MaxBytes: 600})
	mustCommit(t, w, 0, "")
	w.SetSourceOffset(10)
	for range 20 {
		if err := w.AppendOutput("stdout", strings.Repeat("x", 40)+"\n"); err != nil {
			t.Fatal(err)
		}
	}
	mustCommit(t, w, 10, "")
	crash(w)
	reopened, err := Open(path, Options{MaxBytes: 600})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	// A short line would still fit, but the run already passed its cap.
	reopened.SetSourceOffset(20)
	if err := reopened.AppendOutput("stdout", "x\n"); err != nil {
		t.Fatal(err)
	}
	if !reopened.truncated || reopened.dropped == 0 {
		t.Fatalf("cap forgotten: truncated=%v dropped=%d", reopened.truncated, reopened.dropped)
	}
}

func TestSettleLetsTheLastRecordCommit(t *testing.T) {
	w, path := openTest(t, Options{})
	mustCommit(t, w, 0, "")
	w.SetSourceOffset(10)
	for _, event := range []string{
		`{"kind":"text_delta","text":"no newline"}`,
		`{"kind":"tool_call","id":"pending","name":"shell","status":"in_progress"}`,
	} {
		if err := w.AppendAgentEvent([]byte(event)); err != nil {
			t.Fatal(err)
		}
	}
	if ok, _ := w.Commit(10, nil); ok {
		t.Fatal("committed held input")
	}
	if err := w.Settle(); err != nil {
		t.Fatal(err)
	}
	mustCommit(t, w, 10, "")
	entries, err := ReadEntries(path, 0)
	if err != nil || len(entries) != 1 || entries[0].Text != "no newline" {
		t.Fatalf("entries=%+v err=%v", entries, err)
	}
}
