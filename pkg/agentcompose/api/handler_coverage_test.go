package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/images"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestPrepareStreamingHeadersPreservesNoTransform(t *testing.T) {
	headers := http.Header{}
	PrepareStreamingHeaders(headers)
	if got, want := headers.Get("Cache-Control"), "no-cache, no-transform"; got != want {
		t.Fatalf("Cache-Control = %q, want %q", got, want)
	}
	if got, want := headers.Get("X-Accel-Buffering"), "no"; got != want {
		t.Fatalf("X-Accel-Buffering = %q, want %q", got, want)
	}
}

func TestRemoveSandboxRemoveRaceRemainsInternal(t *testing.T) {
	store := &apiSandboxStore{
		session:   &domain.Session{Summary: domain.SessionSummary{ID: "sandbox-1", VMStatus: domain.VMStatusStopped}},
		removeErr: os.ErrNotExist,
	}
	handler := NewSandboxHandler(&fakeSessionDelegate{}, store, nil)
	_, err := handler.RemoveSandbox(context.Background(), connect.NewRequest(&agentcomposev2.RemoveSandboxRequest{SandboxId: "sandbox-1"}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("RemoveSandbox error code = %v, want %v; err=%v", connect.CodeOf(err), connect.CodeInternal, err)
	}
}

func TestGetSandboxStatsReturnsRuntimeMetrics(t *testing.T) {
	value := 42.5
	store := &apiSandboxStore{
		session: &domain.Session{Summary: domain.SessionSummary{ID: "sandbox-1", Driver: "docker", VMStatus: domain.VMStatusRunning}},
		vmState: domain.VMState{
			Driver: "docker",
			BoxID:  "box-1",
		},
	}
	runtime := &apiStatsRuntime{stats: domain.SandboxStats{
		SandboxID:        "sandbox-1",
		Driver:           "docker",
		SampledAt:        time.Date(2026, 7, 4, 8, 0, 0, 0, time.UTC),
		CPUPercent:       domain.MetricValue{Value: &value, Unit: "percent", Status: domain.MetricStatusOK},
		MemoryUsageBytes: domain.MetricValue{Unit: "bytes", Status: domain.MetricStatusUnknown},
	}}
	handler := NewSandboxHandler(&fakeSessionDelegate{}, store, nil, func(*domain.Session) (SandboxStatsRuntime, error) {
		return runtime, nil
	})
	resp, err := handler.GetSandboxStats(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: "sandbox-1"}))
	if err != nil {
		t.Fatalf("GetSandboxStats returned error: %v", err)
	}
	if resp.Msg.GetStats().GetCpuPercent().GetStatus() != agentcomposev2.MetricStatus_METRIC_STATUS_OK || resp.Msg.GetStats().GetCpuPercent().GetValue() != value {
		t.Fatalf("GetSandboxStats response = %#v", resp.Msg.GetStats())
	}
	if resp.Msg.GetStats().GetMemoryUsageBytes().GetStatus() != agentcomposev2.MetricStatus_METRIC_STATUS_UNKNOWN {
		t.Fatalf("memory metric = %#v", resp.Msg.GetStats().GetMemoryUsageBytes())
	}
	if runtime.sessionID != "sandbox-1" || runtime.vmState.BoxID != "box-1" {
		t.Fatalf("runtime call session/vm = %q/%#v", runtime.sessionID, runtime.vmState)
	}
}

