package schedulers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

type HostStore interface {
	CreateEvent(ctx context.Context, event domain.TopicEventRecord) (domain.TopicEventRecord, error)
	UpdateEventPayload(ctx context.Context, eventID, payloadJSON string) error
	GetSchedulerState(ctx context.Context, schedulerID, key string) (string, bool, error)
	SetSchedulerState(ctx context.Context, schedulerID, key, valueJSON string) error
	DeleteSchedulerState(ctx context.Context, schedulerID, key string) error
	AddEventSandboxLink(ctx context.Context, link domain.EventSandboxLink) error
}

type HostEventRecorder interface {
	Add(ctx context.Context, schedulerID, runID, triggerID, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) error
	AddRecord(ctx context.Context, schedulerID, runID, triggerID, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) (domain.SchedulerEvent, error)
}

type HostSandboxRunner interface {
	Ensure(ctx context.Context, scheduler domain.Scheduler, request domain.SchedulerAgentRequest, titleOverridesSession bool) (*domain.Sandbox, string, error)
	Load(ctx context.Context, sessionID string) (*domain.Sandbox, error)
	Shutdown(ctx context.Context, sessionID string) error
}

type HostAgentDefinitionResolver interface {
	ResolveSchedulerAgentDefinition(ctx context.Context, scheduler domain.Scheduler) (*domain.AgentDefinition, error)
}

type HostAgentExecutionRequest struct {
	Provider          string
	AgentDefinitionID string
	Model             string
	RunID             string
	Prompt            string
	Timeout           time.Duration
	OutputSchemaJSON  string
}

type HostAgentExecutor interface {
	ExecuteAgent(ctx context.Context, session *domain.Sandbox, request HostAgentExecutionRequest) (domain.NotebookCell, error)
}

type HostCommandExecutor interface {
	ExecuteSchedulerCommand(ctx context.Context, session *domain.Sandbox, request domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error)
}

type HostLLMRunner interface {
	Generate(ctx context.Context, prompt, model, outputSchema string) (domain.SchedulerLLMResult, error)
}

type HostSandboxRPC interface {
	CallJSONWithSource(ctx context.Context, method, requestJSON, source string) (string, error)
}

type SandboxCreationContext struct {
	AgentDefinitionID string
	Provider          string
}

type sandboxCreationContextKey struct{}

func WithSandboxCreationContext(ctx context.Context, value SandboxCreationContext) context.Context {
	return context.WithValue(ctx, sandboxCreationContextKey{}, value)
}

func SandboxCreationContextFromContext(ctx context.Context) SandboxCreationContext {
	value, _ := ctx.Value(sandboxCreationContextKey{}).(SandboxCreationContext)
	return value
}

type HostPublisher interface {
	Publish(topic string, payload map[string]any)
}

type RunHostDependencies struct {
	Store                   HostStore
	Events                  HostEventRecorder
	Sessions                HostSandboxRunner
	AgentDefinitions        HostAgentDefinitionResolver
	AgentExecutor           HostAgentExecutor
	CommandExecutor         HostCommandExecutor
	ProjectAgentRunner      HostProjectAgentRunner
	LLM                     HostLLMRunner
	SandboxRPC              HostSandboxRPC
	Publisher               HostPublisher
	CommandRequiresCleanup  func(scheduler domain.Scheduler, request domain.SchedulerCommandRequest) bool
	LinkedSandboxIDFromJSON func(method, requestJSON, responseJSON string) string
}

type RuntimeHost struct {
	deps         RunHostDependencies
	scheduler    domain.Scheduler
	execution    RuntimeExecutionContext
	triggerEvent TriggerEventMetadata

	commandSessionIDs       map[string]struct{}
	commandSessionIDOrder   []string
	commandReusableSession  *domain.Sandbox
	projectAgentRunSequence atomic.Uint64
}

func NewRuntimeHost(deps RunHostDependencies, scheduler domain.Scheduler, execution RuntimeExecutionContext, triggerEvent TriggerEventMetadata) *RuntimeHost {
	return &RuntimeHost{deps: deps, scheduler: scheduler, execution: execution, triggerEvent: triggerEvent}
}

// Runtime host events are persisted and their payloads are consumed by clients.
func (h *RuntimeHost) Log(ctx context.Context, message string, payload any) error {
	return h.addSchedulerEvent(ctx, "scheduler.log", "info", message, payload, "", "", "")
}

