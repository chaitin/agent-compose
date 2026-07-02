package loader

import (
	"context"
	"time"

	agentdomain "agent-compose/internal/agent"
	capdomain "agent-compose/internal/capability"
	eventdomain "agent-compose/internal/event"
	eventtypes "agent-compose/internal/eventtypes"
	execdomain "agent-compose/internal/exec"
	imagedomain "agent-compose/internal/image"
	loadertypes "agent-compose/internal/loadertypes"
	modeldomain "agent-compose/internal/model"
	filestore "agent-compose/internal/persistence/filestore"
	projecttypes "agent-compose/internal/projecttypes"
	sessiondomain "agent-compose/internal/session"
	llmdomain "agent-compose/pkg/llm"
)

type Store = filestore.Store
type Driver = sessiondomain.Driver
type Executor = execdomain.Executor
type ImageBackend = imagedomain.ImageBackend
type ImageListRequest = imagedomain.ImageListRequest
type ImageListResult = imagedomain.ImageListResult
type ImagePullRequest = imagedomain.ImagePullRequest
type ImagePullResult = imagedomain.ImagePullResult
type ImageInspectRequest = imagedomain.ImageInspectRequest
type ImageInspectResult = imagedomain.ImageInspectResult
type ImageRemoveRequest = imagedomain.ImageRemoveRequest
type ImageRemoveResult = imagedomain.ImageRemoveResult
type LLMClient = llmdomain.LLMClient
type LLMFacadeToken = llmdomain.LLMFacadeToken
type LLMConfigStore = llmdomain.ConfigStore
type LLMProvider = llmdomain.LLMProvider
type LLMModel = llmdomain.LLMModel
type CapabilityProvider = capdomain.CapabilityProvider
type SessionStreamBroker = sessiondomain.SessionStreamBroker

type SessionTag = modeldomain.SessionTag
type SessionEnvVar = modeldomain.SessionEnvVar
type Session = modeldomain.Session
type SessionSummary = modeldomain.SessionSummary
type SessionListOptions = modeldomain.SessionListOptions
type SessionWorkspace = modeldomain.SessionWorkspace
type WorkspaceConfig = modeldomain.WorkspaceConfig
type NotebookCell = modeldomain.NotebookCell
type SessionEvent = modeldomain.SessionEvent
type RuntimeCommandResult = modeldomain.RuntimeCommandResult
type RuntimeCommandArtifacts = modeldomain.RuntimeCommandArtifacts
type ExecSpec = modeldomain.ExecSpec
type ExecResult = modeldomain.ExecResult
type ExecStreamWriter = modeldomain.ExecStreamWriter
type ExecChunk = modeldomain.ExecChunk
type ExecuteAgentRequest = execdomain.ExecuteAgentRequest
type VMState = modeldomain.VMState
type ProxyState = modeldomain.ProxyState
type SessionVMInfo = execdomain.SessionVMInfo
type BoxRuntime = execdomain.BoxRuntime

type AgentDefinition = agentdomain.AgentDefinition
type TopicEventRecord = eventdomain.TopicEventRecord
type EventDelivery = eventdomain.EventDelivery
type EventSessionLink = eventdomain.EventSessionLink
type LoaderTopicEvent = eventtypes.LoaderTopicEvent
type LoaderAgentRequest = loadertypes.LoaderAgentRequest
type LoaderAgentResult = loadertypes.LoaderAgentResult
type LoaderCommandRequest = loadertypes.LoaderCommandRequest
type LoaderCommandResult = loadertypes.LoaderCommandResult
type LoaderLLMRequest = loadertypes.LoaderLLMRequest
type LoaderLLMResult = loadertypes.LoaderLLMResult
type LoaderSummary = loadertypes.LoaderSummary
type Loader = loadertypes.Loader
type LoaderTrigger = loadertypes.LoaderTrigger
type LoaderRunSummary = loadertypes.LoaderRunSummary
type LoaderEvent = loadertypes.LoaderEvent
type LoaderBinding = loadertypes.LoaderBinding
type ProjectRunRecord = projecttypes.ProjectRunRecord

const (
	LoaderRuntimeScheduler           = loadertypes.LoaderRuntimeScheduler
	LoaderTriggerKindInterval        = loadertypes.LoaderTriggerKindInterval
	LoaderTriggerKindEvent           = loadertypes.LoaderTriggerKindEvent
	LoaderTriggerKindTimeout         = loadertypes.LoaderTriggerKindTimeout
	LoaderTriggerKindCron            = loadertypes.LoaderTriggerKindCron
	LoaderSessionPolicySticky        = loadertypes.LoaderSessionPolicySticky
	LoaderSessionPolicyNew           = loadertypes.LoaderSessionPolicyNew
	LoaderSessionPolicyReuse         = loadertypes.LoaderSessionPolicyReuse
	LoaderConcurrencyPolicySkip      = loadertypes.LoaderConcurrencyPolicySkip
	LoaderConcurrencyPolicyParallel  = loadertypes.LoaderConcurrencyPolicyParallel
	LoaderRunStatusRunning           = loadertypes.LoaderRunStatusRunning
	LoaderRunStatusSucceeded         = loadertypes.LoaderRunStatusSucceeded
	LoaderRunStatusFailed            = loadertypes.LoaderRunStatusFailed
	LoaderRunStatusSkipped           = loadertypes.LoaderRunStatusSkipped
	CellTypeShell                    = execdomain.CellTypeShell
	TopicEventDispatchPublishedToBus = eventdomain.TopicEventDispatchPublishedToBus
	TopicEventDispatchRetrying       = eventdomain.TopicEventDispatchRetrying
	ProjectRunStatusSucceeded        = projecttypes.ProjectRunStatusSucceeded
	SessionTypeScript                = modeldomain.SessionTypeScript
	VMStatusRunning                  = modeldomain.VMStatusRunning
	VMStatusStopped                  = modeldomain.VMStatusStopped
	VMStatusFailed                   = modeldomain.VMStatusFailed
)