func TestGetSandboxStatsRejectsStoppedSandbox(t *testing.T) {
	store := &apiSandboxStore{
		session: &domain.Session{Summary: domain.SessionSummary{ID: "sandbox-1", VMStatus: domain.VMStatusStopped}},
	}
	handler := NewSandboxHandler(&fakeSessionDelegate{}, store, nil, func(*domain.Session) (SandboxStatsRuntime, error) {
		return &apiStatsRuntime{}, nil
	})
	_, err := handler.GetSandboxStats(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: "sandbox-1"}))
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("GetSandboxStats stopped code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestGetSandboxStatsUnsupportedRuntimeIsUnimplemented(t *testing.T) {
	store := &apiSandboxStore{
		session: &domain.Session{Summary: domain.SessionSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning}},
	}
	handler := NewSandboxHandler(&fakeSessionDelegate{}, store, nil)
	_, err := handler.GetSandboxStats(context.Background(), connect.NewRequest(&agentcomposev2.GetSandboxStatsRequest{SandboxId: "sandbox-1"}))
	if connect.CodeOf(err) != connect.CodeUnimplemented {
		t.Fatalf("GetSandboxStats unsupported code = %v, err=%v", connect.CodeOf(err), err)
	}
}

func TestLoaderServiceConnectErrorClassifiesInternalFailures(t *testing.T) {
	tests := []struct {
		err  error
		code connect.Code
	}{
		{err: domain.ResourceError(domain.ErrNotFound, "loader", "missing", "", nil), code: connect.CodeNotFound},
		{err: domain.ClassifyError(domain.ErrFailedPrecondition, "loader is running", nil), code: connect.CodeFailedPrecondition},
		{err: domain.ClassifyError(domain.ErrAlreadyExists, "loader exists", nil), code: connect.CodeAlreadyExists},
		{err: context.DeadlineExceeded, code: connect.CodeDeadlineExceeded},
		{err: sql.ErrConnDone, code: connect.CodeInternal},
		{err: os.ErrPermission, code: connect.CodeInternal},
		{err: errors.New("loader script is required"), code: connect.CodeInvalidArgument},
	}
	for _, tc := range tests {
		if got := connect.CodeOf(loaderServiceConnectError(tc.err)); got != tc.code {
			t.Fatalf("loaderServiceConnectError(%v) = %v, want %v", tc.err, got, tc.code)
		}
	}
}

