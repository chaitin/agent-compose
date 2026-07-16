package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	VMStatusPending  = "PENDING"
	VMStatusRunning  = "RUNNING"
	VMStatusStopped  = "STOPPED"
	VMStatusFailed   = "FAILED"
	VMStatusDeleting = "DELETING"

	SandboxTypeManual = "manual"
	SandboxTypeScript = "script"

	SandboxWorkspaceProvisioningVersion       = 1
	SandboxWorkspaceProvisioningStatusPending = "pending"
	SandboxWorkspaceProvisioningStatusReady   = "ready"
	SandboxWorkspaceProvisioningStatusFailed  = "failed"

	SandboxWorkspaceReclamationStateReclaiming = "reclaiming"
	SandboxWorkspaceReclamationStateReclaimed  = "reclaimed"
)

type SandboxTag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type SandboxEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

func SandboxEnvMap(groups ...[]SandboxEnvVar) map[string]string {
	var merged []SandboxEnvVar
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

func NormalizeEnvItems(items []SandboxEnvVar) []SandboxEnvVar {
	if len(items) == 0 {
		return nil
	}
	merged := make(map[string]SandboxEnvVar, len(items))
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
	result := make([]SandboxEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func MergeEnvItems(globalItems, sessionItems []SandboxEnvVar) []SandboxEnvVar {
	merged := make(map[string]SandboxEnvVar, len(globalItems)+len(sessionItems))
	for _, item := range NormalizeEnvItems(globalItems) {
		merged[item.Name] = item
	}
	for _, item := range NormalizeEnvItems(sessionItems) {
		merged[item.Name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SandboxEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

type SandboxSummary struct {
	ID            string       `json:"id"`
	ShortID       string       `json:"short_id,omitempty"`
	Title         string       `json:"title"`
	TriggerSource string       `json:"trigger_source,omitempty"`
	Driver        string       `json:"driver"`
	VMStatus      string       `json:"vm_status"`
	GuestImage    string       `json:"guest_image,omitempty"`
	PullPolicy    string       `json:"pull_policy,omitempty"`
	RuntimeRef    string       `json:"runtime_ref,omitempty"`
	WorkspacePath string       `json:"workspace_path"`
	ProxyPath     string       `json:"proxy_path"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	CellCount     int          `json:"cell_count"`
	EventCount    int          `json:"event_count"`
	Tags          []SandboxTag `json:"tags,omitempty"`
}

type SandboxListOptions struct {
	SandboxType        string
	TriggerSourceQuery string
	TitleQuery         string
	WorkspaceQuery     string
	Driver             string
	VMStatus           string
	CreatedFrom        time.Time
	CreatedTo          time.Time
	UpdatedFrom        time.Time
	UpdatedTo          time.Time
	BeforeUpdatedAt    time.Time
	BeforeID           string
	Offset             int
	Limit              int
}

type SandboxListResult struct {
	Sandboxes  []*Sandbox
	TotalCount int
	HasMore    bool
	NextOffset int
}

type SandboxWorkspace struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	ConfigJSON string `json:"config_json,omitempty"`
}

type SandboxWorkspaceProvisioning struct {
	Version   int       `json:"version"`
	Status    string    `json:"status"`
	UpdatedAt time.Time `json:"updated_at"`
}

type SandboxWorkspaceReclamation struct {
	State       string    `json:"state"`
	StartedAt   time.Time `json:"started_at"`
	CompletedAt time.Time `json:"completed_at,omitempty"`
	LastError   string    `json:"last_error,omitempty"`
}

type Sandbox struct {
	Summary               SandboxSummary                `json:"summary"`
	BaseWorkspace         string                        `json:"base_workspace,omitempty"`
	WorkspaceID           string                        `json:"workspace_id,omitempty"`
	Workspace             *SandboxWorkspace             `json:"workspace,omitempty"`
	WorkspaceProvisioning *SandboxWorkspaceProvisioning `json:"workspace_provisioning,omitempty"`
	WorkspaceReclamation  *SandboxWorkspaceReclamation  `json:"workspace_reclamation,omitempty"`
	EnvItems              []SandboxEnvVar               `json:"env_items,omitempty"`
	VolumeMounts          []SandboxVolumeMount          `json:"volume_mounts,omitempty"`
	RuntimeEnvItems       []SandboxEnvVar               `json:"-"`
	ProviderEnvItems      []SandboxEnvVar               `json:"-"`
}

func SandboxWorkspaceReclaimed(sandbox *Sandbox) bool {
	if sandbox == nil || sandbox.WorkspaceReclamation == nil {
		return false
	}
	return sandbox.WorkspaceReclamation.State == SandboxWorkspaceReclamationStateReclaimed
}

func SandboxWorkspaceUnavailable(sandbox *Sandbox) bool {
	return sandbox != nil && sandbox.WorkspaceReclamation != nil
}

func ValidateSandboxWorkspaceProvisioning(provisioning *SandboxWorkspaceProvisioning) error {
	if provisioning == nil {
		return fmt.Errorf("%w: workspace provisioning is nil", ErrInvalidArgument)
	}
	if provisioning.Version != SandboxWorkspaceProvisioningVersion {
		return fmt.Errorf(
			"%w: unsupported workspace provisioning version %d",
			ErrInvalidArgument,
			provisioning.Version,
		)
	}
	if !validSandboxWorkspaceProvisioningStatus(provisioning.Status) {
		return fmt.Errorf(
			"%w: unknown workspace provisioning status %q",
			ErrInvalidArgument,
			provisioning.Status,
		)
	}
	return nil
}

func TransitionSandboxWorkspaceProvisioning(sandbox *Sandbox, nextStatus string) error {
	if sandbox == nil {
		return fmt.Errorf("%w: sandbox is nil", ErrInvalidArgument)
	}
	if err := ValidateSandboxWorkspaceProvisioning(sandbox.WorkspaceProvisioning); err != nil {
		return err
	}
	if !validSandboxWorkspaceProvisioningStatus(nextStatus) {
		return fmt.Errorf(
			"%w: unknown workspace provisioning target status %q",
			ErrInvalidArgument,
			nextStatus,
		)
	}

	currentStatus := sandbox.WorkspaceProvisioning.Status
	allowed := currentStatus == SandboxWorkspaceProvisioningStatusPending &&
		(nextStatus == SandboxWorkspaceProvisioningStatusReady || nextStatus == SandboxWorkspaceProvisioningStatusFailed) ||
		currentStatus == SandboxWorkspaceProvisioningStatusFailed &&
			nextStatus == SandboxWorkspaceProvisioningStatusPending
	if !allowed {
		return fmt.Errorf(
			"%w: workspace provisioning transition %q -> %q is not allowed",
			ErrFailedPrecondition,
			currentStatus,
			nextStatus,
		)
	}

	next := *sandbox.WorkspaceProvisioning
	next.Status = nextStatus
	next.UpdatedAt = time.Now().UTC()
	sandbox.WorkspaceProvisioning = &next
	return nil
}

func validSandboxWorkspaceProvisioningStatus(status string) bool {
	switch status {
	case SandboxWorkspaceProvisioningStatusPending,
		SandboxWorkspaceProvisioningStatusReady,
		SandboxWorkspaceProvisioningStatusFailed:
		return true
	default:
		return false
	}
}

func RestoreSandboxTransientFields(dst, src *Sandbox) {
	if dst == nil || src == nil {
		return
	}
	if len(src.RuntimeEnvItems) > 0 {
		dst.RuntimeEnvItems = append([]SandboxEnvVar(nil), src.RuntimeEnvItems...)
	}
	if len(src.ProviderEnvItems) > 0 {
		dst.ProviderEnvItems = append([]SandboxEnvVar(nil), src.ProviderEnvItems...)
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
	ID            string           `json:"id"`
	Type          string           `json:"type,omitempty"`
	Source        string           `json:"source"`
	Stdout        string           `json:"stdout"`
	Stderr        string           `json:"stderr"`
	Output        string           `json:"output"`
	ExitCode      int              `json:"exit_code"`
	Success       bool             `json:"success"`
	Running       bool             `json:"running,omitempty"`
	CreatedAt     time.Time        `json:"created_at"`
	Agent         string           `json:"agent,omitempty"`
	AgentThreadID string           `json:"agent_thread_id,omitempty"`
	StopReason    string           `json:"stop_reason,omitempty"`
	AgentResume   *AgentResumeInfo `json:"agent_resume,omitempty"`
}

func (c *NotebookCell) UnmarshalJSON(data []byte) error {
	type notebookCellAlias NotebookCell
	var decoded struct {
		notebookCellAlias
		LegacyAgentSessionID string `json:"agent_session_id"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*c = NotebookCell(decoded.notebookCellAlias)
	if c.AgentThreadID == "" {
		c.AgentThreadID = decoded.LegacyAgentSessionID
	}
	return nil
}

type AgentResumeInfo struct {
	Provider           string    `json:"provider,omitempty"`
	ThreadID           string    `json:"thread_id,omitempty"`
	ThreadStatePath    string    `json:"thread_state_path,omitempty"`
	ThreadManifestPath string    `json:"thread_manifest_path,omitempty"`
	ProviderLogPaths   []string  `json:"provider_log_paths,omitempty"`
	UpdatedAt          time.Time `json:"updated_at,omitempty"`
}

func (i *AgentResumeInfo) UnmarshalJSON(data []byte) error {
	type agentResumeInfoAlias AgentResumeInfo
	var decoded struct {
		agentResumeInfoAlias
		LegacySessionID           string   `json:"session_id"`
		LegacySessionStatePath    string   `json:"session_state_path"`
		LegacySessionManifestPath string   `json:"session_manifest_path"`
		LegacySessionJSONLPaths   []string `json:"session_jsonl_paths"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*i = AgentResumeInfo(decoded.agentResumeInfoAlias)
	if i.ThreadID == "" {
		i.ThreadID = decoded.LegacySessionID
	}
	if i.ThreadStatePath == "" {
		i.ThreadStatePath = decoded.LegacySessionStatePath
	}
	if i.ThreadManifestPath == "" {
		i.ThreadManifestPath = decoded.LegacySessionManifestPath
	}
	if len(i.ProviderLogPaths) == 0 && len(decoded.LegacySessionJSONLPaths) > 0 {
		i.ProviderLogPaths = append([]string(nil), decoded.LegacySessionJSONLPaths...)
	}
	return nil
}

type StdioStream string

const (
	StdioStdout StdioStream = "stdout"
	StdioStderr StdioStream = "stderr"
)

func NormalizeStdioStream(stream StdioStream) StdioStream {
	if stream == StdioStderr {
		return StdioStderr
	}
	return StdioStdout
}

type ExecChunk struct {
	Text   string
	Stream StdioStream
}

type SandboxEvent struct {
	ID        string    `json:"id"`
	Type      string    `json:"type"`
	Level     string    `json:"level"`
	Message   string    `json:"message"`
	CreatedAt time.Time `json:"created_at"`
}

type AgentRun struct {
	ID            string    `json:"id"`
	Agent         string    `json:"agent"`
	Message       string    `json:"message"`
	Output        string    `json:"output"`
	ExitCode      int       `json:"exit_code"`
	Success       bool      `json:"success"`
	Running       bool      `json:"running,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
	AgentThreadID string    `json:"agent_thread_id,omitempty"`
	StopReason    string    `json:"stop_reason,omitempty"`
}

func (r *AgentRun) UnmarshalJSON(data []byte) error {
	type agentRunAlias AgentRun
	var decoded struct {
		agentRunAlias
		LegacyAgentSessionID string `json:"agent_session_id"`
	}
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = AgentRun(decoded.agentRunAlias)
	if r.AgentThreadID == "" {
		r.AgentThreadID = decoded.LegacyAgentSessionID
	}
	return nil
}

type ExecResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Output   string
	Success  bool
}

const (
	MetricStatusOK          = "ok"
	MetricStatusUnknown     = "unknown"
	MetricStatusUnavailable = "unavailable"
)

type MetricValue struct {
	Value   *float64 `json:"value"`
	Unit    string   `json:"unit"`
	Status  string   `json:"status"`
	Message string   `json:"message,omitempty"`
}

type SandboxStats struct {
	SandboxID        string      `json:"sandbox_id"`
	Driver           string      `json:"driver"`
	SampledAt        time.Time   `json:"sampled_at"`
	CPUPercent       MetricValue `json:"cpu_percent"`
	MemoryUsageBytes MetricValue `json:"memory_usage_bytes"`
	MemoryLimitBytes MetricValue `json:"memory_limit_bytes"`
	MemoryPercent    MetricValue `json:"memory_percent"`
	NetworkRxBytes   MetricValue `json:"network_rx_bytes"`
	NetworkTxBytes   MetricValue `json:"network_tx_bytes"`
	BlockReadBytes   MetricValue `json:"block_read_bytes"`
	BlockWriteBytes  MetricValue `json:"block_write_bytes"`
	UptimeSeconds    MetricValue `json:"uptime_seconds"`
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
	Driver           string    `json:"driver"`
	Mode             string    `json:"mode,omitempty"`
	BoxName          string    `json:"box_name,omitempty"`
	BoxID            string    `json:"box_id,omitempty"`
	Image            string    `json:"image,omitempty"`
	Registry         string    `json:"registry,omitempty"`
	RuntimeHome      string    `json:"runtime_home,omitempty"`
	StartedAt        time.Time `json:"started_at,omitempty"`
	StartAttemptedAt time.Time `json:"start_attempted_at,omitempty"`
	StoppedAt        time.Time `json:"stopped_at,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	BootstrapRef     string    `json:"bootstrap_ref,omitempty"`
}

type ProxyState struct {
	ProxyPath  string `json:"proxy_path"`
	GuestHost  string `json:"guest_host"`
	HostPort   int    `json:"host_port"`
	GuestPort  int    `json:"guest_port"`
	JupyterURL string `json:"jupyter_url,omitempty"`
	Token      string `json:"token,omitempty"`
	Enabled    bool   `json:"enabled,omitempty"`
	Exposed    bool   `json:"exposed,omitempty"`
}

type SandboxVMInfo struct {
	BoxID      string
	JupyterURL string
	ProxyState *ProxyState
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
	ThreadID      string
	StopReason    string
}
