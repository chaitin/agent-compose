package domain

import (
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

func NormalizeLoaderRuntime(runtime string) (string, error) {
	return loaderdomain.NormalizeRuntime(runtime)
}

func NormalizeLoaderTriggerKind(kind string) (string, error) {
	return loaderdomain.NormalizeTriggerKind(kind)
}

func NormalizeLoaderSessionPolicy(policy string) string {
	return loaderdomain.NormalizeSessionPolicy(policy)
}

func NormalizeLoaderConcurrencyPolicy(policy string) string {
	return loaderdomain.NormalizeConcurrencyPolicy(policy)
}

func NormalizeLoaderRunStatus(status string) string {
	return loaderdomain.NormalizeRunStatus(status)
}

func LoaderTriggerStableID(kind, topic string, intervalMs int64, callbackSource string, index int) string {
	return loaderdomain.TriggerStableID(kind, topic, intervalMs, callbackSource, index)
}

func LoaderSourceSHA(script string) string {
	return loaderdomain.SourceSHA(script)
}

func LoaderTriggerTopicMatches(pattern, topic string) bool {
	return loaderdomain.TriggerTopicMatches(pattern, topic)
}

func LoaderTriggerUsesSchedule(kind string) bool {
	return loaderdomain.TriggerUsesSchedule(kind)
}

func LoaderTriggerScheduledAt(now time.Time, delayMs int64) time.Time {
	return loaderdomain.TriggerScheduledAt(now, delayMs)
}
