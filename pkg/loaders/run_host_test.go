package loaders_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func TestRuntimeHostAgentCommandLLMAndSessionRPC(t *testing.T) {
	ctx := context.Background()
	loader := domain.Loader{
		Summary: domain.LoaderSummary{ID: "loader-host", Name: "Loader Host", Runtime: domain.LoaderRuntimeScheduler, DefaultAgent: "gemini"},
	}
	run := &domain.LoaderRunSummary{ID: "run-host", LoaderID: loader.Summary.ID, TriggerID: "trigger-host"}
	store := &hostStoreFake{}
	events := &hostEventsFake{}
	sessions := &hostSessionsFake{
		session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-host", VMStatus: domain.VMStatusRunning}},
	}
	agentExecutor := &hostAgentExecutorFake{cell: domain.NotebookCell{
		ID:            "cell-agent",
		Output:        "agent text",
		Agent:         "gemini",
		AgentThreadID: "agent-session",
		StopReason:    "complete",
		Success:       true,
	}}
	commandExecutor := &hostCommandExecutorFake{result: domain.LoaderCommandResult{
		Output:    "command output",
		Success:   true,
		SandboxID: "session-host",
		CellID:    "cell-command",
	}}
	llm := &hostLLMFake{result: domain.LoaderLLMResult{Text: "llm text", Model: "model-a", ResponseID: "resp-1", FinishReason: "stop"}}
	rpc := &hostRPCFake{response: `{"sessionId":"sandbox-rpc"}`}
	publisher := &hostPublisherFake{}
	host := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Store:            store,
		Events:           events,
		Sessions:         sessions,
		AgentDefinitions: hostAgentDefinitionsFake{},
		AgentExecutor:    agentExecutor,
		CommandExecutor:  commandExecutor,
		LLM:              llm,
		SessionRPC:       rpc,
		Publisher:        publisher,
		CommandRequiresCleanup: func(domain.Loader, domain.LoaderCommandRequest) bool {
			return true
		},
		LinkedSandboxIDFromJSON: func(_, _, responseJSON string) string {
			if strings.Contains(responseJSON, "sandbox-rpc") {
				return "sandbox-rpc"
			}
			return ""
		},
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{EventID: "topic-event"})

	agentResult, err := host.Agent(ctx, "summarize", domain.LoaderAgentRequest{})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if agentResult.Text != "agent text" || agentExecutor.request.Provider != "gemini" || len(sessions.shutdowns) != 1 {
		t.Fatalf("agent result/request/shutdowns = %#v/%#v/%#v", agentResult, agentExecutor.request, sessions.shutdowns)
	}
	if len(publisher.events) != 1 || publisher.events[0].topic != "agent-compose.agent.completed" {
		t.Fatalf("publisher events = %#v", publisher.events)
	}
	if !events.contains("loader.sandbox.created") || !events.contains("loader.agent.completed") || !events.contains("loader.sandbox.stopped") {
		t.Fatalf("agent events = %#v", events.types())
	}

	_, err = host.Command(ctx, domain.LoaderCommandRequest{Mode: "shell", Command: "echo one"})
	if err != nil {
		t.Fatalf("Command first returned error: %v", err)
	}
	_, err = host.Command(ctx, domain.LoaderCommandRequest{Mode: "shell", Command: "echo two"})
	if err != nil {
		t.Fatalf("Command second returned error: %v", err)
	}
	if sessions.ensureCalls != 2 || sessions.loadCalls != 1 || commandExecutor.calls != 2 {
		t.Fatalf("command ensure/load/exec calls = %d/%d/%d", sessions.ensureCalls, sessions.loadCalls, commandExecutor.calls)
	}
	host.CleanupCommandSessions(ctx)
	if len(sessions.shutdowns) != 2 || sessions.shutdowns[1] != "session-host" {
		t.Fatalf("shutdowns after cleanup = %#v", sessions.shutdowns)
	}
	if !events.contains("loader.command.completed") {
		t.Fatalf("command events = %#v", events.types())
	}

	llmResult, err := host.LLM(ctx, "prompt", domain.LoaderLLMRequest{Model: "model-a"})
	if err != nil {
		t.Fatalf("LLM returned error: %v", err)
	}
	if llmResult.Text != "llm text" || llm.prompt != "prompt" {
		t.Fatalf("llm result/prompt = %#v/%q", llmResult, llm.prompt)
	}
	if !events.contains("loader.llm.completed") {
		t.Fatalf("llm events = %#v", events.types())
	}

	responseJSON, err := host.CallSessionRPC(ctx, "GetSession", `{"sessionId":"sandbox-rpc"}`)
	if err != nil {
		t.Fatalf("CallSessionRPC returned error: %v", err)
	}
	if responseJSON != rpc.response || rpc.source != domain.SandboxTypeScript+":"+loader.Summary.ID {
		t.Fatalf("rpc response/source = %q/%q", responseJSON, rpc.source)
	}
	if !store.containsLink("sandbox-rpc", "sandbox_rpc_completed") {
		t.Fatalf("sandbox links = %#v", store.links)
	}
}

