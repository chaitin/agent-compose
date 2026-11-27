package runlog

import (
	"strings"
	"testing"
)

func TestToolIdentityIsScopedToTheTurnAcrossReopen(t *testing.T) {
	w, path := openTest(t, Options{})
	for turn := 1; turn <= 2; turn++ {
		if _, err := w.AppendUser("go"); err != nil {
			t.Fatal(err)
		}
		mustEvent(t, w, `{"kind":"tool_call","id":"item_1","name":"shell","status":"completed","command":"ls"}`)
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		var err error
		w, err = Open(path, Options{})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = w.Close() })
		// Replaying a call in the current turn must still be deduplicated.
		mustEvent(t, w, `{"kind":"tool_call","id":"item_1","name":"shell","status":"completed","command":"ls"}`)
		mustEvent(t, w, `{"kind":"tool_result","id":"item_1","ok":false,"output":"failed"}`)
		mustEvent(t, w, `{"kind":"tool_result","id":"item_1","ok":false,"output":"failed"}`)
		if _, err := w.EndTurn("stop", "done", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var calls, results []uint32
	for _, entry := range entries {
		if entry.Type == EntryToolCall {
			calls = append(calls, entry.Turn)
		}
		if entry.Type == EntryToolResult {
			results = append(results, entry.Turn)
		}
	}
	if len(calls) != 2 || len(results) != 2 || calls[0] != 1 || calls[1] != 2 || results[0] != 1 || results[1] != 2 {
		t.Fatalf("calls=%v results=%v", calls, results)
	}
	if summary := Summarize(entries); summary.ToolCalls != 2 || summary.ToolFailures != 2 || summary.Turns != 2 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestFileChangesSurviveReopenAndRender(t *testing.T) {
	w, path := openTest(t, Options{})
	mustEvent(t, w, `{"kind":"tool_call","id":"patch","name":"apply_patch","toolKind":"edit","status":"in_progress","changes":[{"path":"/workspace/hello.txt","kind":"add"}]}`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	mustEvent(t, w, `{"kind":"tool_call","id":"patch","name":"apply_patch","toolKind":"edit","status":"completed","changes":[{"path":"/workspace/hello.txt","kind":"add"}]}`)
	mustEvent(t, w, `{"kind":"tool_result","id":"patch","ok":true}`)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || len(entries[0].Tool.Changes) != 1 || entries[0].Tool.Changes[0] != (FileChange{Path: "/workspace/hello.txt", Kind: "add"}) {
		t.Fatalf("entries=%+v", entries)
	}
	if text := RenderText(entries); !strings.Contains(text, "add /workspace/hello.txt\n") {
		t.Fatalf("text=%q", text)
	}
}

func TestEndTurnRecordsTheFinalTextOnlyOnTheTurnEnd(t *testing.T) {
	for name, feed := range map[string]func(m *Merger){
		// The answer was streamed, partly still held for coalescing.
		"streamed": func(m *Merger) {
			_, _ = m.Event([]byte(`{"kind":"text_delta","text":"Let me check.\nThe answer is 42."}`))
		},
		// A structured answer arrives as a tool call, never as prose.
		"structured": func(m *Merger) {
			_, _ = m.Event([]byte(`{"kind":"tool_call","id":"t1","name":"StructuredOutput","input":{"answer":42},"status":"completed"}`))
		},
		// An older guest printed its answer as process output.
		"legacy output": func(m *Merger) {
			m.Output("stderr", "The answer is 42.\n")
		},
		// The final text can be the whole transcript (transcript_fallback).
		"transcript": func(m *Merger) {
			_, _ = m.Event([]byte(`{"kind":"text_delta","text":"Let me check.\n"}`))
		},
	} {
		m := NewMerger(0)
		feed(m)
		before := RenderText(entriesOf(m.FlushText()))
		m2 := NewMerger(0)
		feed(m2)
		out := m2.EndTurn("stop", "Let me check.\n\n[tool:Bash]\n\nThe answer is 42.")
		if last := out[len(out)-1].Entry; last.Type != EntryTurnEnd || last.FinalText == "" {
			t.Fatalf("%s: last entry = %#v", name, last)
		}
		if got := RenderText(entriesOf(out[:len(out)-1])); got != before {
			t.Fatalf("%s: EndTurn recorded prose %q, want only the held %q", name, got, before)
		}
	}
}

func entriesOf(pending []Pending) []Entry {
	entries := make([]Entry, 0, len(pending))
	for _, p := range pending {
		entries = append(entries, p.Entry)
	}
	return entries
}