func TestConnectErrorForDomainClassifiesReusableSentinels(t *testing.T) {
	tests := []struct {
		err  error
		code connect.Code
	}{
		{err: domain.ClassifyError(domain.ErrUnsupported, "stats are unsupported", nil), code: connect.CodeUnimplemented},
		{err: domain.ResourceError(domain.ErrNotFound, "sandbox", "missing", "", nil), code: connect.CodeNotFound},
		{err: domain.ClassifyError(domain.ErrInvalidArgument, "bad request", nil), code: connect.CodeInvalidArgument},
		{err: domain.ClassifyError(domain.ErrRequired, "project is required", nil), code: connect.CodeInvalidArgument},
		{err: domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox stopped", nil), code: connect.CodeFailedPrecondition},
		{err: domain.ClassifyError(domain.ErrAlreadyExists, "project exists", nil), code: connect.CodeAlreadyExists},
		{err: context.Canceled, code: connect.CodeCanceled},
		{err: context.DeadlineExceeded, code: connect.CodeDeadlineExceeded},
		{err: errors.New("boom"), code: connect.CodeInternal},
	}
	for _, tc := range tests {
		if got := connect.CodeOf(ConnectErrorForDomain(tc.err)); got != tc.code {
			t.Fatalf("ConnectErrorForDomain(%v) = %v, want %v", tc.err, got, tc.code)
		}
	}
}

func TestImagePullInspectAndSkip(t *testing.T) {
	ctx := context.Background()
	local := testImageBackend{
		inspect: images.InspectResult{Image: &agentcomposev2.Image{
			ImageRef:    "guest:latest",
			ResolvedRef: "guest@sha256:local",
		}},
	}
	handler := NewImageHandler(fakeImageSelector{backend: &local})
	resp, err := handler.PullImage(ctx, connect.NewRequest(&agentcomposev2.PullImageRequest{ImageRef: "guest:latest"}))
	if err != nil {
		t.Fatalf("PullImage local returned error: %v", err)
	}
	if local.pullCalls != 0 {
		t.Fatalf("PullImage local pull calls = %d, want 0", local.pullCalls)
	}
	if resp.Msg.GetResolvedRef() != "guest@sha256:local" || len(resp.Msg.GetWarnings()) == 0 {
		t.Fatalf("PullImage local response = %#v", resp.Msg)
	}

	missing := testImageBackend{
		inspectErr: images.OpError{Op: "inspect image", ImageRef: "missing:latest", Err: imagecache.NewError(imagecache.ErrorKindNotFound, "inspect", "missing:latest", errors.New("missing"))},
		pull: images.PullResult{Image: &agentcomposev2.Image{
			ImageRef:    "missing:latest",
			ResolvedRef: "missing@sha256:pulled",
		}, ResolvedRef: "missing@sha256:pulled"},
	}
	handler = NewImageHandler(fakeImageSelector{backend: &missing})
	resp, err = handler.PullImage(ctx, connect.NewRequest(&agentcomposev2.PullImageRequest{ImageRef: "missing:latest"}))
	if err != nil {
		t.Fatalf("PullImage missing returned error: %v", err)
	}
	if missing.pullCalls != 1 {
		t.Fatalf("PullImage missing pull calls = %d, want 1", missing.pullCalls)
	}
	if resp.Msg.GetResolvedRef() != "missing@sha256:pulled" || len(resp.Msg.GetWarnings()) != 0 {
		t.Fatalf("PullImage missing response = %#v", resp.Msg)
	}
}

func TestKernelAndAgentUnaryHandlerWorkflows(t *testing.T) {
	ctx := context.Background()
	session := &domain.Session{Summary: domain.SessionSummary{ID: "session-1", VMStatus: domain.VMStatusRunning, CreatedAt: time.Now()}}
	cell := domain.NotebookCell{ID: "cell-1", Type: execution.CellTypeJavaScript, Source: "print(1)", Output: "ok", Success: true}
	store := &apiHandlerSessionStore{
		session: session,
		cells:   []domain.NotebookCell{cell},
		events:  []domain.SessionEvent{{ID: "event-1", Type: "assistant", Message: "done", CreatedAt: time.Now()}},
	}
	publisher := &apiHandlerPublisher{}

	kernel := NewKernelHandler(store, apiHandlerCellExecutor{cell: cell}, publisher)
	resp, err := kernel.ExecuteCell(ctx, connect.NewRequest(&agentcomposev1.ExecuteCellRequest{SessionId: "session-1", Type: agentcomposev1.CellType_CELL_TYPE_JAVASCRIPT, Source: "print(1)"}))
	if err != nil {
		t.Fatalf("ExecuteCell returned error: %v", err)
	}
	if resp.Msg.GetCell().GetId() != "cell-1" || len(publisher.events) == 0 {
		t.Fatalf("kernel resp=%#v publisher=%#v", resp.Msg, publisher.events)
	}
	if publisher.events[0].CreatedAt.IsZero() || publisher.events[0].CreatedAt.Location() != time.UTC {
		t.Fatalf("kernel loader topic CreatedAt = %v", publisher.events[0].CreatedAt)
	}
	listResp, err := kernel.ListCells(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: "session-1"}))
	if err != nil || len(listResp.Msg.GetCells()) != 1 {
		t.Fatalf("ListCells resp=%#v err=%v", listResp, err)
	}
	store.session = &domain.Session{Summary: domain.SessionSummary{ID: "session-1", VMStatus: domain.VMStatusStopped}}
	if _, err := kernel.ExecuteCell(ctx, connect.NewRequest(&agentcomposev1.ExecuteCellRequest{SessionId: "session-1"})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected stopped session error, got %v", err)
	}
	store.session = session

	agent := NewAgentHandler(store, apiHandlerAgentDefinitions{agent: domain.AgentDefinition{ID: "agent-1", Provider: "codex", Model: "gpt", EnvItems: []domain.SessionEnvVar{{Name: "A", Value: "B"}}}}, apiHandlerAgentExecutor{cell: cell}, publisher)
	session.Summary.Tags = []domain.SessionTag{{Name: domain.AgentSessionTagID, Value: "agent-1"}, {Name: domain.AgentSessionTagName, Value: "Agent"}}
	sendResp, err := agent.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{SessionId: "session-1", Agent: "codex", Message: "hello"}))
	if err != nil {
		t.Fatalf("SendAgentMessage returned error: %v", err)
	}
	if sendResp.Msg.GetAssistantEvent().GetMessage() == "" {
		t.Fatalf("send response = %#v", sendResp.Msg)
	}
	if len(publisher.events) < 2 || publisher.events[1].CreatedAt.IsZero() || publisher.events[1].CreatedAt.Location() != time.UTC {
		t.Fatalf("agent loader topic events = %#v", publisher.events)
	}
	if _, err := agent.SendAgentMessage(ctx, connect.NewRequest(&agentcomposev1.SendAgentMessageRequest{SessionId: "session-1", Message: " "})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected empty message error, got %v", err)
	}
	eventsResp, err := agent.ListSessionEvents(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: "session-1"}))
	if err != nil || len(eventsResp.Msg.GetEvents()) != 1 {
		t.Fatalf("ListSessionEvents resp=%#v err=%v", eventsResp, err)
	}
}