func TestRuntimeHostProjectAgentPath(t *testing.T) {
	ctx := context.Background()
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:                 "loader-project",
		ManagedProjectID:   "project-1",
		ManagedAgentName:   "reviewer",
		ManagedSchedulerID: "scheduler-1",
	}}
	run := &domain.LoaderRunSummary{ID: "run-project", LoaderID: loader.Summary.ID, TriggerID: "trigger-1"}
	events := &hostEventsFake{}
	projectRunner := &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{
		RunID:      "project-run",
		ProjectID:  "project-1",
		AgentName:  "reviewer",
		Status:     domain.ProjectRunStatusSucceeded,
		SandboxID:  "session-project",
		Output:     "project output",
		ResultJSON: `{"ok":true}`,
	}}
	publisher := &hostPublisherFake{}
	host := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Store:              &hostStoreFake{},
		Events:             events,
		ProjectAgentRunner: projectRunner,
		Publisher:          publisher,
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{EventID: "topic-event"})

	result, err := host.Agent(ctx, "review", domain.LoaderAgentRequest{})
	if err != nil {
		t.Fatalf("Project Agent returned error: %v", err)
	}
	if result.Text != "project output" || projectRunner.request.ProjectID != "project-1" || projectRunner.request.ClientRequestID != run.ID+":agent:1" || projectRunner.request.SandboxConfigHash == "" {
		t.Fatalf("project result/request = %#v/%#v", result, projectRunner.request)
	}
	if !events.contains("loader.agent.completed") || len(publisher.events) != 1 || publisher.events[0].payload["projectRunId"] != "project-run" {
		t.Fatalf("events/publisher = %#v/%#v", events.types(), publisher.events)
	}
	if publisher.events[0].payload["loaderRunId"] != run.ID {
		t.Fatalf("trigger execution publisher payload = %#v", publisher.events[0].payload)
	}
	invocationPublisher := &hostPublisherFake{}
	invocationHost := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		ProjectAgentRunner: projectRunner,
		Publisher:          invocationPublisher,
	}, loader, loaders.RuntimeExecutionContext{ID: "invocation-correlation", Kind: loaders.ExecutionKindInvocation}, loaders.TriggerEventMetadata{})
	if _, err := invocationHost.Agent(ctx, "review", domain.LoaderAgentRequest{}); err != nil {
		t.Fatalf("Invocation Project Agent returned error: %v", err)
	}
	if len(invocationPublisher.events) != 1 || invocationPublisher.events[0].payload["projectRunId"] != "project-run" {
		t.Fatalf("invocation publisher = %#v", invocationPublisher.events)
	}
	if _, ok := invocationPublisher.events[0].payload["loaderRunId"]; ok {
		t.Fatalf("invocation payload exposed correlation id as loader run: %#v", invocationPublisher.events[0].payload)
	}
}

func TestRuntimeHostProjectAgentUsesUniqueRequestIDs(t *testing.T) {
	loader := domain.Loader{Summary: domain.LoaderSummary{
		ID:               "loader-project",
		ManagedProjectID: "project-1",
		ManagedAgentName: "reviewer",
	}}
	run := &domain.LoaderRunSummary{ID: "run-project", LoaderID: loader.Summary.ID, TriggerID: "trigger-1"}
	projectRunner := &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{
		RunID:     "project-run",
		ProjectID: "project-1",
		AgentName: "reviewer",
		Status:    domain.ProjectRunStatusSucceeded,
	}}
	host := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		ProjectAgentRunner: projectRunner,
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{})

	for range 2 {
		if _, err := host.Agent(context.Background(), "review", domain.LoaderAgentRequest{}); err != nil {
			t.Fatalf("Project Agent returned error: %v", err)
		}
	}

	if len(projectRunner.requests) != 2 || projectRunner.requests[0].ClientRequestID != run.ID+":agent:1" || projectRunner.requests[1].ClientRequestID != run.ID+":agent:2" {
		t.Fatalf("project request IDs = %#v", projectRunner.requests)
	}
}

