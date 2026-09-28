package model

import (
	"context"
	"time"
)

const (
	SchedulerRuntimeScheduler = "scheduler"

	SchedulerTriggerKindInterval = "interval"
	SchedulerTriggerKindEvent    = "event"
	SchedulerTriggerKindTimeout  = "timeout"
	SchedulerTriggerKindCron     = "cron"

	SchedulerSandboxPolicySticky = "sticky"
	SchedulerSandboxPolicyNew    = "new"
	SchedulerSandboxPolicyReuse  = "reuse"

	SchedulerConcurrencyPolicySkip     = "skip"
	SchedulerConcurrencyPolicyParallel = "parallel"

	SchedulerRunStatusRunning   = "running"
	SchedulerRunStatusSucceeded = "succeeded"
	SchedulerRunStatusFailed    = "failed"
	SchedulerRunStatusCanceled  = "canceled"
	SchedulerRunStatusSkipped   = "skipped"
)

type SchedulerSummary struct {
	ID                string   `json:"id"`
	Name              string   `json:"name"`
	Description       string   `json:"description,omitempty"`
	Enabled           bool     `json:"enabled"`
	Runtime           string   `json:"runtime"`
	WorkspaceID       string   `json:"workspace_id,omitempty"`
	AgentID           string   `json:"agent_id,omitempty"`
	Driver            string   `json:"driver,omitempty"`
	GuestImage        string   `json:"guest_image,omitempty"`
	DefaultAgent      string   `json:"default_agent,omitempty"`
	SandboxPolicy     string   `json:"sandbox_policy,omitempty"`
	ConcurrencyPolicy string   `json:"concurrency_policy,omitempty"`
	CapsetIDs         []string `json:"capset_ids,omitempty"`
	// Project ownership uses native v2 names internally. The JSON tags retain
	// their historical names for existing event and runtime consumers.
	ProjectID          string    `json:"managed_project_id,omitempty"`
	ProjectRevision    int64     `json:"managed_project_revision,omitempty"`
	AgentName          string    `json:"managed_agent_name,omitempty"`
	ProjectSchedulerID string    `json:"managed_scheduler_id,omitempty"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
	LastError          string    `json:"last_error,omitempty"`
	TriggerCount       int       `json:"trigger_count"`
	RunCount           int       `json:"run_count"`
	EventCount         int       `json:"event_count"`
	LatestRunAt        time.Time `json:"latest_run_at,omitempty"`
}

type Scheduler struct {
	Summary    SchedulerSummary   `json:"summary"`
	Script     string             `json:"script"`
	Model      string             `json:"model,omitempty"`
	AgentModel string             `json:"agent_model,omitempty"`
	RunTimeout time.Duration      `json:"run_timeout,omitempty"`
	Triggers   []SchedulerTrigger `json:"triggers,omitempty"`
	EnvItems   []SandboxEnvVar    `json:"env_items,omitempty"`
	Volumes    []VolumeMountSpec  `json:"volumes,omitempty"`
}

type SchedulerTrigger struct {
	SchedulerID string    `json:"scheduler_id"`
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

type SchedulerRunSummary struct {
	ID               string    `json:"id"`
	SchedulerID      string    `json:"scheduler_id"`
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

type SchedulerEvent struct {
	ID                  string    `json:"id"`
	SchedulerID         string    `json:"scheduler_id"`
	RunID               string    `json:"run_id,omitempty"`
	TriggerID           string    `json:"trigger_id,omitempty"`
	Type                string    `json:"type"`
	Level               string    `json:"level"`
	Message             string    `json:"message"`
	PayloadJSON         string    `json:"payload_json,omitempty"`
	LinkedSandboxID     string    `json:"linked_sandbox_id,omitempty"`
	LinkedCellID        string    `json:"linked_cell_id,omitempty"`
	LinkedAgentThreadID string    `json:"linked_agent_thread_id,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
}

