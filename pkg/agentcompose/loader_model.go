package agentcompose

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"agent-compose/pkg/bus"
	"agent-compose/pkg/model"
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
type LoaderTopicEvent = bus.LoaderTopicEvent

func normalizeLoaderRuntime(runtime string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "", LoaderRuntimeScheduler:
		return LoaderRuntimeScheduler, nil
	default:
		return "", fmt.Errorf("unsupported loader runtime %q", runtime)
	}
}

func normalizeLoaderTriggerKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case LoaderTriggerKindInterval:
		return LoaderTriggerKindInterval, nil
	case LoaderTriggerKindEvent:
		return LoaderTriggerKindEvent, nil
	case LoaderTriggerKindTimeout:
		return LoaderTriggerKindTimeout, nil
	case LoaderTriggerKindCron:
		return LoaderTriggerKindCron, nil
	default:
		return "", fmt.Errorf("unsupported loader trigger kind %q", kind)
	}
}

func normalizeLoaderSessionPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", LoaderSessionPolicySticky, LoaderSessionPolicyReuse:
		return LoaderSessionPolicySticky
	case LoaderSessionPolicyNew:
		return LoaderSessionPolicyNew
	default:
		return LoaderSessionPolicySticky
	}
}

func normalizeLoaderConcurrencyPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", LoaderConcurrencyPolicySkip:
		return LoaderConcurrencyPolicySkip
	case LoaderConcurrencyPolicyParallel, "allow":
		return LoaderConcurrencyPolicyParallel
	default:
		return LoaderConcurrencyPolicySkip
	}
}

func normalizeLoaderRunStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case LoaderRunStatusRunning:
		return LoaderRunStatusRunning
	case LoaderRunStatusSucceeded:
		return LoaderRunStatusSucceeded
	case LoaderRunStatusFailed:
		return LoaderRunStatusFailed
	case LoaderRunStatusSkipped:
		return LoaderRunStatusSkipped
	default:
		return LoaderRunStatusRunning
	}
}

func loaderTriggerStableID(kind, topic string, intervalMs int64, callbackSource string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%d", kind, topic, intervalMs, callbackSource, index)))
	return "auto-" + hex.EncodeToString(h[:6])
}

func loaderSourceSHA(script string) string {
	h := sha256.Sum256([]byte(script))
	return hex.EncodeToString(h[:])
}

func loaderTriggerTopicMatches(pattern, topic string) bool {
	pattern = strings.TrimSpace(pattern)
	topic = strings.TrimSpace(topic)
	if pattern == "" || topic == "" {
		return false
	}
	if pattern == topic {
		return true
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
	}
	return false
}

func timeIsSet(value time.Time) bool {
	return !value.IsZero()
}

func nonZeroTimeUnixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

func loaderTriggerUsesSchedule(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case LoaderTriggerKindInterval, LoaderTriggerKindTimeout, LoaderTriggerKindCron:
		return true
	default:
		return false
	}
}

func loaderTriggerScheduledAt(now time.Time, delayMs int64) time.Time {
	if delayMs <= 0 {
		return time.Time{}
	}
	return now.UTC().Add(time.Duration(delayMs) * time.Millisecond)
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