func TestRuntimeHostErrorBranches(t *testing.T) {
	ctx := context.Background()
	loader := domain.Loader{
		Summary: domain.LoaderSummary{ID: "loader-errors", Name: "Loader Errors", Runtime: domain.LoaderRuntimeScheduler, DefaultAgent: "codex"},
	}
	run := &domain.LoaderRunSummary{ID: "run-errors", LoaderID: loader.Summary.ID, TriggerID: "trigger-errors"}

	events := &hostEventsFake{}
	host := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Events: events,
		Sessions: &hostSessionsFake{
			session:     &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-agent", VMStatus: domain.VMStatusRunning}},
			shutdownErr: errors.New("stop failed"),
		},
		AgentDefinitions: hostAgentDefinitionsFake{},
		AgentExecutor: &hostAgentExecutorFake{
			cell: domain.NotebookCell{ID: "cell-agent", Stderr: "agent stderr", Success: false, ExitCode: 2},
			err:  errors.New("agent failed"),
		},
		Publisher: &hostPublisherFake{},
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{EventID: "topic-event"})
	agentResult, err := host.Agent(ctx, "prompt", domain.LoaderAgentRequest{})
	if err == nil || agentResult.Text != "agent stderr" || !events.contains("loader.agent.failed") || !events.contains("loader.sandbox.stop_failed") {
		t.Fatalf("agent error result=%#v err=%v events=%#v", agentResult, err, events.types())
	}

	projectEvents := &hostEventsFake{}
	projectLoader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-project-errors", ManagedProjectID: "project-1", ManagedAgentName: "reviewer"}}
	projectHost := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Events: projectEvents,
		ProjectAgentRunner: &hostProjectAgentRunnerFake{run: domain.ProjectRunRecord{
			RunID:     "project-run",
			ProjectID: "project-1",
			AgentName: "reviewer",
			Status:    domain.ProjectRunStatusFailed,
			Error:     "project failed",
		}},
		Publisher: &hostPublisherFake{},
	}, projectLoader, triggerExecution(run), loaders.TriggerEventMetadata{})
	projectResult, err := projectHost.Agent(ctx, "prompt", domain.LoaderAgentRequest{})
	if err != nil || projectResult.Text != "project failed" || !projectEvents.contains("loader.agent.failed") {
		t.Fatalf("project failed result=%#v err=%v events=%#v", projectResult, err, projectEvents.types())
	}
	projectHost = loaders.NewRuntimeHost(loaders.RunHostDependencies{
		ProjectAgentRunner: &hostProjectAgentRunnerFake{err: errors.New("project unavailable")},
	}, projectLoader, triggerExecution(run), loaders.TriggerEventMetadata{})
	if _, err := projectHost.Agent(ctx, "prompt", domain.LoaderAgentRequest{}); err == nil {
		t.Fatalf("project runner error returned nil")
	}

	commandEvents := &hostEventsFake{}
	commandHost := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Events: commandEvents,
		Sessions: &hostSessionsFake{
			session:   &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-command", VMStatus: domain.VMStatusRunning}},
			ensureErr: errors.New("ensure failed"),
		},
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{})
	if _, err := commandHost.Command(ctx, domain.LoaderCommandRequest{Mode: "shell", Command: "echo"}); err == nil || !commandEvents.contains("loader.command.failed") {
		t.Fatalf("command ensure err=%v events=%#v", err, commandEvents.types())
	}
	commandEvents = &hostEventsFake{}
	commandHost = loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Events:          commandEvents,
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-command", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: &hostCommandExecutorFake{err: errors.New("command failed"), result: domain.LoaderCommandResult{SandboxID: "session-command", CellID: "cell-command", Output: "partial"}},
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{})
	if result, err := commandHost.Command(ctx, domain.LoaderCommandRequest{Mode: "shell", Command: "false"}); err == nil || result.Output != "partial" || !commandEvents.contains("loader.command.failed") {
		t.Fatalf("command executor result=%#v err=%v events=%#v", result, err, commandEvents.types())
	}
	commandEvents = &hostEventsFake{}
	commandHost = loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Events:          commandEvents,
		Sessions:        &hostSessionsFake{session: &domain.Sandbox{Summary: domain.SandboxSummary{ID: "session-command", VMStatus: domain.VMStatusRunning}}},
		CommandExecutor: &hostCommandExecutorFake{result: domain.LoaderCommandResult{Output: "bad", Success: false, SandboxID: "session-command"}},
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{})
	if result, err := commandHost.Command(ctx, domain.LoaderCommandRequest{Mode: "shell", Command: "false"}); err != nil || result.Success || !commandEvents.contains("loader.command.completed") {
		t.Fatalf("command nonzero result=%#v err=%v events=%#v", result, err, commandEvents.types())
	}

	if _, err := loaders.NewRuntimeHost(loaders.RunHostDependencies{}, loader, triggerExecution(run), loaders.TriggerEventMetadata{}).LLM(ctx, "prompt", domain.LoaderLLMRequest{}); err == nil {
		t.Fatalf("nil LLM returned nil error")
	}
	llmEvents := &hostEventsFake{}
	llmHost := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Events: llmEvents,
		LLM:    &hostLLMFake{err: errors.New("llm failed")},
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{})
	if _, err := llmHost.LLM(ctx, "prompt", domain.LoaderLLMRequest{Model: "model-a"}); err == nil || !llmEvents.contains("loader.llm.failed") {
		t.Fatalf("llm err=%v events=%#v", err, llmEvents.types())
	}

	if _, err := loaders.NewRuntimeHost(loaders.RunHostDependencies{}, loader, triggerExecution(run), loaders.TriggerEventMetadata{}).CallSessionRPC(ctx, "GetSession", `{}`); err == nil {
		t.Fatalf("nil session RPC returned nil error")
	}
	rpcEvents := &hostEventsFake{}
	rpcStore := &hostStoreFake{}
	rpcHost := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Store:      rpcStore,
		Events:     rpcEvents,
		SessionRPC: &hostRPCFake{response: `{"sessionId":"sandbox-rpc"}`, err: errors.New("rpc failed")},
		LinkedSandboxIDFromJSON: func(_, _, responseJSON string) string {
			if strings.Contains(responseJSON, "sandbox-rpc") {
				return "sandbox-rpc"
			}
			return ""
		},
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{EventID: "topic-event"})
	if _, err := rpcHost.CallSessionRPC(ctx, "GetSession", `{"sessionId":"sandbox-rpc"}`); err == nil || !rpcEvents.contains("loader.sandbox.rpc.failed") || !rpcStore.containsLink("sandbox-rpc", "sandbox_rpc_failed") {
		t.Fatalf("rpc err=%v events=%#v links=%#v", err, rpcEvents.types(), rpcStore.links)
	}
}

