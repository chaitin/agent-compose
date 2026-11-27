package runlog

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the run log golden files in testdata")

// The run log's text view is part of its on-disk format: every entry records
// the text offset where its Render output begins, and readers locate offsets
// by re-rendering stored entries. A change to Render therefore shifts the text
// view of logs already written. These goldens make such a change visible; a
// deliberate one must be weighed against existing logs before -update.

func TestRenderGolden(t *testing.T) {
	ok, failed, exitCode := true, false, 2
	cases := []struct {
		name  string
		entry Entry
	}{
		{"user_initial", Entry{Type: EntryUser, Text: "hidden prompt", Initial: true}},
		{"user_blank", Entry{Type: EntryUser, Text: "  \n"}},
		{"user", Entry{Type: EntryUser, Text: "next question"}},
		{"user_trailing_newline", Entry{Type: EntryUser, Text: "next question\n"}},
		{"user_after_open_line", Entry{Type: EntryUser, Text: "next question", Newline: true}},
		{"text", Entry{Type: EntryText, Text: "Let me look.\n"}},
		{"output", Entry{Type: EntryOutput, Stream: "stderr", Data: "warning: x\r\npartial"}},
		{"tool_call_command", Entry{Type: EntryToolCall, Tool: &Tool{ID: "t1", Name: "Bash", Command: " ls -la "}}},
		{"tool_call_input", Entry{Type: EntryToolCall, Tool: &Tool{ID: "t2", Name: "Read", Input: json.RawMessage(` {"path":"a.go"} `)}}},
		{"tool_call_empty_input", Entry{Type: EntryToolCall, Tool: &Tool{ID: "t3", Name: "Think", Input: json.RawMessage(`{}`)}}},
		{"tool_call_changes", Entry{Type: EntryToolCall, Tool: &Tool{ID: "t4", Name: "apply_patch", Changes: []FileChange{{Path: "a.go", Kind: "update"}, {Path: "b.go", Kind: "add"}}}}},
		{"tool_call_unnamed", Entry{Type: EntryToolCall, Tool: &Tool{ID: "t5", Command: "ls"}}},
		{"tool_call_nil", Entry{Type: EntryToolCall}},
		{"tool_result", Entry{Type: EntryToolResult, Tool: &Tool{ID: "t1", OK: &ok, Output: "a.go\nb.go\n"}}},
		{"tool_result_error", Entry{Type: EntryToolResult, Tool: &Tool{ID: "t1", OK: &failed, ExitCode: &exitCode, Output: "partial", Error: " permission denied "}}},
		{"tool_result_error_only", Entry{Type: EntryToolResult, Tool: &Tool{ID: "t1", OK: &failed, Error: "boom"}}},
		{"error", Entry{Type: EntryError, Text: " rate limited ", Severity: "error", Code: "429"}},
		{"error_blank", Entry{Type: EntryError, Text: " "}},
		{"usage", Entry{Type: EntryUsage, Usage: &Usage{Scope: "turn", InputTokens: 10}}},
		{"turn_end", Entry{Type: EntryTurnEnd, StopReason: "stop", FinalText: "done"}},
		{"truncated", Entry{Type: EntryTruncated, DroppedBytes: 1024}},
		{"truncated_journal_gap", Entry{Type: EntryTruncated, SourceGap: true, DroppedBytes: 10}},
	}
	var b strings.Builder
	for _, c := range cases {
		fmt.Fprintf(&b, "%s: %q\n", c.name, Render(c.entry))
	}
	checkGolden(t, "render.golden", b.String())
}

func TestLogGolden(t *testing.T) {
	w, path := openTest(t, Options{})
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	event := func(raw string) { t.Helper(); must(w.AppendAgentEvent([]byte(raw))) }

	_, err := w.AppendInitialPrompt("list the files")
	must(err)
	event(`{"kind":"text_delta","text":"Let me "}`)
	event(`{"kind":"text_delta","text":"look.\nOne moment"}`)
	// claude announces a call before its arguments, then re-emits it.
	event(`{"kind":"tool_call","id":"t1","name":"Bash","toolKind":"execute","status":"in_progress"}`)
	event(`{"kind":"tool_call","id":"t1","name":"Bash","toolKind":"execute","command":"ls","status":"completed"}`)
	must(w.AppendOutput("stderr", "bash: warning\n"))
	event(`{"kind":"tool_result","id":"t1","ok":true,"exitCode":0,"output":"a.go\nb.go\n"}`)
	event(`{"kind":"usage","scope":"turn","model":"m","inputTokens":120,"outputTokens":30,"cachedTokens":5}`)
	event(`{"kind":"text_delta","text":"Two files: a.go and b.go."}`)
	_, err = w.EndTurn("stop", "Two files: a.go and b.go.", nil)
	must(err)
	_, err = w.AppendUser("and the tests?")
	must(err)
	event(`{"kind":"tool_call","id":"t2","name":"Read","input":{"path":"a_test.go"},"status":"completed"}`)
	event(`{"kind":"tool_result","id":"t2","ok":false,"error":"no such file"}`)
	event(`{"kind":"error","severity":"error","code":"tool","message":"read failed"}`)
	_, err = w.EndTurn("error", "", nil)
	must(err)
	must(w.Close())

	data, err := os.ReadFile(path)
	must(err)
	checkGolden(t, "log.golden.jsonl", string(data))
	entries, err := ReadEntries(path, 0)
	must(err)
	checkGolden(t, "log.golden.txt", RenderText(entries))
}

func checkGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s (run with -update to create it): %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("%s differs from the golden file; a change to the run log format shifts the text view of existing logs.\n--- got ---\n%s\n--- want ---\n%s", path, got, want)
	}
}
