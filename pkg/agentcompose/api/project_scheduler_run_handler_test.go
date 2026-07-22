package api

import (
	"context"
	"fmt"
	"testing"
	"time"

	"connectrpc.com/connect"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestProjectHandlerRunSchedulerSupportsMainAndTerminalStatuses(t *testing.T) {
	store, runtime, handler := newSchedulerRunHandlerFixture()
	if store.scheduler.Enabled {
		t.Fatal("fixture scheduler must be disabled to exercise manual execution")
	}
	tests := []struct {
		name       string
		triggerID  string
		status     string
		wantStatus agentcomposev2.SchedulerRunStatus
	}{
		{name: "succeeded", triggerID: "trigger-1", status: domain.LoaderRunStatusSucceeded, wantStatus: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED},
		{name: "canceled", triggerID: "trigger-1", status: domain.LoaderRunStatusCanceled, wantStatus: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED},
		{name: "skipped", triggerID: "trigger-1", status: domain.LoaderRunStatusSkipped, wantStatus: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime.runResult = domain.LoaderRunSummary{
				ID:          "run-" + test.name,
				LoaderID:    store.scheduler.ManagedLoaderID,
				TriggerID:   test.triggerID,
				Status:      test.status,
				StartedAt:   time.Unix(100, 0).UTC(),
				PayloadJSON: `{"value":true}`,
			}
			response, err := handler.RunScheduler(context.Background(), connect.NewRequest(&agentcomposev2.RunSchedulerRequest{
				Project:     &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
				AgentName:   store.scheduler.AgentName,
				TriggerId:   test.triggerID,
				PayloadJson: ` { "value" : true } `,
			}))
			if err != nil {
				t.Fatalf("RunScheduler returned error: %v", err)
			}
			if response.Msg.GetRun().GetStatus() != test.wantStatus || response.Msg.GetRun().GetProjectId() != store.project.ID || response.Msg.GetRun().GetAgentName() != store.scheduler.AgentName {
				t.Fatalf("response run = %#v", response.Msg.GetRun())
			}
			if runtime.lastRequest.LoaderID != store.scheduler.ManagedLoaderID || runtime.lastRequest.TriggerID != test.triggerID || runtime.lastRequest.PayloadJSON != `{"value":true}` {
				t.Fatalf("runtime request = %#v", runtime.lastRequest)
			}
		})
	}
}

func TestProjectHandlerRunSchedulerValidatesPayloadAndMissingTrigger(t *testing.T) {
	store, runtime, handler := newSchedulerRunHandlerFixture()
	request := &agentcomposev2.RunSchedulerRequest{
		Project:   &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
		AgentName: store.scheduler.AgentName,
	}
	request.PayloadJson = `{bad`
	if _, err := handler.RunScheduler(context.Background(), connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid payload code=%v err=%v", connect.CodeOf(err), err)
	}
	if runtime.runCalls != 0 {
		t.Fatalf("runtime calls after invalid payload = %d", runtime.runCalls)
	}

	request.PayloadJson = `{}`
	request.TriggerId = "missing"
	runtime.runErr = domain.ResourceError(domain.ErrNotFound, "loader trigger", "missing", "loader trigger missing not found", nil)
	if _, err := handler.RunScheduler(context.Background(), connect.NewRequest(request)); connect.CodeOf(err) != connect.CodeNotFound {
		t.Fatalf("missing trigger code=%v err=%v", connect.CodeOf(err), err)
	}
}

func TestProjectHandlerInvokeSchedulerReturnsValueWithoutRunResource(t *testing.T) {
	store, runtime, handler := newSchedulerRunHandlerFixture()
	runtime.invokeResult = loaders.InvocationResult{ResultJSON: `{"ok":true}`, DurationMs: 42, Warnings: []string{"warning"}}
	response, err := handler.InvokeScheduler(context.Background(), connect.NewRequest(&agentcomposev2.InvokeSchedulerRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, AgentName: store.scheduler.AgentName, PayloadJson: ` { "value" : true } `,
	}))
	if err != nil || response.Msg.GetResultJson() != `{"ok":true}` || response.Msg.GetDurationMs() != 42 || len(response.Msg.GetWarnings()) != 1 {
		t.Fatalf("InvokeScheduler response=%#v err=%v", response, err)
	}
	if runtime.invokeLoaderID != store.scheduler.ManagedLoaderID || runtime.invokePayload != `{"value":true}` {
		t.Fatalf("invocation loader/payload=%q/%q", runtime.invokeLoaderID, runtime.invokePayload)
	}
	store.scheduler.SpecJSON = `{"triggers":[{"name":"nightly"}]}`
	if _, err := handler.InvokeScheduler(context.Background(), connect.NewRequest(&agentcomposev2.InvokeSchedulerRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, AgentName: store.scheduler.AgentName,
	})); connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("declarative invoke code=%v err=%v", connect.CodeOf(err), err)
	}
}

