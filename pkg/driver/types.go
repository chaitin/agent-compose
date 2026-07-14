package driver

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

type SandboxEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type SandboxSummary struct {
	ID            string    `json:"id"`
	Driver        string    `json:"driver"`
	GuestImage    string    `json:"guest_image,omitempty"`
	PullPolicy    string    `json:"pull_policy,omitempty"`
	RuntimeRef    string    `json:"runtime_ref,omitempty"`
	WorkspacePath string    `json:"workspace_path"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Sandbox struct {
	Summary         SandboxSummary       `json:"summary"`
	EnvItems        []SandboxEnvVar      `json:"env_items,omitempty"`
	VolumeMounts    []SandboxVolumeMount `json:"volume_mounts,omitempty"`
	Network         *SandboxNetwork      `json:"network,omitempty"`
	RuntimeEnvItems []SandboxEnvVar      `json:"-"`
}

type SandboxNetwork struct {
	Attachments []SandboxNetworkEndpoint `json:"attachments,omitempty"`
	Bindings    []SandboxPortBinding     `json:"bindings,omitempty"`
}

type SandboxNetworkEndpoint struct {
	Name               string `json:"name"`
	RuntimeNetworkName string `json:"runtime_network_name"`
}

type SandboxPortBinding struct {
	Networks   []string `json:"networks,omitempty"`
	HostIP     string   `json:"host_ip"`
	HostPort   int      `json:"host_port"`
	GuestPort  int      `json:"guest_port"`
	Protocol   string   `json:"protocol"`
	Visibility string   `json:"visibility"`
	Publisher  string   `json:"publisher"`
}

type SandboxVolumeMount struct {
	ID       string `json:"id,omitempty"`
	Type     string `json:"type"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	ReadOnly bool   `json:"read_only,omitempty"`
	VolumeID string `json:"volume_id,omitempty"`
	Driver   string `json:"driver,omitempty"`
	HostPath string `json:"host_path"`
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
	Enabled    bool   `json:"enabled,omitempty"`
	Exposed    bool   `json:"exposed,omitempty"`
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

type ExecSpec struct {
	Command string            `json:"command"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	Cwd     string            `json:"cwd,omitempty"`
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
	Value   *float64
	Unit    string
	Status  string
	Message string
}

type SandboxStats struct {
	SandboxID        string
	Driver           string
	SampledAt        time.Time
	CPUPercent       MetricValue
	MemoryUsageBytes MetricValue
	MemoryLimitBytes MetricValue
	MemoryPercent    MetricValue
	NetworkRxBytes   MetricValue
	NetworkTxBytes   MetricValue
	BlockReadBytes   MetricValue
	BlockWriteBytes  MetricValue
	UptimeSeconds    MetricValue
}

type ExecStreamWriter func(ExecChunk)

type SandboxVMInfo struct {
	BoxID      string
	JupyterURL string
	ProxyState *ProxyState
}

type SandboxRuntime interface {
	EnsureSandbox(context.Context, *Sandbox, VMState, ProxyState) (SandboxVMInfo, error)
	StopSandbox(context.Context, *Sandbox, VMState) (bool, error)
	RemoveSandbox(context.Context, *Sandbox, VMState) error
	Exec(context.Context, *Sandbox, VMState, ExecSpec) (ExecResult, error)
	ExecStream(context.Context, *Sandbox, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error)
}

func sandboxEnvMap(groups ...[]SandboxEnvVar) map[string]string {
	if len(groups) == 0 {
		return nil
	}
	env := make(map[string]string)
	for groupIndex, items := range groups {
		for _, item := range items {
			name := strings.TrimSpace(item.Name)
			if name == "" || (groupIndex == 0 && LLMProviderKeyName(name)) {
				continue
			}
			env[name] = item.Value
		}
	}
	if len(env) == 0 {
		return nil
	}
	return env
}

// LLMProviderKeyName reports whether name is a long-lived LLM provider credential
// that must never be passed through to a guest runtime. It is the canonical
// denylist shared by the driver env assembly and the agent-compose facade layer.
func LLMProviderKeyName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "LLM_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENROUTER_API_KEY", "AZURE_OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY":
		return true
	default:
		return false
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func hostSandboxDir(session *Sandbox) string {
	if session == nil {
		return ""
	}
	return filepath.Dir(session.Summary.WorkspacePath)
}

func hostSandboxHome(session *Sandbox) string {
	return filepath.Join(hostSandboxDir(session), "home")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `"'"'"'`) + "'"
}
