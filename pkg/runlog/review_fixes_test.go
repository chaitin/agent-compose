package runlog

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestTextOffsetsFollowTheStoredEncoding(t *testing.T) {
	w, path := openTest(t, Options{})
	if _, err := w.AppendInitialPrompt("go"); err != nil {
		t.Fatal(err)
	}
	// JSON changes each of these on the way to disk: invalid UTF-8 becomes
	// U+FFFD, and tool input is compacted and HTML-escaped.
	if err := w.AppendOutput("stdout", "a\xffb\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"t","name":"Edit","input":{ "path" : "a.go", "new": "<b>&" },"status":"completed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "next\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var offset uint64
	for _, e := range entries {
		if e.TextOffset != offset {
			t.Fatalf("seq %d: text offset %d, rendered text so far %d", e.Seq, e.TextOffset, offset)
		}
		offset += uint64(len(Render(e)))
	}
	view, err := OpenTextView(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = view.Close() }()
	got, err := io.ReadAll(io.NewSectionReader(view, 0, int64(view.Size())))
	if err != nil {
		t.Fatal(err)
	}
	if want := RenderText(entries); string(got) != want || !strings.HasSuffix(want, "next\n") {
		t.Fatalf("text view %q, want %q", got, want)
	}
	// Reopening continues from the same offsets.
	reopened, err := Open(path, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	if reopened.textOffset != offset {
		t.Fatalf("reopened at text offset %d, want %d", reopened.textOffset, offset)
	}
}

func TestCheckpointKeepsBytesDroppedBeforeTheirMarker(t *testing.T) {
	w, path := openTest(t, Options{MaxBytes: 600})
	if _, err := w.AppendInitialPrompt("go"); err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 40) + "\n"
	for range 10 {
		if err := w.AppendOutput("stdout", line); err != nil {
			t.Fatal(err)
		}
	}
	dropped := w.dropped
	if dropped == 0 || !w.truncated {
		t.Fatalf("cap not reached: dropped=%d", dropped)
	}
	if err := w.Checkpoint(1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	// The daemon dies before the marker is written.
	_ = w.file.Close()
	w.file = nil

	reopened, err := Open(path, Options{MaxBytes: 600})
	if err != nil {
		t.Fatal(err)
	}
	if !reopened.truncated || reopened.dropped != dropped {
		t.Fatalf("reopened: truncated=%v dropped=%d, want %d", reopened.truncated, reopened.dropped, dropped)
	}
	if err := reopened.AppendOutput("stdout", line); err != nil {
		t.Fatal(err)
	}
	// The next kept entry closes the dropped stretch with its marker.
	if _, err := reopened.EndTurn("stop", ""); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Checkpoint(2, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	var marked uint64
	for _, e := range entries {
		if e.Type == EntryTruncated {
			marked += e.DroppedBytes
		}
	}
	if want := dropped + uint64(len(line)); marked != want {
		t.Fatalf("markers count %d dropped bytes, want %d", marked, want)
	}
}

func TestCheckpointStaysSmallOverALongTurn(t *testing.T) {
	w, path := openTest(t, Options{})
	if _, err := w.AppendInitialPrompt("go"); err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]string{"content": strings.Repeat("i", 100<<10)})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 50 {
		id := string(rune('a'+i%26)) + strings.Repeat("x", i)
		if err := w.AppendAgentEvent([]byte(`{"kind":"text_delta","text":"` + strings.Repeat("p", 40<<10) + `\n"}`)); err != nil {
			t.Fatal(err)
		}
		call, _ := json.Marshal(map[string]any{"kind": "tool_call", "id": id, "name": "Write", "input": json.RawMessage(input), "status": "completed"})
		if err := w.AppendAgentEvent(call); err != nil {
			t.Fatal(err)
		}
		// codex resends the call; its arguments must not come back into state.
		if err := w.AppendAgentEvent(call); err != nil {
			t.Fatal(err)
		}
		if err := w.AppendAgentEvent([]byte(`{"kind":"tool_result","id":"` + id + `","ok":true,"output":"ok"}`)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Checkpoint(1, json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(checkpointPath(path))
	if err != nil {
		t.Fatal(err)
	}
	// 2 MiB of prose and 5 MiB of tool input went by in this turn.
	if info.Size() > 128<<10 {
		t.Fatalf("checkpoint is %d bytes", info.Size())
	}
}

func TestToolResultParentIDIsKept(t *testing.T) {
	w, path := openTest(t, Options{})
	events := []string{
		// claude reports a subagent tool's parent on the result only.
		`{"kind":"tool_call","id":"t1","name":"Bash","command":"ls","status":"completed"}`,
		`{"kind":"tool_result","id":"t1","parentToolUseId":"task-1","ok":true,"output":"a"}`,
		// A result whose call was never announced.
		`{"kind":"tool_result","id":"t2","parentToolUseId":"task-1","ok":true,"output":"b"}`,
	}
	for _, event := range events {
		if err := w.AppendAgentEvent([]byte(event)); err != nil {
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
	var got []string
	for _, e := range entries {
		got = append(got, string(e.Type)+":"+e.Tool.ID+":"+e.Tool.ParentID)
	}
	want := "tool_call:t1:|tool_result:t1:task-1|tool_call:t2:task-1|tool_result:t2:task-1"
	if strings.Join(got, "|") != want {
		t.Fatalf("entries %q, want %q", got, want)
	}
}
