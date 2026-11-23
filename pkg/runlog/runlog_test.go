package runlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTest(t *testing.T, opts Options) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs", "run-1", FileName)
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Unix(100, 0) }
	}
	w, err := Open(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w, path
}

func mustEvent(t *testing.T, w *Writer, raw string) {
	t.Helper()
	if err := w.AppendAgentEvent([]byte(raw)); err != nil {
		t.Fatalf("AppendAgentEvent(%s): %v", raw, err)
	}
}

func types(entries []Entry) string {
	parts := make([]string, 0, len(entries))
	for _, e := range entries {
		parts = append(parts, string(e.Type))
	}
	return strings.Join(parts, ",")
}

func TestClaudeToolCallIsRecordedOnceWithItsArguments(t *testing.T) {
	w, path := openTest(t, Options{})
	if _, err := w.AppendInitialPrompt("list files"); err != nil {
		t.Fatal(err)
	}
	// claude: announce without input, re-emit once arguments finish, then result.
	mustEvent(t, w, `{"kind":"text_delta","text":"Let me look"}`)
	mustEvent(t, w, `{"kind":"tool_call","id":"t1","name":"Bash","toolKind":"execute","status":"in_progress"}`)
	mustEvent(t, w, `{"kind":"tool_call","id":"t1","name":"Bash","toolKind":"execute","status":"completed","input":{"command":"ls"}}`)
	mustEvent(t, w, `{"kind":"tool_result","id":"t1","ok":true,"output":"a\nb\n"}`)
	mustEvent(t, w, `{"kind":"text_delta","text":"Two files."}`)
	if _, err := w.EndTurn("stop", "Two files."); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := types(entries); got != "user,text,tool_call,tool_result,text,turn_end" {
		t.Fatalf("entry types = %s", got)
	}
	call := entries[2].Tool
	if call.ID != "t1" || call.Name != "Bash" || string(call.Input) != `{"command":"ls"}` {
		t.Fatalf("tool call = %#v", call)
	}
	if res := entries[3].Tool; res.OK == nil || !*res.OK || res.Output != "a\nb\n" {
		t.Fatalf("tool result = %#v", res)
	}
	for i, e := range entries {
		if e.Seq != uint64(i+1) || e.Turn != 1 {
			t.Fatalf("entry %d seq=%d turn=%d", i, e.Seq, e.Turn)
		}
	}
	text := RenderText(entries)
	if text != "Let me look\n[tool:Bash]\n{\"command\":\"ls\"}\na\nb\nTwo files." {
		t.Fatalf("text view = %q", text)
	}
}

func TestCodexToolStatusResendsAreMerged(t *testing.T) {
	w, path := openTest(t, Options{})
	mustEvent(t, w, `{"kind":"tool_call","id":"c1","name":"shell","toolKind":"execute","status":"in_progress","command":"go test ./..."}`)
	mustEvent(t, w, `{"kind":"tool_call","id":"c1","name":"shell","toolKind":"execute","status":"in_progress","command":"go test ./..."}`)
	mustEvent(t, w, `{"kind":"tool_call","id":"c1","name":"shell","toolKind":"execute","status":"failed","command":"go test ./...","exitCode":1}`)
	mustEvent(t, w, `{"kind":"tool_result","id":"c1","ok":false,"output":"FAIL"}`)
	mustEvent(t, w, `{"kind":"tool_result","id":"c1","ok":false,"output":"FAIL"}`)
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	if got := types(entries); got != "tool_call,tool_result" {
		t.Fatalf("entry types = %s", got)
	}
	if res := entries[1].Tool; res.ExitCode == nil || *res.ExitCode != 1 || *res.OK {
		t.Fatalf("tool result = %#v", res)
	}
	if s := Summarize(entries); s.ToolCalls != 1 || s.ToolFailures != 1 {
		t.Fatalf("summary = %#v", s)
	}
}