type testImageBackend struct {
	inspect    images.InspectResult
	inspectErr error
	pull       images.PullResult
	pullErr    error
	pullCalls  int
}

func (b *testImageBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (b *testImageBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	b.pullCalls++
	return b.pull, b.pullErr
}

func (b *testImageBackend) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return b.inspect, b.inspectErr
}

func (b *testImageBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

func TestExecHandlerSessionTargetWorkflow(t *testing.T) {
	ctx := context.Background()
	sessionRoot := t.TempDir()
	session := &domain.Session{Summary: domain.SessionSummary{ID: "session-1", VMStatus: domain.VMStatusRunning, WorkspacePath: filepath.Join(sessionRoot, "workspace")}}
	store := &apiExecSessionStore{session: session, vm: domain.VMState{Driver: "docker"}}
	runtime := &apiExecRuntime{}
	handler := NewExecHandler(&appconfig.Config{}, store, apiExecProjectStore{}, func(*domain.Session) (ExecRuntime, error) {
		return runtime, nil
	})
	resp, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_SessionId{SessionId: "session-1"},
		Command: &agentcomposev2.ExecCommand{Command: "echo", Args: []string{"hi"}},
		Env:     []*agentcomposev2.EnvVarSpec{{Name: "FOO", Value: "bar"}, {Name: " "}},
	}))
	if err != nil {
		t.Fatalf("Exec returned error: %v", err)
	}
	if !resp.Msg.GetResult().GetSuccess() || resp.Msg.GetResult().GetStdout() != "hi\n" || runtime.spec.Command != "sh" || !strings.Contains(strings.Join(runtime.spec.Args, " "), "agent-compose-runtime exec") {
		t.Fatalf("exec resp=%#v spec=%#v", resp.Msg.GetResult(), runtime.spec)
	}
	requestFiles, err := filepath.Glob(filepath.Join(sessionRoot, "state", "exec", "*", "command-request.json"))
	if err != nil || len(requestFiles) != 1 {
		t.Fatalf("command request files=%#v err=%v", requestFiles, err)
	}
	var commandRequest execution.RuntimeCommandRequest
	data, err := os.ReadFile(requestFiles[0])
	if err != nil || json.Unmarshal(data, &commandRequest) != nil {
		t.Fatalf("read command request data=%q err=%v", string(data), err)
	}
	if commandRequest.Env["FOO"] != "bar" || commandRequest.Command != "echo" || len(commandRequest.Args) != 1 || commandRequest.Args[0] != "hi" {
		t.Fatalf("command request = %#v", commandRequest)
	}
	outputData, err := os.ReadFile(filepath.Join(filepath.Dir(requestFiles[0]), "output.txt"))
	if err != nil || string(outputData) != "hi\n" {
		t.Fatalf("exec output artifact = %q err=%v", string(outputData), err)
	}
	if _, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{Target: &agentcomposev2.ExecRequest_SessionId{SessionId: "session-1"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected missing command error, got %v", err)
	}
	store.session = &domain.Session{Summary: domain.SessionSummary{ID: "session-1", VMStatus: domain.VMStatusStopped}}
	if _, err := handler.Exec(ctx, connect.NewRequest(&agentcomposev2.ExecRequest{Target: &agentcomposev2.ExecRequest_SessionId{SessionId: "session-1"}, Command: &agentcomposev2.ExecCommand{Command: "echo"}})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("expected stopped session error, got %v", err)
	}
}

