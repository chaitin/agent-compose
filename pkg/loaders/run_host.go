package loaders

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
	GetLoaderState(ctx context.Context, loaderID, key string) (string, bool, error)
	SetLoaderState(ctx context.Context, loaderID, key, valueJSON string) error
	DeleteLoaderState(ctx context.Context, loaderID, key string) error
	AddEventSandboxLink(ctx context.Context, link domain.EventSandboxLink) error
}

type HostEventRecorder interface {
	Add(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) error
	AddRecord(ctx context.Context, loaderID, runID, triggerID, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) (domain.LoaderEvent, error)
}

type HostSessionRunner interface {
	Ensure(ctx context.Context, loader domain.Loader, request domain.LoaderAgentRequest, titleOverridesSession bool) (*domain.Sandbox, string, error)
	Load(ctx context.Context, sessionID string) (*domain.Sandbox, error)
	Shutdown(ctx context.Context, sessionID string) error
}

type HostAgentDefinitionResolver interface {
	ResolveLoaderAgentDefinition(ctx context.Context, loader domain.Loader) (*domain.AgentDefinition, error)
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
	ExecuteLoaderCommand(ctx context.Context, session *domain.Sandbox, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error)
}

type HostLLMRunner interface {
	Generate(ctx context.Context, prompt, model, outputSchema string) (domain.LoaderLLMResult, error)
}

type HostSessionRPC interface {
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
	Sessions                HostSessionRunner
	AgentDefinitions        HostAgentDefinitionResolver
	AgentExecutor           HostAgentExecutor
	CommandExecutor         HostCommandExecutor
	ProjectAgentRunner      HostProjectAgentRunner
	LLM                     HostLLMRunner
	SessionRPC              HostSessionRPC
	Publisher               HostPublisher
	CommandRequiresCleanup  func(loader domain.Loader, request domain.LoaderCommandRequest) bool
	LinkedSandboxIDFromJSON func(method, requestJSON, responseJSON string) string
}

type RuntimeHost struct {
	deps         RunHostDependencies
	loader       domain.Loader
	execution    RuntimeExecutionContext
	triggerEvent TriggerEventMetadata

	commandSessionIDs       map[string]struct{}
	commandSessionIDOrder   []string
	commandReusableSession  *domain.Sandbox
	projectAgentRunSequence atomic.Uint64
}

func NewRuntimeHost(deps RunHostDependencies, loader domain.Loader, execution RuntimeExecutionContext, triggerEvent TriggerEventMetadata) *RuntimeHost {
	return &RuntimeHost{deps: deps, loader: loader, execution: execution, triggerEvent: triggerEvent}
}

func (h *RuntimeHost) Log(ctx context.Context, message string, payload any) error {
	return h.addLoaderEvent(ctx, "loader.log", "info", message, payload, "", "", "")
}

func (h *RuntimeHost) PublishEvent(ctx context.Context, topic string, payloadJSON string) (domain.TopicEventRecord, error) {
	if h.deps.Store == nil {
		return domain.TopicEventRecord{}, fmt.Errorf("event store is unavailable")
	}
	publisherRunID := ""
	if h.execution.Kind == ExecutionKindTrigger {
		publisherRunID = h.execution.ID
	}
	published, err := NewPublishedTopicEvent(topic, payloadJSON, h.triggerEvent, h.loader.Summary.ID, publisherRunID)
	if err != nil {
		return domain.TopicEventRecord{}, err
	}
	created, err := h.deps.Store.CreateEvent(ctx, published.Record)
	if err != nil {
		_ = h.addLoaderEvent(ctx, "loader.event.publish.failed", "error", err.Error(), map[string]any{"topic": published.Record.Topic}, "", "", "")
		return domain.TopicEventRecord{}, err
	}
	if sequenced, err := UpdatePublishedTopicEventSequence(published, created.Sequence); err == nil {
		_ = h.deps.Store.UpdateEventPayload(ctx, created.ID, sequenced.PayloadJSON)
		created.PayloadJSON = sequenced.PayloadJSON
		created.PayloadHash = sequenced.PayloadHash
	}
	_ = h.addLoaderEvent(ctx, "loader.event.published", "info", "loader event published", map[string]any{
		"eventId":       created.ID,
		"sequence":      created.Sequence,
		"topic":         created.Topic,
		"correlationId": created.CorrelationID,
	}, "", "", "")
	return created, nil
}

