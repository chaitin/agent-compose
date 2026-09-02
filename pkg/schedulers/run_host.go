package schedulers

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
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
	Add(ctx context.Context, event SchedulerEventInput) error
	AddRecord(ctx context.Context, event SchedulerEventInput) (domain.SchedulerEvent, error)
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

// ExecuteAgent returns the cell (full transcript) plus the provider's final
// assistant message, when the provider emitted one distinct from the
// transcript; the message is "" when no such message exists. On a failed
// run the message may instead be a synthesized failure summary (not
// provider text) — callers must check cell.Success before preferring it
// over the transcript.
type HostAgentExecutor interface {
	ExecuteAgent(ctx context.Context, session *domain.Sandbox, request HostAgentExecutionRequest) (domain.NotebookCell, string, error)
}

type HostCommandExecutor interface {
	ExecuteSchedulerCommand(ctx context.Context, session *domain.Sandbox, request domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error)
}

type HostLLMRunner interface {
	Generate(ctx context.Context, request HostLLMGenerateRequest) (domain.SchedulerLLMResult, error)
}

type HostLLMGenerateRequest struct {
	Prompt       string
	Model        string
	OutputSchema string
	SchedulerID  string
	EnvItems     []domain.SandboxEnvVar
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
	return h.addSchedulerEvent(ctx, SchedulerEventInput{
		EventType: "scheduler.log",
		Level:     "info",
		Message:   message,
		Payload:   payload,
	})
}

func (h *RuntimeHost) PublishEvent(ctx context.Context, topic string, payloadJSON string) (domain.TopicEventRecord, error) {
	if h.deps.Store == nil {
		return domain.TopicEventRecord{}, fmt.Errorf("event store is unavailable")
	}
	publisherRunID := ""
	if h.execution.Kind == ExecutionKindTrigger {
		publisherRunID = h.execution.ID
	}
	published, err := NewPublishedTopicEvent(PublishTopicEventRequest{
		Topic: topic, PayloadJSON: payloadJSON, Trigger: h.triggerEvent, SchedulerID: h.scheduler.Summary.ID, RunID: publisherRunID,
	})
	if err != nil {
		return domain.TopicEventRecord{}, err
	}
	created, err := h.deps.Store.CreateEvent(ctx, published.Record)
	if err != nil {
		_ = h.addSchedulerEvent(ctx, SchedulerEventInput{
			EventType: "scheduler.event.publish.failed",
			Level:     "error",
			Message:   err.Error(),
			Payload:   map[string]any{"topic": published.Record.Topic},
		})
		return domain.TopicEventRecord{}, err
	}
	if sequenced, err := UpdatePublishedTopicEventSequence(published, created.Sequence); err == nil {
		_ = h.deps.Store.UpdateEventPayload(ctx, created.ID, sequenced.PayloadJSON)
		created.PayloadJSON = sequenced.PayloadJSON
		created.PayloadHash = sequenced.PayloadHash
	}
	_ = h.addSchedulerEvent(ctx, SchedulerEventInput{
		EventType: "scheduler.event.published",
		Level:     "info",
		Message:   "scheduler event published",
		Payload: map[string]any{
			"eventId":       created.ID,
			"sequence":      created.Sequence,
			"topic":         created.Topic,
			"correlationId": created.CorrelationID,
		},
	})
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
		event, _ := h.addSchedulerEventRecord(ctx, SchedulerEventInput{
			EventType:       "scheduler.sandbox.rpc.failed",
			Level:           "error",
			Message:         firstHostNonEmpty(err.Error(), fmt.Sprintf("%s failed", method)),
			Payload:         map[string]any{"method": method, "requestJson": requestJSON},
			LinkedSandboxID: linkedSandboxID,
		})
		h.addEventSandboxLink(ctx, event, linkedSandboxID, "sandbox_rpc_failed")
		return "", err
	}
	event, _ := h.addSchedulerEventRecord(ctx, SchedulerEventInput{
		EventType:       "scheduler.sandbox.rpc.completed",
		Level:           "info",
		Message:         fmt.Sprintf("%s completed", method),
		Payload:         map[string]any{"method": method, "requestJson": requestJSON, "responseJson": responseJSON},
		LinkedSandboxID: linkedSandboxID,
	})
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
		_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
			EventType:       eventType,
			Level:           "info",
			Message:         "scheduler sandbox ready",
			Payload:         map[string]any{"sandboxId": session.Summary.ID},
			LinkedSandboxID: session.Summary.ID,
		})
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

	cell, assistantMessage, execErr := h.deps.AgentExecutor.ExecuteAgent(ctx, session, HostAgentExecutionRequest{
		Provider:          agentConfig.Provider,
		AgentDefinitionID: agentDefinitionID,
		Model:             agentConfig.Model,
		RunID:             h.execution.ID,
		Prompt:            prompt,
		Timeout:           request.Timeout,
		OutputSchemaJSON:  request.OutputSchema,
	})
	if execErr != nil || !cell.Success {
		// assistantMessage may be a synthesized failure summary (see
		// agentAssistantMessage), not provider text; trust it only on
		// success, or a failed run's real transcript gets displaced by
		// that placeholder.
		assistantMessage = ""
	}
	finalText := firstHostNonEmpty(assistantMessage, cell.Output, cell.Stdout, cell.Stderr)
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
	_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
		EventType:           eventName,
		Level:               level,
		Message:             firstHostNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)),
		Payload:             result,
		LinkedSandboxID:     result.SandboxID,
		LinkedCellID:        result.CellID,
		LinkedAgentThreadID: result.AgentThreadID,
	})
	h.publishAgentCompleted(result, nil)
	h.shutdownSessionAndRecordEvent(ctx, session.Summary.ID, "scheduler sandbox after agent run", "scheduler sandbox stopped after agent run")
	if execErr != nil {
		return result, execErr
	}
	return result, nil
}

