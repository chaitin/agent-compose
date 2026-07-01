package loaders

import (
	"context"
	"sort"
	"strings"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"

	"agent-compose/pkg/bus"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/events"
	"agent-compose/pkg/executor"
	"agent-compose/pkg/images"
	"agent-compose/pkg/llm"
	"agent-compose/pkg/model"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage"
	"agent-compose/pkg/workspaces"
)

const (
	VMStatusRunning = model.VMStatusRunning
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed

	SessionTypeScript = model.SessionTypeScript

	CellTypeShell = model.CellTypeShell

	TopicEventSourceWebhook = model.TopicEventSourceWebhook
	TopicEventSourceLoader  = model.TopicEventSourceLoader

	TopicEventDispatchPending        = model.TopicEventDispatchPending
	TopicEventDispatchPublishedToBus = model.TopicEventDispatchPublishedToBus

	EventDeliveryStatusMatched      = model.EventDeliveryStatusMatched
	EventDeliveryStatusRunStarted   = model.EventDeliveryStatusRunStarted
	EventDeliveryStatusRunSucceeded = model.EventDeliveryStatusRunSucceeded
	EventDeliveryStatusRunFailed    = model.EventDeliveryStatusRunFailed
	EventDeliveryStatusSkipped      = model.EventDeliveryStatusSkipped

	ProjectRunStatusSucceeded = model.ProjectRunStatusSucceeded

	agentResultPrefix   = "__AGENT_RESULT__"
	commandResultPrefix = "__COMMAND_RESULT__"
)

type Store = storage.Store
type ConfigStore = storage.ConfigStore

type SessionTag = model.SessionTag
type SessionEnvVar = model.SessionEnvVar
type SessionWorkspace = model.SessionWorkspace
type SessionListOptions = model.SessionListOptions
type Session = model.Session
type SessionEvent = model.SessionEvent
type NotebookCell = model.NotebookCell
type WorkspaceConfig = model.WorkspaceConfig
type AgentDefinition = model.AgentDefinition
type VMState = model.VMState
type ProxyState = model.ProxyState
type ExecSpec = model.ExecSpec
type ExecResult = model.ExecResult
type ExecChunk = model.ExecChunk
type ExecStreamWriter = model.ExecStreamWriter
type RuntimeCommandResult = model.RuntimeCommandResult
type RuntimeCommandArtifacts = model.RuntimeCommandArtifacts
type ExecuteAgentRequest = executor.ExecuteAgentRequest

type TopicEventRecord = model.TopicEventRecord
type EventDelivery = model.EventDelivery
type EventSessionLink = model.EventSessionLink

type ProjectRunRecord = model.ProjectRunRecord

type LoaderBus = bus.LoaderBus
type Driver = runtimes.Driver
type BoxRuntime = runtimes.BoxRuntime
type SessionVMInfo = runtimes.SessionVMInfo
type RuntimeProvider = runtimes.RuntimeProvider
type ImageBackend = images.ImageBackend
type CapabilityProvider = capabilities.Provider
type CapabilityIntegration = capabilities.Integration
type DashboardOverviewHub = dashboard.DashboardOverviewHub

type WebhookRunQueue = events.WebhookRunQueue
type webhookQueueReservation = events.WebhookQueueReservation

type SessionStreamBroker = sessions.SessionStreamBroker
type SessionRPCBridge = sessions.SessionRPCBridge

type LLMClient = llm.LLMClient

type Executor struct {
	config    *appconfig.Config
	store     *Store
	configDB  *ConfigStore
	runtimes  RuntimeProvider
	streams   executor.StreamPublisher
	component *executor.Executor
}

func NewExecutor(config *appconfig.Config, store *Store, configDB *ConfigStore, runtimes RuntimeProvider, streams executor.StreamPublisher) *Executor {
	return &Executor{config: config, store: store, configDB: configDB, runtimes: runtimes, streams: streams}
}

func (e *Executor) componentExecutor() *executor.Executor {
	if e == nil {
		return nil
	}
	if e.component == nil {
		streams := e.streams
		if streams == nil {
			streams = noopStreamPublisher{}
		}
		e.component = executor.New(e.config, e.store, e.configDB, e.runtimes, streams, llm.EnsureSessionLLMFacadeConfig)
	}
	return e.component
}

func (e *Executor) ExecuteAgentRequest(ctx context.Context, session *Session, request ExecuteAgentRequest) (NotebookCell, SessionEvent, SessionEvent, error) {
	return e.componentExecutor().ExecuteAgentRequest(ctx, session, request)
}

func (e *Executor) ExecuteLoaderCommand(ctx context.Context, session *Session, request LoaderCommandRequest) (LoaderCommandResult, error) {
	return e.componentExecutor().ExecuteLoaderCommand(ctx, session, request)
}

type noopStreamPublisher struct{}

