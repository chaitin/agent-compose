package app

import (
	agentdomain "agent-compose/internal/agent"
	capdomain "agent-compose/internal/capability"
	eventdomain "agent-compose/internal/event"
	execdomain "agent-compose/internal/exec"
	imagedomain "agent-compose/internal/image"
	loaderdomain "agent-compose/internal/loader"
	modeldomain "agent-compose/internal/model"
	filestore "agent-compose/internal/persistence/filestore"
	sqlitestore "agent-compose/internal/persistence/sqlite"
	rundomain "agent-compose/internal/run"
	runtimedomain "agent-compose/internal/runtime"
	sessiondomain "agent-compose/internal/session"
	workspacedomain "agent-compose/internal/workspace"
	driverpkg "agent-compose/pkg/driver"
	llmdomain "agent-compose/pkg/llm"
)

type Store = filestore.Store
type ConfigStore = sqlitestore.ConfigStore
type Driver = sessiondomain.Driver
type RuntimeProvider = runtimedomain.RuntimeProvider
type sessionAliveRuntime = runtimedomain.SessionAliveRuntime
type Executor = execdomain.Executor
type BoxRuntime = runtimedomain.BoxRuntime
type LoaderManager = loaderdomain.LoaderManager
type QJSLoaderEngine = loaderdomain.QJSLoaderEngine
type LoaderBus = loaderdomain.LoaderBus
type SessionStreamBroker = sessiondomain.SessionStreamBroker
type EventDispatcher = eventdomain.EventDispatcher
type LLMClient = llmdomain.LLMClient
type RunCoordinator = rundomain.RunCoordinator
type ProjectRunStartRequest = rundomain.ProjectRunStartRequest
type ProjectRunTransitionRequest = rundomain.ProjectRunTransitionRequest

type SessionTag = modeldomain.SessionTag
type SessionEnvVar = modeldomain.SessionEnvVar
type SessionSummary = modeldomain.SessionSummary
type SessionListOptions = modeldomain.SessionListOptions
type SessionListResult = modeldomain.SessionListResult
type SessionWorkspace = modeldomain.SessionWorkspace
type Session = modeldomain.Session
type WorkspaceConfig = modeldomain.WorkspaceConfig
type fileWorkspaceContent = workspacedomain.FileWorkspaceContent
type gitWorkspaceConfig = workspacedomain.GitWorkspaceConfig
type fileWorkspaceConfig = workspacedomain.FileWorkspaceConfig
type NotebookCell = modeldomain.NotebookCell
type AgentResumeInfo = modeldomain.AgentResumeInfo
type ExecChunk = modeldomain.ExecChunk
type SessionEvent = modeldomain.SessionEvent
type sessionWatchEvent = sessiondomain.SessionWatchEvent
type sessionWatchEventType = sessiondomain.SessionWatchEventType
type AgentRun = modeldomain.AgentRun
type ExecResult = modeldomain.ExecResult
type RuntimeCommandArtifacts = modeldomain.RuntimeCommandArtifacts
type RuntimeCommandResult = modeldomain.RuntimeCommandResult
type ExecStreamWriter = modeldomain.ExecStreamWriter
type VMState = modeldomain.VMState
type ProxyState = modeldomain.ProxyState
type ExecSpec = modeldomain.ExecSpec
type SessionVMInfo = runtimedomain.SessionVMInfo
type AgentRunResult = modeldomain.AgentRunResult
type AgentExecutionStream = execdomain.AgentExecutionStream
type CellExecutionStream = execdomain.CellExecutionStream
type ExecuteAgentRequest = execdomain.ExecuteAgentRequest
type execStreamAccumulator = execdomain.ExecStreamAccumulator