func TestResultWithoutCompletedCallStillRecordsTheCall(t *testing.T) {
	w, path := openTest(t, Options{})
	mustEvent(t, w, `{"kind":"tool_call","id":"t9","name":"Read","status":"in_progress"}`)
	mustEvent(t, w, `{"kind":"tool_result","id":"t9","ok":true,"output":"x"}`)
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	if got := types(entries); got != "tool_call,tool_result" || entries[0].Tool.Name != "Read" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestTextIsCoalescedAtLineBoundaries(t *testing.T) {
	w, path := openTest(t, Options{})
	for _, delta := range []string{"Hel", "lo ", "wor", "ld\nSec", "ond"} {
		mustEvent(t, w, `{"kind":"text_delta","text":"`+strings.ReplaceAll(delta, "\n", `\n`)+`"}`)
	}
	entries, _ := ReadEntries(path, 0)
	if len(entries) != 1 || entries[0].Text != "Hello world\n" {
		t.Fatalf("before flush = %#v", entries)
	}
	_ = w.Close()
	entries, _ = ReadEntries(path, 0)
	if len(entries) != 2 || entries[1].Text != "Second" {
		t.Fatalf("after close = %#v", entries)
	}
}

func TestEndTurnAppendsOnlyTheUnstreamedTail(t *testing.T) {
	w, path := openTest(t, Options{})
	mustEvent(t, w, `{"kind":"text_delta","text":"The answer"}`)
	if _, err := w.EndTurn("stop", "The answer is 42."); err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendUser("again"); err != nil {
		t.Fatal(err)
	}
	mustEvent(t, w, `{"kind":"usage","scope":"turn","inputTokens":1}`)
	if _, err := w.EndTurn("stop", "Only final."); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	if got := types(entries); got != "text,text,turn_end,user,usage,text,turn_end" {
		t.Fatalf("entry types = %s", got)
	}
	if entries[1].Text != " is 42." || entries[5].Text != "Only final." || entries[5].Turn != 2 {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestReopenContinuesSequenceTurnAndToolDedup(t *testing.T) {
	w, path := openTest(t, Options{})
	_, _ = w.AppendUser("hi")
	mustEvent(t, w, `{"kind":"tool_call","id":"t1","name":"Bash","status":"completed","command":"ls"}`)
	_ = w.AppendOutput("stderr", "warn\n")
	_ = w.Close()

	// A torn line, as a crash mid-write would leave.
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"seq":99,"ty`)
	_ = f.Close()

	again, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustEvent(t, again, `{"kind":"tool_call","id":"t1","name":"Bash","status":"completed","command":"ls"}`)
	mustEvent(t, again, `{"kind":"tool_result","id":"t1","ok":true,"output":"a"}`)
	end, err := again.EndTurn("stop", "")
	if err != nil {
		t.Fatal(err)
	}
	_ = again.Close()
	if end.Seq != 5 || end.Turn != 1 {
		t.Fatalf("turn end after reopen = %#v", end)
	}
	entries, _ := ReadEntries(path, 0)
	if got := types(entries); got != "user,tool_call,output,tool_result,turn_end" {
		t.Fatalf("entry types = %s", got)
	}
}

func TestTextViewStartsInsideAnEntry(t *testing.T) {
	w, path := openTest(t, Options{})
	_ = w.AppendOutput("stdout", "abcdef")
	_ = w.AppendOutput("stdout", "ghi")
	_ = w.Close()
	v, err := OpenTextView(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = v.Close() }()
	if text := readAllFrom(t, v, 4, 64); text != "efghi" || v.Size() != 9 {
		t.Fatalf("text from 4 = %q, size %d", text, v.Size())
	}
	if text := readAllFrom(t, v, 9, 64); text != "" {
		t.Fatalf("text from end = %q", text)
	}
	entries, _ := ReadEntries(path, 0)
	if entries[1].TextOffset != 6 {
		t.Fatalf("second offset = %d", entries[1].TextOffset)
	}
}

func TestCapMarkerCountsTheBytesItDropped(t *testing.T) {
	w, path := openTest(t, Options{MaxBytes: 600})
	_, _ = w.AppendUser("go")
	for i := 0; i < 10; i++ {
		_ = w.AppendOutput("stdout", strings.Repeat("x", 100))
	}
	if _, err := w.EndTurn("stop", ""); err != nil {
		t.Fatal(err)
	}
	// A second stretch, dropped after the turn end, is closed by Close.
	_ = w.AppendOutput("stdout", strings.Repeat("y", 30))
	_ = w.AppendOutput("stdout", strings.Repeat("y", 12))
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	var kept uint64
	var markers []Entry
	for _, e := range entries {
		switch e.Type {
		case EntryOutput:
			kept += uint64(len(e.Data))
		case EntryTruncated:
			markers = append(markers, e)
		}
	}
	// Each marker sits where its text went missing: before the turn end it
	// precedes, and at the very end for the stretch Close closed.
	n := len(entries)
	if len(markers) != 2 || n < 3 || entries[n-3].Type != EntryTruncated || entries[n-2].Type != EntryTurnEnd ||
		entries[n-1].Type != EntryTruncated || !Summarize(entries).Truncated {
		t.Fatalf("entries = %s", types(entries))
	}
	if markers[0].DroppedBytes != 1000-kept || markers[1].DroppedBytes != 42 {
		t.Fatalf("dropped = %d, %d; kept %d of 1000", markers[0].DroppedBytes, markers[1].DroppedBytes, kept)
	}
}

func TestLogIsUncappedByDefault(t *testing.T) {
	w, path := openTest(t, Options{})
	chunk := strings.Repeat("z", 1<<20)
	for i := 0; i < 20; i++ {
		if err := w.AppendOutput("stdout", chunk); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	if len(entries) != 20 || Summarize(entries).Truncated {
		t.Fatalf("entries = %s", types(entries))
	}
}

func TestToolOutputKeepsHeadAndTail(t *testing.T) {
	w, path := openTest(t, Options{ToolOutputHalf: 4})
	mustEvent(t, w, `{"kind":"tool_call","id":"t","name":"Bash","status":"completed","command":"x"}`)
	mustEvent(t, w, `{"kind":"tool_result","id":"t","ok":false,"output":"HEAD-middle-middle-TAIL"}`)
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	res := entries[1].Tool
	if !strings.HasPrefix(res.Output, "HEAD") || !strings.HasSuffix(res.Output, "TAIL") || res.OutputBytes != 23 {
		t.Fatalf("tool result = %#v", res)
	}
	if got, n := truncateMiddle("ab中文cd", 3); !strings.HasPrefix(got, "ab\n") || n != 10 {
		t.Fatalf("truncateMiddle split a rune: %q", got)
	}
}

func TestSummaryUsesTheCoarsestUsageScope(t *testing.T) {
	w, path := openTest(t, Options{})
	mustEvent(t, w, `{"kind":"usage","scope":"step","inputTokens":10,"outputTokens":1}`)
	mustEvent(t, w, `{"kind":"usage","scope":"step","inputTokens":20,"outputTokens":2}`)
	mustEvent(t, w, `{"kind":"usage","scope":"run","inputTokens":30,"outputTokens":3,"costUsd":0.5}`)
	mustEvent(t, w, `{"kind":"error","severity":"warning","message":"retrying"}`)
	mustEvent(t, w, `{"kind":"error","severity":"error","message":"boom"}`)
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	s := Summarize(entries)
	if s.InputTokens != 30 || s.OutputTokens != 3 || s.CostUSD != 0.5 || s.LastError != "boom" || s.Turns != 1 {
		t.Fatalf("summary = %#v", s)
	}
}

func TestOnAppendSeesEntriesInOrder(t *testing.T) {
	var seen []string
	w, _ := openTest(t, Options{OnAppend: func(e Entry, rendered string) { seen = append(seen, string(e.Type)+":"+rendered) }})
	_ = w.AppendOutput("stdout", "a")
	_, _ = w.EndTurn("stop", "")
	if strings.Join(seen, "|") != "output:a|turn_end:" {
		t.Fatalf("seen = %v", seen)
	}
}

func TestPaths(t *testing.T) {
	if got := RunPath("/data", "r1"); got != "/data/runs/r1/run.log.jsonl" {
		t.Fatal(got)
	}
	if got := SchedulerCellPath("/data", "s", "r", "c"); got != "/data/schedulers/s/runs/r/cells/c.log.jsonl" {
		t.Fatal(got)
	}
}

func TestFollowUpUserInputIsShownOnItsOwnLine(t *testing.T) {
	w, path := openTest(t, Options{})
	_, _ = w.AppendInitialPrompt("first")
	mustEvent(t, w, `{"kind":"text_delta","text":"no newline"}`)
	_, _ = w.EndTurn("stop", "no newline")
	_, _ = w.AppendUser("second")
	_, _ = w.AppendUser("third\n")
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	if text := RenderText(entries); text != "no newline\nsecond\nthird\n" {
		t.Fatalf("text view = %q", text)
	}
}

func TestEndTurnWithoutAgentEventsKeepsTheAnswerOnlyInTheTurnEnd(t *testing.T) {
	// An older runtime sends no events and mirrors its transcript as output.
	w, path := openTest(t, Options{})
	_ = w.AppendOutput("stderr", "The answer is 42.\n")
	if _, err := w.EndTurn("stop", "The answer is 42."); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	if got := types(entries); got != "output,turn_end" || entries[1].FinalText != "The answer is 42." {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestOversizedToolInputIsClamped(t *testing.T) {
	w, path := openTest(t, Options{})
	if _, err := w.AppendInitialPrompt("big input"); err != nil {
		t.Fatal(err)
	}
	raw := `{"kind":"tool_call","id":"t1","name":"Bash","status":"completed","input":{"data":"` +
		strings.Repeat("y", maxToolInputBytes+100) + `"}}`
	if err := w.AppendAgentEvent([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var call *Entry
	for i := range entries {
		if entries[i].Type == EntryToolCall {
			call = &entries[i]
		}
	}
	if call == nil || call.Tool == nil {
		t.Fatal("tool call not recorded")
	}
	if len(call.Tool.Input) > maxToolInputBytes+1024 {
		t.Fatalf("tool input not clamped: %d bytes", len(call.Tool.Input))
	}
	if !json.Valid(call.Tool.Input) {
		t.Fatalf("clamped tool input is not valid JSON: %q", call.Tool.Input)
	}
}

func TestDecodeEntriesSkipsOversizedLine(t *testing.T) {
	// A single line past maxLogLineBytes must be skipped, not fail the read.
	var b strings.Builder
	b.WriteString(`{"seq":1,"type":"text","text":"before"}` + "\n")
	b.WriteString(strings.Repeat("x", int(maxLogLineBytes)+1))
	b.WriteByte('\n')
	b.WriteString(`{"seq":2,"type":"text","text":"after"}` + "\n")
	entries, next, err := decodeEntries(strings.NewReader(b.String()), 0, 0)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 2 || entries[0].Seq != 1 || entries[1].Seq != 2 || next == 0 {
		t.Fatalf("entries = %#v, next = %d", entries, next)
	}
}

func TestReadEntryBatchReadsOnlyAppended(t *testing.T) {
	path := filepath.Join(t.TempDir(), FileName)
	appendLine := func(s string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		if _, err := f.WriteString(s + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	appendLine(`{"seq":1,"type":"text","text":"a"}`)
	entries, next, err := ReadEntryBatch(path, 0, 0, 0)
	if err != nil || len(entries) != 1 || entries[0].Seq != 1 || next == 0 {
		t.Fatalf("first read: %#v next=%d err=%v", entries, next, err)
	}
	appendLine(`{"seq":2,"type":"text","text":"b"}`)
	entries, next2, err := ReadEntryBatch(path, next, 0, 0)
	if err != nil || len(entries) != 1 || entries[0].Seq != 2 || next2 <= next {
		t.Fatalf("second read: %#v next=%d err=%v", entries, next2, err)
	}
}

func TestOversizedEssentialEntryIsDropped(t *testing.T) {
	w, path := openTest(t, Options{})
	if _, err := w.AppendInitialPrompt("first"); err != nil {
		t.Fatal(err)
	}
	// A user message larger than the per-line bound must be dropped so the log
	// stays readable, rather than written whole past MaxBytes.
	if _, err := w.AppendUser(strings.Repeat("u", int(maxLogLineBytes)+1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, e := range entries {
		if e.Type == EntryUser && len(e.Text) > int(maxLogLineBytes) {
			t.Fatalf("oversized user entry was written: %d bytes", len(e.Text))
		}
	}
}