func TestRuntimeHostLogPublishEventAndState(t *testing.T) {
	ctx := context.Background()
	loader := domain.Loader{Summary: domain.LoaderSummary{ID: "loader-state", Name: "State Loader"}}
	run := &domain.LoaderRunSummary{ID: "run-state", LoaderID: loader.Summary.ID, TriggerID: "trigger-state"}
	store := &hostStoreFake{}
	events := &hostEventsFake{}
	host := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Store:  store,
		Events: events,
	}, loader, triggerExecution(run), loaders.TriggerEventMetadata{
		EventID:       "trigger-event",
		CorrelationID: "correlation-1",
	})

	if err := host.Log(ctx, "hello", map[string]any{"ok": true}); err != nil {
		t.Fatalf("Log returned error: %v", err)
	}
	if !events.contains("loader.log") {
		t.Fatalf("events after Log = %#v", events.types())
	}

	created, err := host.PublishEvent(ctx, "runtime.demo", `{"value":1}`)
	if err != nil {
		t.Fatalf("PublishEvent returned error: %v", err)
	}
	if created.Topic != "runtime.demo" || created.Sequence != 7 || created.PayloadJSON == `{"value":1}` {
		t.Fatalf("created event = %#v", created)
	}
	if created.PublisherRunID != run.ID {
		t.Fatalf("trigger publisher run ID = %q, want %q", created.PublisherRunID, run.ID)
	}
	if !events.contains("loader.event.published") {
		t.Fatalf("events after PublishEvent = %#v", events.types())
	}
	invocationStore := &hostStoreFake{}
	invocationHost := loaders.NewRuntimeHost(loaders.RunHostDependencies{Store: invocationStore}, loader, loaders.RuntimeExecutionContext{
		ID: "invocation-correlation", Kind: loaders.ExecutionKindInvocation,
	}, loaders.TriggerEventMetadata{})
	invocationEvent, err := invocationHost.PublishEvent(ctx, "runtime.demo", `{"value":2}`)
	if err != nil {
		t.Fatalf("Invocation PublishEvent returned error: %v", err)
	}
	if invocationEvent.PublisherRunID != "" {
		t.Fatalf("invocation correlation ID exposed as publisher run ID: %#v", invocationEvent)
	}

	if err := host.StateSet(ctx, "cursor", `{"offset":2}`); err != nil {
		t.Fatalf("StateSet returned error: %v", err)
	}
	value, ok, err := host.StateGet(ctx, "cursor")
	if err != nil || !ok || value != `{"offset":2}` {
		t.Fatalf("StateGet value=%q ok=%v err=%v", value, ok, err)
	}
	if err := host.StateDelete(ctx, "cursor"); err != nil {
		t.Fatalf("StateDelete returned error: %v", err)
	}
	if _, ok, err := host.StateGet(ctx, "cursor"); err != nil || ok {
		t.Fatalf("StateGet after delete ok=%v err=%v", ok, err)
	}

	missingStoreHost := loaders.NewRuntimeHost(loaders.RunHostDependencies{}, loader, triggerExecution(run), loaders.TriggerEventMetadata{})
	if _, err := missingStoreHost.PublishEvent(ctx, "runtime.demo", `{}`); err == nil || !strings.Contains(err.Error(), "event store is unavailable") {
		t.Fatalf("PublishEvent missing store error = %v", err)
	}
}