func (h *RuntimeHost) PublishEvent(ctx context.Context, topic string, payloadJSON string) (domain.TopicEventRecord, error) {
	if h.deps.Store == nil {
		return domain.TopicEventRecord{}, fmt.Errorf("event store is unavailable")
	}
	publisherRunID := ""
	if h.execution.Kind == ExecutionKindTrigger {
		publisherRunID = h.execution.ID
	}
	published, err := NewPublishedTopicEvent(topic, payloadJSON, h.triggerEvent, h.scheduler.Summary.ID, publisherRunID)
	if err != nil {
		return domain.TopicEventRecord{}, err
	}
	created, err := h.deps.Store.CreateEvent(ctx, published.Record)
	if err != nil {
		_ = h.addSchedulerEvent(ctx, "scheduler.event.publish.failed", "error", err.Error(), map[string]any{"topic": published.Record.Topic}, "", "", "")
		return domain.TopicEventRecord{}, err
	}
	if sequenced, err := UpdatePublishedTopicEventSequence(published, created.Sequence); err == nil {
		_ = h.deps.Store.UpdateEventPayload(ctx, created.ID, sequenced.PayloadJSON)
		created.PayloadJSON = sequenced.PayloadJSON
		created.PayloadHash = sequenced.PayloadHash
	}
	_ = h.addSchedulerEvent(ctx, "scheduler.event.published", "info", "scheduler event published", map[string]any{
		"eventId":       created.ID,
		"sequence":      created.Sequence,
		"topic":         created.Topic,
		"correlationId": created.CorrelationID,
	}, "", "", "")
	return created, nil
}

func (h *RuntimeHost) StateGet(ctx context.Context, key string) (string, bool, error) {
	return h.deps.Store.GetSchedulerState(ctx, h.scheduler.Summary.ID, key)
}

func (h *RuntimeHost) StateSet(ctx context.Context, key, valueJSON string) error {
	return h.deps.Store.SetSchedulerState(ctx, h.scheduler.Summary.ID, key, valueJSON)
}

func (h *RuntimeHost) StateDelete(ctx context.Context, key string) error {
	return h.deps.Store.DeleteSchedulerState(ctx, h.scheduler.Summary.ID, key)
}

func (h *RuntimeHost) CallSandboxRPC(ctx context.Context, method, requestJSON string) (string, error) {
	if h.deps.SandboxRPC == nil {
		return "", fmt.Errorf("sandbox rpc bridge is unavailable")
	}
	method = strings.TrimSpace(method)
	requestJSON = strings.TrimSpace(requestJSON)
	ctx = WithSandboxCreationContext(ctx, SandboxCreationContext{
		AgentDefinitionID: strings.TrimSpace(h.scheduler.Summary.AgentID),
		Provider:          domain.NormalizeAgentKind(h.scheduler.Summary.DefaultAgent),
	})
	responseJSON, err := h.deps.SandboxRPC.CallJSONWithSource(ctx, method, requestJSON, domain.SandboxTypeScript+":"+h.scheduler.Summary.ID)
	linkedSandboxID := h.linkedSandboxID(method, requestJSON, responseJSON)
	if err != nil {
		event, _ := h.addSchedulerEventRecord(ctx, "scheduler.sandbox.rpc.failed", "error", firstHostNonEmpty(err.Error(), fmt.Sprintf("%s failed", method)), map[string]any{"method": method, "requestJson": requestJSON}, linkedSandboxID, "", "")
		h.addEventSandboxLink(ctx, event, linkedSandboxID, "sandbox_rpc_failed")
		return "", err
	}
	event, _ := h.addSchedulerEventRecord(ctx, "scheduler.sandbox.rpc.completed", "info", fmt.Sprintf("%s completed", method), map[string]any{"method": method, "requestJson": requestJSON, "responseJson": responseJSON}, linkedSandboxID, "", "")
	h.addEventSandboxLink(ctx, event, linkedSandboxID, "sandbox_rpc_completed")
	return responseJSON, nil
}