func TestProjectHandlerSchedulerRunLifecycle(t *testing.T) {
	store, runtime, handler := newSchedulerRunHandlerFixture()
	startedAt := time.Unix(200, 0).UTC()
	runtime.startResult = domain.LoaderRunSummary{ID: "run-start", LoaderID: store.scheduler.ManagedLoaderID, TriggerID: "trigger-1", Status: domain.LoaderRunStatusRunning, StartedAt: startedAt}
	started, err := handler.StartSchedulerRun(context.Background(), connect.NewRequest(&agentcomposev2.StartSchedulerRunRequest{
		Project:     &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
		AgentName:   store.scheduler.AgentName,
		TriggerId:   "trigger-1",
		PayloadJson: `{"start":true}`,
	}))
	if err != nil || started.Msg.GetRun().GetStatus() != agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_RUNNING {
		t.Fatalf("StartSchedulerRun response=%#v err=%v", started, err)
	}

	getRun := domain.LoaderRunSummary{ID: "run-get", LoaderID: store.scheduler.ManagedLoaderID, TriggerID: "trigger-1", Status: domain.LoaderRunStatusCanceled, Error: "user stop", StartedAt: startedAt}
	store.runs = []domain.LoaderRunSummary{getRun}
	runtime.getResult = getRun
	got, err := handler.GetSchedulerRun(context.Background(), connect.NewRequest(&agentcomposev2.GetSchedulerRunRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
		RunId:   getRun.ID,
	}))
	if err != nil || got.Msg.GetRun().GetStatus() != agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED || got.Msg.GetRun().GetError() != "user stop" {
		t.Fatalf("GetSchedulerRun response=%#v err=%v", got, err)
	}

	newer := domain.LoaderRunSummary{ID: "run-new", LoaderID: store.scheduler.ManagedLoaderID, TriggerID: "trigger-1", Status: domain.LoaderRunStatusSucceeded, StartedAt: startedAt.Add(time.Second)}
	older := domain.LoaderRunSummary{ID: "run-old", LoaderID: store.scheduler.ManagedLoaderID, TriggerID: "trigger-1", Status: domain.LoaderRunStatusSkipped, StartedAt: startedAt}
	store.runs = []domain.LoaderRunSummary{newer, older}
	store.sandboxIDs = map[loaders.LoaderRunKey][]string{{LoaderID: newer.LoaderID, RunID: newer.ID}: {"sandbox-1"}}
	first, err := handler.ListSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.ListSchedulerRunsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
		Limit:   1,
	}))
	if err != nil || len(first.Msg.GetRuns()) != 1 || first.Msg.GetRuns()[0].GetRunId() != newer.ID || len(first.Msg.GetRuns()[0].GetSandboxIds()) != 1 || first.Msg.GetNextCursor() == "" {
		t.Fatalf("ListSchedulerRuns first=%#v err=%v", first, err)
	}
	second, err := handler.ListSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.ListSchedulerRunsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
		Limit:   1,
		Cursor:  first.Msg.GetNextCursor(),
	}))
	if err != nil || len(second.Msg.GetRuns()) != 1 || second.Msg.GetRuns()[0].GetRunId() != older.ID || second.Msg.GetNextCursor() != "" {
		t.Fatalf("ListSchedulerRuns second=%#v err=%v", second, err)
	}
	if _, err := handler.ListSchedulerRuns(context.Background(), connect.NewRequest(&agentcomposev2.ListSchedulerRunsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, TriggerId: "trigger-1", Status: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED, Limit: 1,
	})); err != nil || !store.lastRunFilter.RequireTrigger || store.lastRunFilter.TriggerID != "trigger-1" || store.lastRunFilter.Status != domain.LoaderRunStatusSkipped {
		t.Fatalf("ListSchedulerRuns filter=%#v err=%v", store.lastRunFilter, err)
	}

	store.runs = []domain.LoaderRunSummary{getRun}
	runtime.stopResult = getRun
	runtime.stopRequested = true
	stopped, err := handler.StopSchedulerRun(context.Background(), connect.NewRequest(&agentcomposev2.StopSchedulerRunRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID},
		RunId:   getRun.ID,
		Reason:  "user stop",
	}))
	if err != nil || !stopped.Msg.GetStopRequested() || runtime.stopReason != "user stop" {
		t.Fatalf("StopSchedulerRun response=%#v reason=%q err=%v", stopped, runtime.stopReason, err)
	}
}