func (h *RuntimeHost) Command(ctx context.Context, request domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error) {
	cleanupSession := h.commandRequiresCleanup(request)
	agentRequest := domain.SchedulerAgentRequest{
		SandboxPolicy:    CommandSandboxPolicy(request),
		Title:            request.Title,
		Driver:           request.Driver,
		GuestImage:       request.GuestImage,
		PullPolicy:       request.PullPolicy,
		WorkspaceID:      request.WorkspaceID,
		JupyterEnabled:   request.JupyterEnabled,
		SandboxEnv:       CommandSandboxEnv(request),
		Volumes:          request.Volumes,
		BindingTriggerID: h.execution.TriggerID,
	}
	session, eventType, err := h.ensureCommandSession(ctx, agentRequest, cleanupSession)
	if err != nil {
		_ = h.addSchedulerEvent(ctx, SchedulerEventInput{
			EventType: "scheduler.command.failed",
			Level:     "error",
			Message:   err.Error(),
			Payload:   CommandEventPayload(request, domain.SchedulerCommandResult{}),
		})
		return domain.SchedulerCommandResult{}, err
	}
	if eventType != "" {
		_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
			EventType:       eventType,
			Level:           "info",
			Message:         "scheduler command sandbox ready",
			Payload:         map[string]any{"sandboxId": session.Summary.ID},
			LinkedSandboxID: session.Summary.ID,
		})
	}
	h.trackCommandSession(session.Summary.ID, cleanupSession)
	if err := h.persistCommandSandboxLink(ctx, request, session.Summary.ID); err != nil {
		return domain.SchedulerCommandResult{}, err
	}

	result, err := h.deps.CommandExecutor.ExecuteSchedulerCommand(ctx, session, request)
	if err != nil {
		_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
			EventType:       "scheduler.command.failed",
			Level:           "error",
			Message:         err.Error(),
			Payload:         CommandEventPayload(request, result),
			LinkedSandboxID: result.SandboxID,
			LinkedCellID:    result.CellID,
		})
		return result, err
	}
	level := "info"
	if !result.Success {
		level = "error"
	}
	// message is always empty for this event type: full output lives in the
	// sandbox cell artifact and is reconstructed on read by ResolveEventMessage
	// (see docs/design/scheduler_event_storage_design.md §4/§6).
	_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
		EventType:       "scheduler.command.completed",
		Level:           level,
		Payload:         CommandEventPayload(request, result),
		LinkedSandboxID: result.SandboxID,
		LinkedCellID:    result.CellID,
	})
	return result, nil
}

