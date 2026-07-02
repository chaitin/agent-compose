package sqlite

import (
	loadertypes "agent-compose/internal/loadertypes"
	modeldomain "agent-compose/internal/model"
	"encoding/json"
	"fmt"
	"net/url"
	pathpkg "path"
	"strings"
	"time"

	cronlib "github.com/robfig/cron/v3"
)

const (
	llmAPIProtocolResponses       = "responses"
	llmAPIProtocolChatCompletions = "chat_completions"
	llmAPIProtocolMessages        = "messages"
)

func normalizeJSONDocument(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "{}", nil
	}
	var parsed any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", err
	}
	normalized, err := json.Marshal(parsed)
	if err != nil {
		return "", fmt.Errorf("marshal normalized json: %w", err)
	}
	return string(normalized), nil
}

func marshalJSONCompact(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func nonZeroTimeUnixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

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
	case "", loadertypes.LoaderSessionPolicySticky, loadertypes.LoaderSessionPolicyReuse:
		return loadertypes.LoaderSessionPolicySticky
	case loadertypes.LoaderSessionPolicyNew:
		return loadertypes.LoaderSessionPolicyNew
	default:
		return loadertypes.LoaderSessionPolicySticky
	}
}

func normalizeLoaderConcurrencyPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", loadertypes.LoaderConcurrencyPolicySkip:
		return loadertypes.LoaderConcurrencyPolicySkip
	case loadertypes.LoaderConcurrencyPolicyParallel, "allow":
		return loadertypes.LoaderConcurrencyPolicyParallel
	default:
		return loadertypes.LoaderConcurrencyPolicySkip
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultLoaderName(now time.Time) string {
	return "Loader " + now.UTC().Format("2006-01-02 15:04")
}

func normalizeAgentKind(agent string) string {
	switch strings.ToLower(strings.TrimSpace(agent)) {
	case "", "codex":
		return "codex"
	case "claude":
		return "claude"
	case "gemini":
		return "gemini"
	case "opencode":
		return "opencode"
	default:
		return strings.ToLower(strings.TrimSpace(agent))
	}
}

var normalizeCapsetIDs = modeldomain.NormalizeCapsetIDs

func timeIsSet(value time.Time) bool {
	return !value.IsZero()
}

func loaderTriggerScheduledAt(now time.Time, delayMs int64) time.Time {
	if delayMs <= 0 {
		return time.Time{}
	}
	return now.UTC().Add(time.Duration(delayMs) * time.Millisecond)
}

func loaderTriggerUsesSchedule(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case LoaderTriggerKindInterval, LoaderTriggerKindTimeout, LoaderTriggerKindCron:
		return true
	default:
		return false
	}
}

func loaderTriggerNextFireAt(now time.Time, trigger LoaderTrigger, fired bool) (time.Time, error) {
	now = now.UTC()
	switch strings.ToLower(strings.TrimSpace(trigger.Kind)) {
	case LoaderTriggerKindInterval:
		return loaderTriggerScheduledAt(now, trigger.IntervalMs), nil
	case LoaderTriggerKindTimeout:
		if fired {
			return time.Time{}, nil
		}
		return loaderTriggerScheduledAt(now, trigger.IntervalMs), nil
	case LoaderTriggerKindCron:
		spec, err := parseLoaderCronSpecJSON(trigger.SpecJSON)
		if err != nil {
			return time.Time{}, err
		}
		location, err := time.LoadLocation(spec.Timezone)
		if err != nil {
			return time.Time{}, fmt.Errorf("load cron timezone %q: %w", spec.Timezone, err)
		}
		schedule, err := loaderCronParser.Parse(spec.Expr)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse cron expression %q: %w", spec.Expr, err)
		}
		return schedule.Next(now.In(location)).UTC(), nil
	default:
		return time.Time{}, nil
	}
}

func normalizeProjectRunSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "", ProjectRunSourceManual:
		return ProjectRunSourceManual
	case ProjectRunSourceScheduler:
		return ProjectRunSourceScheduler
	case ProjectRunSourceAPI:
		return ProjectRunSourceAPI
	default:
		return strings.ToLower(strings.TrimSpace(source))
	}
}

const loaderDefaultCronTimezone = "UTC"

type loaderCronSpec struct {
	Kind     string `json:"kind,omitempty"`
	Expr     string `json:"expr"`
	Timezone string `json:"timezone,omitempty"`
}

var loaderCronParser = cronlib.NewParser(cronlib.SecondOptional | cronlib.Minute | cronlib.Hour | cronlib.Dom | cronlib.Month | cronlib.Dow | cronlib.Descriptor)

func normalizeLoaderCronSpecJSON(raw string) (string, error) {
	spec, err := parseLoaderCronSpecJSON(raw)
	if err != nil {
		return "", err
	}
	return marshalJSONCompact(spec)
}

func parseLoaderCronSpecJSON(raw string) (loaderCronSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return loaderCronSpec{}, fmt.Errorf("cron spec is required")
	}
	var spec loaderCronSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return loaderCronSpec{}, fmt.Errorf("decode cron spec: %w", err)
	}
	return normalizeLoaderCronSpec(spec)
}

func normalizeLoaderCronSpec(spec loaderCronSpec) (loaderCronSpec, error) {
	spec.Kind = LoaderTriggerKindCron
	spec.Expr = strings.TrimSpace(spec.Expr)
	spec.Timezone = strings.TrimSpace(spec.Timezone)
	if spec.Expr == "" {
		return loaderCronSpec{}, fmt.Errorf("cron expr is required")
	}
	if spec.Timezone == "" {
		spec.Timezone = loaderDefaultCronTimezone
	}
	if _, err := time.LoadLocation(spec.Timezone); err != nil {
		return loaderCronSpec{}, fmt.Errorf("load cron timezone %q: %w", spec.Timezone, err)
	}
	if _, err := loaderCronParser.Parse(spec.Expr); err != nil {
		return loaderCronSpec{}, fmt.Errorf("parse cron expression %q: %w", spec.Expr, err)
	}
	return spec, nil
}

func normalizeLLMAPIEndpointForProtocol(raw, protocol string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if protocol == llmAPIProtocolChatCompletions && (strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/") {
		parsed.Path = "/v1/chat/completions"
		return parsed.String()
	}
	cleanPath := strings.TrimRight(parsed.Path, "/")
	if protocol == llmAPIProtocolChatCompletions && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
		parsed.Path = pathpkg.Join(parsed.Path, "/chat/completions")
		return parsed.String()
	}
	if protocol == llmAPIProtocolChatCompletions && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/chat/completions")
		return parsed.String()
	}
	if protocol == llmAPIProtocolResponses && strings.HasSuffix(cleanPath, "/openai") {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
		return parsed.String()
	}
	if protocol == llmAPIProtocolResponses && (cleanPath == "/v1" || strings.HasSuffix(cleanPath, "/openai/v1")) {
		parsed.Path = pathpkg.Join(parsed.Path, "/responses")
		return parsed.String()
	}
	if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
		parsed.Path = pathpkg.Join(parsed.Path, "/v1/responses")
	}
	return parsed.String()
}
