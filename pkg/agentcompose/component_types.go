package agentcompose

import (
	"agent-compose/pkg/images"
	"agent-compose/pkg/loaders"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/storage"
	"agent-compose/pkg/workspaces"
)

type Store = storage.Store
type ConfigStore = storage.ConfigStore
type CapabilityGatewaySettings = storage.CapabilityGatewaySettings
type ProjectRecord = storage.ProjectRecord
type ProjectRevisionRecord = storage.ProjectRevisionRecord
type ProjectAgentRecord = storage.ProjectAgentRecord
type ProjectSchedulerRecord = storage.ProjectSchedulerRecord
type ProjectRunRecord = storage.ProjectRunRecord
type ProjectListOptions = storage.ProjectListOptions
type ProjectRunListOptions = storage.ProjectRunListOptions
type ProjectListResult = storage.ProjectListResult
type ProjectSessionRelationFilter = storage.ProjectSessionRelationFilter
type ProjectSessionStatus = storage.ProjectSessionStatus

const (
	ProjectRunStatusPending   = storage.ProjectRunStatusPending
	ProjectRunStatusRunning   = storage.ProjectRunStatusRunning
	ProjectRunStatusSucceeded = storage.ProjectRunStatusSucceeded
	ProjectRunStatusFailed    = storage.ProjectRunStatusFailed
	ProjectRunStatusCanceled  = storage.ProjectRunStatusCanceled

	ProjectRunSourceManual    = storage.ProjectRunSourceManual
	ProjectRunSourceScheduler = storage.ProjectRunSourceScheduler
	ProjectRunSourceAPI       = storage.ProjectRunSourceAPI

	storedUnixMillisecondThreshold = storage.StoredUnixMillisecondThreshold
	llmFacadeTokenRetention        = storage.LLMFacadeTokenRetention
	defaultSessionListLimit        = storage.DefaultSessionListLimit
)

type LoaderSummary = loaders.LoaderSummary
type Loader = loaders.Loader
type LoaderTrigger = loaders.LoaderTrigger
type LoaderRunSummary = loaders.LoaderRunSummary
type LoaderEvent = loaders.LoaderEvent
type LoaderBinding = loaders.LoaderBinding
type LoaderAgentRequest = loaders.LoaderAgentRequest
type LoaderAgentResult = loaders.LoaderAgentResult
type LoaderCommandRequest = loaders.LoaderCommandRequest
type LoaderCommandResult = loaders.LoaderCommandResult
type LoaderLLMRequest = loaders.LoaderLLMRequest
type LoaderLLMResult = loaders.LoaderLLMResult
type LoaderTopicEvent = loaders.LoaderTopicEvent
type LoaderHost = loaders.LoaderHost
type LoaderValidationResult = loaders.LoaderValidationResult
type LoaderExecutionRequest = loaders.LoaderExecutionRequest
type LoaderExecutionResult = loaders.LoaderExecutionResult
type LoaderEngine = loaders.LoaderEngine
type QJSLoaderEngine = loaders.QJSLoaderEngine
type LoaderManager = loaders.LoaderManager
type LoaderService = loaders.Service

const (
	LoaderRuntimeScheduler = loaders.LoaderRuntimeScheduler

	LoaderTriggerKindInterval = loaders.LoaderTriggerKindInterval
	LoaderTriggerKindEvent    = loaders.LoaderTriggerKindEvent
	LoaderTriggerKindTimeout  = loaders.LoaderTriggerKindTimeout
	LoaderTriggerKindCron     = loaders.LoaderTriggerKindCron

	LoaderSessionPolicySticky = loaders.LoaderSessionPolicySticky
	LoaderSessionPolicyNew    = loaders.LoaderSessionPolicyNew
	LoaderSessionPolicyReuse  = loaders.LoaderSessionPolicyReuse

	LoaderConcurrencyPolicySkip     = loaders.LoaderConcurrencyPolicySkip
	LoaderConcurrencyPolicyParallel = loaders.LoaderConcurrencyPolicyParallel

	LoaderRunStatusRunning   = loaders.LoaderRunStatusRunning
	LoaderRunStatusSucceeded = loaders.LoaderRunStatusSucceeded
	LoaderRunStatusFailed    = loaders.LoaderRunStatusFailed
	LoaderRunStatusSkipped   = loaders.LoaderRunStatusSkipped
)

type ProjectService = projects.Service
type ProjectRunStartRequest = projects.ProjectRunStartRequest
type ProjectRunTransitionRequest = projects.ProjectRunTransitionRequest
type ProjectRunPreparation = projects.ProjectRunPreparation
type ProjectRunSessionResult = projects.ProjectRunSessionResult
type RunCoordinator = projects.RunCoordinator

type ImageBackend = images.ImageBackend
type ImageListRequest = images.ImageListRequest
type ImageListResult = images.ImageListResult
type ImagePullRequest = images.ImagePullRequest
type ImagePullResult = images.ImagePullResult
type ImageInspectRequest = images.ImageInspectRequest
type ImageInspectResult = images.ImageInspectResult
type ImageRemoveRequest = images.ImageRemoveRequest
type ImageRemoveResult = images.ImageRemoveResult
type DockerImageBackend = images.DockerImageBackend
type OCIImageBackend = images.OCIImageBackend
type AutoImageBackend = images.AutoImageBackend
type DockerPingFunc = images.DockerPingFunc

type WorkspaceService = workspaces.Service
