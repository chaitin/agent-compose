package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func (s *apiProjectRunStore) ListProjectRunMessagesPage(ctx context.Context, runID, sandboxID string, offset, limit int) ([]domain.ProjectRunEventRecord, error) {
	items := s.runMessages(runID, sandboxID)
	start := min(max(offset, 0), len(items))
	return items[start:min(start+limit, len(items))], nil
}

func (s *apiProjectRunStore) CountProjectRunMessages(ctx context.Context, runID, sandboxID string) (int, error) {
	return len(s.runMessages(runID, sandboxID)), nil
}

func (s *apiProjectRunStore) runMessages(runID, sandboxID string) []domain.ProjectRunEventRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []domain.ProjectRunEventRecord
	for _, event := range s.runEvents {
		if event.Kind != domain.ProjectRunEventKindUserMessage && event.Kind != domain.ProjectRunEventKindAgentMessage {
			continue
		}
		if runID != "" && event.RunID != runID {
			continue
		}
		if sandboxID != "" && s.runs[event.RunID].SandboxID != sandboxID {
			continue
		}
		out = append(out, event)
	}
	return out
}

func TestListRunMessagesReturnsTheConversationByRunOrSandbox(t *testing.T) {
	sandboxID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	store := &apiProjectRunStore{
		runs: map[string]domain.ProjectRunRecord{
			"run-1": {RunID: "run-1", ProjectID: "project-1", SandboxID: sandboxID},
			"run-2": {RunID: "run-2", ProjectID: "project-1", SandboxID: sandboxID},
		},
		runEvents: []domain.ProjectRunEventRecord{
			{ID: "e1", RunID: "run-1", Kind: domain.ProjectRunEventKindUserMessage, Text: "hi", LogSeq: 1},
			{ID: "e2", RunID: "run-1", Kind: domain.ProjectRunEventKindAgentMessage, Text: "hello", StopReason: "stop", LogSeq: 4},
			{ID: "e3", RunID: "run-1", Kind: domain.ProjectRunEventKindStatus},
			{ID: "e4", RunID: "run-2", Kind: domain.ProjectRunEventKindUserMessage, Text: "again", LogSeq: 1},
		},
	}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()
	ctx := context.Background()

	byRun, err := client.ListRunMessages(ctx, connect.NewRequest(&agentcomposev2.ListRunMessagesRequest{RunId: "run-1"}))
	if err != nil {
		t.Fatal(err)
	}
	got := byRun.Msg.GetMessages()
	if byRun.Msg.GetTotal() != 2 || len(got) != 2 || got[0].GetRole() != agentcomposev2.RunMessageRole_RUN_MESSAGE_ROLE_USER ||
		got[1].GetRole() != agentcomposev2.RunMessageRole_RUN_MESSAGE_ROLE_AGENT || got[1].GetLogSeq() != 4 || got[1].GetStopReason() != "stop" {
		t.Fatalf("messages by run = %#v", got)
	}
	bySandbox, err := client.ListRunMessages(ctx, connect.NewRequest(&agentcomposev2.ListRunMessagesRequest{SandboxId: sandboxID, Limit: 2, Offset: 1}))
	if err != nil {
		t.Fatal(err)
	}
	if bySandbox.Msg.GetTotal() != 3 || len(bySandbox.Msg.GetMessages()) != 2 || bySandbox.Msg.GetMessages()[1].GetRunId() != "run-2" {
		t.Fatalf("messages by sandbox = %#v", bySandbox.Msg)
	}
	for _, req := range []*agentcomposev2.ListRunMessagesRequest{{}, {RunId: "run-1", SandboxId: sandboxID}} {
		_, err := client.ListRunMessages(ctx, connect.NewRequest(req))
		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeInvalidArgument {
			t.Fatalf("ListRunMessages(%v) err = %v, want invalid argument", req, err)
		}
	}
}