func TestProjectHandlerListsProjectSchedulerEventsWithIdentityAndCursor(t *testing.T) {
	store, _, handler := newSchedulerRunHandlerFixture()
	createdAt := time.Unix(300, 0).UTC()
	store.events = []domain.LoaderEvent{
		{ID: "event-2", LoaderID: store.scheduler.ManagedLoaderID, RunID: "run-1", TriggerID: "trigger-1", Type: "loader.log", LinkedSandboxID: "sandbox-1", CreatedAt: createdAt},
		{ID: "event-1", LoaderID: store.scheduler.ManagedLoaderID, RunID: "run-1", TriggerID: "trigger-1", Type: "loader.run.started", CreatedAt: createdAt.Add(-time.Second)},
	}
	first, err := handler.ListProjectSchedulerEvents(context.Background(), connect.NewRequest(&agentcomposev2.ListProjectSchedulerEventsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, Limit: 1,
	}))
	if err != nil || len(first.Msg.GetEvents()) != 1 || first.Msg.GetEvents()[0].GetAgentName() != store.scheduler.AgentName ||
		first.Msg.GetEvents()[0].GetSchedulerId() != store.scheduler.SchedulerID || first.Msg.GetEvents()[0].GetLinkedSandboxId() != "sandbox-1" || first.Msg.GetNextCursor() == "" {
		t.Fatalf("first event page=%#v err=%v", first, err)
	}
	second, err := handler.ListProjectSchedulerEvents(context.Background(), connect.NewRequest(&agentcomposev2.ListProjectSchedulerEventsRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: store.project.ID}, Limit: 1, Cursor: first.Msg.GetNextCursor(),
	}))
	if err != nil || len(second.Msg.GetEvents()) != 1 || second.Msg.GetEvents()[0].GetId() != "event-1" || second.Msg.GetNextCursor() != "" {
		t.Fatalf("second event page=%#v err=%v", second, err)
	}
}

func TestProjectSchedulerEventCursorBindsAllFilters(t *testing.T) {
	event := domain.LoaderEvent{ID: "event-1", LoaderID: "loader-1", CreatedAt: time.Unix(400, 0).UTC()}
	token := encodeProjectSchedulerEventCursor("project-1", 3, "agent-1", "trigger-1", "run-1", event)
	if _, err := decodeProjectSchedulerEventCursor(token, "project-1", 3, "agent-1", "trigger-1", "run-1"); err != nil {
		t.Fatalf("decode event cursor: %v", err)
	}
	for _, scope := range []struct {
		project             string
		revision            int64
		agent, trigger, run string
	}{
		{project: "project-2", revision: 3, agent: "agent-1", trigger: "trigger-1", run: "run-1"},
		{project: "project-1", revision: 4, agent: "agent-1", trigger: "trigger-1", run: "run-1"},
		{project: "project-1", revision: 3, agent: "agent-2", trigger: "trigger-1", run: "run-1"},
		{project: "project-1", revision: 3, agent: "agent-1", trigger: "trigger-2", run: "run-1"},
		{project: "project-1", revision: 3, agent: "agent-1", trigger: "trigger-1", run: "run-2"},
	} {
		if _, err := decodeProjectSchedulerEventCursor(token, scope.project, scope.revision, scope.agent, scope.trigger, scope.run); err == nil {
			t.Fatalf("cursor accepted scope %#v", scope)
		}
	}
}

