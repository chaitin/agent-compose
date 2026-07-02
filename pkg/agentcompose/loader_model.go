package agentcompose

import (
	"strings"
	"time"

	loaderdomain "agent-compose/internal/agentcompose/loader"
)

const (
	LoaderRuntimeScheduler = loaderdomain.RuntimeScheduler

	LoaderTriggerKindInterval = loaderdomain.TriggerKindInterval
	LoaderTriggerKindEvent    = loaderdomain.TriggerKindEvent
	LoaderTriggerKindTimeout  = loaderdomain.TriggerKindTimeout
	LoaderTriggerKindCron     = loaderdomain.TriggerKindCron

	LoaderSessionPolicySticky = loaderdomain.SessionPolicySticky
	LoaderSessionPolicyNew    = loaderdomain.SessionPolicyNew
	LoaderSessionPolicyReuse  = loaderdomain.SessionPolicyReuse

	LoaderConcurrencyPolicySkip     = loaderdomain.ConcurrencyPolicySkip
	LoaderConcurrencyPolicyParallel = loaderdomain.ConcurrencyPolicyParallel

	LoaderRunStatusRunning   = loaderdomain.RunStatusRunning
	LoaderRunStatusSucceeded = loaderdomain.RunStatusSucceeded
	LoaderRunStatusFailed    = loaderdomain.RunStatusFailed
	LoaderRunStatusSkipped   = loaderdomain.RunStatusSkipped
)

type LoaderSummary = loaderdomain.Summary
type Loader = loaderdomain.Definition
type LoaderTrigger = loaderdomain.Trigger
type LoaderRunSummary = loaderdomain.RunSummary
type LoaderEvent = loaderdomain.Event
type LoaderBinding = loaderdomain.Binding
type LoaderAgentRequest = loaderdomain.AgentRequest
type LoaderAgentResult = loaderdomain.AgentResult
type LoaderCommandRequest = loaderdomain.CommandRequest
type LoaderCommandResult = loaderdomain.CommandResult
type LoaderLLMRequest = loaderdomain.LLMRequest
type LoaderLLMResult = loaderdomain.LLMResult
type LoaderTopicEvent = loaderdomain.TopicEvent

func normalizeLoaderRuntime(runtime string) (string, error) {
	return loaderdomain.NormalizeRuntime(runtime)
}

func normalizeLoaderTriggerKind(kind string) (string, error) {
	return loaderdomain.NormalizeTriggerKind(kind)
}

func normalizeLoaderSessionPolicy(policy string) string {
	return loaderdomain.NormalizeSessionPolicy(policy)
}

func normalizeLoaderConcurrencyPolicy(policy string) string {
	return loaderdomain.NormalizeConcurrencyPolicy(policy)
}

func normalizeLoaderRunStatus(status string) string {
	return loaderdomain.NormalizeRunStatus(status)
}

func loaderTriggerStableID(kind, topic string, intervalMs int64, callbackSource string, index int) string {
	return loaderdomain.TriggerStableID(kind, topic, intervalMs, callbackSource, index)
}

func loaderSourceSHA(script string) string {
	return loaderdomain.SourceSHA(script)
}

func loaderTriggerTopicMatches(pattern, topic string) bool {
	return loaderdomain.TriggerTopicMatches(pattern, topic)
}

func timeIsSet(value time.Time) bool {
	return loaderdomain.TimeIsSet(value)
}

func nonZeroTimeUnixMilli(value time.Time) int64 {
	return loaderdomain.NonZeroTimeUnixMilli(value)
}

func loaderTriggerUsesSchedule(kind string) bool {
	return loaderdomain.TriggerUsesSchedule(kind)
}

func loaderTriggerScheduledAt(now time.Time, delayMs int64) time.Time {
	return loaderdomain.TriggerScheduledAt(now, delayMs)
}

func defaultLoaderName(now time.Time) string {
	return "Loader " + now.UTC().Format("2006-01-02 15:04")
}

func defaultLoaderScript() string {
	return strings.TrimSpace(`function main(payload) {
  const result = {
    status: "ready",
    now: new Date().toISOString(),
    payload: payload ?? null,
  };
  scheduler.log("loader ready", result);
  return result;
}

scheduler.interval("heartbeat", function heartbeat() {
  scheduler.log("heartbeat", { at: new Date().toISOString() });
}, 60000);

scheduler.on("agent-compose.session.created", "on-session-created", function onSession(event) {
  scheduler.log("session created", event);
});
`)
}