const (
	CellTypeShell                    = modeldomain.CellTypeShell
	CellTypeAgent                    = modeldomain.CellTypeAgent
	CellTypePython                   = execdomain.CellTypePython
	CellTypeJavaScript               = execdomain.CellTypeJavaScript
	VMStatusPending                  = modeldomain.VMStatusPending
	VMStatusRunning                  = modeldomain.VMStatusRunning
	VMStatusStopped                  = modeldomain.VMStatusStopped
	VMStatusFailed                   = modeldomain.VMStatusFailed
	llmAPIProtocolResponses          = "responses"
	llmAPIProtocolChatCompletions    = "chat_completions"
	llmAPIProtocolMessages           = "messages"
	llmProviderFamilyOpenAI          = sqlitestore.LLMProviderFamilyOpenAI
	llmProviderFamilyAnthropic       = sqlitestore.LLMProviderFamilyAnthropic
	llmProviderScopeSystem           = "system"
	llmProviderScopeEnvDefault       = "env_default"
	llmProviderScopeSessionEnv       = "session_env"
	LoaderTriggerKindInterval        = loaderdomain.LoaderTriggerKindInterval
	LoaderTriggerKindEvent           = loaderdomain.LoaderTriggerKindEvent
	LoaderTriggerKindTimeout         = loaderdomain.LoaderTriggerKindTimeout
	LoaderTriggerKindCron            = loaderdomain.LoaderTriggerKindCron
	LoaderRuntimeScheduler           = loaderdomain.LoaderRuntimeScheduler
	LoaderSessionPolicyNew           = loaderdomain.LoaderSessionPolicyNew
	LoaderSessionPolicySticky        = loaderdomain.LoaderSessionPolicySticky
	LoaderConcurrencyPolicySkip      = loaderdomain.LoaderConcurrencyPolicySkip
	LoaderConcurrencyPolicyParallel  = loaderdomain.LoaderConcurrencyPolicyParallel
	LoaderRunStatusRunning           = loaderdomain.LoaderRunStatusRunning
	LoaderRunStatusSucceeded         = loaderdomain.LoaderRunStatusSucceeded
	LoaderRunStatusSkipped           = loaderdomain.LoaderRunStatusSkipped
	ProjectRunStatusPending          = sqlitestore.ProjectRunStatusPending
	ProjectRunStatusRunning          = sqlitestore.ProjectRunStatusRunning
	ProjectRunStatusSucceeded        = sqlitestore.ProjectRunStatusSucceeded
	ProjectRunStatusFailed           = sqlitestore.ProjectRunStatusFailed
	ProjectRunStatusCanceled         = sqlitestore.ProjectRunStatusCanceled
	ProjectRunSourceManual           = sqlitestore.ProjectRunSourceManual
	ProjectRunSourceScheduler        = sqlitestore.ProjectRunSourceScheduler
	ProjectRunSourceAPI              = sqlitestore.ProjectRunSourceAPI
	SessionTypeManual                = modeldomain.SessionTypeManual
	SessionTypeScript                = modeldomain.SessionTypeScript
	TopicEventSourceWebhook          = eventdomain.TopicEventSourceWebhook
	TopicEventSourceLoader           = eventdomain.TopicEventSourceLoader
	TopicEventDispatchPending        = eventdomain.TopicEventDispatchPending
	TopicEventDispatchPublishedToBus = eventdomain.TopicEventDispatchPublishedToBus
	EventDeliveryStatusRunSucceeded  = eventdomain.EventDeliveryStatusRunSucceeded

	sessionWatchEventTypeSessionUpdated = sessiondomain.SessionWatchEventTypeSessionUpdated
	sessionWatchEventTypeCellStarted    = sessiondomain.SessionWatchEventTypeCellStarted
	sessionWatchEventTypeCellOutput     = sessiondomain.SessionWatchEventTypeCellOutput
	sessionWatchEventTypeCellCompleted  = sessiondomain.SessionWatchEventTypeCellCompleted
	sessionWatchEventTypeEventAdded     = sessiondomain.SessionWatchEventTypeEventAdded
)

type AgentDefinition = agentdomain.AgentDefinition
type AgentDefinitionListOptions = agentdomain.AgentDefinitionListOptions
type AgentDefinitionListResult = agentdomain.AgentDefinitionListResult
type AgentValidationResult = agentdomain.AgentValidationResult
type AgentCurrentRunSummary = agentdomain.AgentCurrentRunSummary
type AgentLatestRunSummary = agentdomain.AgentLatestRunSummary

type LoaderSummary = loaderdomain.LoaderSummary
type Loader = loaderdomain.Loader
type LoaderTrigger = loaderdomain.LoaderTrigger
type LoaderRunSummary = loaderdomain.LoaderRunSummary
type LoaderEvent = loaderdomain.LoaderEvent
type LoaderBinding = loaderdomain.LoaderBinding
type LoaderAgentRequest = loaderdomain.LoaderAgentRequest
type LoaderAgentResult = loaderdomain.LoaderAgentResult
type LoaderCommandRequest = loaderdomain.LoaderCommandRequest
type LoaderCommandResult = loaderdomain.LoaderCommandResult
type LoaderLLMRequest = loaderdomain.LoaderLLMRequest
type LoaderLLMResult = loaderdomain.LoaderLLMResult
type LoaderTopicEvent = loaderdomain.LoaderTopicEvent
type LoaderValidationResult = loaderdomain.LoaderValidationResult
type LoaderHost = loaderdomain.LoaderHost
type LoaderExecutionRequest = loaderdomain.LoaderExecutionRequest
type LoaderExecutionResult = loaderdomain.LoaderExecutionResult