func (h *RuntimeHost) persistCommandSandboxLink(ctx context.Context, request domain.SchedulerCommandRequest, sandboxID string) error {
	if h.execution.Kind != ExecutionKindTrigger || h.deps.Events == nil {
		return nil
	}
	if err := h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
		EventType:       "scheduler.command.started",
		Level:           "info",
		Message:         "scheduler command started",
		Payload:         map[string]any{"sandboxId": sandboxID},
		LinkedSandboxID: sandboxID,
	}); err != nil {
		persistErr := fmt.Errorf("persist scheduler command sandbox link: %w", err)
		failedEventErr := h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
			EventType:       "scheduler.command.failed",
			Level:           "error",
			Message:         persistErr.Error(),
			Payload:         CommandEventPayload(request, domain.SchedulerCommandResult{SandboxID: sandboxID}),
			LinkedSandboxID: sandboxID,
		})
		if failedEventErr != nil {
			slog.Error("failed to persist scheduler command failure event", "scheduler_id", h.scheduler.Summary.ID, "run_id", h.execution.ID, "sandbox_id", sandboxID, "error", failedEventErr)
		}
		slog.Error("scheduler command sandbox link persistence failed", "scheduler_id", h.scheduler.Summary.ID, "run_id", h.execution.ID, "sandbox_id", sandboxID, "error", persistErr)
		return persistErr
	}
	return nil
}

func (h *RuntimeHost) LLM(ctx context.Context, prompt string, request domain.SchedulerLLMRequest) (domain.SchedulerLLMResult, error) {
	if h.deps.LLM == nil {
		return domain.SchedulerLLMResult{}, fmt.Errorf("llm client is unavailable")
	}
	model := firstHostNonEmpty(
		strings.TrimSpace(request.Model),
		schedulerEnvValue(h.scheduler.EnvItems, "LLM_MODEL"),
		strings.TrimSpace(h.scheduler.Model),
		strings.TrimSpace(h.scheduler.AgentModel),
	)
	result, err := h.deps.LLM.Generate(ctx, HostLLMGenerateRequest{
		Prompt: prompt, Model: model, OutputSchema: request.OutputSchema,
		SchedulerID: h.scheduler.Summary.ID, EnvItems: h.scheduler.EnvItems,
	})
	if err != nil {
		_ = h.addSchedulerEvent(ctx, SchedulerEventInput{
			EventType: "scheduler.llm.failed",
			Level:     "error",
			Message:   err.Error(),
			Payload:   map[string]any{"model": strings.TrimSpace(request.Model)},
		})
		return domain.SchedulerLLMResult{}, err
	}
	_ = h.addSchedulerEvent(ctx, SchedulerEventInput{
		EventType: "scheduler.llm.completed",
		Level:     "info",
		Message:   firstHostNonEmpty(result.Text, "llm completed"),
		Payload:   result,
	})
	return result, nil
}

