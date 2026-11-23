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
		if _, err := w.EndTurn("stop", "done"); err != nil {
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

func TestLegacyOutputTrackingStaysSmall(t *testing.T) {
	big := strings.Repeat("diagnostic line\n", (1<<20)/16)

	// A guest with agent events never compares its final text with output,
	// so a noisy turn keeps nothing for the checkpoint.
	m := NewMerger(0)
	if _, err := m.Event([]byte(`{"kind":"text_delta","text":"hi"}`)); err != nil {
		t.Fatal(err)
	}
	m.Output("stderr", big)
	if got := m.checkpoint().LegacyOutput; got != "" {
		t.Fatalf("legacy output kept %d bytes after an agent event", len(got))
	}

	// An older guest prints its answer after a long transcript; only the tail
	// is kept, and it still recognizes the mirrored answer.
	m = NewMerger(0)
	m.Output("stderr", big)
	m.Output("stderr", "The answer is 42.\n")
	if got := len(m.checkpoint().LegacyOutput); got > maxLegacyOutputBytes {
		t.Fatalf("legacy output kept %d bytes", got)
	}
	out := m.EndTurn("stop", "The answer is 42.")
	if len(out) != 1 || out[0].Entry.Type != EntryTurnEnd {
		t.Fatalf("EndTurn repeated the mirrored answer: %#v", out)
	}

	// A checkpoint from an older daemon may carry a long prefix.
	m = NewMerger(0)
	m.restoreCheckpoint(&mergerCheckpoint{LegacyOutput: big})
	if got := len(m.legacyOutput); got != maxLegacyOutputBytes {
		t.Fatalf("restored legacy output = %d bytes", got)
	}
}
