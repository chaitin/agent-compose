package projecttypes

import "time"

const (
	ProjectRunStatusPending   = "pending"
	ProjectRunStatusRunning   = "running"
	ProjectRunStatusSucceeded = "succeeded"
	ProjectRunStatusFailed    = "failed"
	ProjectRunStatusCanceled  = "canceled"

	ProjectRunSourceManual    = "manual"
	ProjectRunSourceScheduler = "scheduler"
	ProjectRunSourceAPI       = "api"
)

type ProjectRunRecord struct {
	RunID           string    `json:"run_id"`
	ProjectID       string    `json:"project_id"`
	ProjectName     string    `json:"project_name,omitempty"`
	ProjectRevision int64     `json:"project_revision"`
	AgentName       string    `json:"agent_name,omitempty"`
	ManagedAgentID  string    `json:"managed_agent_id,omitempty"`
	Source          string    `json:"source,omitempty"`
	SchedulerID     string    `json:"scheduler_id,omitempty"`
	TriggerID       string    `json:"trigger_id,omitempty"`
	Status          string    `json:"status"`
	SessionID       string    `json:"session_id,omitempty"`
	ExitCode        int       `json:"exit_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	Prompt          string    `json:"prompt,omitempty"`
	Output          string    `json:"output,omitempty"`
	ResultJSON      string    `json:"result_json,omitempty"`
	LogsPath        string    `json:"logs_path,omitempty"`
	ArtifactsDir    string    `json:"artifacts_dir,omitempty"`
	CleanupError    string    `json:"cleanup_error,omitempty"`
	Driver          string    `json:"driver,omitempty"`
	ImageRef        string    `json:"image_ref,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	DurationMs      int64     `json:"duration_ms,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}
