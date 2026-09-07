package model

import (
	"fmt"
	"strings"
	"time"
)

const (
	// MaxProjectRunLabels caps how many labels one run may carry and how many
	// may be ANDed into a single run list filter. Both directions need a bound.
	// Unbounded writes let a single run grow project_run_label without limit,
	// and the filter path emits one EXISTS subquery per label, which trips
	// SQLite's expression-tree depth limit of 1000 somewhere past 900 labels
	// and surfaces to the caller as an opaque internal error. The cap sits far
	// below that ceiling because labels identify a run rather than describe it.
	MaxProjectRunLabels = 64
	// MaxProjectRunLabelKeyBytes and MaxProjectRunLabelValueBytes are byte
	// counts, not rune counts, so a multi-byte key reaches the limit sooner
	// than its character count suggests.
	MaxProjectRunLabelKeyBytes   = 128
	MaxProjectRunLabelValueBytes = 256
)

// ValidateProjectRunLabelCount bounds a label set without normalizing it, for
// the list filter path where labels are matched rather than stored.
func ValidateProjectRunLabelCount(labels map[string]string) error {
	if len(labels) > MaxProjectRunLabels {
		return fmt.Errorf("run labels exceed the %d label limit (got %d)", MaxProjectRunLabels, len(labels))
	}
	return nil
}

// NormalizeProjectRunLabels validates a label set and returns it with keys
// trimmed, so a direct API caller sending " env " and a CLI caller sending
// "env" converge on one label instead of two that silently miss each other at
// filter time. Two raw keys that trim to the same key are rejected rather than
// resolved by map iteration order, which would pick a winner at random.
func NormalizeProjectRunLabels(labels map[string]string) (map[string]string, error) {
	if err := ValidateProjectRunLabelCount(labels); err != nil {
		return nil, err
	}
	if len(labels) == 0 {
		return nil, nil
	}
	normalized := make(map[string]string, len(labels))
	for key, value := range labels {
		trimmed := strings.TrimSpace(key)
		if trimmed == "" {
			return nil, fmt.Errorf("run label key must not be empty")
		}
		if len(trimmed) > MaxProjectRunLabelKeyBytes {
			return nil, fmt.Errorf("run label key %q exceeds %d bytes", trimmed, MaxProjectRunLabelKeyBytes)
		}
		if len(value) > MaxProjectRunLabelValueBytes {
			return nil, fmt.Errorf("run label value for key %q exceeds %d bytes", trimmed, MaxProjectRunLabelValueBytes)
		}
		if _, exists := normalized[trimmed]; exists {
			return nil, fmt.Errorf("run label key %q is specified more than once after trimming surrounding whitespace", trimmed)
		}
		normalized[trimmed] = value
	}
	return normalized, nil
}

const (
	ProjectRunStatusPending   = "pending"
	ProjectRunStatusRunning   = "running"
	ProjectRunStatusSucceeded = "succeeded"
	ProjectRunStatusFailed    = "failed"
	ProjectRunStatusCanceled  = "canceled"

	ProjectRunSourceManual    = "manual"
	ProjectRunSourceScheduler = "scheduler"
	ProjectRunSourceAPI       = "api"

	ProjectRunCleanupStopOnCompletion   = "stop_on_completion"
	ProjectRunCleanupKeepRunning        = "keep_running"
	ProjectRunCleanupRemoveOnCompletion = "remove_on_completion"

	ProjectRunCompletionActionNone   = "none"
	ProjectRunCompletionActionStop   = "stop"
	ProjectRunCompletionActionRemove = "remove"
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
	ID           string    `json:"id,omitempty"`
	ShortID      string    `json:"short_id,omitempty"`
	ProjectID    string    `json:"project_id"`
	SchedulerID  string    `json:"scheduler_id"`
	AgentName    string    `json:"agent_name"`
	Revision     int64     `json:"revision"`
	Enabled      bool      `json:"enabled"`
	TriggerCount int       `json:"trigger_count"`
	SpecJSON     string    `json:"spec_json"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	RunCount     int       `json:"run_count,omitempty"`
	LatestRunAt  time.Time `json:"latest_run_at,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
}