func (h *RuntimeHost) StateGet(ctx context.Context, key string) (string, bool, error) {
	return h.deps.Store.GetLoaderState(ctx, h.loader.Summary.ID, key)
}

func (h *RuntimeHost) StateSet(ctx context.Context, key, valueJSON string) error {
	return h.deps.Store.SetLoaderState(ctx, h.loader.Summary.ID, key, valueJSON)
}

func (h *RuntimeHost) StateDelete(ctx context.Context, key string) error {
	return h.deps.Store.DeleteLoaderState(ctx, h.loader.Summary.ID, key)
}

func (h *RuntimeHost) CallSessionRPC(ctx context.Context, method, requestJSON string) (string, error) {
	if h.deps.SessionRPC == nil {
		return "", fmt.Errorf("sandbox rpc bridge is unavailable")
	}
	method = strings.TrimSpace(method)
	requestJSON = strings.TrimSpace(requestJSON)
	ctx = WithSandboxCreationContext(ctx, SandboxCreationContext{
		AgentDefinitionID: strings.TrimSpace(h.loader.Summary.AgentID),
		Provider:          domain.NormalizeAgentKind(h.loader.Summary.DefaultAgent),
	})
	responseJSON, err := h.deps.SessionRPC.CallJSONWithSource(ctx, method, requestJSON, domain.SandboxTypeScript+":"+h.loader.Summary.ID)
	linkedSandboxID := h.linkedSandboxID(method, requestJSON, responseJSON)
	if err != nil {
		event, _ := h.addLoaderEventRecord(ctx, "loader.sandbox.rpc.failed", "error", firstHostNonEmpty(err.Error(), fmt.Sprintf("%s failed", method)), map[string]any{"method": method, "requestJson": requestJSON}, linkedSandboxID, "", "")
		h.addEventSandboxLink(ctx, event, linkedSandboxID, "sandbox_rpc_failed")
		return "", err
	}
	event, _ := h.addLoaderEventRecord(ctx, "loader.sandbox.rpc.completed", "info", fmt.Sprintf("%s completed", method), map[string]any{"method": method, "requestJson": requestJSON, "responseJson": responseJSON}, linkedSandboxID, "", "")
	h.addEventSandboxLink(ctx, event, linkedSandboxID, "sandbox_rpc_completed")
	return responseJSON, nil
}

