package api

import (
	"context"
	"github.com/chaitin/agent-compose/pkg/runlog"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestReadRunLogChunkFromOffsetKeepsFramesBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	want := strings.Repeat("a", runLogFileChunkBytes*2+137)
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	var got strings.Builder
	var offset uint64
	for {
		chunk, next, atEnd, err := readRunLogChunkFromOffset(path, offset)
		if err != nil {
			t.Fatalf("read chunk at %d: %v", offset, err)
		}
		if len(chunk) > runLogFileChunkBytes || next < offset {
			t.Fatalf("chunk length/next = %d/%d at %d", len(chunk), next, offset)
		}
		got.WriteString(chunk)
		offset = next
		if atEnd {
			break
		}
	}
	if got.String() != want {
		t.Fatalf("reassembled log length = %d, want %d", got.Len(), len(want))
	}
}

func TestReadRunLogChunkFromOffsetKeepsUTF8CodePointsIntact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	want := strings.Repeat("a", runLogFileChunkBytes-1) + "界" + strings.Repeat("b", 32)
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	first, offset, atEnd, err := readRunLogChunkFromOffset(path, 0)
	if err != nil {
		t.Fatalf("read first chunk: %v", err)
	}
	if atEnd || !strings.HasSuffix(first, "a") || len(first) != runLogFileChunkBytes-1 {
		t.Fatalf("first chunk end/length = %t/%d", atEnd, len(first))
	}
	second, _, atEnd, err := readRunLogChunkFromOffset(path, offset)
	if err != nil {
		t.Fatalf("read second chunk: %v", err)
	}
	if !atEnd || first+second != want {
		t.Fatalf("reassembled UTF-8 log end/length = %t/%d, want true/%d", atEnd, len(first+second), len(want))
	}
}

func TestReadRunLogChunkFromOffsetDefersIncompleteUTF8AtEOF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	runeBytes := []byte("界")
	initial := append([]byte("ready "), runeBytes[:2]...)
	if err := os.WriteFile(path, initial, 0o600); err != nil {
		t.Fatalf("write partial log: %v", err)
	}
	first, offset, atEnd, err := readRunLogChunkFromOffset(path, 0)
	if err != nil {
		t.Fatalf("read partial log: %v", err)
	}
	if first != "ready " || offset != uint64(len("ready ")) || !atEnd || !utf8.ValidString(first) {
		t.Fatalf("partial chunk/data offset/end/valid = %q/%d/%t/%t", first, offset, atEnd, utf8.ValidString(first))
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open partial log for append: %v", err)
	}
	if _, err := file.Write(runeBytes[2:]); err != nil {
		_ = file.Close()
		t.Fatalf("complete log rune: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close completed log: %v", err)
	}
	second, next, atEnd, err := readRunLogChunkFromOffset(path, offset)
	if err != nil {
		t.Fatalf("read completed rune: %v", err)
	}
	if second != "界" || next != uint64(len(initial)+1) || !atEnd || !utf8.ValidString(second) {
		t.Fatalf("completed chunk/data offset/end/valid = %q/%d/%t/%t", second, next, atEnd, utf8.ValidString(second))
	}
}

func TestReadRunLogChunkFromOffsetRejectsInvalidUTF8(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, []byte{'o', 'k', 0xff}, 0o600); err != nil {
		t.Fatalf("write invalid log: %v", err)
	}
	if _, _, _, err := readRunLogChunkFromOffset(path, 0); err == nil || !strings.Contains(err.Error(), "invalid UTF-8") {
		t.Fatalf("read invalid log error = %v", err)
	}
}

func TestTailRunLogOffsetScansBackwardsInBoundedChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	prefix := strings.Repeat("x", runLogFileChunkBytes+17)
	data := prefix + "\nlast-one\nlast-two\n"
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	offset, err := tailRunLogOffset(path, 2)
	if err != nil {
		t.Fatalf("tail offset: %v", err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("open log: %v", err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
		t.Fatalf("seek tail: %v", err)
	}
	tail, err := io.ReadAll(file)
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if string(tail) != "last-one\nlast-two\n" {
		t.Fatalf("tail = %q", tail)
	}
}

func TestFollowRunLogsStreamsMetadataAndBoundedFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	want := strings.Repeat("log-line\n", runLogFileChunkBytes/4)
	if err := os.WriteFile(path, []byte(want), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-large": {RunID: "run-large", ProjectID: "project-1", AgentName: "reviewer", Prompt: "inspect the repository", Status: domain.ProjectRunStatusSucceeded, LogsPath: path},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()
	stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{
		ProjectId: "project-1", RunId: "run-large", IncludeMetadata: true,
	}))
	if err != nil {
		t.Fatalf("FollowRunLogs returned error: %v", err)
	}
	var data strings.Builder
	var metadata, final bool
	for stream.Receive() {
		chunk := stream.Msg()
		if chunk.GetRun() != nil && chunk.GetPrompt() == "inspect the repository" {
			metadata = true
		}
		if len(chunk.GetData()) > runLogFileChunkBytes {
			t.Fatalf("streamed chunk length = %d", len(chunk.GetData()))
		}
		data.WriteString(chunk.GetData())
		final = final || chunk.GetIsFinal()
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("FollowRunLogs receive error: %v", err)
	}
	if !metadata || !final || data.String() != want {
		t.Fatalf("metadata/final/data length = %t/%t/%d, want true/true/%d", metadata, final, data.Len(), len(want))
	}
	assertSchedulerStreamHeaders(t, stream.ResponseHeader())
}

func TestFollowRunLogsExplicitZeroTailSkipsExistingOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run.log")
	if err := os.WriteFile(path, []byte("existing output\n"), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-tail-zero": {RunID: "run-tail-zero", ProjectID: "project-1", AgentName: "reviewer", Prompt: "prompt", Status: domain.ProjectRunStatusSucceeded, LogsPath: path},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()
	stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{
		ProjectId: "project-1", RunId: "run-tail-zero", TailSet: true, IncludeMetadata: true,
	}))
	if err != nil {
		t.Fatalf("FollowRunLogs returned error: %v", err)
	}
	var data strings.Builder
	var metadata, final bool
	for stream.Receive() {
		chunk := stream.Msg()
		data.WriteString(chunk.GetData())
		metadata = metadata || chunk.GetRun() != nil
		final = final || chunk.GetIsFinal()
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("FollowRunLogs receive error: %v", err)
	}
	if data.Len() != 0 || !metadata || !final {
		t.Fatalf("zero-tail data/metadata/final = %q/%t/%t", data.String(), metadata, final)
	}
}

func TestRunLogReadersServeTheRunLogTextView(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "r", runlog.FileName)
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AppendInitialPrompt("hidden prompt"); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stdout", "one\ntwo\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"t","name":"Bash","status":"completed","command":"ls"}`)); err != nil {
		t.Fatal(err)
	}
	if err := w.AppendOutput("stderr", "三\n"); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	const want = "one\ntwo\n\n[tool:Bash]\n$ ls\n三\n"
	text, next, atEnd, err := readRunLogChunkFromOffset(path, 0)
	if err != nil || text != want || next != uint64(len(want)) || !atEnd {
		t.Fatalf("full read = %q next=%d atEnd=%v err=%v", text, next, atEnd, err)
	}
	if text, _, _, _ := readRunLogChunkFromOffset(path, 4); text != want[4:] {
		t.Fatalf("read from 4 = %q", text)
	}
	offset, err := tailRunLogOffset(path, 2)
	if err != nil || want[offset:] != "$ ls\n三\n" {
		t.Fatalf("tail offset %d (%q) err=%v", offset, want[offset:], err)
	}
}