func TestExecHandlerSelectorErrors(t *testing.T) {
	handler := NewExecHandler(&appconfig.Config{}, &apiExecSessionStore{}, apiExecProjectStore{err: sql.ErrNoRows}, func(*domain.Session) (ExecRuntime, error) {
		return &apiExecRuntime{}, nil
	})
	if _, err := handler.Exec(context.Background(), connect.NewRequest(&agentcomposev2.ExecRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected target error, got %v", err)
	}
	if _, err := handler.Exec(context.Background(), connect.NewRequest(&agentcomposev2.ExecRequest{Target: &agentcomposev2.ExecRequest_RunId{RunId: "missing"}, Command: &agentcomposev2.ExecCommand{Command: "echo"}})); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("expected run not found, got %v", err)
	}
}

func TestProjectAndRunHandlersStoreBackedWorkflows(t *testing.T) {
	ctx := context.Background()
	store := &apiProjectRunStore{
		projects: []domain.ProjectRecord{{ID: "project-1", Name: "Project", CurrentRevision: 1}},
		agents: []domain.ProjectAgentRecord{{
			ProjectID: "project-1", AgentName: "worker", ManagedAgentID: "agent-1", Driver: "boxlite", Image: "guest:latest",
		}},
		schedulers: []domain.ProjectSchedulerRecord{{ProjectID: "project-1", SchedulerID: "scheduler-1", AgentName: "worker", Enabled: true}},
		revision:   domain.ProjectRevisionRecord{ProjectID: "project-1", Revision: 1, SpecJSON: `{"agents":[{"name":"worker"}]}`},
		runs: map[string]domain.ProjectRunRecord{
			"run-1": {RunID: "run-1", ProjectID: "project-1", ProjectName: "Project", AgentName: "worker", Status: domain.ProjectRunStatusRunning, Source: domain.ProjectRunSourceAPI, ResultJSON: "{}"},
		},
	}
	projectHandler := NewProjectHandler(nil, store)
	projectResp, err := projectHandler.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Name: "Project"}, IncludeSpec: true}))
	if err != nil {
		t.Fatalf("GetProject returned error: %v", err)
	}
	if projectResp.Msg.GetProject().GetSummary().GetProjectId() != "project-1" || projectResp.Msg.GetProject().GetSpec() == nil {
		t.Fatalf("project response = %#v", projectResp.Msg.GetProject())
	}
	listProjects, err := projectHandler.ListProjects(ctx, connect.NewRequest(&agentcomposev2.ListProjectsRequest{Query: "Project", Limit: 10}))
	if err != nil || len(listProjects.Msg.GetProjects()) != 1 {
		t.Fatalf("ListProjects resp=%#v err=%v", listProjects, err)
	}
	store.projects = append(store.projects, domain.ProjectRecord{ID: "project-2", Name: "Project"})
	if _, err := projectHandler.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{Project: &agentcomposev2.ProjectRef{Name: "Project"}})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected ambiguous project error, got %v", err)
	}
	store.projects = store.projects[:1]
	if _, err := projectHandler.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected missing project ref error, got %v", err)
	}

	runHandler := NewRunHandler(nil, store)
	runResp, err := runHandler.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{RunId: "run-1", ProjectId: "project-1"}))
	if err != nil || runResp.Msg.GetRun().GetSummary().GetRunId() != "run-1" {
		t.Fatalf("GetRun resp=%#v err=%v", runResp, err)
	}
	listRuns, err := runHandler.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{ProjectId: "project-1", Limit: 10}))
	if err != nil || len(listRuns.Msg.GetRuns()) != 1 {
		t.Fatalf("ListRuns resp=%#v err=%v", listRuns, err)
	}
	stopResp, err := runHandler.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{RunId: "run-1", Reason: "stop"}))
	if err != nil || !stopResp.Msg.GetStopRequested() || stopResp.Msg.GetRun().GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_CANCELED {
		t.Fatalf("StopRun resp=%#v err=%v", stopResp, err)
	}
	terminalResp, err := runHandler.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{RunId: "run-1"}))
	if err != nil || terminalResp.Msg.GetStopRequested() {
		t.Fatalf("terminal StopRun resp=%#v err=%v", terminalResp, err)
	}
	if _, err := runHandler.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("expected run id error, got %v", err)
	}
}

