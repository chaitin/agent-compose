package loaders_test

import (
	"context"
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
		session: &domain.Session{Summary: domain.SessionSummary{ID: "session-host", VMStatus: domain.VMStatusRunning}},
	}
	agentExecutor := &hostAgentExecutorFake{cell: domain.NotebookCell{
		ID:             "cell-agent",
		Output:         "agent text",
		Agent:          "gemini",
		AgentSessionID: "agent-session",
		StopReason:     "complete",
		Success:        true,
	}}
	commandExecutor := &hostCommandExecutorFake{result: domain.LoaderCommandResult{
		Output:    "command output",
		Success:   true,
		SessionID: "session-host",
		CellID:    "cell-command",
	}}
	llm := &hostLLMFake{result: domain.LoaderLLMResult{Text: "llm text", Model: "model-a", ResponseID: "resp-1", FinishReason: "stop"}}
	rpc := &hostRPCFake{response: `{"sessionId":"session-rpc"}`}
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
		LinkedSessionIDFromJSON: func(_, _, responseJSON string) string {
			if strings.Contains(responseJSON, "session-rpc") {
				return "session-rpc"
			}
			return ""
		},
	}, loader, run, loaders.TriggerEventMetadata{EventID: "topic-event"})

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
	if !events.contains("loader.session.created") || !events.contains("loader.agent.completed") || !events.contains("loader.session.stopped") {
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

	responseJSON, err := host.CallSessionRPC(ctx, "GetSession", `{"sessionId":"session-rpc"}`)
	if err != nil {
		t.Fatalf("CallSessionRPC returned error: %v", err)
	}
	if responseJSON != rpc.response || rpc.source != domain.SessionTypeScript+":"+loader.Summary.ID {
		t.Fatalf("rpc response/source = %q/%q", responseJSON, rpc.source)
	}
	if !store.containsLink("session-rpc", "session_rpc_completed") {
		t.Fatalf("session links = %#v", store.links)
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
		SessionID:  "session-project",
		Output:     "project output",
		ResultJSON: `{"ok":true}`,
	}}
	publisher := &hostPublisherFake{}
	host := loaders.NewRuntimeHost(loaders.RunHostDependencies{
		Store:              &hostStoreFake{},
		Events:             events,
		ProjectAgentRunner: projectRunner,
		Publisher:          publisher,
	}, loader, run, loaders.TriggerEventMetadata{})

	result, err := host.Agent(ctx, "review", domain.LoaderAgentRequest{})
	if err != nil {
		t.Fatalf("Project Agent returned error: %v", err)
	}
	if result.Text != "project output" || projectRunner.request.ProjectID != "project-1" || projectRunner.request.ClientRequestID != run.ID {
		t.Fatalf("project result/request = %#v/%#v", result, projectRunner.request)
	}
	if !events.contains("loader.agent.completed") || len(publisher.events) != 1 || publisher.events[0].payload["projectRunId"] != "project-run" {
		t.Fatalf("events/publisher = %#v/%#v", events.types(), publisher.events)
	}
}

type hostStoreFake struct {
	events []domain.TopicEventRecord
	state  map[string]string
	links  []domain.EventSessionLink
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

func (s *hostStoreFake) AddEventSessionLink(_ context.Context, link domain.EventSessionLink) error {
	s.links = append(s.links, link)
	return nil
}

func (s *hostStoreFake) containsLink(sessionID, relation string) bool {
	for _, link := range s.links {
		if link.SessionID == sessionID && link.Relation == relation {
			return true
		}
	}
	return false
}

type hostEventsFake struct {
	items []domain.LoaderEvent
}

func (e *hostEventsFake) Add(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSessionID, linkedCellID, linkedAgentSessionID string) error {
	_, err := e.AddRecord(ctx, loaderID, runID, triggerID, eventType, level, message, payload, linkedSessionID, linkedCellID, linkedAgentSessionID)
	return err
}

func (e *hostEventsFake) AddRecord(_ context.Context, loaderID, runID, triggerID, eventType, level, message string, _ any, linkedSessionID, linkedCellID, linkedAgentSessionID string) (domain.LoaderEvent, error) {
	event := domain.LoaderEvent{
		ID:                   fmt.Sprintf("event-%d", len(e.items)+1),
		LoaderID:             loaderID,
		RunID:                runID,
		TriggerID:            triggerID,
		Type:                 eventType,
		Level:                level,
		Message:              message,
		LinkedSessionID:      linkedSessionID,
		LinkedCellID:         linkedCellID,
		LinkedAgentSessionID: linkedAgentSessionID,
		CreatedAt:            time.Now().UTC(),
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
	session     *domain.Session
	ensureCalls int
	loadCalls   int
	shutdowns   []string
}

func (s *hostSessionsFake) Ensure(context.Context, domain.Loader, domain.LoaderAgentRequest, bool) (*domain.Session, string, error) {
	s.ensureCalls++
	return s.session, "loader.session.created", nil
}

func (s *hostSessionsFake) Load(context.Context, string) (*domain.Session, error) {
	s.loadCalls++
	return s.session, nil
}

func (s *hostSessionsFake) Shutdown(_ context.Context, sessionID string) error {
	s.shutdowns = append(s.shutdowns, sessionID)
	return nil
}

type hostAgentDefinitionsFake struct{}

func (hostAgentDefinitionsFake) ResolveLoaderAgentDefinition(context.Context, domain.Loader) (*domain.AgentDefinition, error) {
	return nil, nil
}

type hostAgentExecutorFake struct {
	request loaders.HostAgentExecutionRequest
	cell    domain.NotebookCell
}

func (e *hostAgentExecutorFake) ExecuteAgent(_ context.Context, _ *domain.Session, request loaders.HostAgentExecutionRequest) (domain.NotebookCell, error) {
	e.request = request
	return e.cell, nil
}

type hostCommandExecutorFake struct {
	calls  int
	result domain.LoaderCommandResult
}

func (e *hostCommandExecutorFake) ExecuteLoaderCommand(context.Context, *domain.Session, domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	e.calls++
	return e.result, nil
}

type hostProjectAgentRunnerFake struct {
	request loaders.HostProjectAgentRequest
	run     domain.ProjectRunRecord
}

func (r *hostProjectAgentRunnerFake) RunProjectAgent(_ context.Context, request loaders.HostProjectAgentRequest) (domain.ProjectRunRecord, error, error) {
	r.request = request
	return r.run, nil, nil
}

type hostLLMFake struct {
	prompt string
	result domain.LoaderLLMResult
}

func (l *hostLLMFake) Generate(_ context.Context, prompt, _, _ string) (domain.LoaderLLMResult, error) {
	l.prompt = prompt
	return l.result, nil
}

type hostRPCFake struct {
	response string
	source   string
}

func (r *hostRPCFake) CallJSONWithSource(_ context.Context, _, _, source string) (string, error) {
	r.source = source
	return r.response, nil
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