func (noopStreamPublisher) PublishCellStarted(string, model.NotebookCell)   {}
func (noopStreamPublisher) PublishCellOutput(string, string, string, bool)  {}
func (noopStreamPublisher) PublishCellCompleted(string, model.NotebookCell) {}
func (noopStreamPublisher) PublishEventAdded(string, model.SessionEvent)    {}

type ProjectAgentRunner interface {
	RunProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error)
}

type noopProjectAgentRunner struct{}

func (noopProjectAgentRunner) RunProjectAgent(context.Context, *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error) {
	return ProjectRunRecord{}, nil, nil
}

type Service struct {
	configDB *storage.ConfigStore
	loaders  *LoaderManager
	bus      *LoaderBus
}

func NewService(configDB *storage.ConfigStore, manager *LoaderManager, bus *LoaderBus) *Service {
	return &Service{configDB: configDB, loaders: manager, bus: bus}
}

func NewLoaderBusWithBuffer(size int) *LoaderBus {
	return bus.NewLoaderBusWithBuffer(size)
}

type EventDispatcher = events.EventDispatcher

func NewEventDispatcher(rootCtx context.Context, configDB *ConfigStore, loaderBus *LoaderBus) *EventDispatcher {
	return events.NewEventDispatcher(rootCtx, configDB, loaderBus)
}

func newWebhookRunQueueFromConfig(config *appconfig.Config) (*WebhookRunQueue, error) {
	return events.NewWebhookRunQueueFromConfig(config)
}

func newWebhookRunQueue(defaultWorkers int) *WebhookRunQueue {
	return events.NewWebhookRunQueue(defaultWorkers)
}

func noopWebhookQueueReservations(count int) []*webhookQueueReservation {
	return events.NoopWebhookQueueReservations(count)
}

func NewDockerImageBackend() ImageBackend {
	return images.NewDockerImageBackend()
}

func topicEventPayloadSHA256(payloadJSON string) string {
	return events.TopicEventPayloadSHA256(payloadJSON)
}

func normalizeAgentKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "":
		return ""
	case "codex":
		return "codex"
	case "claude", "claude-code", "claude_code":
		return "claude"
	case "gemini", "gemini-cli", "gemini_cli":
		return "gemini"
	case "opencode", "open-code", "open_code":
		return "opencode"
	default:
		return kind
	}
}

type agentExecutionConfig struct {
	Provider          string
	AgentDefinitionID string
	Model             string
	EnvItems          []SessionEnvVar
}

func agentExecutionConfigFromDefinition(agent AgentDefinition, fallbackProvider string) agentExecutionConfig {
	provider := normalizeAgentKind(agent.Provider)
	if provider == "" {
		provider = normalizeAgentKind(fallbackProvider)
	}
	modelName := strings.TrimSpace(agent.Model)
	if provider == "opencode" {
		modelName = strings.TrimSpace(sessionEnvMap(agent.EnvItems)["OPENCODE_MODEL"])
	}
	return agentExecutionConfig{
		Provider:          provider,
		AgentDefinitionID: strings.TrimSpace(agent.ID),
		Model:             modelName,
		EnvItems:          append([]SessionEnvVar(nil), agent.EnvItems...),
	}
}

func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
	return model.SessionEnvMap(groups...)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	merged := make(map[string]SessionEnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		merged[name] = SessionEnvVar{Name: name, Value: item.Value, Secret: item.Secret}
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	merged := make(map[string]SessionEnvVar, len(globalItems)+len(sessionItems))
	for _, item := range normalizeEnvItems(globalItems) {
		merged[item.Name] = item
	}
	for _, item := range normalizeEnvItems(sessionItems) {
		merged[item.Name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func filterPersistedRuntimeEnv(items []SessionEnvVar) []SessionEnvVar {
	return llm.FilterPersistedRuntimeEnv(items)
}

func toSessionWorkspaceSnapshot(item WorkspaceConfig) *SessionWorkspace {
	return &SessionWorkspace{ID: item.ID, Name: item.Name, Type: item.Type, ConfigJSON: item.ConfigJSON}
}

func restoreSessionTransientFields(dst, src *Session) {
	model.RestoreSessionTransientFields(dst, src)
}

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session) error {
	return workspaces.PrepareSessionWorkspace(ctx, config, configDB, session)
}

func buildCapabilityGatewaySessionVars(publicTarget string, capsetIDs []string) ([]SessionEnvVar, []SessionTag) {
	return capabilities.BuildGatewaySessionVars(publicTarget, capsetIDs)
}

func writeCapabilityGuide(ctx context.Context, provider CapabilityProvider, store *Store, streams *SessionStreamBroker, session *Session, capsetIDs []string) {
	capabilities.WriteGuide(ctx, provider, store, streams, session, capsetIDs)
}

func capabilityGatewayProxyTarget(provider CapabilityProvider) string {
	return capabilities.GatewayProxyTarget(provider)
}

func sessionCapabilityCapsets(session *Session) []string {
	return capabilities.SessionCapabilityCapsets(session)
}
