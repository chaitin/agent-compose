package loadertypes

import (
	modeldomain "agent-compose/internal/model"
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
	Summary  LoaderSummary               `json:"summary"`
	Script   string                      `json:"script"`
	Triggers []LoaderTrigger             `json:"triggers,omitempty"`
	EnvItems []modeldomain.SessionEnvVar `json:"env_items,omitempty"`
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