func schedulerEnvValue(items []domain.SandboxEnvVar, name string) string {
	for _, item := range items {
		if strings.EqualFold(strings.TrimSpace(item.Name), name) {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}

func (h *RuntimeHost) CleanupCommandSessions(ctx context.Context) {
	sessionIDs := append([]string(nil), h.commandSessionIDOrder...)
	h.commandSessionIDs = nil
	h.commandSessionIDOrder = nil
	for _, sessionID := range sessionIDs {
		h.shutdownSessionAndRecordEvent(ctx, sessionID, "scheduler command sandbox after run", "scheduler command sandbox stopped after run")
	}
}

// shutdownSessionAndRecordEvent stops the sandbox and records the outcome as
// a linked scheduler event. warnContext names the caller's scenario for the
// failure log line; stoppedMessage is the event message recorded on success.
func (h *RuntimeHost) shutdownSessionAndRecordEvent(ctx context.Context, sessionID, warnContext, stoppedMessage string) {
	if err := h.deps.Sessions.Shutdown(ctx, sessionID); err != nil {
		slog.Warn("failed to stop "+warnContext, "scheduler_id", h.scheduler.Summary.ID, "sandbox_id", sessionID, "error", err)
		_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
			EventType:       "scheduler.sandbox.stop_failed",
			Level:           "error",
			Message:         err.Error(),
			Payload:         map[string]any{"sandboxId": sessionID},
			LinkedSandboxID: sessionID,
		})
		return
	}
	_ = h.addLinkedSchedulerEvent(ctx, SchedulerEventInput{
		EventType:       "scheduler.sandbox.stopped",
		Level:           "info",
		Message:         stoppedMessage,
		Payload:         map[string]any{"sandboxId": sessionID},
		LinkedSandboxID: sessionID,
	})
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

// SchedulerEventInput describes one scheduler event to record, for both the
// RuntimeHost event-recording helpers (which bind SchedulerID/RunID/TriggerID
// from the host's own execution before calling addSchedulerEvent) and the
// HostEventRecorder interface (which needs the full record explicitly). It
// mirrors domain.SchedulerEvent's fields but stays a distinct, input-only
// type: SchedulerEvent's ID/CreatedAt are generated by the recorder, and its
// PayloadJSON is the already-serialized form of this type's raw Payload, so
// reusing SchedulerEvent directly as input would let a caller-set ID,
// CreatedAt, or PayloadJSON be silently ignored.
type SchedulerEventInput struct {
	SchedulerID         string
	RunID               string
	TriggerID           string
	EventType           string
	Level               string
	Message             string
	Payload             any
	LinkedSandboxID     string
	LinkedCellID        string
	LinkedAgentThreadID string
}

func (h *RuntimeHost) addSchedulerEvent(ctx context.Context, event SchedulerEventInput) error {
	if h.deps.Events == nil {
		return nil
	}
	event.SchedulerID, event.RunID, event.TriggerID = h.scheduler.Summary.ID, h.execution.ID, h.execution.TriggerID
	return h.deps.Events.Add(ctx, event)
}

func (h *RuntimeHost) addSchedulerEventRecord(ctx context.Context, event SchedulerEventInput) (domain.SchedulerEvent, error) {
	if h.deps.Events == nil {
		return domain.SchedulerEvent{}, nil
	}
	event.SchedulerID, event.RunID, event.TriggerID = h.scheduler.Summary.ID, h.execution.ID, h.execution.TriggerID
	return h.deps.Events.AddRecord(ctx, event)
}

func (h *RuntimeHost) addLinkedSchedulerEvent(ctx context.Context, event SchedulerEventInput) error {
	recorded, err := h.addSchedulerEventRecord(ctx, event)
	if err != nil {
		return err
	}
	h.addEventSandboxLink(ctx, recorded, event.LinkedSandboxID, recorded.Type)
	return nil
}

func (h *RuntimeHost) addEventSandboxLink(ctx context.Context, event domain.SchedulerEvent, sandboxID, relation string) {
	if h.deps.Store == nil || strings.TrimSpace(sandboxID) == "" || strings.TrimSpace(event.ID) == "" || strings.TrimSpace(relation) == "" || h.triggerEvent.EventID == "" {
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
		len(domain.NormalizeEnvItems(AgentSandboxEnv(request))) > 0 ||
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