func TestSchedulerRunCursorIsScopedToProjectAndAgent(t *testing.T) {
	run := domain.LoaderRunSummary{ID: "run-1", LoaderID: "loader-1", StartedAt: time.Unix(300, 0).UTC()}
	token := encodeSchedulerRunCursor("project-1", 3, "agent-1", "trigger-1", "running", run)
	cursor, err := decodeSchedulerRunCursor(token, "project-1", 3, "agent-1", "trigger-1", "running")
	if err != nil || cursor.RunID != run.ID || cursor.LoaderID != run.LoaderID || !cursor.StartedAt.Equal(run.StartedAt) {
		t.Fatalf("decode cursor=%#v err=%v", cursor, err)
	}
	if _, err := decodeSchedulerRunCursor(token, "project-2", 3, "agent-1", "trigger-1", "running"); err == nil {
		t.Fatal("cursor accepted a different project")
	}
	if _, err := decodeSchedulerRunCursor(token, "project-1", 4, "agent-1", "trigger-1", "running"); err == nil {
		t.Fatal("cursor accepted a different project revision")
	}
	if _, err := decodeSchedulerRunCursor(token, "project-1", 3, "agent-2", "trigger-1", "running"); err == nil {
		t.Fatal("cursor accepted a different agent")
	}
	if _, err := decodeSchedulerRunCursor(token, "project-1", 3, "agent-1", "trigger-2", "running"); err == nil {
		t.Fatal("cursor accepted a different trigger")
	}
	if _, err := decodeSchedulerRunCursor(token, "project-1", 3, "agent-1", "trigger-1", "failed"); err == nil {
		t.Fatal("cursor accepted a different status")
	}
	if _, err := decodeSchedulerRunCursor("not-base64!", "project-1", 3, "agent-1", "trigger-1", "running"); err == nil {
		t.Fatal("invalid cursor returned nil error")
	}
}

func TestSchedulerRunStatusToProto(t *testing.T) {
	tests := map[string]agentcomposev2.SchedulerRunStatus{
		domain.LoaderRunStatusRunning:   agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_RUNNING,
		domain.LoaderRunStatusSucceeded: agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED,
		domain.LoaderRunStatusFailed:    agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED,
		domain.LoaderRunStatusCanceled:  agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED,
		domain.LoaderRunStatusSkipped:   agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED,
		"unknown":                       agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_UNSPECIFIED,
	}
	for status, want := range tests {
		if got := schedulerRunStatusToProto(status); got != want {
			t.Fatalf("schedulerRunStatusToProto(%q)=%v, want %v", status, got, want)
		}
	}
}

func newSchedulerRunHandlerFixture() (*schedulerRunProjectStoreFake, *schedulerRunRuntimeFake, *ProjectHandler) {
	store := &schedulerRunProjectStoreFake{
		project: domain.ProjectRecord{ID: "project-1", Name: "Project", CurrentRevision: 1},
		scheduler: domain.ProjectSchedulerRecord{
			ProjectID:       "project-1",
			AgentName:       "agent-1",
			SchedulerID:     "scheduler-1",
			ManagedLoaderID: "loader-1",
			Revision:        1,
			Enabled:         false,
			SpecJSON:        `{"script":"function main() {}"}`,
		},
	}
	runtime := &schedulerRunRuntimeFake{}
	return store, runtime, NewProjectHandler(nil, store, runtime)
}

type schedulerRunProjectStoreFake struct {
	project       domain.ProjectRecord
	scheduler     domain.ProjectSchedulerRecord
	schedulers    []domain.ProjectSchedulerRecord
	runs          []domain.LoaderRunSummary
	events        []domain.LoaderEvent
	sandboxIDs    map[loaders.LoaderRunKey][]string
	lastRunFilter loaders.LoaderRunPageFilter
}

func (s *schedulerRunProjectStoreFake) ListLoaderEventsPage(_ context.Context, filter loaders.LoaderEventPageFilter) ([]domain.LoaderEvent, error) {
	start := 0
	if filter.BeforeEventID != "" {
		start = len(s.events)
		for index, event := range s.events {
			if event.ID == filter.BeforeEventID {
				start = index + 1
				break
			}
		}
	}
	end := min(start+filter.Limit, len(s.events))
	return append([]domain.LoaderEvent(nil), s.events[start:end]...), nil
}

func (s *schedulerRunProjectStoreFake) GetProject(context.Context, string) (domain.ProjectRecord, error) {
	return s.project, nil
}

func (s *schedulerRunProjectStoreFake) ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error) {
	return domain.ProjectListResult{Projects: []domain.ProjectRecord{s.project}}, nil
}

func (s *schedulerRunProjectStoreFake) ListProjectAgents(context.Context, string) ([]domain.ProjectAgentRecord, error) {
	return nil, nil
}