var NewDockerImageBackend = imagedomain.NewDockerImageBackend
var NewStoreForConfig = filestore.NewStoreForConfig
var NewExecutorForTest = execdomain.NewExecutorForTest
var mirrorRuntimeCommandArtifacts = execdomain.MirrorRuntimeCommandArtifacts
var NewEventDispatcher = eventdomain.NewEventDispatcher
var NewLLMClientForTest = llmdomain.NewClientForTest
var hostSessionDir = execdomain.HostSessionDir
var writeJSONArtifact = execdomain.WriteJSONArtifact
var firstNonZeroInt = execdomain.FirstNonZeroInt

const commandResultPrefix = execdomain.CommandResultPrefix
const agentResultPrefix = execdomain.AgentResultPrefix

type dashboardNotifier interface {
	Notify(string)
}

type SessionRPCBridge interface {
	CallJSONWithSource(ctx context.Context, method, requestJSON, source string) (string, error)
}

type ConfigStore interface {
	ListLoaders(context.Context) ([]Loader, error)
	CreateLoader(context.Context, Loader) (Loader, error)
	UpdateLoader(context.Context, Loader) (Loader, error)
	UpsertManagedLoader(context.Context, Loader) (Loader, error)
	DeleteLoader(context.Context, string) error
	DisableLoadersByDefaultAgent(context.Context, string) (int, error)
	ListLoaderSummaries(context.Context) ([]LoaderSummary, error)
	GetLoader(context.Context, string) (Loader, error)
	ListManagedLoaders(context.Context, string) ([]Loader, error)
	ReplaceLoaderTriggers(context.Context, string, []LoaderTrigger) ([]LoaderTrigger, error)
	SetLoaderEnabled(context.Context, string, bool) error
	SetLoaderTriggerEnabled(context.Context, string, string, bool) error
	UpdateLoaderLastError(context.Context, string, string) error
	MarkLoaderTriggerFired(context.Context, string, string, time.Time, time.Time) error
	CreateLoaderRun(context.Context, LoaderRunSummary) error
	UpdateLoaderRun(context.Context, LoaderRunSummary) error
	GetLoaderRun(context.Context, string, string) (LoaderRunSummary, error)
	ListLoaderRuns(context.Context, string, int) ([]LoaderRunSummary, error)
	ListRecentLoaderRuns(context.Context, int) ([]LoaderRunSummary, error)
	AddLoaderEvent(context.Context, LoaderEvent) error
	ListLoaderEvents(context.Context, string, int) ([]LoaderEvent, error)
	CreateEvent(context.Context, TopicEventRecord) (TopicEventRecord, error)
	UpdateEventPayload(context.Context, string, string) error
	AddEventSessionLink(context.Context, EventSessionLink) error
	GetLoaderState(context.Context, string, string) (string, bool, error)
	SetLoaderState(context.Context, string, string, string) error
	DeleteLoaderState(context.Context, string, string) error
	GetLoaderBinding(context.Context, string) (LoaderBinding, bool, error)
	UpsertLoaderBinding(context.Context, LoaderBinding) error
	ListGlobalEnv(context.Context) ([]SessionEnvVar, error)
	ReplaceGlobalEnv(context.Context, []SessionEnvVar) ([]SessionEnvVar, error)
	GetLLMFacadeToken(context.Context, string) (LLMFacadeToken, error)
	ListPendingEvents(context.Context, int) ([]TopicEventRecord, error)
	GetEvent(context.Context, string) (TopicEventRecord, error)
	ListDispatchableEvents(context.Context, time.Time, int) ([]TopicEventRecord, error)
	ClaimEvent(context.Context, string, string, time.Time, time.Time) (bool, error)
	ReleaseEventClaim(context.Context, string, string, string, string, time.Time) error
	MarkEventPublished(context.Context, string, string, time.Time) error
	MarkEventNoSubscriber(context.Context, string, string, time.Time) error
	GetWorkspaceConfig(context.Context, string) (WorkspaceConfig, error)
	GetAgentDefinition(context.Context, string) (AgentDefinition, error)
	UpsertEventDelivery(context.Context, EventDelivery) error
	ListEnabledLLMProviders(context.Context) ([]LLMProvider, error)
	ListEnabledLLMModels(context.Context) ([]LLMModel, error)
	LLMProviderModelWireAPI(context.Context, string, string) (string, bool, error)
}
