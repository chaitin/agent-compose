package model

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

const (
	LoaderRuntimeScheduler = "scheduler"

	LoaderTriggerKindInterval = "interval"
	LoaderTriggerKindEvent    = "event"
	LoaderTriggerKindTimeout  = "timeout"
	LoaderTriggerKindCron     = "cron"

	LoaderSessionPolicySticky = "sticky"
	LoaderSessionPolicyNew    = "new"
	LoaderSessionPolicyReuse  = "reuse"

	LoaderConcurrencyPolicySkip     = "skip"
	LoaderConcurrencyPolicyParallel = "parallel"

	LoaderRunStatusRunning   = "running"
	LoaderRunStatusSucceeded = "succeeded"
	LoaderRunStatusFailed    = "failed"
	LoaderRunStatusSkipped   = "skipped"
)

type LoaderSummary struct {
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

type Loader struct {
	Summary  LoaderSummary     `json:"summary"`
	Script   string            `json:"script"`
	Triggers []LoaderTrigger   `json:"triggers,omitempty"`
	EnvItems []SessionEnvVar   `json:"env_items,omitempty"`
	Volumes  []VolumeMountSpec `json:"volumes,omitempty"`
}

type LoaderTrigger struct {
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

type LoaderRunSummary struct {
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

type LoaderEvent struct {
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

type LoaderBinding struct {
	LoaderID  string    `json:"loader_id"`
	SessionID string    `json:"session_id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type LoaderAgentRequest struct {
	Agent         string            `json:"agent,omitempty"`
	SessionPolicy string            `json:"sessionPolicy,omitempty"`
	Timeout       time.Duration     `json:"timeout,omitempty"`
	Title         string            `json:"title,omitempty"`
	Driver        string            `json:"driver,omitempty"`
	GuestImage    string            `json:"guestImage,omitempty"`
	WorkspaceID   string            `json:"workspaceId,omitempty"`
	SessionEnv    []SessionEnvVar   `json:"sessionEnv,omitempty"`
	Volumes       []VolumeMountSpec `json:"volumes,omitempty"`
	OutputSchema  string            `json:"outputSchema,omitempty"`
}

type LoaderAgentResult struct {
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

type LoaderCommandRequest struct {
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
	SessionEnv     []SessionEnvVar   `json:"sessionEnv,omitempty"`
	Volumes        []VolumeMountSpec `json:"volumes,omitempty"`
}

type LoaderCommandResult struct {
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

type LoaderLLMRequest struct {
	Model        string `json:"model,omitempty"`
	OutputSchema string `json:"outputSchema,omitempty"`
}

type LoaderLLMResult struct {
	Text         string `json:"text,omitempty"`
	Model        string `json:"model,omitempty"`
	ResponseID   string `json:"responseId,omitempty"`
	FinishReason string `json:"finishReason,omitempty"`
	JSON         any    `json:"json"`
}

type LoaderTopicEvent struct {
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

func NormalizeLoaderRuntime(runtime string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(runtime)) {
	case "", LoaderRuntimeScheduler:
		return LoaderRuntimeScheduler, nil
	default:
		return "", fmt.Errorf("unsupported loader runtime %q", runtime)
	}
}

func NormalizeLoaderTriggerKind(kind string) (string, error) {
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

func NormalizeLoaderSessionPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", LoaderSessionPolicySticky, LoaderSessionPolicyReuse:
		return LoaderSessionPolicySticky
	case LoaderSessionPolicyNew:
		return LoaderSessionPolicyNew
	default:
		return LoaderSessionPolicySticky
	}
}

func NormalizeLoaderConcurrencyPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "", LoaderConcurrencyPolicySkip:
		return LoaderConcurrencyPolicySkip
	case LoaderConcurrencyPolicyParallel, "allow":
		return LoaderConcurrencyPolicyParallel
	default:
		return LoaderConcurrencyPolicySkip
	}
}

func NormalizeLoaderRunStatus(status string) string {
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

func LoaderTriggerStableID(kind, topic string, intervalMs int64, callbackSource string, index int) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d|%s|%d", kind, topic, intervalMs, callbackSource, index)))
	return "auto-" + hex.EncodeToString(h[:6])
}

func LoaderSourceSHA(script string) string {
	h := sha256.Sum256([]byte(script))
	return hex.EncodeToString(h[:])
}

func LoaderTriggerTopicMatches(pattern, topic string) bool {
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

func TimeIsSet(value time.Time) bool {
	return !value.IsZero()
}

func NonZeroTimeUnixMilli(value time.Time) int64 {
	if value.IsZero() {
		return 0
	}
	return value.UTC().UnixMilli()
}

func LoaderTriggerUsesSchedule(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case LoaderTriggerKindInterval, LoaderTriggerKindTimeout, LoaderTriggerKindCron:
		return true
	default:
		return false
	}
}

func LoaderTriggerScheduledAt(now time.Time, delayMs int64) time.Time {
	if delayMs <= 0 {
		return time.Time{}
	}
	return now.UTC().Add(time.Duration(delayMs) * time.Millisecond)
}

func DefaultLoaderName(now time.Time) string {
	return "Loader " + now.UTC().Format("2006-01-02 15:04")
}

func DefaultLoaderScript() string {
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