type TopicEventRecord = eventdomain.TopicEventRecord
type TopicEventFilter = eventdomain.TopicEventFilter
type WebhookSource = eventdomain.WebhookSource
type EventDelivery = eventdomain.EventDelivery
type EventSessionLink = eventdomain.EventSessionLink
type EventSessionTraceItem = eventdomain.EventSessionTraceItem

type CapabilityGatewaySettings = sqlitestore.CapabilityGatewaySettings
type ProjectRecord = sqlitestore.ProjectRecord
type ProjectRevisionRecord = sqlitestore.ProjectRevisionRecord
type ProjectAgentRecord = sqlitestore.ProjectAgentRecord
type ProjectSchedulerRecord = sqlitestore.ProjectSchedulerRecord
type ProjectRunRecord = sqlitestore.ProjectRunRecord
type ProjectListOptions = sqlitestore.ProjectListOptions
type ProjectRunListOptions = sqlitestore.ProjectRunListOptions
type ProjectListResult = sqlitestore.ProjectListResult
type ProjectSessionRelationFilter = sqlitestore.ProjectSessionRelationFilter
type ProjectSessionStatus = sqlitestore.ProjectSessionStatus
type LLMProvider = sqlitestore.LLMProvider
type LLMModel = sqlitestore.LLMModel
type LLMResolvedTarget = sqlitestore.LLMResolvedTarget
type LLMFacadeToken = sqlitestore.LLMFacadeToken

type ImageBackend = imagedomain.ImageBackend
type ImageListRequest = imagedomain.ImageListRequest
type ImageListResult = imagedomain.ImageListResult
type ImagePullRequest = imagedomain.ImagePullRequest
type ImagePullResult = imagedomain.ImagePullResult
type ImageInspectRequest = imagedomain.ImageInspectRequest
type ImageInspectResult = imagedomain.ImageInspectResult
type ImageRemoveRequest = imagedomain.ImageRemoveRequest
type ImageRemoveResult = imagedomain.ImageRemoveResult

