package runlog

import (
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
	if _, err := w.AppendUser("list files"); err != nil {
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
	if _, err := w.EndTurn("stop", "Only final."); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	if got := types(entries); got != "text,text,turn_end,user,text,turn_end" {
		t.Fatalf("entry types = %s", got)
	}
	if entries[1].Text != " is 42." || entries[4].Text != "Only final." || entries[4].Turn != 2 {
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
	if text, _ := TextFrom(entries, 0); text != RenderText(entries) {
		t.Fatalf("TextFrom(0) = %q", text)
	}
}

func TestTextFromStartsInsideAnEntry(t *testing.T) {
	w, path := openTest(t, Options{})
	_ = w.AppendOutput("stdout", "abcdef")
	_ = w.AppendOutput("stdout", "ghi")
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	text, end := TextFrom(entries, 4)
	if text != "efghi" || end != 9 {
		t.Fatalf("TextFrom(4) = %q, %d", text, end)
	}
	if text, end := TextFrom(entries, 9); text != "" || end != 9 {
		t.Fatalf("TextFrom(end) = %q, %d", text, end)
	}
	if entries[1].TextOffset != 6 {
		t.Fatalf("second offset = %d", entries[1].TextOffset)
	}
}

func TestCapWritesOneMarkerAndKeepsEssentialEntries(t *testing.T) {
	w, path := openTest(t, Options{MaxBytes: 600})
	_, _ = w.AppendUser("go")
	for i := 0; i < 10; i++ {
		_ = w.AppendOutput("stdout", strings.Repeat("x", 100))
	}
	if _, err := w.EndTurn("stop", ""); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	entries, _ := ReadEntries(path, 0)
	markers := 0
	for _, e := range entries {
		if e.Type == EntryTruncated {
			markers++
		}
	}
	if markers != 1 || entries[len(entries)-1].Type != EntryTurnEnd || !Summarize(entries).Truncated {
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