func TestFollowRunLogsStructuredReturnsEntriesAfterStartSeq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "run-1", runlog.FileName)
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.AppendInitialPrompt("go")
	_ = w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"t","name":"Bash","status":"completed","command":"ls"}`))
	_ = w.AppendAgentEvent([]byte(`{"kind":"tool_result","id":"t","ok":false,"exitCode":2,"output":"no"}`))
	_, _ = w.EndTurn("stop", "done", nil)
	_ = w.Close()
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1":  {RunID: "run-1", ProjectID: "project-1", Status: domain.ProjectRunStatusSucceeded, LogsPath: path, LogFormat: runlog.FormatJSONL, SummaryJSON: `{"tool_calls":1,"tool_failures":1}`},
		"legacy": {RunID: "legacy", ProjectID: "project-1", Status: domain.ProjectRunStatusSucceeded, LogsPath: filepath.Join(t.TempDir(), "output.txt")},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	chunks := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{RunId: "run-1", Structured: true, StartSeq: 1, Follow: true})
	if len(chunks) != 2 || !chunks[1].GetIsFinal() {
		t.Fatalf("chunks = %#v", chunks)
	}
	entries := chunks[0].GetEntries()
	// The final text is recorded on the turn end only.
	if len(entries) != 3 || entries[0].GetSeq() != 2 || entries[0].GetToolCall().GetCommand() != "ls" ||
		entries[1].GetToolResult().GetOk() || entries[1].GetToolResult().GetExitCode() != 2 ||
		entries[2].GetTurnEnd().GetFinalText() != "done" {
		t.Fatalf("entries = %#v", entries)
	}
	if summary := chunks[1].GetRun().GetLogSummary(); summary.GetToolCalls() != 1 || summary.GetToolFailures() != 1 {
		t.Fatalf("summary = %#v", summary)
	}

	legacy := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{RunId: "legacy", Structured: true})
	if len(legacy) != 1 || !legacy[0].GetLegacyFormat() || !legacy[0].GetIsFinal() || len(legacy[0].GetEntries()) != 0 {
		t.Fatalf("legacy chunks = %#v", legacy)
	}
}

type fakeSchedulerRuns map[string]domain.SchedulerRunSummary

func (f fakeSchedulerRuns) GetSchedulerRunByID(_ context.Context, runID string) (domain.SchedulerRunSummary, error) {
	run, ok := f[runID]
	if !ok {
		return domain.SchedulerRunSummary{}, domain.ErrNotFound
	}
	return run, nil
}

func TestFollowRunLogsServesASchedulerStepLog(t *testing.T) {
	dataRoot := t.TempDir()
	stepPath, err := runlog.SchedulerCellPath(dataRoot, "sched-1", "run-1", "cell-1")
	if err != nil {
		t.Fatal(err)
	}
	w, err := runlog.Open(stepPath, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.AppendOutput("stdout", "line one\nline two\n")
	_ = w.Close()
	handler := NewRunHandler(nil, &apiProjectRunStore{}).WithSchedulerStepLogs(dataRoot, fakeSchedulerRuns{"run-1": {ID: "run-1", SchedulerID: "sched-1"}})
	client, closeServer := newRunHandlerTestClient(t, handler)
	defer closeServer()

	text := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{SchedulerRunId: "run-1", CellId: "cell-1", TailLines: 1})
	if len(text) != 2 || text[0].GetData() != "line two\n" || !text[1].GetIsFinal() {
		t.Fatalf("text chunks = %q / %q final=%v", text[0].GetData(), text[len(text)-1].GetData(), text[len(text)-1].GetIsFinal())
	}
	structured := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{SchedulerRunId: "run-1", CellId: "cell-1", Structured: true})
	if len(structured) != 2 || structured[0].GetEntries()[0].GetOutput().GetData() != "line one\nline two\n" {
		t.Fatalf("structured chunks = %#v", structured)
	}
	for _, chunk := range structured {
		if chunk.GetRunStatus() != agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED {
			t.Fatalf("scheduler step chunk run_status = %s, want unset: a step has no Agent Run status", chunk.GetRunStatus())
		}
	}

	for name, req := range map[string]*agentcomposev2.FollowRunLogsRequest{
		"missing cell":  {SchedulerRunId: "run-1"},
		"with run id":   {RunId: "run-x", SchedulerRunId: "run-1", CellId: "cell-1"},
		"path traverse": {SchedulerRunId: "run-1", CellId: "../x"},
		"unknown run":   {SchedulerRunId: "run-9", CellId: "cell-1"},
		"no step log":   {SchedulerRunId: "run-1", CellId: "cell-2"},
	} {
		stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(req))
		if err == nil {
			for stream.Receive() {
			}
			err = stream.Err()
		}
		if err == nil {
			t.Fatalf("%s: FollowRunLogs succeeded", name)
		}
	}
}

func TestIntegrationRunLogFileChangesAndRepeatedToolIDsReachBothViews(t *testing.T) {
	path := filepath.Join(t.TempDir(), runlog.FileName)
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"first", "second"} {
		if _, err := w.AppendUser(prompt); err != nil {
			t.Fatal(err)
		}
		if err := w.AppendAgentEvent([]byte(`{"kind":"tool_call","id":"item_1","name":"apply_patch","toolKind":"edit","status":"completed","changes":[{"path":"/workspace/hello.txt","kind":"add"}]}`)); err != nil {
			t.Fatal(err)
		}
		if err := w.AppendAgentEvent([]byte(`{"kind":"tool_result","id":"item_1","ok":true}`)); err != nil {
			t.Fatal(err)
		}
		if _, err := w.EndTurn("stop", "done", nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := runlog.ReadEntries(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := json.Marshal(runlog.Summarize(entries))
	if err != nil {
		t.Fatal(err)
	}
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", Status: domain.ProjectRunStatusSucceeded, LogFormat: runlog.FormatJSONL, LogsPath: path, SummaryJSON: string(summary)},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()
	chunks := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{RunId: "run-1", Structured: true})
	var turns []uint32
	for _, chunk := range chunks {
		for _, entry := range chunk.GetEntries() {
			if call := entry.GetToolCall(); call != nil {
				turns = append(turns, entry.GetTurn())
				changes := call.GetChanges()
				if len(changes) != 1 || changes[0].GetPath() != "/workspace/hello.txt" || changes[0].GetKind() != "add" {
					t.Fatalf("changes=%v", changes)
				}
			}
		}
	}
	if len(turns) != 2 || turns[0] != 1 || turns[1] != 2 || chunks[len(chunks)-1].GetRun().GetLogSummary().GetToolCalls() != 2 {
		t.Fatalf("turns=%v chunks=%v", turns, chunks)
	}
	var text strings.Builder
	for _, chunk := range collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{RunId: "run-1"}) {
		text.WriteString(chunk.GetData())
	}
	if strings.Count(text.String(), "add /workspace/hello.txt\n") != 2 {
		t.Fatalf("text=%q", text.String())
	}
}

func TestFollowRunLogsStructuredSendsALongLogInBoundedChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs", "run-1", runlog.FileName)
	w, err := runlog.Open(path, runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	const outputs = 2*runLogEntriesPerChunk + 17
	for i := 0; i < outputs; i++ {
		if err := w.AppendOutput("stdout", fmt.Sprintf("%d\n", i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", Status: domain.ProjectRunStatusSucceeded, LogFormat: runlog.FormatJSONL, LogsPath: path},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()
	chunks := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{RunId: "run-1", Structured: true, StartSeq: 5})
	var last uint64 = 5
	count := 0
	for _, chunk := range chunks {
		if len(chunk.GetEntries()) > runLogEntriesPerChunk {
			t.Fatalf("chunk of %d entries", len(chunk.GetEntries()))
		}
		for _, entry := range chunk.GetEntries() {
			if entry.GetSeq() != last+1 {
				t.Fatalf("seq %d after %d", entry.GetSeq(), last)
			}
			last = entry.GetSeq()
			count++
		}
	}
	if count != outputs-5 || !chunks[len(chunks)-1].GetIsFinal() {
		t.Fatalf("entries = %d, chunks = %d", count, len(chunks))
	}
}
