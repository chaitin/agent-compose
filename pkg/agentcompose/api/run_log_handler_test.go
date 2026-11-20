package api

import (
	"context"
	"errors"
	"path/filepath"
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
	_, _ = w.EndTurn("stop", "done")
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
	// The final answer was never streamed, so it is recorded as text before
	// the turn end.
	if len(entries) != 4 || entries[0].GetSeq() != 2 || entries[0].GetToolCall().GetCommand() != "ls" ||
		entries[1].GetToolResult().GetOk() || entries[1].GetToolResult().GetExitCode() != 2 ||
		entries[2].GetText().GetText() != "done" || entries[3].GetTurnEnd().GetFinalText() != "done" {
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

func (f fakeSchedulerRuns) GetSchedulerRun(_ context.Context, schedulerID, runID string) (domain.SchedulerRunSummary, error) {
	run, ok := f[schedulerID+"/"+runID]
	if !ok {
		return domain.SchedulerRunSummary{}, domain.ErrNotFound
	}
	return run, nil
}

func TestFollowRunLogsServesASchedulerStepLog(t *testing.T) {
	dataRoot := t.TempDir()
	w, err := runlog.Open(runlog.SchedulerCellPath(dataRoot, "sched-1", "run-1", "cell-1"), runlog.Options{})
	if err != nil {
		t.Fatal(err)
	}
	_ = w.AppendOutput("stdout", "line one\nline two\n")
	_ = w.Close()
	handler := NewRunHandler(nil, &apiProjectRunStore{}).WithSchedulerStepLogs(dataRoot, fakeSchedulerRuns{"sched-1/run-1": {ID: "run-1", SchedulerID: "sched-1"}})
	client, closeServer := newRunHandlerTestClient(t, handler)
	defer closeServer()

	text := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{SchedulerId: "sched-1", SchedulerRunId: "run-1", CellId: "cell-1", TailLines: 1})
	if len(text) != 2 || text[0].GetData() != "line two\n" || !text[1].GetIsFinal() {
		t.Fatalf("text chunks = %q / %q final=%v", text[0].GetData(), text[len(text)-1].GetData(), text[len(text)-1].GetIsFinal())
	}
	structured := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{SchedulerId: "sched-1", SchedulerRunId: "run-1", CellId: "cell-1", Structured: true})
	if len(structured) != 2 || structured[0].GetEntries()[0].GetOutput().GetData() != "line one\nline two\n" {
		t.Fatalf("structured chunks = %#v", structured)
	}

	for name, req := range map[string]*agentcomposev2.FollowRunLogsRequest{
		"missing cell":  {SchedulerId: "sched-1", SchedulerRunId: "run-1"},
		"with run id":   {RunId: "run-x", SchedulerId: "sched-1", SchedulerRunId: "run-1", CellId: "cell-1"},
		"path traverse": {SchedulerId: "sched-1", SchedulerRunId: "run-1", CellId: "../x"},
		"unknown run":   {SchedulerId: "sched-1", SchedulerRunId: "run-9", CellId: "cell-1"},
		"no step log":   {SchedulerId: "sched-1", SchedulerRunId: "run-1", CellId: "cell-2"},
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
