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
	Warnings        []string  `json:"warnings,omitempty"`
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
	SessionID   string
	SchedulerID string
	Status      string
	Source      string
	Offset      int
	Limit       int
}

type ProjectListResult struct {
	Projects   []ProjectRecord
	TotalCount int
	HasMore    bool
	NextOffset int
}

type ProjectSessionRelationFilter struct {
	ProjectID string
	AgentName string
	SessionID string
	Statuses  []string
	Limit     int
}

type ProjectSessionStatus struct {
	Run            ProjectRunRecord `json:"run"`
	Session        *Session         `json:"session,omitempty"`
	SessionMissing bool             `json:"session_missing,omitempty"`
}
