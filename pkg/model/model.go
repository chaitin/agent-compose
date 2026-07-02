package model

import (
	"context"
	"net/http"
	"strings"
	"time"
)

const (
	VMStatusPending = "PENDING"
	VMStatusRunning = "RUNNING"
	VMStatusStopped = "STOPPED"
	VMStatusFailed  = "FAILED"

	SessionTypeManual = "manual"
	SessionTypeScript = "script"

	CellTypeShell      = "shell"
	CellTypeJavaScript = "javascript"
	CellTypePython     = "python"
	CellTypeAgent      = "agent"
)

const (
	AgentProviderCodex    = "codex"
	AgentProviderClaude   = "claude"
	AgentProviderGemini   = "gemini"
	AgentProviderOpenCode = "opencode"

	DefaultAgentProvider = AgentProviderCodex
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

const (
	TopicEventSourceWebhook = "webhook"
	TopicEventSourceLoader  = "loader"
	TopicEventSourceSystem  = "system"

	TopicEventDispatchPending        = "pending"
	TopicEventDispatchPublishing     = "publishing_to_bus"
	TopicEventDispatchPublishedToBus = "published_to_bus"
	TopicEventDispatchNoSubscriber   = "no_subscriber"
	TopicEventDispatchRetrying       = "retrying"
	TopicEventDispatchDeadLetter     = "dead_letter"

	EventDeliveryStatusMatched      = "matched"
	EventDeliveryStatusRunStarted   = "run_started"
	EventDeliveryStatusRunSucceeded = "run_succeeded"
	EventDeliveryStatusRunFailed    = "run_failed"
	EventDeliveryStatusSkipped      = "skipped"
)

const (
	LLMProviderFamilyOpenAI       = "openai"
	LLMProviderFamilyAnthropic    = "anthropic"
	LLMProviderScopeSystem        = "system"
	LLMProviderScopeEnvDefault    = "env_default"
	LLMProviderScopeSessionEnv    = "session_env"
	LLMProviderIDDefaultOpenAI    = "default"
	LLMProviderIDDefaultAnthropic = "anthropic"
)

type SessionTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SessionEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

func SessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
	var merged []SessionEnvVar
	for _, items := range groups {
		merged = append(merged, items...)
	}
	if len(merged) == 0 {
		return nil
	}
	env := make(map[string]string, len(merged))
	for _, item := range merged {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		env[name] = item.Value
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

type SessionSummary struct {
	ID            string       `json:"id"`
	Title         string       `json:"title"`
	TriggerSource string       `json:"trigger_source,omitempty"`
	Driver        string       `json:"driver"`
	VMStatus      string       `json:"vm_status"`
	GuestImage    string       `json:"guest_image,omitempty"`
	RuntimeRef    string       `json:"runtime_ref,omitempty"`
	WorkspacePath string       `json:"workspace_path"`
	ProxyPath     string       `json:"proxy_path"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	CellCount     int          `json:"cell_count"`
	EventCount    int          `json:"event_count"`
	Tags          []SessionTag `json:"tags,omitempty"`
}

type SessionListOptions struct {
	SessionType        string
	TriggerSourceQuery string
	TitleQuery         string
	WorkspaceQuery     string
	Driver             string
	VMStatus           string
	CreatedFrom        time.Time
	CreatedTo          time.Time
	UpdatedFrom        time.Time
	UpdatedTo          time.Time
	Offset             int
	Limit              int
}

type SessionListResult struct {
	Sessions   []*Session
	TotalCount int
	HasMore    bool
	NextOffset int
}

type SessionWorkspace struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	ConfigJSON string `json:"config_json,omitempty"`
}

type Session struct {
	Summary          SessionSummary    `json:"summary"`
	BaseWorkspace    string            `json:"base_workspace,omitempty"`
	WorkspaceID      string            `json:"workspace_id,omitempty"`
	Workspace        *SessionWorkspace `json:"workspace,omitempty"`
	EnvItems         []SessionEnvVar   `json:"env_items,omitempty"`
	RuntimeEnvItems  []SessionEnvVar   `json:"-"`
	ProviderEnvItems []SessionEnvVar   `json:"-"`
}

func RestoreSessionTransientFields(dst, src *Session) {
	if dst == nil || src == nil {
		return
	}
	if len(src.RuntimeEnvItems) > 0 {
		dst.RuntimeEnvItems = append([]SessionEnvVar(nil), src.RuntimeEnvItems...)
	}
	if len(src.ProviderEnvItems) > 0 {
		dst.ProviderEnvItems = append([]SessionEnvVar(nil), src.ProviderEnvItems...)
	}
}

type WorkspaceConfig struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	ConfigJSON string    `json:"config_json"`
	Comment    string    `json:"comment,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type NotebookCell struct {
	ID             string           `json:"id"`
	Type           string           `json:"type,omitempty"`
	Source         string           `json:"source"`
	Stdout         string           `json:"stdout"`
	Stderr         string           `json:"stderr"`
	Output         string           `json:"output"`
	ExitCode       int              `json:"exit_code"`
	Success        bool             `json:"success"`
	Running        bool             `json:"running,omitempty"`
	CreatedAt      time.Time        `json:"created_at"`
	Agent          string           `json:"agent,omitempty"`
	AgentSessionID string           `json:"agent_session_id,omitempty"`
	StopReason     string           `json:"stop_reason,omitempty"`
	AgentResume    *AgentResumeInfo `json:"agent_resume,omitempty"`
}

type AgentResumeInfo struct {
	Provider            string    `json:"provider,omitempty"`
	SessionID           string    `json:"session_id,omitempty"`
	SessionStatePath    string    `json:"session_state_path,omitempty"`
	SessionManifestPath string    `json:"session_manifest_path,omitempty"`
	SessionJSONLPaths   []string  `json:"session_jsonl_paths,omitempty"`
	UpdatedAt           time.Time `json:"updated_at,omitempty"`
}

type ExecChunk struct {
	Text     string
	IsStderr bool
}

type SessionEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentRun struct {
	ID             string    `json:"id"`
	Agent          string    `json:"agent"`
	Message        string    `json:"message"`
	Output         string    `json:"output"`
	ExitCode       int       `json:"exit_code"`
	Success        bool      `json:"success"`
	Running        bool      `json:"running,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	AgentSessionID string    `json:"agent_session_id,omitempty"`
	StopReason     string    `json:"stop_reason,omitempty"`
}

type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Output   string
	Success  bool
}

type RuntimeCommandArtifacts struct {
	Stdout  string `json:"stdout"`
	Stderr  string `json:"stderr"`
	Output  string `json:"output"`
	Request string `json:"request"`
	Result  string `json:"result"`
}

type RuntimeCommandResult struct {
	Stdout          string                  `json:"stdout"`
	Stderr          string                  `json:"stderr"`
	Output          string                  `json:"output"`
	ExitCode        int                     `json:"exitCode"`
	Success         bool                    `json:"success"`
	StdoutTruncated bool                    `json:"stdoutTruncated"`
	StderrTruncated bool                    `json:"stderrTruncated"`
	OutputTruncated bool                    `json:"outputTruncated"`
	Artifacts       RuntimeCommandArtifacts `json:"artifacts"`
}

type ExecStreamWriter func(ExecChunk)

type VMState struct {
	Driver       string    `json:"driver"`
	Mode         string    `json:"mode,omitempty"`
	BoxName      string    `json:"box_name,omitempty"`
	BoxID        string    `json:"box_id,omitempty"`
	Image        string    `json:"image,omitempty"`
	Registry     string    `json:"registry,omitempty"`
	RuntimeHome  string    `json:"runtime_home,omitempty"`
	StartedAt    time.Time `json:"started_at,omitempty"`
	StoppedAt    time.Time `json:"stopped_at,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	BootstrapRef string    `json:"bootstrap_ref,omitempty"`
}

type ProxyState struct {
	ProxyPath  string `json:"proxy_path"`
	GuestHost  string `json:"guest_host"`
	HostPort   int    `json:"host_port"`
	GuestPort  int    `json:"guest_port"`
	JupyterURL string `json:"jupyter_url,omitempty"`
	Token      string `json:"token,omitempty"`
}

type ExecSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
}

type AgentRunResult struct {
	Agent         string
	DisplayOutput string
	FinalText     string
	JSONText      string
	Transcript    string
	Success       bool
	ExitCode      int
	SessionID     string
	StopReason    string
}

type SessionVMInfo struct {
	BoxID      string
	JupyterURL string
	ProxyState *ProxyState
}

type AgentDefinition struct {
	ID                     string          `json:"id"`
	Name                   string          `json:"name"`
	Description            string          `json:"description,omitempty"`
	Enabled                bool            `json:"enabled"`
	DeletedAt              time.Time       `json:"deleted_at,omitempty"`
	Provider               string          `json:"provider"`
	Model                  string          `json:"model,omitempty"`
	SystemPrompt           string          `json:"system_prompt,omitempty"`
	Driver                 string          `json:"driver,omitempty"`
	GuestImage             string          `json:"guest_image,omitempty"`
	WorkspaceID            string          `json:"workspace_id,omitempty"`
	EnvItems               []SessionEnvVar `json:"env_items,omitempty"`
	ConfigJSON             string          `json:"config_json"`
	CapsetIDs              []string        `json:"capset_ids,omitempty"`
	ManagedProjectID       string          `json:"managed_project_id,omitempty"`
	ManagedProjectRevision int64           `json:"managed_project_revision,omitempty"`
	ManagedAgentName       string          `json:"managed_agent_name,omitempty"`
	CreatedAt              time.Time       `json:"created_at"`
	UpdatedAt              time.Time       `json:"updated_at"`
}

type AgentDefinitionListOptions struct {
	Query           string
	IncludeDisabled bool
	Offset          int
	Limit           int
}

type AgentDefinitionListResult struct {
	Agents     []AgentDefinition
	TotalCount int
	HasMore    bool
	NextOffset int
}

type AgentCurrentRunSummary struct {
	RunningSessionCount int
}

type AgentLatestRunSummary struct {
	RunType string
	Status  string
	RunID   string
	Title   string
	At      time.Time
}

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
	Summary  LoaderSummary   `json:"summary"`
	Script   string          `json:"script"`
	Triggers []LoaderTrigger `json:"triggers,omitempty"`
	EnvItems []SessionEnvVar `json:"env_items,omitempty"`
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
	Agent         string          `json:"agent,omitempty"`
	SessionPolicy string          `json:"sessionPolicy,omitempty"`
	Timeout       time.Duration   `json:"timeout,omitempty"`
	Title         string          `json:"title,omitempty"`
	Driver        string          `json:"driver,omitempty"`
	GuestImage    string          `json:"guestImage,omitempty"`
	WorkspaceID   string          `json:"workspaceId,omitempty"`
	SessionEnv    []SessionEnvVar `json:"sessionEnv,omitempty"`
	OutputSchema  string          `json:"outputSchema,omitempty"`
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

type ProjectRecord struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
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

type ProjectRunPreparation struct {
	EnvItems         []SessionEnvVar
	ProviderEnvItems []SessionEnvVar
	CapsetIDs        []string
	WorkspaceConfig  *WorkspaceConfig
	Workspace        *SessionWorkspace
}

type ProjectRunSessionResult struct {
	Session *Session
	Created bool
}

type ProjectRunStartRequest struct {
	ProjectID       string
	AgentName       string
	Source          string
	SchedulerID     string
	TriggerID       string
	Prompt          string
	ClientRequestID string
}

type ProjectRunTransitionRequest struct {
	RunID        string
	Status       string
	SessionID    string
	ExitCode     int
	Error        string
	Output       string
	ResultJSON   string
	LogsPath     string
	ArtifactsDir string
	CleanupError string
}

type TopicEventRecord struct {
	ID              string    `json:"event_id"`
	Sequence        int64     `json:"sequence"`
	Topic           string    `json:"topic"`
	Source          string    `json:"source"`
	Provider        string    `json:"provider,omitempty"`
	Intent          string    `json:"intent,omitempty"`
	CorrelationID   string    `json:"correlation_id"`
	IdempotencyKey  string    `json:"idempotency_key,omitempty"`
	DeliveryID      string    `json:"delivery_id,omitempty"`
	PayloadHash     string    `json:"payload_hash"`
	PayloadJSON     string    `json:"payload_json"`
	DispatchStatus  string    `json:"dispatch_status"`
	ParentEventID   string    `json:"parent_event_id,omitempty"`
	PublisherType   string    `json:"publisher_type,omitempty"`
	PublisherID     string    `json:"publisher_id,omitempty"`
	PublisherRunID  string    `json:"publisher_run_id,omitempty"`
	ReplayOfEventID string    `json:"replay_of_event_id,omitempty"`
	ClaimID         string    `json:"claim_id,omitempty"`
	ClaimUntil      time.Time `json:"claim_until,omitempty"`
	AttemptCount    int       `json:"attempt_count,omitempty"`
	NextAttemptAt   time.Time `json:"next_attempt_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	DeadLetterAt    time.Time `json:"dead_letter_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	DispatchedAt    time.Time `json:"dispatched_at,omitempty"`
}

type TopicEventFilter struct {
	EventID        string
	Topic          string
	CorrelationID  string
	AfterSequence  int64
	Limit          int
	DispatchStatus string
}

type WebhookSource struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Provider        string    `json:"provider"`
	TopicPrefix     string    `json:"topic_prefix"`
	TokenHash       string    `json:"token_hash,omitempty"`
	SignatureType   string    `json:"signature_type,omitempty"`
	SignatureSecret string    `json:"signature_secret,omitempty"`
	BodyLimitBytes  int64     `json:"body_limit_bytes,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type EventDelivery struct {
	EventID   string    `json:"event_id"`
	LoaderID  string    `json:"loader_id"`
	TriggerID string    `json:"trigger_id"`
	RunID     string    `json:"run_id,omitempty"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EventSessionLink struct {
	EventID       string    `json:"event_id"`
	SessionID     string    `json:"session_id"`
	Relation      string    `json:"relation"`
	LoaderID      string    `json:"loader_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	TriggerID     string    `json:"trigger_id,omitempty"`
	LoaderEventID string    `json:"loader_event_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type EventSessionTraceItem struct {
	SessionID     string    `json:"session_id"`
	Relation      string    `json:"relation"`
	LoaderID      string    `json:"loader_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	TriggerID     string    `json:"trigger_id,omitempty"`
	LoaderEventID string    `json:"loader_event_id,omitempty"`
	EventID       string    `json:"event_id"`
	CreatedAt     time.Time `json:"created_at"`
}

type LLMProvider struct {
	ID                           string
	Name                         string
	ProviderType                 string
	DefaultWireAPI               string
	BaseURL                      string
	APIKey                       string
	AuthHeader                   string
	AuthScheme                   string
	HeadersJSON                  string
	UseGenericResponsesTextParts bool
	Weight                       int
	Enabled                      bool
	Scope                        string
	CreatedAt                    time.Time
	UpdatedAt                    time.Time
}

type LLMModel struct {
	ID           string
	Name         string
	Description  string
	DefaultModel bool
	Enabled      bool
	Scope        string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type LLMResolvedTarget struct {
	Provider LLMProvider
	Model    LLMModel
	WireAPI  string
	Endpoint string
	Headers  http.Header
}

type LLMFacadeToken struct {
	SessionID        string
	TokenHash        string
	TokenFingerprint string
	Model            string
	ProviderID       string
	WireAPI          string
	Source           string
	RunID            string
	IssuedAt         time.Time
	ExpiresAt        time.Time
	RevokedAt        time.Time
}

type LLMGenerateResult struct {
	Text         string
	Model        string
	ResponseID   string
	FinishReason string
}

type CellExecutionStream struct {
	OnStart func(NotebookCell) error
	OnChunk func(string, ExecChunk) error
}

type AgentExecutionStream struct {
	OnStart func(NotebookCell) error
	OnChunk func(string, ExecChunk) error
}

type ExecuteAgentRequest struct {
	Agent             string
	AgentDefinitionID string
	Model             string
	ProviderEnvItems  []SessionEnvVar
	RunID             string
	Message           string
	Timeout           time.Duration
	OutputSchemaJSON  string
	Stream            AgentExecutionStream
}