func (h *RuntimeHost) Agent(ctx context.Context, prompt string, request domain.SchedulerAgentRequest) (domain.SchedulerAgentResult, error) {
	request.BindingTriggerID = h.execution.TriggerID
	if h.useProjectAgentRun(request) {
		return h.ProjectAgent(ctx, prompt, request)
	}
	session, eventType, err := h.deps.Sessions.Ensure(ctx, h.scheduler, request, true)
	if err != nil {
		return domain.SchedulerAgentResult{}, err
	}
	if eventType != "" {
		_ = h.addLinkedSchedulerEvent(ctx, eventType, "info", "scheduler sandbox ready", map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	}

	agentConfig := execution.AgentConfig{Provider: domain.NormalizeAgentKind(request.Agent)}
	var agentDefinitionID string
	if agentConfig.Provider == "" {
		agentDefinition, err := h.deps.AgentDefinitions.ResolveSchedulerAgentDefinition(ctx, h.scheduler)
		if err != nil {
			return domain.SchedulerAgentResult{}, err
		}
		if agentDefinition != nil {
			agentConfig = execution.AgentConfigFromDefinition(*agentDefinition, "")
			agentDefinitionID = strings.TrimSpace(agentDefinition.ID)
		}
	}
	if agentDefinitionID == "" {
		agentDefinitionID = strings.TrimSpace(h.scheduler.Summary.AgentID)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = domain.NormalizeAgentKind(h.scheduler.Summary.DefaultAgent)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = "codex"
	}

	cell, execErr := h.deps.AgentExecutor.ExecuteAgent(ctx, session, HostAgentExecutionRequest{
		Provider:          agentConfig.Provider,
		AgentDefinitionID: agentDefinitionID,
		Model:             agentConfig.Model,
		RunID:             h.execution.ID,
		Prompt:            prompt,
		Timeout:           request.Timeout,
		OutputSchemaJSON:  request.OutputSchema,
	})
	finalText := firstHostNonEmpty(cell.Output, cell.Stdout, cell.Stderr)
	jsonValue, jsonErr := JSONResult(finalText, request.OutputSchema, "agent finalText")
	if jsonErr != nil && execErr == nil {
		execErr = jsonErr
	}
	result := domain.SchedulerAgentResult{
		Text:          finalText,
		Output:        cell.Output,
		FinalText:     finalText,
		JSON:          jsonValue,
		SandboxID:     session.Summary.ID,
		CellID:        cell.ID,
		Agent:         firstHostNonEmpty(cell.Agent, agentConfig.Provider),
		AgentThreadID: cell.AgentThreadID,
		StopReason:    cell.StopReason,
		Success:       cell.Success,
		ExitCode:      cell.ExitCode,
	}
	level := "info"
	eventName := "scheduler.agent.completed"
	if execErr != nil {
		level = "error"
		eventName = "scheduler.agent.failed"
		result.Text = firstHostNonEmpty(result.Text, execErr.Error())
	}
	_ = h.addLinkedSchedulerEvent(ctx, eventName, level, firstHostNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)), result, result.SandboxID, result.CellID, result.AgentThreadID)
	h.publishAgentCompleted(result, nil)
	if shutdownErr := h.deps.Sessions.Shutdown(ctx, session.Summary.ID); shutdownErr != nil {
		slog.Warn("failed to stop scheduler sandbox after agent run", "scheduler_id", h.scheduler.Summary.ID, "sandbox_id", session.Summary.ID, "error", shutdownErr)
		_ = h.addLinkedSchedulerEvent(ctx, "scheduler.sandbox.stop_failed", "error", shutdownErr.Error(), map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	} else {
		_ = h.addLinkedSchedulerEvent(ctx, "scheduler.sandbox.stopped", "info", "scheduler sandbox stopped after agent run", map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	}
	if execErr != nil {
		return result, execErr
	}
	return result, nil
}

func (h *RuntimeHost) Command(ctx context.Context, request domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error) {
	cleanupSession := h.commandRequiresCleanup(request)
	agentRequest := domain.SchedulerAgentRequest{
		SandboxPolicy:    domain.SchedulerCommandSandboxPolicy(request),
		Title:            request.Title,
		Driver:           request.Driver,
		GuestImage:       request.GuestImage,
		PullPolicy:       request.PullPolicy,
		WorkspaceID:      request.WorkspaceID,
		JupyterEnabled:   request.JupyterEnabled,
		SandboxEnv:       domain.SchedulerCommandSandboxEnv(request),
		Volumes:          request.Volumes,
		BindingTriggerID: h.execution.TriggerID,
	}
	session, eventType, err := h.ensureCommandSession(ctx, agentRequest, cleanupSession)
	if err != nil {
		_ = h.addSchedulerEvent(ctx, "scheduler.command.failed", "error", err.Error(), CommandEventPayload(request, domain.SchedulerCommandResult{}), "", "", "")
		return domain.SchedulerCommandResult{}, err
	}
	if eventType != "" {
		_ = h.addLinkedSchedulerEvent(ctx, eventType, "info", "scheduler command sandbox ready", map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	}
	h.trackCommandSession(session.Summary.ID, cleanupSession)
	if err := h.persistCommandSandboxLink(ctx, session.Summary.ID); err != nil {
		return domain.SchedulerCommandResult{}, err
	}

	result, err := h.deps.CommandExecutor.ExecuteSchedulerCommand(ctx, session, request)
	if err != nil {
		_ = h.addLinkedSchedulerEvent(ctx, "scheduler.command.failed", "error", err.Error(), CommandEventPayload(request, result), result.SandboxID, result.CellID, "")
		return result, err
	}
	level := "info"
	if !result.Success {
		level = "error"
	}
	_ = h.addLinkedSchedulerEvent(ctx, "scheduler.command.completed", level, firstHostNonEmpty(result.Output, result.Stdout, result.Stderr, "scheduler command completed"), CommandEventPayload(request, result), result.SandboxID, result.CellID, "")
	return result, nil
}

func (h *RuntimeHost) persistCommandSandboxLink(ctx context.Context, sandboxID string) error {
	if h.execution.Kind != ExecutionKindTrigger {
		return nil
	}
	if h.deps.Events == nil {
		return fmt.Errorf("persist scheduler command sandbox link: scheduler event recorder is unavailable")
	}
	if err := h.addLinkedSchedulerEvent(ctx, "scheduler.command.started", "info", "scheduler command started", map[string]any{"sandboxId": sandboxID}, sandboxID, "", ""); err != nil {
		return fmt.Errorf("persist scheduler command sandbox link: %w", err)
	}
	return nil
}

func (h *RuntimeHost) LLM(ctx context.Context, prompt string, request domain.SchedulerLLMRequest) (domain.SchedulerLLMResult, error) {
	if h.deps.LLM == nil {
		return domain.SchedulerLLMResult{}, fmt.Errorf("llm client is unavailable")
	}
	result, err := h.deps.LLM.Generate(ctx, prompt, request.Model, request.OutputSchema)
	if err != nil {
		_ = h.addSchedulerEvent(ctx, "scheduler.llm.failed", "error", err.Error(), map[string]any{"model": strings.TrimSpace(request.Model)}, "", "", "")
		return domain.SchedulerLLMResult{}, err
	}
	_ = h.addSchedulerEvent(ctx, "scheduler.llm.completed", "info", firstHostNonEmpty(result.Text, "llm completed"), result, "", "", "")
	return result, nil
}

func (h *RuntimeHost) CleanupCommandSessions(ctx context.Context) {
	sessionIDs := append([]string(nil), h.commandSessionIDOrder...)
	h.commandSessionIDs = nil
	h.commandSessionIDOrder = nil
	for _, sessionID := range sessionIDs {
		if err := h.deps.Sessions.Shutdown(ctx, sessionID); err != nil {
			slog.Warn("failed to stop scheduler command sandbox after run", "scheduler_id", h.scheduler.Summary.ID, "sandbox_id", sessionID, "error", err)
			_ = h.addLinkedSchedulerEvent(ctx, "scheduler.sandbox.stop_failed", "error", err.Error(), map[string]any{"sandboxId": sessionID}, sessionID, "", "")
			continue
		}
		_ = h.addLinkedSchedulerEvent(ctx, "scheduler.sandbox.stopped", "info", "scheduler command sandbox stopped after run", map[string]any{"sandboxId": sessionID}, sessionID, "", "")
	}
}

func (h *RuntimeHost) ensureCommandSession(ctx context.Context, request domain.SchedulerAgentRequest, cleanupSession bool) (*domain.Sandbox, string, error) {
	if cleanupSession && h.commandReusableSession != nil {
		if loaded, err := h.deps.Sessions.Load(ctx, h.commandReusableSession.Summary.ID); err == nil && loaded.Summary.VMStatus == domain.VMStatusRunning {
			return loaded, "", nil
		}
	}
	session, eventType, err := h.deps.Sessions.Ensure(ctx, h.scheduler, request, false)
	if err != nil {
		return nil, "", err
	}
	if cleanupSession {
		h.commandReusableSession = session
	}
	return session, eventType, nil
}

func (h *RuntimeHost) trackCommandSession(sessionID string, cleanup bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || !cleanup {
		return
	}
	if h.commandSessionIDs == nil {
		h.commandSessionIDs = map[string]struct{}{}
	}
	if _, ok := h.commandSessionIDs[sessionID]; ok {
		return
	}
	h.commandSessionIDs[sessionID] = struct{}{}
	h.commandSessionIDOrder = append(h.commandSessionIDOrder, sessionID)
}

func (h *RuntimeHost) addSchedulerEvent(ctx context.Context, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) error {
	if h.deps.Events == nil {
		return nil
	}
	return h.deps.Events.Add(ctx, h.scheduler.Summary.ID, h.execution.ID, h.execution.TriggerID, eventType, level, message, payload, linkedSandboxID, linkedCellID, linkedAgentThreadID)
}

func (h *RuntimeHost) addSchedulerEventRecord(ctx context.Context, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) (domain.SchedulerEvent, error) {
	if h.deps.Events == nil {
		return domain.SchedulerEvent{}, nil
	}
	return h.deps.Events.AddRecord(ctx, h.scheduler.Summary.ID, h.execution.ID, h.execution.TriggerID, eventType, level, message, payload, linkedSandboxID, linkedCellID, linkedAgentThreadID)
}

func (h *RuntimeHost) addLinkedSchedulerEvent(ctx context.Context, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) error {
	event, err := h.addSchedulerEventRecord(ctx, eventType, level, message, payload, linkedSandboxID, linkedCellID, linkedAgentThreadID)
	if err != nil {
		return err
	}
	h.addEventSandboxLink(ctx, event, linkedSandboxID, event.Type)
	return nil
}

func (h *RuntimeHost) addEventSandboxLink(ctx context.Context, event domain.SchedulerEvent, sandboxID, relation string) {
	if h.deps.Store == nil || strings.TrimSpace(sandboxID) == "" || h.triggerEvent.EventID == "" {
		return
	}
	if err := h.deps.Store.AddEventSandboxLink(ctx, domain.EventSandboxLink{
		EventID:          h.triggerEvent.EventID,
		SandboxID:        sandboxID,
		Relation:         relation,
		SchedulerID:      h.scheduler.Summary.ID,
		RunID:            h.execution.ID,
		TriggerID:        h.execution.TriggerID,
		SchedulerEventID: event.ID,
		CreatedAt:        event.CreatedAt,
	}); err != nil {
		slog.Warn("failed to add event sandbox link", "event_id", h.triggerEvent.EventID, "sandbox_id", sandboxID, "run_id", h.execution.ID, "error", err)
	}
}

func (h *RuntimeHost) publishAgentCompleted(result domain.SchedulerAgentResult, projectRun *domain.ProjectRunRecord) {
	if h.deps.Publisher == nil {
		return
	}
	payload := map[string]any{
		"sandboxId":     result.SandboxID,
		"cellId":        result.CellID,
		"agent":         result.Agent,
		"agentThreadId": result.AgentThreadID,
		"success":       result.Success,
		"stopReason":    result.StopReason,
		"source":        "scheduler",
		"schedulerId":   h.scheduler.Summary.ID,
	}
	if projectRun != nil {
		if h.execution.Kind == ExecutionKindTrigger {
			payload["schedulerRunId"] = h.execution.ID
		}
		payload["projectId"] = projectRun.ProjectID
		payload["projectRunId"] = projectRun.RunID
	}
	h.deps.Publisher.Publish("agent-compose.agent.completed", payload)
}

func (h *RuntimeHost) commandRequiresCleanup(request domain.SchedulerCommandRequest) bool {
	if h.deps.CommandRequiresCleanup == nil {
		return CommandRequestRequiresCleanup(h.scheduler, request)
	}
	return h.deps.CommandRequiresCleanup(h.scheduler, request)
}

func (h *RuntimeHost) linkedSandboxID(method, requestJSON, responseJSON string) string {
	if h.deps.LinkedSandboxIDFromJSON == nil {
		return ""
	}
	return h.deps.LinkedSandboxIDFromJSON(method, requestJSON, responseJSON)
}

func AgentRequestOverridesSession(request domain.SchedulerAgentRequest, includeTitle bool) bool {
	return (includeTitle && strings.TrimSpace(request.Title) != "") ||
		strings.TrimSpace(request.Driver) != "" ||
		strings.TrimSpace(request.GuestImage) != "" ||
		strings.TrimSpace(request.WorkspaceID) != "" ||
		len(domain.NormalizeEnvItems(domain.SchedulerAgentSandboxEnv(request))) > 0 ||
		len(request.Volumes) > 0
}

func firstHostNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func execErrString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
