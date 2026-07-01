package storage

import (
	"strings"

	"agent-compose/pkg/model"
)

const (
	VMStatusPending = model.VMStatusPending
	VMStatusRunning = model.VMStatusRunning
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed

	SessionTypeManual = model.SessionTypeManual
	SessionTypeScript = model.SessionTypeScript

	CellTypeShell      = model.CellTypeShell
	CellTypeJavaScript = model.CellTypeJavaScript
	CellTypePython     = model.CellTypePython
	CellTypeAgent      = model.CellTypeAgent
)

const (
	LoaderRuntimeScheduler = model.LoaderRuntimeScheduler

	LoaderTriggerKindInterval = model.LoaderTriggerKindInterval
	LoaderTriggerKindEvent    = model.LoaderTriggerKindEvent
	LoaderTriggerKindTimeout  = model.LoaderTriggerKindTimeout
	LoaderTriggerKindCron     = model.LoaderTriggerKindCron

	LoaderSessionPolicySticky = model.LoaderSessionPolicySticky
	LoaderSessionPolicyNew    = model.LoaderSessionPolicyNew
	LoaderSessionPolicyReuse  = model.LoaderSessionPolicyReuse

	LoaderConcurrencyPolicySkip     = model.LoaderConcurrencyPolicySkip
	LoaderConcurrencyPolicyParallel = model.LoaderConcurrencyPolicyParallel

	LoaderRunStatusRunning   = model.LoaderRunStatusRunning
	LoaderRunStatusSucceeded = model.LoaderRunStatusSucceeded
	LoaderRunStatusFailed    = model.LoaderRunStatusFailed
	LoaderRunStatusSkipped   = model.LoaderRunStatusSkipped
)

const (
	TopicEventSourceWebhook = model.TopicEventSourceWebhook
	TopicEventSourceLoader  = model.TopicEventSourceLoader
	TopicEventSourceSystem  = model.TopicEventSourceSystem

	TopicEventDispatchPending        = model.TopicEventDispatchPending
	TopicEventDispatchPublishing     = model.TopicEventDispatchPublishing
	TopicEventDispatchPublishedToBus = model.TopicEventDispatchPublishedToBus
	TopicEventDispatchNoSubscriber   = model.TopicEventDispatchNoSubscriber
	TopicEventDispatchRetrying       = model.TopicEventDispatchRetrying
	TopicEventDispatchDeadLetter     = model.TopicEventDispatchDeadLetter

	EventDeliveryStatusMatched      = model.EventDeliveryStatusMatched
	EventDeliveryStatusRunStarted   = model.EventDeliveryStatusRunStarted
	EventDeliveryStatusRunSucceeded = model.EventDeliveryStatusRunSucceeded
	EventDeliveryStatusRunFailed    = model.EventDeliveryStatusRunFailed
	EventDeliveryStatusSkipped      = model.EventDeliveryStatusSkipped
)

const (
	LLMProviderFamilyOpenAI       = model.LLMProviderFamilyOpenAI
	LLMProviderFamilyAnthropic    = model.LLMProviderFamilyAnthropic
	LLMProviderScopeSystem        = model.LLMProviderScopeSystem
	LLMProviderScopeEnvDefault    = model.LLMProviderScopeEnvDefault
	LLMProviderScopeSessionEnv    = model.LLMProviderScopeSessionEnv
	LLMProviderIDDefaultOpenAI    = model.LLMProviderIDDefaultOpenAI
	LLMProviderIDDefaultAnthropic = model.LLMProviderIDDefaultAnthropic
)

type SessionTag = model.SessionTag
type SessionEnvVar = model.SessionEnvVar
type SessionSummary = model.SessionSummary
type SessionListOptions = model.SessionListOptions
type SessionListResult = model.SessionListResult
type SessionWorkspace = model.SessionWorkspace
type Session = model.Session
type WorkspaceConfig = model.WorkspaceConfig
type NotebookCell = model.NotebookCell
type AgentResumeInfo = model.AgentResumeInfo
type ExecChunk = model.ExecChunk
type SessionEvent = model.SessionEvent
type AgentRun = model.AgentRun
type ExecResult = model.ExecResult
type RuntimeCommandArtifacts = model.RuntimeCommandArtifacts
type RuntimeCommandResult = model.RuntimeCommandResult
type ExecStreamWriter = model.ExecStreamWriter
type VMState = model.VMState
type ProxyState = model.ProxyState
type ExecSpec = model.ExecSpec
type AgentRunResult = model.AgentRunResult
type SessionVMInfo = model.SessionVMInfo

type AgentDefinition = model.AgentDefinition
type AgentDefinitionListOptions = model.AgentDefinitionListOptions
type AgentDefinitionListResult = model.AgentDefinitionListResult
type AgentCurrentRunSummary = model.AgentCurrentRunSummary
type AgentLatestRunSummary = model.AgentLatestRunSummary

type LoaderSummary = model.LoaderSummary
type Loader = model.Loader
type LoaderTrigger = model.LoaderTrigger
type LoaderRunSummary = model.LoaderRunSummary
type LoaderEvent = model.LoaderEvent
type LoaderBinding = model.LoaderBinding
type LoaderAgentRequest = model.LoaderAgentRequest
type LoaderAgentResult = model.LoaderAgentResult
type LoaderCommandRequest = model.LoaderCommandRequest
type LoaderCommandResult = model.LoaderCommandResult
type LoaderLLMRequest = model.LoaderLLMRequest
type LoaderLLMResult = model.LoaderLLMResult
type LoaderTopicEvent = model.LoaderTopicEvent

type ProjectRunPreparation = model.ProjectRunPreparation
type ProjectRunSessionResult = model.ProjectRunSessionResult
type ProjectRunStartRequest = model.ProjectRunStartRequest
type ProjectRunTransitionRequest = model.ProjectRunTransitionRequest

type TopicEventRecord = model.TopicEventRecord
type TopicEventFilter = model.TopicEventFilter
type WebhookSource = model.WebhookSource
type EventDelivery = model.EventDelivery
type EventSessionLink = model.EventSessionLink
type EventSessionTraceItem = model.EventSessionTraceItem

type LLMProvider = model.LLMProvider
type LLMModel = model.LLMModel
type LLMResolvedTarget = model.LLMResolvedTarget
type LLMFacadeToken = model.LLMFacadeToken
type LLMGenerateResult = model.LLMGenerateResult

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeAgentKind(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "codex", "":
		return "codex"
	case "claude":
		return "claude"
	case "gemini":
		return "gemini"
	case "opencode", "open-code", "open_code":
		return "opencode"
	default:
		return ""
	}
}

func normalizeCapsetIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}
