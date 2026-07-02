package session

import (
	execdomain "agent-compose/internal/agentcompose/exec"
	loaderdomain "agent-compose/internal/agentcompose/loader"
	agentworkspace "agent-compose/internal/agentcompose/workspace"
	"strings"
	"time"
)

const (
	VMStatusPending = "PENDING"
	VMStatusRunning = "RUNNING"
	VMStatusStopped = "STOPPED"
	VMStatusFailed  = "FAILED"

	TypeManual = "manual"
	TypeScript = "script"
)

type Tag struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type EnvVar = loaderdomain.EnvVar

func EnvMap(groups ...[]EnvVar) map[string]string {
	var merged []EnvVar
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

type Summary struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	TriggerSource string    `json:"trigger_source,omitempty"`
	Driver        string    `json:"driver"`
	VMStatus      string    `json:"vm_status"`
	GuestImage    string    `json:"guest_image,omitempty"`
	RuntimeRef    string    `json:"runtime_ref,omitempty"`
	WorkspacePath string    `json:"workspace_path"`
	ProxyPath     string    `json:"proxy_path"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	CellCount     int       `json:"cell_count"`
	EventCount    int       `json:"event_count"`
	Tags          []Tag     `json:"tags,omitempty"`
}

type ListOptions struct {
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

type ListResult struct {
	Sessions   []*Session
	TotalCount int
	HasMore    bool
	NextOffset int
}

type Workspace = agentworkspace.Snapshot

type Session struct {
	Summary          Summary    `json:"summary"`
	BaseWorkspace    string     `json:"base_workspace,omitempty"`
	WorkspaceID      string     `json:"workspace_id,omitempty"`
	Workspace        *Workspace `json:"workspace,omitempty"`
	EnvItems         []EnvVar   `json:"env_items,omitempty"`
	RuntimeEnvItems  []EnvVar   `json:"-"`
	ProviderEnvItems []EnvVar   `json:"-"`
}

func RestoreTransientFields(dst, src *Session) {
	if dst == nil || src == nil {
		return
	}
	if len(src.RuntimeEnvItems) > 0 {
		dst.RuntimeEnvItems = append([]EnvVar(nil), src.RuntimeEnvItems...)
	}
	if len(src.ProviderEnvItems) > 0 {
		dst.ProviderEnvItems = append([]EnvVar(nil), src.ProviderEnvItems...)
	}
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

type Event struct {
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

type ExecChunk = execdomain.Chunk
type ExecResult = execdomain.Result
type ExecSpec = execdomain.Spec
type ExecStreamWriter = execdomain.StreamWriter
type AgentRunResult = execdomain.AgentRunResult

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