func (s *schedulerRunProjectStoreFake) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	if s.schedulers != nil {
		return append([]domain.ProjectSchedulerRecord(nil), s.schedulers...), nil
	}
	return []domain.ProjectSchedulerRecord{s.scheduler}, nil
}

func (s *schedulerRunProjectStoreFake) GetProjectRevision(context.Context, string, int64) (domain.ProjectRevisionRecord, error) {
	return domain.ProjectRevisionRecord{}, nil
}

func (s *schedulerRunProjectStoreFake) GetLoaderRunForLoaders(_ context.Context, loaderIDs []string, runID string) (domain.LoaderRunSummary, error) {
	for _, run := range s.runs {
		if run.ID != runID {
			continue
		}
		for _, loaderID := range loaderIDs {
			if run.LoaderID == loaderID {
				return run, nil
			}
		}
	}
	return domain.LoaderRunSummary{}, domain.ResourceError(domain.ErrNotFound, "loader run", runID, fmt.Sprintf("loader run %s not found", runID), nil)
}

func (s *schedulerRunProjectStoreFake) ListLoaderRunsPage(_ context.Context, filter loaders.LoaderRunPageFilter) ([]domain.LoaderRunSummary, error) {
	s.lastRunFilter = filter
	start := 0
	if filter.BeforeRunID != "" {
		start = len(s.runs)
		for index, run := range s.runs {
			if run.ID == filter.BeforeRunID {
				start = index + 1
				break
			}
		}
	}
	end := min(start+filter.Limit, len(s.runs))
	return append([]domain.LoaderRunSummary(nil), s.runs[start:end]...), nil
}

func (s *schedulerRunProjectStoreFake) ListLoaderRunSandboxIDs(_ context.Context, _ []loaders.LoaderRunKey) (map[loaders.LoaderRunKey][]string, error) {
	return s.sandboxIDs, nil
}

type schedulerRunRuntimeFake struct {
	runResult      domain.LoaderRunSummary
	startResult    domain.LoaderRunSummary
	getResult      domain.LoaderRunSummary
	stopResult     domain.LoaderRunSummary
	runErr         error
	stopRequested  bool
	runCalls       int
	lastRequest    loaders.SchedulerRunRequest
	stopReason     string
	invokeResult   loaders.InvocationResult
	invokeLoaderID string
	invokePayload  string
	pruneRequest   loaders.SchedulerRunPruneRequest
	pruneResult    loaders.SchedulerRunPruneResult
	pruneErr       error
}

func (f *schedulerRunRuntimeFake) PruneSchedulerRuns(_ context.Context, request loaders.SchedulerRunPruneRequest) (loaders.SchedulerRunPruneResult, error) {
	f.pruneRequest = request
	return f.pruneResult, f.pruneErr
}

func (f *schedulerRunRuntimeFake) InvokeScheduler(_ context.Context, loaderID, payloadJSON string) (loaders.InvocationResult, error) {
	f.invokeLoaderID = loaderID
	f.invokePayload = payloadJSON
	return f.invokeResult, nil
}

func (f *schedulerRunRuntimeFake) SetLoaderEnabled(context.Context, string, bool) (domain.Loader, error) {
	return domain.Loader{}, nil
}

func (f *schedulerRunRuntimeFake) SetLoaderTriggerEnabled(context.Context, string, string, bool) (domain.Loader, error) {
	return domain.Loader{}, nil
}

func (f *schedulerRunRuntimeFake) RunScheduler(_ context.Context, request loaders.SchedulerRunRequest) (domain.LoaderRunSummary, error) {
	f.runCalls++
	f.lastRequest = request
	return f.runResult, f.runErr
}

func (f *schedulerRunRuntimeFake) StartSchedulerRun(_ context.Context, request loaders.SchedulerRunRequest) (domain.LoaderRunSummary, error) {
	f.lastRequest = request
	return f.startResult, nil
}

func (f *schedulerRunRuntimeFake) GetSchedulerRun(context.Context, string, string) (domain.LoaderRunSummary, error) {
	return f.getResult, nil
}

func (f *schedulerRunRuntimeFake) StopSchedulerRun(_ context.Context, _, _, reason string) (domain.LoaderRunSummary, bool, error) {
	f.stopReason = reason
	return f.stopResult, f.stopRequested, nil
}