func TestFollowRunLogsStreamsOffsetsTailAndFinal(t *testing.T) {
	tempDir := t.TempDir()
	logPath := tempDir + "/output.txt"
	if err := os.WriteFile(logPath, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatalf("write log fixture: %v", err)
	}
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusSucceeded, LogsPath: logPath},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	all := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1"})
	if len(all) != 2 || all[0].GetData() != "one\ntwo\nthree\n" || all[0].GetOffset() != 14 || !all[1].GetIsFinal() {
		t.Fatalf("all chunks = %#v", all)
	}

	tail := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", TailLines: 2, Follow: true})
	if len(tail) != 2 || tail[0].GetData() != "two\nthree\n" || tail[0].GetOffset() != 14 || !tail[1].GetIsFinal() {
		t.Fatalf("tail chunks = %#v", tail)
	}

	offset := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", StartOffset: 4})
	if len(offset) != 2 || offset[0].GetData() != "two\nthree\n" || offset[0].GetOffset() != 14 || !offset[1].GetIsFinal() {
		t.Fatalf("offset chunks = %#v", offset)
	}
}

func TestFollowRunLogsMissingLogFileReturnsEmptyFinalForTerminalRun(t *testing.T) {
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "project-1", AgentName: "worker", Status: domain.ProjectRunStatusFailed, LogsPath: t.TempDir() + "/missing.txt"},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	chunks := collectRunLogChunks(t, client, &agentcomposev2.FollowRunLogsRequest{ProjectId: "project-1", RunId: "run-1", Follow: true})
	if len(chunks) != 1 || !chunks[0].GetIsFinal() || chunks[0].GetData() != "" || chunks[0].GetRunStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED {
		t.Fatalf("missing log chunks = %#v", chunks)
	}
}

func TestFollowRunLogsRejectsProjectMismatch(t *testing.T) {
	store := &apiProjectRunStore{runs: map[string]domain.ProjectRunRecord{
		"run-1": {RunID: "run-1", ProjectID: "project-1", Status: domain.ProjectRunStatusSucceeded},
	}}
	client, closeServer := newRunHandlerTestClient(t, NewRunHandler(nil, store))
	defer closeServer()

	stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{ProjectId: "project-2", RunId: "run-1"}))
	if err != nil {
		t.Fatalf("FollowRunLogs returned setup error: %v", err)
	}
	for stream.Receive() {
		t.Fatalf("unexpected chunk: %#v", stream.Msg())
	}
	if code := connect.CodeOf(stream.Err()); code != connect.CodeNotFound {
		t.Fatalf("FollowRunLogs code = %s, want %s (err=%v)", code, connect.CodeNotFound, stream.Err())
	}
}

func newRunHandlerTestClient(t *testing.T, handler *RunHandler) (agentcomposev2connect.RunServiceClient, func()) {
	t.Helper()
	mux := http.NewServeMux()
	path, serviceHandler := agentcomposev2connect.NewRunServiceHandler(handler)
	mux.Handle(path, serviceHandler)
	server := httptest.NewServer(mux)
	return agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL), server.Close
}

