package driver

import (
	"context"
	"path/filepath"
	"strings"
	"time"
)

type SessionEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type SessionSummary struct {
	ID            string    `json:"id"`
	Driver        string    `json:"driver"`
	GuestImage    string    `json:"guest_image,omitempty"`
	PullPolicy    string    `json:"pull_policy,omitempty"`
	RuntimeRef    string    `json:"runtime_ref,omitempty"`
	WorkspacePath string    `json:"workspace_path"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type Session struct {
	Summary         SessionSummary  `json:"summary"`
	EnvItems        []SessionEnvVar `json:"env_items,omitempty"`
	RuntimeEnvItems []SessionEnvVar `json:"-"`
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

type ExecChunk struct {
	Text     string
	IsStderr bool
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

type ExecStreamWriter func(ExecChunk)

type SessionVMInfo struct {
	BoxID      string
	JupyterURL string
	ProxyState *ProxyState
}

type BoxRuntime interface {
	EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error)
	StopSession(context.Context, *Session, VMState) (bool, error)
	Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error)
	ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error)
}

func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
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

func hostSessionDir(session *Session) string {
	if session == nil {
		return ""
	}
	return filepath.Dir(session.Summary.WorkspacePath)
}

func hostSessionHome(session *Session) string {
	return filepath.Join(hostSessionDir(session), "home")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `"'"'"'`) + "'"
}
