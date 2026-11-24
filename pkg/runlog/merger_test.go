package runlog

import (
	"encoding/json"
	"fmt"
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

// longAnswer is an answer past maxTurnProseBytes with no repeated stretch, so
// only the true alignment of the kept tail can match it.
func longAnswer() string {
	var b strings.Builder
	for i := 0; b.Len() < 3*maxTurnProseBytes; i++ {
		fmt.Fprintf(&b, "line %d of the answer\n", i)
	}
	return b.String()
}

func streamProse(t *testing.T, m *Merger, text string) {
	t.Helper()
	for len(text) > 0 {
		n := min(len(text), 3000)
		delta, _ := json.Marshal(map[string]string{"kind": "text_delta", "text": text[:n]})
		if _, err := m.Event(delta); err != nil {
			t.Fatal(err)
		}
		text = text[n:]
	}
}

// appendedText returns the prose an EndTurn added before its turn_end.
func appendedText(out []Pending) string {
	var b strings.Builder
	for _, p := range out {
		if p.Entry.Type == EntryText {
			b.WriteString(p.Entry.Text)
		}
	}
	return b.String()
}

func TestEndTurnDoesNotRepeatAStreamedAnswerLongerThanTheProseTail(t *testing.T) {
	answer := longAnswer()

	m := NewMerger(0)
	streamProse(t, m, answer)
	m.FlushText()
	if got := appendedText(m.EndTurn("stop", answer)); got != "" {
		t.Fatalf("EndTurn repeated %d bytes of a fully streamed answer", len(got))
	}

	// Only the part that never streamed is appended.
	m = NewMerger(0)
	streamProse(t, m, "Let me look.\n")
	streamProse(t, m, answer[:len(answer)-5000])
	m.FlushText()
	if got := appendedText(m.EndTurn("stop", answer)); got != answer[len(answer)-5000:] {
		t.Fatalf("EndTurn appended %d bytes, want the 5000 unstreamed", len(got))
	}

	// The same holds across a restart.
	m = NewMerger(0)
	streamProse(t, m, answer)
	m.FlushText()
	restored := NewMerger(0)
	restored.restoreCheckpoint(m.checkpoint())
	if got := appendedText(restored.EndTurn("stop", answer)); got != "" {
		t.Fatalf("EndTurn after restore repeated %d bytes", len(got))
	}

	// A checkpoint from an older daemon holds the whole prose and no length.
	restored = NewMerger(0)
	restored.restoreCheckpoint(&mergerCheckpoint{TurnProse: answer, TurnEvents: true})
	if got := appendedText(restored.EndTurn("stop", answer)); got != "" {
		t.Fatalf("EndTurn after an older checkpoint repeated %d bytes", len(got))
	}
}

func TestEndTurnDoesNotCountMoreProseThanStreamed(t *testing.T) {
	// A repetitive answer matches the kept tail at many places; the match may
	// not reach past what was actually streamed.
	m := NewMerger(0)
	streamProse(t, m, strings.Repeat("a", maxTurnProseBytes+6000))
	m.FlushText()
	answer := strings.Repeat("a", 2*maxTurnProseBytes)
	if got := len(appendedText(m.EndTurn("stop", answer))); got != maxTurnProseBytes-6000 {
		t.Fatalf("EndTurn appended %d bytes, want %d", got, maxTurnProseBytes-6000)
	}
}

func TestLegacyOutputRecognizesAnAnswerLongerThanItsTail(t *testing.T) {
	answer := longAnswer()

	m := NewMerger(0)
	m.Output("stderr", "working...\n")
	m.Output("stderr", answer+"\n")
	if got := appendedText(m.EndTurn("stop", answer)); got != "" {
		t.Fatalf("EndTurn repeated %d bytes of the mirrored answer", len(got))
	}

	// An answer the guest never printed is still recorded.
	m = NewMerger(0)
	m.Output("stderr", strings.Repeat("diagnostic line\n", 2*maxLegacyOutputBytes/16))
	if got := appendedText(m.EndTurn("stop", answer)); got != answer {
		t.Fatalf("EndTurn appended %d bytes, want the whole unprinted answer", len(got))
	}
}
