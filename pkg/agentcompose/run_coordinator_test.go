package agentcompose

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	loaderspkg "agent-compose/pkg/loaders"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestIntegrationRunAgentStreamReturnsRealtimeOutput(t *testing.T) {
	testRunAgentStreamReturnsRealtimeOutput(t)
}

func TestE2ERunAgentStreamReturnsRealtimeOutput(t *testing.T) {
	testRunAgentStreamReturnsRealtimeOutput(t)
}

func testRunAgentStreamReturnsRealtimeOutput(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "stream review",
		Source:          agentcomposev2.RunSource_RUN_SOURCE_API,
		ClientRequestId: "stream-success-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream returned error: %v", err)
	}
	completed := lastRunAgentStreamEvent(events, agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED)
	if completed == nil || completed.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED || completed.GetRunId() == "" {
		t.Fatalf("completed stream event = %#v events=%#v", completed, events)
	}
	stored, err := store.GetProjectRun(ctx, completed.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun stream returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusSucceeded || stored.SessionID == "" || !strings.Contains(stored.Output, "loader agent transcript") {
		t.Fatalf("stored stream run = %#v", stored)
	}
}

func TestIntegrationRunAgentStreamAgentFailurePersistsRun(t *testing.T) {
	testRunAgentStreamAgentFailurePersistsRun(t)
}

func TestE2ERunAgentStreamAgentFailurePersistsRun(t *testing.T) {
	testRunAgentStreamAgentFailurePersistsRun(t)
}

func TestRunAgentStreamEmptyStdoutFailureIncludesProviderStderr(t *testing.T) {
	_, service, projectID := setupRunCoordinatorProject(t)
	runtime := runServiceFakeRuntime(t, service)
	runtime.agentExitCode = 1
	runtime.agentNoPayload = true
	runtime.agentStderr = `Codex provider config error: wire_api = "chat" is no longer supported`
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "trigger provider config failure",
		ClientRequestId: "stream-agent-empty-stdout-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream empty stdout failure returned RPC error: %v", err)
	}
	completed := lastRunAgentStreamEvent(events, agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED)
	if completed == nil || completed.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED {
		t.Fatalf("completed failure event = %#v events=%#v", completed, events)
	}
	if !strings.Contains(completed.GetRun().GetError(), "wire_api") || !strings.Contains(completed.GetRun().GetError(), "chat") {
		t.Fatalf("completed failure error = %q, want provider stderr", completed.GetRun().GetError())
	}
}

func testRunAgentStreamAgentFailurePersistsRun(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	runServiceFakeRuntime(t, service).agentExitCode = 7
	client, closeServer := newRunServiceTestClient(t, service)
	defer closeServer()
	ctx := context.Background()

	events, err := collectRunAgentStreamEvents(ctx, client, &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "stream failure",
		ClientRequestId: "stream-agent-failure-request",
	})
	if err != nil {
		t.Fatalf("RunAgentStream failure returned RPC error: %v", err)
	}
	completed := lastRunAgentStreamEvent(events, agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED)
	if completed == nil || completed.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED || completed.GetRun().GetExitCode() != 7 || completed.GetRun().GetSessionId() == "" {
		t.Fatalf("completed failure event = %#v events=%#v", completed, events)
	}
	stored, err := store.GetProjectRun(ctx, completed.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun failure returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusFailed || stored.ExitCode != 7 || stored.SessionID != completed.GetRun().GetSessionId() {
		t.Fatalf("stored failed stream run = %#v", stored)
	}
}

func TestIntegrationManagedSchedulerAgentUsesProjectRunPipeline(t *testing.T) {
	testManagedSchedulerAgentUsesProjectRunPipeline(t)
}

func TestE2EManagedSchedulerAgentUsesProjectRunPipeline(t *testing.T) {
	testManagedSchedulerAgentUsesProjectRunPipeline(t)
}

