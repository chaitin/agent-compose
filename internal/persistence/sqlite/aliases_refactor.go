package sqlite

import (
	agentdomain "agent-compose/internal/agent"
	eventdomain "agent-compose/internal/event"
	loadertypes "agent-compose/internal/loadertypes"
	modeldomain "agent-compose/internal/model"
	filestore "agent-compose/internal/persistence/filestore"
	llmdomain "agent-compose/pkg/llm"
)

type SessionEnvVar = modeldomain.SessionEnvVar
type SessionTag = modeldomain.SessionTag
type WorkspaceConfig = modeldomain.WorkspaceConfig
type Session = modeldomain.Session
type CapabilityGatewaySettings = modeldomain.CapabilityGatewaySettings
type Store = filestore.Store

const SessionTypeManual = modeldomain.SessionTypeManual

type AgentDefinition = agentdomain.AgentDefinition
type AgentDefinitionListOptions = agentdomain.AgentDefinitionListOptions
type AgentDefinitionListResult = agentdomain.AgentDefinitionListResult

type LoaderSummary = loadertypes.LoaderSummary
type Loader = loadertypes.Loader
type LoaderTrigger = loadertypes.LoaderTrigger
type LoaderRunSummary = loadertypes.LoaderRunSummary
type LoaderEvent = loadertypes.LoaderEvent
type LoaderBinding = loadertypes.LoaderBinding

type TopicEventRecord = eventdomain.TopicEventRecord
type TopicEventFilter = eventdomain.TopicEventFilter
type EventDelivery = eventdomain.EventDelivery
type EventSessionLink = eventdomain.EventSessionLink
type EventSessionTraceItem = eventdomain.EventSessionTraceItem
type WebhookSource = eventdomain.WebhookSource

type LLMProvider = llmdomain.LLMProvider
type LLMModel = llmdomain.LLMModel
type LLMResolvedTarget = llmdomain.LLMResolvedTarget
type LLMFacadeToken = llmdomain.LLMFacadeToken

var normalizeAgentDefinition = agentdomain.NormalizeAgentDefinition
var validateTopicEventName = eventdomain.ValidateTopicEventName
var normalizeTopicEventSource = eventdomain.NormalizeTopicEventSource
var normalizeTopicEventDispatchStatus = eventdomain.NormalizeTopicEventDispatchStatus
var normalizeEventDeliveryStatus = eventdomain.NormalizeEventDeliveryStatus
var topicEventPayloadSHA256 = eventdomain.TopicEventPayloadSHA256

const (
	LoaderRuntimeScheduler    = loadertypes.LoaderRuntimeScheduler
	LoaderTriggerKindInterval = loadertypes.LoaderTriggerKindInterval
	LoaderTriggerKindEvent    = loadertypes.LoaderTriggerKindEvent
	LoaderTriggerKindTimeout  = loadertypes.LoaderTriggerKindTimeout
	LoaderTriggerKindCron     = loadertypes.LoaderTriggerKindCron
	LoaderRunStatusRunning    = loadertypes.LoaderRunStatusRunning
	LoaderRunStatusSucceeded  = loadertypes.LoaderRunStatusSucceeded
	LoaderRunStatusFailed     = loadertypes.LoaderRunStatusFailed
	LoaderRunStatusSkipped    = loadertypes.LoaderRunStatusSkipped

	TopicEventDispatchPending        = eventdomain.TopicEventDispatchPending
	TopicEventDispatchPublishing     = eventdomain.TopicEventDispatchPublishing
	TopicEventDispatchPublishedToBus = eventdomain.TopicEventDispatchPublishedToBus
	TopicEventDispatchNoSubscriber   = eventdomain.TopicEventDispatchNoSubscriber
	TopicEventDispatchRetrying       = eventdomain.TopicEventDispatchRetrying
	TopicEventDispatchDeadLetter     = eventdomain.TopicEventDispatchDeadLetter
	EventDeliveryStatusMatched       = eventdomain.EventDeliveryStatusMatched
)