func (h *RuntimeHost) Agent(ctx context.Context, prompt string, request domain.LoaderAgentRequest) (domain.LoaderAgentResult, error) {
	request.BindingTriggerID = h.execution.TriggerID
	if h.useProjectManagedAgentRun(request) {
		return h.ProjectAgent(ctx, prompt, request)
	}
	session, eventType, err := h.deps.Sessions.Ensure(ctx, h.loader, request, true)
	if err != nil {
		return domain.LoaderAgentResult{}, err
	}
	if eventType != "" {
		_ = h.addLinkedLoaderEvent(ctx, eventType, "info", "loader sandbox ready", map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	}

	agentConfig := execution.AgentConfig{Provider: domain.NormalizeAgentKind(request.Agent)}
	var agentDefinitionID string
	if agentConfig.Provider == "" {
		agentDefinition, err := h.deps.AgentDefinitions.ResolveLoaderAgentDefinition(ctx, h.loader)
		if err != nil {
			return domain.LoaderAgentResult{}, err
		}
		if agentDefinition != nil {
			agentConfig = execution.AgentConfigFromDefinition(*agentDefinition, "")
			agentDefinitionID = strings.TrimSpace(agentDefinition.ID)
		}
	}
	if agentDefinitionID == "" {
		agentDefinitionID = strings.TrimSpace(h.loader.Summary.AgentID)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = domain.NormalizeAgentKind(h.loader.Summary.DefaultAgent)
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
	result := domain.LoaderAgentResult{
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
	eventName := "loader.agent.completed"
	if execErr != nil {
		level = "error"
		eventName = "loader.agent.failed"
		result.Text = firstHostNonEmpty(result.Text, execErr.Error())
	}
	_ = h.addLinkedLoaderEvent(ctx, eventName, level, firstHostNonEmpty(result.Text, fmt.Sprintf("%s completed", result.Agent)), result, result.SandboxID, result.CellID, result.AgentThreadID)
	h.publishAgentCompleted(result, nil)
	if shutdownErr := h.deps.Sessions.Shutdown(ctx, session.Summary.ID); shutdownErr != nil {
		slog.Warn("failed to stop loader sandbox after agent run", "loader_id", h.loader.Summary.ID, "sandbox_id", session.Summary.ID, "error", shutdownErr)
		_ = h.addLinkedLoaderEvent(ctx, "loader.sandbox.stop_failed", "error", shutdownErr.Error(), map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	} else {
		_ = h.addLinkedLoaderEvent(ctx, "loader.sandbox.stopped", "info", "loader sandbox stopped after agent run", map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	}
	if execErr != nil {
		return result, execErr
	}
	return result, nil
}

func (h *RuntimeHost) Command(ctx context.Context, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	cleanupSession := h.commandRequiresCleanup(request)
	agentRequest := domain.LoaderAgentRequest{
		SandboxPolicy:    domain.LoaderCommandSandboxPolicy(request),
		Title:            request.Title,
		Driver:           request.Driver,
		GuestImage:       request.GuestImage,
		PullPolicy:       request.PullPolicy,
		WorkspaceID:      request.WorkspaceID,
		JupyterEnabled:   request.JupyterEnabled,
		SandboxEnv:       domain.LoaderCommandSandboxEnv(request),
		Volumes:          request.Volumes,
		BindingTriggerID: h.execution.TriggerID,
	}
	session, eventType, err := h.ensureCommandSession(ctx, agentRequest, cleanupSession)
	if err != nil {
		_ = h.addLoaderEvent(ctx, "loader.command.failed", "error", err.Error(), CommandEventPayload(request, domain.LoaderCommandResult{}), "", "", "")
		return domain.LoaderCommandResult{}, err
	}
	if eventType != "" {
		_ = h.addLinkedLoaderEvent(ctx, eventType, "info", "loader command sandbox ready", map[string]any{"sandboxId": session.Summary.ID}, session.Summary.ID, "", "")
	}
	h.trackCommandSession(session.Summary.ID, cleanupSession)

	result, err := h.deps.CommandExecutor.ExecuteLoaderCommand(ctx, session, request)
	if err != nil {
		_ = h.addLinkedLoaderEvent(ctx, "loader.command.failed", "error", err.Error(), CommandEventPayload(request, result), result.SandboxID, result.CellID, "")
		return result, err
	}
	level := "info"
	if !result.Success {
		level = "error"
	}
	_ = h.addLinkedLoaderEvent(ctx, "loader.command.completed", level, firstHostNonEmpty(result.Output, result.Stdout, result.Stderr, "loader command completed"), CommandEventPayload(request, result), result.SandboxID, result.CellID, "")
	return result, nil
}

func (h *RuntimeHost) LLM(ctx context.Context, prompt string, request domain.LoaderLLMRequest) (domain.LoaderLLMResult, error) {
	if h.deps.LLM == nil {
		return domain.LoaderLLMResult{}, fmt.Errorf("llm client is unavailable")
	}
	result, err := h.deps.LLM.Generate(ctx, prompt, request.Model, request.OutputSchema)
	if err != nil {
		_ = h.addLoaderEvent(ctx, "loader.llm.failed", "error", err.Error(), map[string]any{"model": strings.TrimSpace(request.Model)}, "", "", "")
		return domain.LoaderLLMResult{}, err
	}
	_ = h.addLoaderEvent(ctx, "loader.llm.completed", "info", firstHostNonEmpty(result.Text, "llm completed"), result, "", "", "")
	return result, nil
}

func (h *RuntimeHost) CleanupCommandSessions(ctx context.Context) {
	sessionIDs := append([]string(nil), h.commandSessionIDOrder...)
	h.commandSessionIDs = nil
	h.commandSessionIDOrder = nil
	for _, sessionID := range sessionIDs {
		if err := h.deps.Sessions.Shutdown(ctx, sessionID); err != nil {
			slog.Warn("failed to stop loader command sandbox after run", "loader_id", h.loader.Summary.ID, "sandbox_id", sessionID, "error", err)
			_ = h.addLinkedLoaderEvent(ctx, "loader.sandbox.stop_failed", "error", err.Error(), map[string]any{"sandboxId": sessionID}, sessionID, "", "")
			continue
		}
		_ = h.addLinkedLoaderEvent(ctx, "loader.sandbox.stopped", "info", "loader command sandbox stopped after run", map[string]any{"sandboxId": sessionID}, sessionID, "", "")
	}
}

func (h *RuntimeHost) ensureCommandSession(ctx context.Context, request domain.LoaderAgentRequest, cleanupSession bool) (*domain.Sandbox, string, error) {
	if cleanupSession && h.commandReusableSession != nil {
		if loaded, err := h.deps.Sessions.Load(ctx, h.commandReusableSession.Summary.ID); err == nil && loaded.Summary.VMStatus == domain.VMStatusRunning {
			return loaded, "", nil
		}
	}
	session, eventType, err := h.deps.Sessions.Ensure(ctx, h.loader, request, false)
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

func (h *RuntimeHost) addLoaderEvent(ctx context.Context, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) error {
	if h.deps.Events == nil {
		return nil
	}
	return h.deps.Events.Add(ctx, h.loader.Summary.ID, h.execution.ID, h.execution.TriggerID, eventType, level, message, payload, linkedSandboxID, linkedCellID, linkedAgentThreadID)
}

func (h *RuntimeHost) addLoaderEventRecord(ctx context.Context, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) (domain.LoaderEvent, error) {
	if h.deps.Events == nil {
		return domain.LoaderEvent{}, nil
	}
	return h.deps.Events.AddRecord(ctx, h.loader.Summary.ID, h.execution.ID, h.execution.TriggerID, eventType, level, message, payload, linkedSandboxID, linkedCellID, linkedAgentThreadID)
}

func (h *RuntimeHost) addLinkedLoaderEvent(ctx context.Context, eventType, level, message string, payload any, linkedSandboxID, linkedCellID, linkedAgentThreadID string) error {
	event, err := h.addLoaderEventRecord(ctx, eventType, level, message, payload, linkedSandboxID, linkedCellID, linkedAgentThreadID)
	if err != nil {
		return err
	}
	h.addEventSandboxLink(ctx, event, linkedSandboxID, event.Type)
	return nil
}

func (h *RuntimeHost) addEventSandboxLink(ctx context.Context, event domain.LoaderEvent, sandboxID, relation string) {
	if h.deps.Store == nil || strings.TrimSpace(sandboxID) == "" || h.triggerEvent.EventID == "" {
		return
	}
	if err := h.deps.Store.AddEventSandboxLink(ctx, domain.EventSandboxLink{
		EventID:       h.triggerEvent.EventID,
		SandboxID:     sandboxID,
		Relation:      relation,
		LoaderID:      h.loader.Summary.ID,
		RunID:         h.execution.ID,
		TriggerID:     h.execution.TriggerID,
		LoaderEventID: event.ID,
		CreatedAt:     event.CreatedAt,
	}); err != nil {
		slog.Warn("failed to add event sandbox link", "event_id", h.triggerEvent.EventID, "sandbox_id", sandboxID, "run_id", h.execution.ID, "error", err)
	}
}

func (h *RuntimeHost) publishAgentCompleted(result domain.LoaderAgentResult, projectRun *domain.ProjectRunRecord) {
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
		"source":        "loader",
		"loaderId":      h.loader.Summary.ID,
	}
	if projectRun != nil {
		if h.execution.Kind == ExecutionKindTrigger {
			payload["loaderRunId"] = h.execution.ID
		}
		payload["projectId"] = projectRun.ProjectID
		payload["projectRunId"] = projectRun.RunID
	}
	h.deps.Publisher.Publish("agent-compose.agent.completed", payload)
}

func (h *RuntimeHost) commandRequiresCleanup(request domain.LoaderCommandRequest) bool {
	if h.deps.CommandRequiresCleanup == nil {
		return CommandRequestRequiresCleanup(h.loader, request)
	}
	return h.deps.CommandRequiresCleanup(h.loader, request)
}

func (h *RuntimeHost) linkedSandboxID(method, requestJSON, responseJSON string) string {
	if h.deps.LinkedSandboxIDFromJSON == nil {
		return ""
	}
	return h.deps.LinkedSandboxIDFromJSON(method, requestJSON, responseJSON)
}

func AgentRequestOverridesSession(request domain.LoaderAgentRequest, includeTitle bool) bool {
	return (includeTitle && strings.TrimSpace(request.Title) != "") ||
		strings.TrimSpace(request.Driver) != "" ||
		strings.TrimSpace(request.GuestImage) != "" ||
		strings.TrimSpace(request.WorkspaceID) != "" ||
		len(domain.NormalizeEnvItems(domain.LoaderAgentSandboxEnv(request))) > 0 ||
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