func TestManagedSchedulerManualRunPreservesProjectSecretEnv(t *testing.T) {
	spec := newProjectServiceTestSpec("scheduler-secret", "gpt-test")
	spec.Agents[0].Env = []*agentcomposev2.EnvVarSpec{
		{Name: "SAFELINE_API_TOKEN", Value: "valid-token", Secret: true},
	}
	store, service, projectID := setupRunPreparationProject(t, spec, t.TempDir())
	manager := newRunServiceLoaderManager(t, service)
	ctx := context.Background()
	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers returned error: %v", err)
	}
	loader, err := store.GetLoader(ctx, schedulers[0].ManagedLoaderID)
	if err != nil {
		t.Fatalf("GetLoader managed scheduler returned error: %v", err)
	}
	_, err = manager.RunNow(ctx, loader.Summary.ID, loader.Triggers[0].ID, `{}`, 0)
	if err != nil {
		t.Fatalf("RunNow manual managed scheduler returned error: %v", err)
	}
	runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
		ProjectID:   projectID,
		Source:      ProjectRunSourceScheduler,
		SchedulerID: loader.Summary.ManagedSchedulerID,
	})
	if err != nil {
		t.Fatalf("ListProjectRunsByOptions scheduler returned error: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("scheduler project runs = %#v", runs)
	}
	session, err := service.store.GetSession(ctx, runs[0].SessionID)
	if err != nil {
		t.Fatalf("GetSession scheduler manual run returned error: %v", err)
	}
	env := envItemsByName(session.EnvItems)
	if got := env["SAFELINE_API_TOKEN"]; got.Value != "valid-token" || !got.Secret {
		t.Fatalf("SAFELINE_API_TOKEN env = %#v, want preserved secret value", got)
	}
}

func testManagedSchedulerAgentUsesProjectRunPipeline(t *testing.T) {
	store, service, projectID := setupRunCoordinatorProject(t)
	manager := newRunServiceLoaderManager(t, service)
	ctx := context.Background()
	schedulers, err := store.ListProjectSchedulers(ctx, projectID)
	if err != nil {
		t.Fatalf("ListProjectSchedulers returned error: %v", err)
	}
	loader, err := store.GetLoader(ctx, schedulers[0].ManagedLoaderID)
	if err != nil {
		t.Fatalf("GetLoader managed scheduler returned error: %v", err)
	}
	run, err := manager.RunNow(ctx, loader.Summary.ID, loader.Triggers[0].ID, `{"prompt":"scheduled project prompt"}`, 0)
	if err != nil {
		t.Fatalf("RunNow managed scheduler returned error: %v", err)
	}
	runs, err := store.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
		ProjectID:   projectID,
		Source:      ProjectRunSourceScheduler,
		SchedulerID: loader.Summary.ManagedSchedulerID,
	})
	if err != nil {
		t.Fatalf("ListProjectRunsByOptions scheduler returned error: %v", err)
	}
	if len(runs) != 1 || runs[0].Status != ProjectRunStatusSucceeded || runs[0].SessionID == "" {
		t.Fatalf("scheduler project runs = %#v loaderRun=%#v", runs, run)
	}
	if events, err := store.ListLoaderEvents(ctx, loader.Summary.ID, 20); err != nil || !loaderEventsContain(events, "loader.agent.completed") {
		t.Fatalf("loader events = %#v err=%v", events, err)
	}
}

func setupRunCoordinatorProject(t *testing.T) (*ConfigStore, *Service, string) {
	t.Helper()
	return setupRunPreparationProject(t, newProjectServiceTestSpec("demo", "gpt-test"), t.TempDir())
}