type SchedulerBinding struct {
	SchedulerID       string    `json:"scheduler_id"`
	TriggerID         string    `json:"trigger_id,omitempty"`
	SandboxID         string    `json:"sandbox_id"`
	SandboxConfigHash string    `json:"sandbox_config_hash,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
}

type SchedulerAgentRequest struct {
	Agent            string            `json:"agent,omitempty"`
	SandboxPolicy    string            `json:"sandboxPolicy,omitempty"`
	Timeout          time.Duration     `json:"timeout,omitempty"`
	Title            string            `json:"title,omitempty"`
	Driver           string            `json:"driver,omitempty"`
	GuestImage       string            `json:"guestImage,omitempty"`
	PullPolicy       string            `json:"pullPolicy,omitempty"`
	WorkspaceID      string            `json:"workspaceId,omitempty"`
	JupyterEnabled   bool              `json:"jupyter,omitempty"`
	SandboxEnv       []SandboxEnvVar   `json:"sandboxEnv,omitempty"`
	Volumes          []VolumeMountSpec `json:"volumes,omitempty"`
	OutputSchema     string            `json:"outputSchema,omitempty"`
	BindingTriggerID string            `json:"-"`
}

type SchedulerAgentResult struct {
	Text          string `json:"text,omitempty"`
	Output        string `json:"output,omitempty"`
	FinalText     string `json:"finalText,omitempty"`
	JSON          any    `json:"json"`
	SandboxID     string `json:"sandboxId,omitempty"`
	CellID        string `json:"cellId,omitempty"`
	Agent         string `json:"agent,omitempty"`
	AgentThreadID string `json:"agentThreadId,omitempty"`
	StopReason    string `json:"stopReason,omitempty"`
	Success       bool   `json:"success"`
	ExitCode      int    `json:"exitCode"`
}

type SchedulerCommandRequest struct {
	Mode           string            `json:"mode"`
	Command        string            `json:"command,omitempty"`
	Args           []string          `json:"args,omitempty"`
	Script         string            `json:"script,omitempty"`
	Cwd            string            `json:"cwd,omitempty"`
	Env            map[string]string `json:"env,omitempty"`
	TimeoutMs      int64             `json:"timeoutMs,omitempty"`
	MaxOutputBytes int64             `json:"maxOutputBytes,omitempty"`
	SandboxPolicy  string            `json:"sandboxPolicy,omitempty"`
	Title          string            `json:"title,omitempty"`
	Driver         string            `json:"driver,omitempty"`
	GuestImage     string            `json:"guestImage,omitempty"`
	PullPolicy     string            `json:"pullPolicy,omitempty"`
	WorkspaceID    string            `json:"workspaceId,omitempty"`
	JupyterEnabled bool              `json:"jupyter,omitempty"`
	SandboxEnv     []SandboxEnvVar   `json:"sandboxEnv,omitempty"`
	Volumes        []VolumeMountSpec `json:"volumes,omitempty"`
}

type SchedulerCommandResult struct {
	Stdout          string            `json:"stdout"`
	Stderr          string            `json:"stderr"`
	Output          string            `json:"output"`
	ExitCode        int               `json:"exitCode"`
	Success         bool              `json:"success"`
	StdoutTruncated bool              `json:"stdoutTruncated,omitempty"`
	StderrTruncated bool              `json:"stderrTruncated,omitempty"`
	OutputTruncated bool              `json:"outputTruncated,omitempty"`
	SandboxID       string            `json:"sandboxId,omitempty"`
	CellID          string            `json:"cellId,omitempty"`
	Artifacts       map[string]string `json:"artifacts,omitempty"`
}

type SchedulerLLMRequest struct {
	Model        string `json:"model,omitempty"`
	OutputSchema string `json:"outputSchema,omitempty"`
}

type SchedulerLLMResult struct {
	Text         string `json:"text,omitempty"`
	Model        string `json:"model,omitempty"`
	ResponseID   string `json:"responseId,omitempty"`
	FinishReason string `json:"finishReason,omitempty"`
	JSON         any    `json:"json"`
}

type SchedulerTopicEvent struct {
	EventID  string `json:"event_id,omitempty"`
	Topic    string `json:"topic"`
	Source   string `json:"source,omitempty"`
	Provider string `json:"provider,omitempty"`
	// PublisherProjectID is the Engine Project that published the event, or
	// empty for daemon-level publishers. Delivery scope is decided against it.
	PublisherProjectID string                                         `json:"publisher_project_id,omitempty"`
	Payload            map[string]any                                 `json:"payload,omitempty"`
	CreatedAt          time.Time                                      `json:"created_at"`
	Ack                func(context.Context) error                    `json:"-"`
	NoSubscriberAck    func(context.Context) error                    `json:"-"`
	Retry              func(context.Context, string, time.Time) error `json:"-"`
	Release            func()                                         `json:"-"`
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