type ProjectRunRecord struct {
	RunID           string    `json:"run_id"`
	ProjectID       string    `json:"project_id"`
	ProjectName     string    `json:"project_name,omitempty"`
	ProjectRevision int64     `json:"project_revision"`
	AgentName       string    `json:"agent_name,omitempty"`
	AgentID         string    `json:"managed_agent_id,omitempty"`
	Source          string    `json:"source,omitempty"`
	SchedulerID     string    `json:"scheduler_id,omitempty"`
	SchedulerRunID  string    `json:"-"`
	TriggerID       string    `json:"trigger_id,omitempty"`
	Status          string    `json:"status"`
	SandboxID       string    `json:"sandbox_id,omitempty"`
	ExitCode        int       `json:"exit_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	ErrorStack      string    `json:"error_stack,omitempty"`
	Prompt          string    `json:"prompt,omitempty"`
	Output          string    `json:"output,omitempty"`
	ResultJSON      string    `json:"result_json,omitempty"`
	LogsPath        string    `json:"logs_path,omitempty"`
	ArtifactsDir    string    `json:"artifacts_dir,omitempty"`
	CleanupError    string    `json:"cleanup_error,omitempty"`
	CleanupPolicy   string    `json:"cleanup_policy"`
	SandboxCreated  bool      `json:"sandbox_created"`
	Driver          string    `json:"driver,omitempty"`
	ImageRef        string    `json:"image_ref,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	CompletedAt     time.Time `json:"completed_at,omitempty"`
	DurationMs      int64     `json:"duration_ms,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
	Warnings        []string  `json:"warnings,omitempty"`
	// Labels holds user-defined key/value pairs attached to the run at start
	// time. It is not a column on project_run; it is populated by GetProjectRun
	// from the project_run_label table on read, and by Coordinator.BeginRun on
	// write for the store layer to persist into project_run_label.
	Labels map[string]string `json:"labels,omitempty"`
}

// ProjectRunCompletionRecord is the durable intent to finish a run after its
// sandbox lifecycle action succeeds. TransitionJSON preserves the exact
// result selected by the first completion writer across daemon restarts.
type ProjectRunCompletionRecord struct {
	RunID          string    `json:"run_id"`
	TargetStatus   string    `json:"target_status"`
	TransitionJSON string    `json:"transition_json"`
	CleanupAction  string    `json:"cleanup_action"`
	Attempt        int       `json:"attempt"`
	LastError      string    `json:"last_error,omitempty"`
	NextAttemptAt  time.Time `json:"next_attempt_at,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

// ProjectRunCompletionFailure describes one failed completion attempt to record
// against a project run, including when the next retry should occur.
type ProjectRunCompletionFailure struct {
	RunID         string
	Message       string
	Attempt       int
	NextAttemptAt time.Time
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

// ProjectRunEventSandboxCursor describes the position a sandbox-scoped
// project run event listing should resume after, plus the page size to
// return.
type ProjectRunEventSandboxCursor struct {
	AfterCreatedAt time.Time
	AfterRunID     string
	AfterSequence  uint64
	Limit          int
}

type ProjectListOptions struct {
	Query          string
	IncludeRemoved bool
	Offset         int
	Limit          int
}

type ProjectRunListOptions struct {
	ProjectID      string
	AgentName      string
	SandboxID      string
	SchedulerID    string
	SchedulerRunID string
	Status         string
	Source         string
	StartedFrom    *time.Time
	StartedTo      *time.Time
	Offset         int
	Limit          int
	// Labels filters to runs carrying every given key/value pair (AND semantics).
	// An empty map applies no filter.
	Labels map[string]string
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
	Projects          []ProjectRecord
	CountsByProjectID map[string]ProjectListCounts
	TotalCount        int
	HasMore           bool
	NextOffset        int
}

type ProjectListCounts struct {
	AgentCount     uint32
	SchedulerCount uint32
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