func collectRunLogChunks(t *testing.T, client agentcomposev2connect.RunServiceClient, req *agentcomposev2.FollowRunLogsRequest) []*agentcomposev2.RunLogChunk {
	t.Helper()
	stream, err := client.FollowRunLogs(context.Background(), connect.NewRequest(req))
	if err != nil {
		t.Fatalf("FollowRunLogs setup error: %v", err)
	}
	var chunks []*agentcomposev2.RunLogChunk
	for stream.Receive() {
		chunks = append(chunks, stream.Msg())
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("FollowRunLogs stream error: %v", err)
	}
	return chunks
}

type apiHandlerSessionStore struct {
	session *domain.Session
	cells   []domain.NotebookCell
	events  []domain.SessionEvent
}

func (s *apiHandlerSessionStore) GetSession(context.Context, string) (*domain.Session, error) {
	if s.session == nil {
		return nil, errors.New("missing")
	}
	return s.session, nil
}

func (s *apiHandlerSessionStore) ListCells(context.Context, string) ([]domain.NotebookCell, error) {
	return s.cells, nil
}

func (s *apiHandlerSessionStore) ListEvents(context.Context, string) ([]domain.SessionEvent, error) {
	return s.events, nil
}

type apiHandlerCellExecutor struct {
	cell domain.NotebookCell
}

func (e apiHandlerCellExecutor) ExecuteCell(context.Context, *domain.Session, string, string) (domain.NotebookCell, error) {
	return e.cell, nil
}

func (e apiHandlerCellExecutor) ExecuteCellStream(context.Context, *domain.Session, string, string, execution.CellExecutionStream) (domain.NotebookCell, error) {
	return e.cell, nil
}

type apiHandlerAgentDefinitions struct {
	agent domain.AgentDefinition
}

func (s apiHandlerAgentDefinitions) GetAgentDefinition(context.Context, string) (domain.AgentDefinition, error) {
	return s.agent, nil
}

type apiHandlerAgentExecutor struct {
	cell domain.NotebookCell
}

func (e apiHandlerAgentExecutor) ExecuteAgentRequest(_ context.Context, _ *domain.Session, req execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SessionEvent, domain.SessionEvent, error) {
	if strings.TrimSpace(req.Message) == "" {
		return domain.NotebookCell{}, domain.SessionEvent{}, domain.SessionEvent{}, errors.New("message")
	}
	return e.cell,
		domain.SessionEvent{ID: "user", Type: "user", Message: req.Message, CreatedAt: time.Now()},
		domain.SessionEvent{ID: "assistant", Type: "assistant", Message: "done", CreatedAt: time.Now()},
		nil
}

type apiHandlerPublisher struct {
	events []domain.LoaderTopicEvent
}

func (p *apiHandlerPublisher) Publish(event domain.LoaderTopicEvent) bool {
	p.events = append(p.events, event)
	return true
}

type apiExecSessionStore struct {
	session *domain.Session
	vm      domain.VMState
}

func (s *apiExecSessionStore) GetSession(context.Context, string) (*domain.Session, error) {
	if s.session == nil {
		return nil, sql.ErrNoRows
	}
	return s.session, nil
}

func (s *apiExecSessionStore) GetVMState(string) (domain.VMState, error) {
	return s.vm, nil
}

type apiExecProjectStore struct {
	err error
}

func (s apiExecProjectStore) GetProject(context.Context, string) (domain.ProjectRecord, error) {
	return domain.ProjectRecord{}, s.err
}

func (s apiExecProjectStore) GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{}, s.err
}

func (s apiExecProjectStore) ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{}, s.err
}

func (s apiExecProjectStore) ListProjectSessionRuns(context.Context, domain.ProjectSessionRelationFilter) ([]domain.ProjectRunRecord, error) {
	return nil, s.err
}

type apiExecRuntime struct {
	spec domain.ExecSpec
}