func setupRunPreparationProject(t *testing.T, spec *agentcomposev2.ProjectSpec, projectDir string) (*ConfigStore, *Service, string) {
	t.Helper()
	store := newTestConfigStore(t)
	service := newProjectServiceTestService(t, store)
	service.config.DataRoot = filepath.Join(t.TempDir(), "data")
	service.config.SessionRoot = filepath.Join(t.TempDir(), "sessions")
	service.config.JupyterProxyBasePath = "/agent-compose/session"
	service.config.JupyterGuestPort = 8888
	service.store = mustTestStore(t, service.config)
	service.driver = &fakeSessionDriver{}
	runtime := &fakeLoaderAgentRuntime{}
	runtimes := fixedRuntimeProvider{runtime: runtime}
	streams := &SessionStreamBroker{subscribers: map[string]map[int]chan sessionWatchEvent{}}
	service.runtimes = runtimes
	service.streams = streams
	service.executor = &Executor{config: service.config, store: service.store, configDB: store, runtimes: runtimes, streams: streams}
	ctx := context.Background()
	composePath := filepath.Join(projectDir, "agent-compose.yml")
	resp, err := service.ApplyProject(ctx, connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec:   spec,
		Source: &agentcomposev2.ProjectSource{ComposePath: composePath},
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if !resp.Msg.GetApplied() {
		t.Fatalf("ApplyProject response = %#v", resp.Msg)
	}
	return store, service, resp.Msg.GetProject().GetSummary().GetProjectId()
}

func newRunServiceTestClient(t *testing.T, service *Service) (agentcomposev2connect.RunServiceClient, func()) {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(service)
	mux.Handle(path, handler)
	server := httptest.NewServer(mux)
	return agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL), server.Close
}

func collectRunAgentStreamEvents(ctx context.Context, client agentcomposev2connect.RunServiceClient, req *agentcomposev2.RunAgentRequest) ([]*agentcomposev2.RunAgentStreamResponse, error) {
	stream, err := client.RunAgentStream(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	var events []*agentcomposev2.RunAgentStreamResponse
	for stream.Receive() {
		events = append(events, stream.Msg())
	}
	return events, stream.Err()
}

func lastRunAgentStreamEvent(events []*agentcomposev2.RunAgentStreamResponse, eventType agentcomposev2.RunAgentStreamEventType) *agentcomposev2.RunAgentStreamResponse {
	for index := len(events) - 1; index >= 0; index-- {
		if events[index].GetEventType() == eventType {
			return events[index]
		}
	}
	return nil
}

func runServiceFakeRuntime(t *testing.T, service *Service) *fakeLoaderAgentRuntime {
	t.Helper()
	provider, ok := service.runtimes.(fixedRuntimeProvider)
	if !ok {
		t.Fatalf("service runtime provider = %T, want fixedRuntimeProvider", service.runtimes)
	}
	runtime, ok := provider.runtime.(*fakeLoaderAgentRuntime)
	if !ok {
		t.Fatalf("fixed runtime = %T, want *fakeLoaderAgentRuntime", provider.runtime)
	}
	return runtime
}

func newRunServiceLoaderManager(t testing.TB, service *Service) *LoaderManager {
	t.Helper()
	var componentStreams *loaderspkg.SessionStreamBroker
	if service.streams != nil {
		componentStreams = service.streams.componentBroker()
	}
	return newTestLoaderManager(t, loaderspkg.ManagerDeps{
		Config:             service.config,
		RootCtx:            context.Background(),
		Store:              service.store,
		ConfigDB:           service.configDB,
		Driver:             service.driver,
		Executor:           loaderspkg.NewExecutor(service.config, service.store, service.configDB, service.runtimes, componentStreams),
		Streams:            componentStreams,
		Bus:                NewLoaderBusWithBuffer(16),
		Engine:             &QJSLoaderEngine{},
		ProjectAgentRunner: service.projectService(),
	})
}

func loaderEventsContain(events []LoaderEvent, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func envItemsByName(items []SessionEnvVar) map[string]SessionEnvVar {
	env := make(map[string]SessionEnvVar, len(items))
	for _, item := range items {
		env[item.Name] = item
	}
	return env
}