func triggerExecution(run *domain.LoaderRunSummary) loaders.RuntimeExecutionContext {
	return loaders.RuntimeExecutionContext{ID: run.ID, TriggerID: run.TriggerID, Kind: loaders.ExecutionKindTrigger}
}

type hostStoreFake struct {
	events []domain.TopicEventRecord
	state  map[string]string
	links  []domain.EventSandboxLink
}

func (s *hostStoreFake) CreateEvent(_ context.Context, event domain.TopicEventRecord) (domain.TopicEventRecord, error) {
	event.ID = firstNonEmptyTest(event.ID, "event-created")
	event.Sequence = 7
	s.events = append(s.events, event)
	return event, nil
}

func (s *hostStoreFake) UpdateEventPayload(_ context.Context, eventID, payloadJSON string) error {
	for index := range s.events {
		if s.events[index].ID == eventID {
			s.events[index].PayloadJSON = payloadJSON
		}
	}
	return nil
}

func (s *hostStoreFake) GetLoaderState(_ context.Context, _, key string) (string, bool, error) {
	value, ok := s.state[key]
	return value, ok, nil
}

func (s *hostStoreFake) SetLoaderState(_ context.Context, _, key, valueJSON string) error {
	if s.state == nil {
		s.state = map[string]string{}
	}
	s.state[key] = valueJSON
	return nil
}

func (s *hostStoreFake) DeleteLoaderState(_ context.Context, _, key string) error {
	delete(s.state, key)
	return nil
}

func (s *hostStoreFake) AddEventSandboxLink(_ context.Context, link domain.EventSandboxLink) error {
	s.links = append(s.links, link)
	return nil
}

func (s *hostStoreFake) containsLink(sessionID, relation string) bool {
	for _, link := range s.links {
		if link.SandboxID == sessionID && link.Relation == relation {
			return true
		}
	}
	return false
}

type hostEventsFake struct {
	items []domain.LoaderEvent
}

func (e *hostEventsFake) Add(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) error {
	_, err := e.AddRecord(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSandboxID, linkedCellID, linkedAgentThreadID)
	return err
}

