package loader

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	RuntimeScheduler = "scheduler"

	TriggerKindInterval = "interval"
	TriggerKindEvent    = "event"
	TriggerKindTimeout  = "timeout"
	TriggerKindCron     = "cron"

	SessionPolicySticky = "sticky"
	SessionPolicyNew    = "new"
	SessionPolicyReuse  = "reuse"

	ConcurrencyPolicySkip     = "skip"
	ConcurrencyPolicyParallel = "parallel"

	RunStatusRunning   = "running"
	RunStatusSucceeded = "succeeded"
	RunStatusFailed    = "failed"
	RunStatusSkipped   = "skipped"
)

type EnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type Summary struct {
	ID                 string    `json:"id"`
	Name               string    `json:"name"`
	Description        string    `json:"description,omitempty"`
	Enabled            bool      `json:"enabled"`
	Runtime            string    `json:"runtime"`
	WorkspaceID        string    `json:"workspace_id,omitempty"`
	AgentID            string    `json:"agent_id,omitempty"`
	Driver             string    `json:"driver,omitempty"`
	GuestImage         string    `json:"guest_image,omitempty"`
	DefaultAgent       string    `json:"default_agent,omitempty"`
	SessionPolicy      string    `json:"session_policy,omitempty"`
	ConcurrencyPolicy  string    `json:"concurrency_policy,omitempty"`
	CapsetIDs          []string  `json:"capset_ids,omitempty"`
	ManagedProjectID   string    `json:"managed_project_id,omitempty"`
	ManagedRevision    int64     `json:"managed_project_revision,omitempty"`
	ManagedAgentName   string    `json:"managed_agent_name,omitempty"`
	ManagedSchedulerID string    `json:"managed_scheduler_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	LastError          string    `json:"last_error,omitempty"`
	TriggerCount       int       `json:"trigger_count"`
	RunCount           int       `json:"run_count"`
	EventCount         int       `json:"event_count"`
	LatestRunAt        time.Time `json:"latest_run_at,omitempty"`
}

type Definition struct {
	Summary  Summary   `json:"summary"`
	Script   string    `json:"script"`
	Triggers []Trigger `json:"triggers,omitempty"`
	EnvItems []EnvVar  `json:"env_items,omitempty"`
}

type Trigger struct {
	LoaderID    string    `json:"loader_id"`
	ID          string    `json:"id"`
	Kind        string    `json:"kind"`
	Topic       string    `json:"topic,omitempty"`
	IntervalMs  int64     `json:"interval_ms,omitempty"`
	Enabled     bool      `json:"enabled"`
	AutoID      bool      `json:"auto_id,omitempty"`
	SpecJSON    string    `json:"spec_json,omitempty"`
	NextFireAt  time.Time `json:"next_fire_at,omitempty"`
	LastFiredAt time.Time `json:"last_fired_at,omitempty"`
}

type RunSummary struct {
	ID               string    `json:"id"`
	LoaderID         string    `json:"loader_id"`
	TriggerID        string    `json:"trigger_id,omitempty"`
	TriggerKind      string    `json:"trigger_kind,omitempty"`
	TriggerSource    string    `json:"trigger_source,omitempty"`
	Status           string    `json:"status"`
	StartedAt        time.Time `json:"started_at"`
	CompletedAt      time.Time `json:"completed_at,omitempty"`
	DurationMs       int64     `json:"duration_ms,omitempty"`
	Error            string    `json:"error,omitempty"`
	ResultJSON       string    `json:"result_json,omitempty"`
	PayloadJSON      string    `json:"payload_json,omitempty"`
	SourceScriptHash string    `json:"source_script_sha256,omitempty"`
	ArtifactsDir     string    `json:"artifacts_dir,omitempty"`
}

type Event struct {
	ID                   string    `json:"id"`
	LoaderID             string    `json:"loader_id"`
	RunID                string    `json:"run_id,omitempty"`
	TriggerID            string    `json:"trigger_id,omitempty"`
	Type                 string    `json:"type"`
	Level                string    `json:"level"`
	Message              string    `json:"message"`
	PayloadJSON          string    `json:"payload_json,omitempty"`
	LinkedSessionID      string    `json:"linked_session_id,omitempty"`
	LinkedCellID         string    `json:"linked_cell_id,omitempty"`
	LinkedAgentSessionID string    `json:"linked_agent_session_id,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

type Binding struct {
	LoaderID  string    `json:"loader_id"`
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type AgentRequest struct {
	Agent         string        `json:"agent,omitempty"`
	SessionPolicy string        `json:"sessionPolicy,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
	Title         string        `json:"title,omitempty"`
	Driver        string        `json:"driver,omitempty"`
	GuestImage    string        `json:"guestImage,omitempty"`
	WorkspaceID   string        `json:"workspaceId,omitempty"`
	SessionEnv    []EnvVar      `json:"sessionEnv,omitempty"`
	OutputSchema  string        `json:"outputSchema,omitempty"`
}

type AgentResult struct {
	Text           string `json:"text,omitempty"`
	Output         string `json:"output,omitempty"`
	FinalText      string `json:"finalText,omitempty"`
	JSON           any    `json:"json"`
	SessionID      string `json:"sessionId,omitempty"`
	CellID         string `json:"cellId,omitempty"`
	Agent          string `json:"agent,omitempty"`
	AgentSessionID string `json:"agentSessionId,omitempty"`
	StopReason     string `json:"stopReason,omitempty"`
	Success        bool   `json:"success"`
	ExitCode       int    `json:"exitCode"`
}

type CommandRequest struct {
	Mode           string            `json:"mode"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Script         string            `json:"script,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutMs      int64             `json:"timeoutMs,omitempty"`
	MaxOutputBytes int64             `json:"maxOutputBytes,omitempty"`
	SessionPolicy  string            `json:"sessionPolicy,omitempty"`
	Title          string            `json:"title,omitempty"`
	Driver         string            `json:"driver,omitempty"`
	GuestImage     string            `json:"guestImage,omitempty"`
	WorkspaceID    string            `json:"workspaceId,omitempty"`
	SessionEnv     []EnvVar          `json:"sessionEnv,omitempty"`
}

type CommandResult struct {
	Stdout          string            `json:"stdout"`
	Stderr          string            `json:"stderr"`
	Output          string            `json:"output"`
	ExitCode        int               `json:"exitCode"`
	Success         bool              `json:"success"`
	StdoutTruncated bool              `json:"stdoutTruncated,omitempty"`
	StderrTruncated bool              `json:"stderrTruncated,omitempty"`
	OutputTruncated bool              `json:"outputTruncated,omitempty"`
	SessionID       string            `json:"sessionId,omitempty"`
	CellID          string            `json:"cellId,omitempty"`
	Artifacts       map[string]string `json:"artifacts,omitempty"`
}

type LLMRequest struct {
	Model        string `json:"model,omitempty"`
	OutputSchema string `json:"outputSchema,omitempty"`
}

type LLMResult struct {
	Text         string `json:"text,omitempty"`
	Model        string `json:"model,omitempty"`
	ResponseID   string `json:"responseId,omitempty"`
	FinishReason string `json:"finishReason,omitempty"`
	JSON         any    `json:"json"`
}

type TopicEvent struct {
	EventID         string                                         `json:"event_id,omitempty"`
	Topic           string                                         `json:"topic"`
	Source          string                                         `json:"source,omitempty"`
	Provider        string                                         `json:"provider,omitempty"`
	Payload         map[string]any                                 `json:"payload,omitempty"`
	CreatedAt       time.Time                                      `json:"created_at"`
	Ack             func(context.Context) error                    `json:"-"`
	NoSubscriberAck func(context.Context) error                    `json:"-"`
	Retry           func(context.Context, string, time.Time) error `json:"-"`
	Release         func()                                         `json:"-"`
}

func NormalizeRuntime(runtime string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "", RuntimeScheduler:
		return RuntimeScheduler, nil
	default:
		return "", fmt.Errorf("unsupported loader runtime %q", runtime)
	}
}

func NormalizeTriggerKind(kind string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case TriggerKindInterval:
		return TriggerKindInterval, nil
	case TriggerKindEvent:
		return TriggerKindEvent, nil
	case TriggerKindTimeout:
		return TriggerKindTimeout, nil
	case TriggerKindCron:
		return TriggerKindCron, nil
	default:
		return "", fmt.Errorf("unsupported loader trigger kind %q", kind)
	}
}

func NormalizeSessionPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", SessionPolicySticky, SessionPolicyReuse:
		return SessionPolicySticky
	case SessionPolicyNew:
		return SessionPolicyNew
	default:
		return SessionPolicySticky
	}
}

func NormalizeConcurrencyPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", ConcurrencyPolicySkip:
		return ConcurrencyPolicySkip
	case ConcurrencyPolicyParallel, "allow":
		return ConcurrencyPolicyParallel
	default:
		return ConcurrencyPolicySkip
	}
}

func NormalizeRunStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case RunStatusRunning:
		return RunStatusRunning
	case RunStatusSucceeded:
		return RunStatusSucceeded
	case RunStatusFailed:
		return RunStatusFailed
	case RunStatusSkipped:
		return RunStatusSkipped
	default:
		return RunStatusRunning
	}
}

func NormalizeAgentKind(agent string) string {
	agent = strings.ToLower(strings.TrimSpace(agent))
	switch agent {
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
		return agent
	}
}

func NormalizeEnvItems(items []EnvVar) []EnvVar {
	if len(items) == 0 {
		return nil
	}
	merged := make(map[string]EnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		item.Name = name
		merged[name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]EnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func TriggerStableID(kind, topic string, intervalMs int64, callbackSource string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%d", kind, topic, intervalMs, callbackSource, index)))
	return "auto-" + hex.EncodeToString(h[:6])
}

func SourceSHA(script string) string {
	h := sha256.Sum256([]byte(script))
	return hex.EncodeToString(h[:])
}

func TriggerTopicMatches(pattern, topic string) bool {
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

func TriggerUsesSchedule(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case TriggerKindInterval, TriggerKindTimeout, TriggerKindCron:
		return true
	default:
		return false
	}
}

func TriggerScheduledAt(now time.Time, delayMs int64) time.Time {
	if delayMs <= 0 {
		return time.Time{}
	}
	return now.UTC().Add(time.Duration(delayMs) * time.Millisecond)
}

func TimeIsSet(value time.Time) bool {
	return !value.IsZero()
}

func NonZeroTimeUnixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}