var NewOCIImageBackend = imagedomain.NewOCIImageBackend
var NewAutoImageBackend = imagedomain.NewAutoImageBackend
var ociMetadataToProtoImage = imagedomain.OCIMetadataToProtoImage
var openFileWorkspaceContent = workspacedomain.OpenFileWorkspaceContent
var fileWorkspaceContentRelRoot = workspacedomain.FileWorkspaceContentRelRoot
var fileWorkspaceContentDirName = workspacedomain.FileWorkspaceContentDirName
var fileWorkspaceContentRoot = workspacedomain.FileWorkspaceContentRoot
var validateFileWorkspaceConfig = workspacedomain.ValidateFileWorkspaceConfig
var copyRootDirectoryContents = workspacedomain.CopyRootDirectoryContents
var normalizeGitCloneTarget = workspacedomain.NormalizeGitCloneTarget
var openFileWorkspaceDataRoot = workspacedomain.OpenFileWorkspaceDataRoot
var ensureRootParentDir = workspacedomain.EnsureRootParentDir
var extractWorkspaceTarArchive = workspacedomain.ExtractWorkspaceTarArchive
var prepareSessionWorkspace = workspacedomain.PrepareSessionWorkspace
var mergeEnvItems = modeldomain.MergeEnvItems
var normalizeCapsetIDs = modeldomain.NormalizeCapsetIDs
var buildCapabilityGatewaySessionVars = capdomain.BuildCapabilityGatewaySessionVars
var capabilityGatewayProxyTarget = capdomain.CapabilityGatewayProxyTarget
var writeCapabilityGuide = capdomain.WriteCapabilityGuide
var capProxyTargetEnvName = capdomain.CapProxyTargetEnvName
var capabilitySessionTokenEnvName = capdomain.CapabilitySessionTokenEnvName
var sessionCapabilityGuidePath = capdomain.SessionCapabilityGuidePath
var sessionCapabilityCapsets = filestore.SessionCapabilityCapsets
var restoreSessionTransientFields = modeldomain.RestoreSessionTransientFields
var toProtoEnvItems = agentdomain.ToProtoEnvItems
var toProtoAgentDefinition = agentdomain.ToProtoAgentDefinition
var normalizeAgentDefinition = agentdomain.NormalizeAgentDefinition
var mergeExecResults = execdomain.MergeExecResults
var firstNonZeroInt = execdomain.FirstNonZeroInt
var ListProjectSessionStatuses = sqlitestore.ListProjectSessionStatuses
var NewProjectRecordFromSpec = sqlitestore.NewProjectRecordFromSpec
var NewProjectAgentRecordFromSpec = sqlitestore.NewProjectAgentRecordFromSpec
var NewProjectSchedulerRecordFromSpec = sqlitestore.NewProjectSchedulerRecordFromSpec
var StableProjectID = sqlitestore.StableProjectID
var StableManagedAgentID = sqlitestore.StableManagedAgentID
var StableManagedLoaderID = sqlitestore.StableManagedLoaderID
var StableManagedTriggerID = sqlitestore.StableManagedTriggerID
var StableProjectSchedulerID = sqlitestore.StableProjectSchedulerID
var StableProjectRunID = sqlitestore.StableProjectRunID
var stableReadableID = sqlitestore.StableReadableID
var normalizeLoader = sqlitestore.NormalizeLoader
var normalizeLoaderTrigger = sqlitestore.NormalizeLoaderTrigger
var llmProviderKeyName = driverpkg.LLMProviderKeyName
var loaderCronSpecJSON = loaderdomain.LoaderCronSpecJSON
var marshalJSONCompact = loaderdomain.MarshalJSONCompact
var NewRunCoordinator = rundomain.NewRunCoordinator
var projectRunStatusIsTerminal = rundomain.ProjectRunStatusIsTerminal
var hostSessionDir = execdomain.HostSessionDir
var toDriverProxyState = runtimedomain.ToDriverProxyState
var guestSessionHome = execdomain.GuestSessionHome
var shellQuote = execdomain.ShellQuote
var defaultLoaderCommandMaxOutputBytes = execdomain.DefaultLoaderCommandMaxOutputBytes
var runtimeEnvMap = execdomain.RuntimeEnvMap
var managedRuntimeEnvMap = execdomain.ManagedRuntimeEnvMap
var sessionEnvMap = modeldomain.SessionEnvMap
var sessionListOptionsFromProto = modeldomain.SessionListOptionsFromProto
var topicEventPayloadSHA256 = eventdomain.TopicEventPayloadSHA256
var validateTopicEventName = eventdomain.ValidateTopicEventName
var NewStore = filestore.NewStore
var NewStoreForConfig = filestore.NewStoreForConfig
var NewConfigStore = sqlitestore.NewConfigStore
var newLLMFacadeToken = sqlitestore.NewLLMFacadeTokenForTest
var NewRuntimeProvider = runtimedomain.NewRuntimeProvider
var NewDriver = sessiondomain.NewDriver
var NewExecutor = execdomain.NewExecutor
var NewExecutorForTest = execdomain.NewExecutorForTest
var NewLLMClient = llmdomain.NewLLMClient
var NewLLMClientForTest = llmdomain.NewClientForTest
var NewLoaderBus = loaderdomain.NewLoaderBus
var NewLoaderBusForTest = loaderdomain.NewLoaderBusForTest
var NewSessionStreamBroker = sessiondomain.NewSessionStreamBroker
var NewSessionStreamBrokerForTest = sessiondomain.NewSessionStreamBrokerForTest
var NewLoaderEngine = loaderdomain.NewLoaderEngine
var NewLoaderManager = loaderdomain.NewLoaderManager
var NewLoaderManagerForTest = loaderdomain.NewLoaderManagerForTest
var NewLoaderRunHostForTest = loaderdomain.NewLoaderRunHostForTest
var NewEventDispatcher = eventdomain.NewEventDispatcher
var NewCapProxyServer = filestore.NewCapProxyServer

const defaultAgentProvider = "codex"

var normalizeLLMWireAPI = sqlitestore.NormalizeLLMWireAPI
var normalizeLLMProviderType = sqlitestore.NormalizeLLMProviderType
var resolveRuntimeLLMTarget = sqlitestore.ResolveRuntimeLLMTarget
var applyLLMForwardHeaders = llmdomain.ApplyLLMForwardHeaders
var llmEndpointForProvider = sqlitestore.LLMEndpointForProvider

type dashboardNotifier interface {
	Notify(string)
}