func (e *hostEventsFake) AddRecord(_ context.Context, loaderID, runID, triggerID, eventType, level, message string, _ any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) (domain.LoaderEvent, error) {
	event := domain.LoaderEvent{
		ID:                  fmt.Sprintf("event-%d", len(e.items)+1),
		LoaderID:            loaderID,
		RunID:               runID,
		TriggerID:           triggerID,
		Type:                eventType,
		Level:               level,
		Message:             message,
		LinkedSandboxID:     linkedSandboxID,
		LinkedCellID:        linkedCellID,
		LinkedAgentThreadID: linkedAgentThreadID,
		CreatedAt:           time.Now().UTC(),
	}
	e.items = append(e.items, event)
	return event, nil
}

func (e *hostEventsFake) contains(eventType string) bool {
	for _, item := range e.items {
		if item.Type == eventType {
			return true
		}
	}
	return false
}

func (e *hostEventsFake) types() []string {
	result := make([]string, 0, len(e.items))
	for _, item := range e.items {
		result = append(result, item.Type)
	}
	return result
}

type hostSessionsFake struct {
	session     *domain.Sandbox
	ensureErr   error
	loadErr     error
	shutdownErr error
	ensureCalls int
	loadCalls   int
	shutdowns   []string
}

func (s *hostSessionsFake) Ensure(context.Context, domain.Loader, domain.LoaderAgentRequest, bool) (*domain.Sandbox, string, error) {
	s.ensureCalls++
	if s.ensureErr != nil {
		return nil, "", s.ensureErr
	}
	return s.session, "loader.sandbox.created", nil
}

func (s *hostSessionsFake) Load(context.Context, string) (*domain.Sandbox, error) {
	s.loadCalls++
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return s.session, nil
}

func (s *hostSessionsFake) Shutdown(_ context.Context, sessionID string) error {
	s.shutdowns = append(s.shutdowns, sessionID)
	if s.shutdownErr != nil {
		return s.shutdownErr
	}
	return nil
}

type hostAgentDefinitionsFake struct{}

func (hostAgentDefinitionsFake) ResolveLoaderAgentDefinition(context.Context, domain.Loader) (*domain.AgentDefinition, error) {
	return nil, nil
}

type hostAgentExecutorFake struct {
	request loaders.HostAgentExecutionRequest
	cell    domain.NotebookCell
	err     error
}

func (e *hostAgentExecutorFake) ExecuteAgent(_ context.Context, _ *domain.Sandbox, request loaders.HostAgentExecutionRequest) (domain.NotebookCell, error) {
	e.request = request
	return e.cell, e.err
}

type hostCommandExecutorFake struct {
	calls  int
	result domain.LoaderCommandResult
	err    error
}

func (e *hostCommandExecutorFake) ExecuteLoaderCommand(context.Context, *domain.Sandbox, domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	e.calls++
	return e.result, e.err
}

type hostProjectAgentRunnerFake struct {
	request  loaders.HostProjectAgentRequest
	requests []loaders.HostProjectAgentRequest
	run      domain.ProjectRunRecord
	execErr  error
	err      error
}

func (r *hostProjectAgentRunnerFake) RunProjectAgent(_ context.Context, request loaders.HostProjectAgentRequest) (domain.ProjectRunRecord, error, error) {
	r.request = request
	r.requests = append(r.requests, request)
	return r.run, r.execErr, r.err
}

type hostLLMFake struct {
	prompt string
	result domain.LoaderLLMResult
	err    error
}

func (l *hostLLMFake) Generate(_ context.Context, prompt, _, _ string) (domain.LoaderLLMResult, error) {
	l.prompt = prompt
	return l.result, l.err
}

type hostRPCFake struct {
	response string
	source   string
	err      error
}

func (r *hostRPCFake) CallJSONWithSource(_ context.Context, _, _, source string) (string, error) {
	r.source = source
	return r.response, r.err
}

type hostPublisherFake struct {
	events []publishedEvent
}

type publishedEvent struct {
	topic   string
	payload map[string]any
}

func (p *hostPublisherFake) Publish(topic string, payload map[string]any) {
	p.events = append(p.events, publishedEvent{topic: topic, payload: payload})
}

func firstNonEmptyTest(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