func (r *apiExecRuntime) ExecStream(_ context.Context, _ *domain.Session, _ domain.VMState, spec domain.ExecSpec, writer domain.ExecStreamWriter) (domain.ExecResult, error) {
	r.spec = spec
	writer(domain.ExecChunk{Text: "$ echo hi\n"})
	writer(domain.ExecChunk{Text: "hi\n"})
	payload := apiRuntimeCommandPayload(domain.RuntimeCommandResult{Stdout: "hi\n", Output: "hi\n", ExitCode: 0, Success: true})
	writer(domain.ExecChunk{Text: payload})
	return domain.ExecResult{Stdout: payload, Output: "$ echo hi\nhi\n" + payload, ExitCode: 0, Success: true}, nil
}

func apiRuntimeCommandPayload(result domain.RuntimeCommandResult) string {
	data, _ := json.Marshal(result)
	return execution.CommandResultPrefix + string(data) + "\n"
}

type apiSandboxStore struct {
	session   *domain.Session
	vmState   domain.VMState
	removeErr error
}

func (s *apiSandboxStore) GetSession(context.Context, string) (*domain.Session, error) {
	return s.session, nil
}

func (s *apiSandboxStore) RemoveSession(context.Context, string) error {
	return s.removeErr
}

func (s *apiSandboxStore) GetVMState(string) (domain.VMState, error) {
	return s.vmState, nil
}

type apiStatsRuntime struct {
	stats     domain.SandboxStats
	sessionID string
	vmState   domain.VMState
}

func (r *apiStatsRuntime) Stats(_ context.Context, session *domain.Session, vmState domain.VMState) (domain.SandboxStats, error) {
	if session != nil {
		r.sessionID = session.Summary.ID
	}
	r.vmState = vmState
	return r.stats, nil
}

type apiProjectRunStore struct {
	projects   []domain.ProjectRecord
	agents     []domain.ProjectAgentRecord
	schedulers []domain.ProjectSchedulerRecord
	revision   domain.ProjectRevisionRecord
	runs       map[string]domain.ProjectRunRecord
}

func (s *apiProjectRunStore) GetProject(_ context.Context, projectID string) (domain.ProjectRecord, error) {
	for _, project := range s.projects {
		if project.ID == projectID {
			return project, nil
		}
	}
	return domain.ProjectRecord{}, sql.ErrNoRows
}

func (s *apiProjectRunStore) ListProjects(_ context.Context, _ domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{Projects: s.projects, TotalCount: len(s.projects)}, nil
}

func (s *apiProjectRunStore) ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error) {
	return s.agents, nil
}

func (s *apiProjectRunStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	return s.schedulers, nil
}

func (s *apiProjectRunStore) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return s.revision, nil
}

func (s *apiProjectRunStore) GetProjectAgent(context.Context, string, string) (domain.ProjectAgentRecord, error) {
	if len(s.agents) == 0 {
		return domain.ProjectAgentRecord{}, sql.ErrNoRows
	}
	return s.agents[0], nil
}

func (s *apiProjectRunStore) GetManagedAgentDefinition(context.Context, string) (runs.ManagedAgentDefinition, error) {
	return runs.ManagedAgentDefinition{ID: "agent-1", Enabled: true, ManagedProjectID: "project-1", ManagedAgentName: "worker"}, nil
}

func (s *apiProjectRunStore) CreateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	s.runs[run.RunID] = run
	return run, nil
}

func (s *apiProjectRunStore) GetProjectRun(_ context.Context, runID string) (domain.ProjectRunRecord, error) {
	run, ok := s.runs[runID]
	if !ok {
		return domain.ProjectRunRecord{}, sql.ErrNoRows
	}
	return run, nil
}

func (s *apiProjectRunStore) UpdateProjectRun(_ context.Context, run domain.ProjectRunRecord) (domain.ProjectRunRecord, error) {
	s.runs[run.RunID] = run
	return run, nil
}

func (s *apiProjectRunStore) ListProjectRunsByOptions(_ context.Context, _ domain.ProjectRunListOptions) ([]domain.ProjectRunRecord, error) {
	items := make([]domain.ProjectRunRecord, 0, len(s.runs))
	for _, run := range s.runs {
		items = append(items, run)
	}
	return items, nil
}
