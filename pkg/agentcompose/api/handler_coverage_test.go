package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
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

func TestExecHandlerSessionTargetWorkflow(t *testing.T) {
	ctx := context.Background()
	session := &domain.Session{Summary: domain.SessionSummary{ID: "session-1", VMStatus: domain.VMStatusRunning}}
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
	if !resp.Msg.GetResult().GetSuccess() || resp.Msg.GetResult().GetStdout() != "hi\n" || runtime.spec.Env["FOO"] != "bar" {
		t.Fatalf("exec resp=%#v spec=%#v", resp.Msg.GetResult(), runtime.spec)
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
	writer(domain.ExecChunk{Text: "hi\n"})
	return domain.ExecResult{Stdout: "hi\n", Output: "hi\n", ExitCode: 0, Success: true}, nil
}

type apiSandboxStore struct {
	session   *domain.Session
	removeErr error
}

func (s *apiSandboxStore) GetSession(context.Context, string) (*domain.Session, error) {
	return s.session, nil
}

func (s *apiSandboxStore) RemoveSession(context.Context, string) error {
	return s.removeErr
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
