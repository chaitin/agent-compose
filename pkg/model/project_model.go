package model

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

type ProjectRecord struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	ShortID         string    `json:"short_id,omitempty"`
	SourcePath      string    `json:"source_path,omitempty"`
	SourceJSON      string    `json:"source_json"`
	CurrentRevision int64     `json:"current_revision"`
	SpecHash        string    `json:"spec_hash,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	RemovedAt       time.Time `json:"removed_at,omitempty"`
}

type ProjectRevisionRecord struct {
	ProjectID string    `json:"project_id"`
	Revision  int64     `json:"revision"`
	SpecHash  string    `json:"spec_hash"`
	SpecJSON  string    `json:"spec_json"`
	CreatedAt time.Time `json:"created_at"`
}

type ProjectAgentRecord struct {
	ID               string    `json:"id,omitempty"`
	Name             string    `json:"name,omitempty"`
	ShortID          string    `json:"short_id,omitempty"`
	ProjectID        string    `json:"project_id"`
	AgentName        string    `json:"agent_name"`
	ManagedAgentID   string    `json:"managed_agent_id,omitempty"`
	Revision         int64     `json:"revision"`
	Provider         string    `json:"provider,omitempty"`
	Model            string    `json:"model,omitempty"`
	Image            string    `json:"image,omitempty"`
	Driver           string    `json:"driver,omitempty"`
	SchedulerEnabled bool      `json:"scheduler_enabled"`
	SpecJSON         string    `json:"spec_json"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

type ProjectSchedulerRecord struct {
	ID              string    `json:"id,omitempty"`
	ShortID         string    `json:"short_id,omitempty"`
	ProjectID       string    `json:"project_id"`
	SchedulerID     string    `json:"scheduler_id"`
	AgentName       string    `json:"agent_name"`
	ManagedLoaderID string    `json:"managed_loader_id,omitempty"`
	Revision        int64     `json:"revision"`
	Enabled         bool      `json:"enabled"`
	TriggerCount    int       `json:"trigger_count"`
	SpecJSON        string    `json:"spec_json"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	RunCount        int       `json:"run_count,omitempty"`
	LatestRunAt     time.Time `json:"latest_run_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
}

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
	SandboxID       string    `json:"sandbox_id,omitempty"`
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
	Warnings        []string  `json:"warnings,omitempty"`
}

type ProjectRunEventKind string

const (
	ProjectRunEventKindUserMessage   ProjectRunEventKind = "user_message"
	ProjectRunEventKindAgentMessage  ProjectRunEventKind = "agent_message"
	ProjectRunEventKindAgentActivity ProjectRunEventKind = "agent_activity"
	ProjectRunEventKindStatus        ProjectRunEventKind = "status"
)

type ProjectRunEventRecord struct {
	ID          string              `json:"id"`
	RunID       string              `json:"run_id"`
	Sequence    uint64              `json:"sequence"`
	Kind        ProjectRunEventKind `json:"kind"`
	Text        string              `json:"text,omitempty"`
	Agent       string              `json:"agent,omitempty"`
	Name        string              `json:"name,omitempty"`
	PayloadJSON string              `json:"payload_json,omitempty"`
	Success     bool                `json:"success"`
	ExitCode    int                 `json:"exit_code,omitempty"`
	StopReason  string              `json:"stop_reason,omitempty"`
	CreatedAt   time.Time           `json:"created_at"`
}

type ProjectListOptions struct {
	Query          string
	IncludeRemoved bool
	Offset         int
	Limit          int
}

type ProjectRunListOptions struct {
	ProjectID   string
	AgentName   string
	SandboxID   string
	SchedulerID string
	Status      string
	Source      string
	Offset      int
	Limit       int
}

type ProjectAgentRunState struct {
	AgentName                string
	RunningRunCount          uint32
	RunningSchedulerRunCount uint32
	LatestRunID              string
	LatestStatus             string
	LatestSource             string
	LatestAt                 time.Time
}

type ProjectListResult struct {
	Projects   []ProjectRecord
	TotalCount int
	HasMore    bool
	NextOffset int
}

type ProjectSandboxRelationFilter struct {
	ProjectID string
	AgentName string
	SandboxID string
	Statuses  []string
	Limit     int
}

type ProjectSandboxStatus struct {
	Run            ProjectRunRecord `json:"run"`
	Sandbox        *Sandbox         `json:"sandbox,omitempty"`
	SandboxMissing bool             `json:"sandbox_missing,omitempty"`
}
