package drivertypes

import (
	"context"
	"fmt"

	appconfig "agent-compose/pkg/config"
	"path/filepath"
	"strings"
	"time"
)

const (
	RuntimeDriverBoxlite      = "boxlite"
	RuntimeDriverDocker       = "docker"
	RuntimeDriverMicrosandbox = "microsandbox"
)

const DirectoryOnlyGuestSessionPath = "/data"

type SessionEnvVar struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"`
	Secret bool   `json:"secret,omitempty"`
}

type SessionSummary struct {
	ID            string    `json:"id"`
	Driver        string    `json:"driver"`
	GuestImage    string    `json:"guest_image,omitempty"`
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

func SessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
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
func sessionEnvMap(groups ...[]SessionEnvVar) map[string]string {
	return SessionEnvMap(groups...)
}

func LLMProviderKeyName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "LLM_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "OPENROUTER_API_KEY", "AZURE_OPENAI_API_KEY", "GOOGLE_API_KEY", "GEMINI_API_KEY":
		return true
	default:
		return false
	}
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func HostSessionDir(session *Session) string {
	if session == nil {
		return ""
	}
	return filepath.Dir(session.Summary.WorkspacePath)
}

func HostSessionHome(session *Session) string {
	return filepath.Join(HostSessionDir(session), "home")
}

func ShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `"'"'"'`) + "'"
}

func DirectoryOnlyGuestSessionBootstrapCommand(config *appconfig.Config) string {
	appconfig.ApplyDefaultGuestPaths(config)
	commands := []string{
		"if [ -d " + ShellQuote(filepath.Join(DirectoryOnlyGuestSessionPath, "workspace")) + " ] && [ -d " + ShellQuote(filepath.Join(DirectoryOnlyGuestSessionPath, "home")) + " ]; then",
	}
	for _, link := range []struct {
		source string
		target string
	}{
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "workspace"), target: config.GuestWorkspacePath},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "state"), target: config.GuestStateRoot},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "runtime"), target: config.GuestRuntimeRoot},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "logs"), target: config.GuestLogRoot},
		{source: filepath.Join(DirectoryOnlyGuestSessionPath, "home"), target: config.GuestHomePath},
	} {
		source := filepath.Clean(link.source)
		target := filepath.Clean(link.target)
		if source == target {
			continue
		}
		commands = append(commands,
			"  rm -rf "+ShellQuote(target)+";",
			"  mkdir -p "+ShellQuote(filepath.Dir(target))+";",
			"  ln -s "+ShellQuote(source)+" "+ShellQuote(target)+";",
		)
	}
	commands = append(commands, "fi")
	return strings.Join(commands, " ")
}

func ResolveRuntimeDriver(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return RuntimeDriverDocker
	case RuntimeDriverBoxlite:
		return RuntimeDriverBoxlite
	case RuntimeDriverDocker, "docker-engine":
		return RuntimeDriverDocker
	case "msb", RuntimeDriverMicrosandbox:
		return RuntimeDriverMicrosandbox
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func ValidateRuntimeDriver(value string) error {
	switch ResolveRuntimeDriver(value) {
	case RuntimeDriverBoxlite, RuntimeDriverDocker, RuntimeDriverMicrosandbox:
		return nil
	default:
		return fmt.Errorf("unsupported agent-compose runtime driver %q", strings.TrimSpace(value))
	}
}

func ResolveSessionRuntimeDriver(value, fallback string) (string, error) {
	input := value
	if strings.TrimSpace(input) == "" {
		input = fallback
	}
	driver := ResolveRuntimeDriver(input)
	if err := ValidateRuntimeDriver(driver); err != nil {
		return "", err
	}
	return driver, nil
}

func DefaultGuestImageForDriver(config *appconfig.Config, driver string) string {
	switch ResolveRuntimeDriver(driver) {
	case RuntimeDriverMicrosandbox:
		return config.MicrosandboxDefaultImage
	case RuntimeDriverDocker:
		return FirstNonEmpty(config.DockerDefaultImage, config.DefaultImage)
	}
	return config.DefaultImage
}

func RuntimeHomeForDriver(config *appconfig.Config, driver string) string {
	switch ResolveRuntimeDriver(driver) {
	case RuntimeDriverMicrosandbox:
		return config.MicrosandboxHome
	case RuntimeDriverDocker:
		return config.DockerHome
	}
	return config.BoxliteHome
}

func ImageCacheRootForDriver(config *appconfig.Config) string {
	if config == nil {
		return filepath.Join(".", "data", "images")
	}
	if root := strings.TrimSpace(config.ImageCacheRoot); root != "" {
		return root
	}
	if dataRoot := strings.TrimSpace(config.DataRoot); dataRoot != "" {
		return filepath.Join(dataRoot, "images")
	}
	return filepath.Join(".", "data", "images")
}
