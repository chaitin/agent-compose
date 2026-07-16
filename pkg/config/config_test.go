package config

import (
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/samber/do/v2"
)

func TestMain(m *testing.M) {
	clearConfigTestEnv()
	os.Exit(m.Run())
}

func clearConfigTestEnv() {
	envFile, err := findConfigTestEnvFile()
	if err != nil {
		return
	}
	values, err := godotenv.Read(envFile)
	if err != nil {
		return
	}
	for key := range values {
		_ = os.Unsetenv(key)
	}
}

func findConfigTestEnvFile() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		path := filepath.Join(dir, ".env")
		if _, err := os.Stat(path); err == nil {
			return path, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}

func TestNewConfigParsesEnvironment(t *testing.T) {
	testNewConfigParsesEnvironment(t)
}

func TestNewConfigNormalizesJupyterProxyBase(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want string
	}{
		{name: "default", raw: "", want: "/jupyter"},
		{name: "trims whitespace", raw: "  /agent-compose/jupyter  ", want: "/agent-compose/jupyter"},
		{name: "adds leading slash", raw: "agent-compose/jupyter", want: "/agent-compose/jupyter"},
		{name: "trims trailing slash", raw: "/agent-compose/jupyter/", want: "/agent-compose/jupyter"},
		{name: "root falls back to default", raw: "/", want: "/jupyter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
			t.Setenv("JUPYTER_PROXY_BASE", tc.raw)

			di := do.New()
			do.ProvideValue(di, slog.Default())
			config, err := NewConfig(di)
			if err != nil {
				t.Fatalf("NewConfig returned error: %v", err)
			}
			if config.JupyterProxyBasePath != tc.want {
				t.Fatalf("JupyterProxyBasePath = %q, want %q", config.JupyterProxyBasePath, tc.want)
			}
		})
	}
}

func TestNewConfigRejectsInvalidCleanupDurations(t *testing.T) {
	for _, test := range []struct {
		name      string
		interval  string
		workspace string
		image     string
	}{
		{name: "negative workspace TTL", workspace: "-1h"},
		{name: "invalid image TTL", image: "tomorrow"},
		{name: "zero interval while enabled", interval: "0", workspace: "1h"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
			t.Setenv("CLEANUP_INTERVAL", test.interval)
			t.Setenv("WORKSPACE_CLEANUP_TTL", test.workspace)
			t.Setenv("IMAGE_CACHE_CLEANUP_TTL", test.image)
			di := do.New()
			do.ProvideValue(di, slog.Default())
			if _, err := NewConfig(di); err == nil {
				t.Fatal("NewConfig returned nil error")
			}
		})
	}
}

func testNewConfigParsesEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("HTTP_LISTEN", "127.0.0.1:9000")
	t.Setenv("AGENT_COMPOSE_AUTH_TOKEN", "daemon-token")
	t.Setenv("AGENT_COMPOSE_SOCKET", filepath.Join(root, "agent-compose.sock"))
	t.Setenv("AGENT_COMPOSE_HOST", "https://agent-compose.example")
	t.Setenv("LLM_API_ENDPOINT", "https://llm.example")
	t.Setenv("LLM_API_PROTOCOL", "chat_completions")
	t.Setenv("LLM_API_KEY", "llm-key")
	t.Setenv("LLM_MODEL", "model-x")
	t.Setenv("LLM_TIMEOUT", "7s")
	t.Setenv("AGENT_TIMEOUT", "8s")
	t.Setenv("RUNTIME_DRIVER", "docker-engine")
	t.Setenv("DEFAULT_IMAGE", "box:latest")
	t.Setenv("DOCKER_DEFAULT_IMAGE", "docker:latest")
	t.Setenv("MICROSANDBOX_INSECURE_REGISTRIES", "one.example, two.example\nthree.example")
	t.Setenv("IMAGE_STORE_MODE", "oci")
	t.Setenv("IMAGE_CACHE_ROOT", filepath.Join(root, "custom-images"))
	t.Setenv("IMAGE_INSECURE_REGISTRIES", "oci-one.example; oci-two.example\noci-three.example")
	t.Setenv("BOX_DISK_SIZE_GB", "11")
	t.Setenv("CACHE_TTL", "2h")
	t.Setenv("CLEANUP_INTERVAL", "30m")
	t.Setenv("WORKSPACE_CLEANUP_TTL", "24h")
	t.Setenv("IMAGE_CACHE_CLEANUP_TTL", "48h")
	t.Setenv("GUEST_WORKSPACE", "/workspace")
	t.Setenv("GUEST_HOME", "/home/test")
	t.Setenv("GUEST_STATE_ROOT", "/state")
	t.Setenv("GUEST_RUNTIME_ROOT", "/runtime")
	t.Setenv("GUEST_LOG_ROOT", "/logs")
	t.Setenv("JUPYTER_GUEST_PORT", "9999")
	t.Setenv("JUPYTER_PROXY_BASE", "/agent-compose/jupyter/")
	t.Setenv("SANDBOX_START_TIMEOUT", "9s")
	t.Setenv("SANDBOX_STOP_TIMEOUT", "10s")
	t.Setenv("JUPYTER_READY_TIMEOUT", "45s")
	t.Setenv("WEBHOOK_BODY_LIMIT_BYTES", "1234")
	t.Setenv("WEBHOOK_QUEUE_RULES_JSON", `[{"name":"repo-a","workers":2,"match":{"topic":"webhook.github.push"}}]`)
	t.Setenv("WEBHOOK_QUEUE_DEFAULT_WORKERS", "6")
	t.Setenv("WORKSPACE_UPLOAD_LIMIT_BYTES", "4321")

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	if config.HttpListen != "127.0.0.1:9000" || config.RuntimeDriver != RuntimeDriverDocker {
		t.Fatalf("listen/driver = %q/%q", config.HttpListen, config.RuntimeDriver)
	}
	if config.DaemonAuthToken != "daemon-token" {
		t.Fatalf("daemon auth token = %q", config.DaemonAuthToken)
	}
	if config.AgentComposeSocket != filepath.Join(root, "agent-compose.sock") || config.AgentComposeHost != "https://agent-compose.example" {
		t.Fatalf("daemon endpoint config = %q/%q", config.AgentComposeSocket, config.AgentComposeHost)
	}
	if config.LLMAPIEndpoint != "https://llm.example" || config.LLMAPIProtocol != "chat_completions" || config.LLMAPIKey != "llm-key" || config.LLMModel != "model-x" {
		t.Fatalf("llm config = %#v", config)
	}
	if config.LLMTimeout != 7*time.Second || config.AgentTimeout != 8*time.Second {
		t.Fatalf("timeouts = %s/%s", config.LLMTimeout, config.AgentTimeout)
	}
	if config.BoxDiskSizeGB != 11 || config.CacheTTL != 2*time.Hour {
		t.Fatalf("box disk/cache = %d/%s", config.BoxDiskSizeGB, config.CacheTTL)
	}
	if config.CleanupInterval != 30*time.Minute || config.WorkspaceCleanupTTL != 24*time.Hour || config.ImageCacheCleanupTTL != 48*time.Hour {
		t.Fatalf("cleanup config = %s/%s/%s", config.CleanupInterval, config.WorkspaceCleanupTTL, config.ImageCacheCleanupTTL)
	}
	if config.DefaultImage != "box:latest" || config.DockerDefaultImage != "docker:latest" || config.MicrosandboxDefaultImage != "box:latest" {
		t.Fatalf("image config = %#v", config)
	}
	if config.ImageStoreMode != ImageStoreModeOCI || config.ImageCacheRoot != filepath.Join(root, "custom-images") {
		t.Fatalf("image store config = %#v", config)
	}
	if config.JupyterGuestPort != 9999 || config.SandboxStartTimeout != 9*time.Second || config.SandboxStopTimeout != 10*time.Second {
		t.Fatalf("session config = %#v", config)
	}
	if config.JupyterReadyTimeout != 45*time.Second {
		t.Fatalf("jupyter ready timeout = %s", config.JupyterReadyTimeout)
	}
	if config.GuestWorkspacePath != "/workspace" || config.GuestHomePath != "/root" || config.GuestStateRoot != "/state" || config.GuestRuntimeRoot != "/runtime" || config.GuestLogRoot != "/logs" {
		t.Fatalf("guest paths = %#v", config)
	}
	if config.WebhookBodyLimitBytes != 1234 || config.WorkspaceUploadLimitBytes != 4321 {
		t.Fatalf("limits = %d/%d", config.WebhookBodyLimitBytes, config.WorkspaceUploadLimitBytes)
	}
	if config.WebhookQueueRulesJSON == "" || config.WebhookQueueDefaultWorkers != 6 {
		t.Fatalf("webhook queue config = %q/%d", config.WebhookQueueRulesJSON, config.WebhookQueueDefaultWorkers)
	}
	if config.JupyterProxyBasePath != "/agent-compose/jupyter" {
		t.Fatalf("jupyter proxy base path = %q", config.JupyterProxyBasePath)
	}
	if got := strings.Join(config.MicrosandboxInsecure, "|"); got != "one.example|two.example|three.example" {
		t.Fatalf("microsandbox insecure = %q", got)
	}
	if got := strings.Join(config.ImageInsecureRegistries, "|"); got != "oci-one.example|oci-two.example|oci-three.example" {
		t.Fatalf("image insecure registries = %q", got)
	}
}

func TestNewConfigAllowsDefaultRootsAndRequiresValidDriver(t *testing.T) {
	testNewConfigAllowsDefaultRootsAndRequiresValidDriver(t)
}

func testNewConfigAllowsDefaultRootsAndRequiresValidDriver(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error for default roots: %v", err)
	}
	if config.SandboxRoot != filepath.Join(root, "data", "sandboxes") {
		t.Fatalf("SandboxRoot = %q, want default sandboxes root", config.SandboxRoot)
	}
	if config.SandboxRootExplicit {
		t.Fatalf("SandboxRootExplicit = true, want false for default root")
	}

	t.Setenv("RUNTIME_DRIVER", "bad-driver")
	if _, err := NewConfig(di); err == nil {
		t.Fatalf("expected invalid runtime driver to fail")
	}
}

func TestNewConfigAcceptsLegacySessionEnvironment(t *testing.T) {
	tests := []struct {
		name   string
		legacy string
		value  string
		check  func(*Config) bool
	}{
		{name: "root", legacy: "SESSION_ROOT", value: filepath.Join("legacy", "sessions"), check: func(c *Config) bool { return strings.HasSuffix(c.SandboxRoot, filepath.Join("legacy", "sessions")) }},
		{name: "docker host root", legacy: "DOCKER_HOST_SESSION_ROOT", value: filepath.Join("legacy", "host-sessions"), check: func(c *Config) bool {
			return strings.HasSuffix(c.DockerHostSandboxRoot, filepath.Join("legacy", "host-sessions"))
		}},
		{name: "start timeout", legacy: "SESSION_START_TIMEOUT", value: "1s", check: func(c *Config) bool { return c.SandboxStartTimeout == time.Second }},
		{name: "stop timeout", legacy: "SESSION_STOP_TIMEOUT", value: "1s", check: func(c *Config) bool { return c.SandboxStopTimeout == time.Second }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
			t.Setenv(tc.legacy, tc.value)
			var logs strings.Builder
			di := do.New()
			do.ProvideValue(di, slog.New(slog.NewTextHandler(&logs, nil)))
			config, err := NewConfig(di)
			if err != nil || !tc.check(config) {
				t.Fatalf("NewConfig config=%#v error=%v for legacy %s", config, err, tc.legacy)
			}
			if !strings.Contains(logs.String(), "using deprecated environment variable") || !strings.Contains(logs.String(), tc.legacy) {
				t.Fatalf("logs = %q, want deprecation warning for %s", logs.String(), tc.legacy)
			}
		})
	}
}

func TestNewConfigUsesNonEmptyLegacySessionsRootByDefault(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data")
	legacyRoot := filepath.Join(dataRoot, "sessions")
	if err := os.MkdirAll(filepath.Join(legacyRoot, "legacy-id"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATA_ROOT", dataRoot)
	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}
	if config.SandboxRoot != legacyRoot || config.SandboxRootExplicit {
		t.Fatalf("legacy root config = %#v", config)
	}
}

func TestNewConfigIgnoresLegacySessionsRootFile(t *testing.T) {
	dataRoot := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dataRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(dataRoot, "sessions")
	if err := os.WriteFile(legacyRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("DATA_ROOT", dataRoot)
	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}
	want := filepath.Join(dataRoot, "sandboxes")
	if config.SandboxRoot != want || config.SandboxRootExplicit {
		t.Fatalf("SandboxRoot = %q, explicit = %t; want %q, false", config.SandboxRoot, config.SandboxRootExplicit, want)
	}
}

func TestNewConfigUsesSandboxEnvironmentWhenLegacyAlsoSet(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("SESSION_ROOT", filepath.Join(root, "legacy-sessions"))
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "new-sandboxes"))
	t.Setenv("DOCKER_HOST_SESSION_ROOT", filepath.Join(root, "legacy-host-sessions"))
	t.Setenv("DOCKER_HOST_SANDBOX_ROOT", filepath.Join(root, "new-host-sandboxes"))
	t.Setenv("SESSION_START_TIMEOUT", "1s")
	t.Setenv("SANDBOX_START_TIMEOUT", "2s")
	t.Setenv("SESSION_STOP_TIMEOUT", "3s")
	t.Setenv("SANDBOX_STOP_TIMEOUT", "4s")

	var logs strings.Builder
	logger := slog.New(slog.NewTextHandler(&logs, nil))
	di := do.New()
	do.ProvideValue(di, logger)
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}
	if config.SandboxRoot != filepath.Join(root, "new-sandboxes") ||
		config.DockerHostSandboxRoot != filepath.Join(root, "new-host-sandboxes") ||
		config.SandboxStartTimeout != 2*time.Second ||
		config.SandboxStopTimeout != 4*time.Second {
		t.Fatalf("sandbox env config = %#v", config)
	}
	if !config.SandboxRootExplicit {
		t.Fatalf("SandboxRootExplicit = false, want true when SANDBOX_ROOT is set")
	}
	for _, legacy := range []string{"SESSION_ROOT", "DOCKER_HOST_SESSION_ROOT", "SESSION_START_TIMEOUT", "SESSION_STOP_TIMEOUT"} {
		if !strings.Contains(logs.String(), "deprecated environment variable ignored") || !strings.Contains(logs.String(), legacy) {
			t.Fatalf("logs = %q, want warning for ignored %s", logs.String(), legacy)
		}
	}
}

func TestNewConfigJupyterReadyTimeoutDefaultAndGuard(t *testing.T) {
	cases := []struct {
		name  string
		value string // "" means unset
		want  time.Duration
	}{
		{"default when unset", "", 30 * time.Second},
		{"custom value honored", "90s", 90 * time.Second},
		{"zero falls back to default", "0s", 30 * time.Second},
		{"negative falls back to default", "-5s", 30 * time.Second},
		{"invalid falls back to default", "not-a-duration", 30 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DATA_ROOT", filepath.Join(t.TempDir(), "data"))
			t.Setenv("JUPYTER_READY_TIMEOUT", tc.value)
			di := do.New()
			do.ProvideValue(di, slog.Default())
			config, err := NewConfig(di)
			if err != nil {
				t.Fatalf("NewConfig returned error: %v", err)
			}
			if config.JupyterReadyTimeout != tc.want {
				t.Fatalf("JupyterReadyTimeout = %s, want %s", config.JupyterReadyTimeout, tc.want)
			}
		})
	}
}

func TestNewConfigDefaultsDaemonListenConfig(t *testing.T) {
	testNewConfigDefaultsDaemonListenConfig(t)
}

func TestNewConfigDefaultsDaemonSocketToVarRunWithoutRuntimeDir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	if config.AgentComposeSocket != DefaultAgentComposeSocketPath {
		t.Fatalf("AgentComposeSocket = %q, want %q", config.AgentComposeSocket, DefaultAgentComposeSocketPath)
	}
}

func testNewConfigDefaultsDaemonListenConfig(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "runtime")
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("XDG_RUNTIME_DIR", runtimeDir)
	t.Setenv("HTTP_LISTEN", "")
	t.Setenv("AGENT_COMPOSE_SOCKET", "")
	t.Setenv("AGENT_COMPOSE_HOST", "")

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	if config.HttpListen != "" {
		t.Fatalf("HttpListen = %q, want empty by default", config.HttpListen)
	}
	wantSocket := filepath.Join(runtimeDir, "agent-compose.sock")
	if config.AgentComposeSocket != wantSocket {
		t.Fatalf("AgentComposeSocket = %q, want %q", config.AgentComposeSocket, wantSocket)
	}
	if config.AgentComposeHost != "" {
		t.Fatalf("AgentComposeHost = %q, want empty", config.AgentComposeHost)
	}
}

func TestNewConfigUsesExplicitDaemonSocket(t *testing.T) {
	testNewConfigUsesExplicitDaemonSocket(t)
}

func testNewConfigUsesExplicitDaemonSocket(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	socketPath := filepath.Join(root, "custom", "agent-compose.sock")
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("AGENT_COMPOSE_SOCKET", socketPath)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}
	if config.AgentComposeSocket != socketPath {
		t.Fatalf("AgentComposeSocket = %q, want %q", config.AgentComposeSocket, socketPath)
	}
}

func TestNewConfigEnablesTCPOnlyWhenHTTPListenIsExplicit(t *testing.T) {
	testNewConfigEnablesTCPOnlyWhenHTTPListenIsExplicit(t)
}

func testNewConfigEnablesTCPOnlyWhenHTTPListenIsExplicit(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("HTTP_LISTEN", "127.0.0.1:9100")

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}
	if config.HttpListen != "127.0.0.1:9100" {
		t.Fatalf("HttpListen = %q, want explicit TCP listen", config.HttpListen)
	}
}

func TestNewConfigWarnsForPublicHTTPListen(t *testing.T) {
	testNewConfigWarnsForPublicHTTPListen(t)
}

func testNewConfigWarnsForPublicHTTPListen(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name     string
		listen   string
		env      map[string]string
		wantWarn bool
	}{
		{
			name:     "public listen without auth warns",
			listen:   "0.0.0.0:7410",
			wantWarn: true,
		},
		{
			name:     "all IPv6 listen without auth warns",
			listen:   "[::]:7410",
			wantWarn: true,
		},
		{
			name:   "loopback listen without auth is allowed",
			listen: "127.0.0.1:7410",
		},
		{
			name:   "localhost listen without auth is allowed",
			listen: "localhost:7410",
		},
		{
			name:   "browser auth env no longer secures daemon listen",
			listen: "0.0.0.0:7410",
			env: map[string]string{
				"AUTH_PASSWORD": "secret",
				"AUTH_SECRET":   "auth-secret",
			},
			wantWarn: true,
		},
		{
			name:   "oauth env no longer secures daemon listen",
			listen: "0.0.0.0:7410",
			env: map[string]string{
				"OAUTH_APIKEY":       "client-id",
				"OAUTH_CALLBACK_URL": "http://localhost:7410/oauth/callback",
			},
			wantWarn: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
			t.Setenv("HTTP_LISTEN", tc.listen)
			for key, value := range tc.env {
				t.Setenv(key, value)
			}

			var logs strings.Builder
			logger := slog.New(slog.NewTextHandler(&logs, nil))
			di := do.New()
			do.ProvideValue(di, logger)
			_, err := NewConfig(di)
			if err != nil {
				t.Fatalf("NewConfig returned error: %v", err)
			}
			hasWarning := strings.Contains(logs.String(), "HTTP_LISTEN exposes the daemon")
			if hasWarning != tc.wantWarn {
				t.Fatalf("warning present = %t, want %t, logs = %q", hasWarning, tc.wantWarn, logs.String())
			}
		})
	}
}

func TestNewConfigRejectsInvalidDaemonAddresses(t *testing.T) {
	testNewConfigRejectsInvalidDaemonAddresses(t)
}

func testNewConfigRejectsInvalidDaemonAddresses(t *testing.T) {
	t.Helper()
	for _, tc := range []struct {
		name      string
		envName   string
		envValue  string
		wantParts []string
	}{
		{
			name:      "http listen missing port",
			envName:   "HTTP_LISTEN",
			envValue:  "127.0.0.1",
			wantParts: []string{"HTTP_LISTEN", "127.0.0.1"},
		},
		{
			name:      "agent compose host missing scheme",
			envName:   "AGENT_COMPOSE_HOST",
			envValue:  "127.0.0.1:7410",
			wantParts: []string{"AGENT_COMPOSE_HOST", "127.0.0.1:7410"},
		},
		{
			name:      "agent compose host unsupported scheme",
			envName:   "AGENT_COMPOSE_HOST",
			envValue:  "ftp://127.0.0.1:7410",
			wantParts: []string{"AGENT_COMPOSE_HOST", "ftp://127.0.0.1:7410"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
			t.Setenv(tc.envName, tc.envValue)

			di := do.New()
			do.ProvideValue(di, slog.Default())
			_, err := NewConfig(di)
			if err == nil {
				t.Fatal("NewConfig returned nil error, want invalid daemon address error")
			}
			for _, part := range tc.wantParts {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error %q does not contain %q", err.Error(), part)
				}
			}
		})
	}
}

func TestNewConfigDefaultsImagesFromDefaultImage(t *testing.T) {
	testNewConfigDefaultsImagesFromDefaultImage(t)
}

func testNewConfigDefaultsImagesFromDefaultImage(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	if config.DefaultImage != "debian:bookworm-slim" || config.DockerDefaultImage != "debian:bookworm-slim" || config.MicrosandboxDefaultImage != "debian:bookworm-slim" {
		t.Fatalf("default image config = %#v", config)
	}
	if config.ImageStoreMode != ImageStoreModeAuto {
		t.Fatalf("ImageStoreMode = %q, want %q", config.ImageStoreMode, ImageStoreModeAuto)
	}
	if config.ImageCacheRoot != filepath.Join(root, "data", "images") {
		t.Fatalf("ImageCacheRoot = %q, want %q", config.ImageCacheRoot, filepath.Join(root, "data", "images"))
	}
	if len(config.ImageInsecureRegistries) != 0 {
		t.Fatalf("ImageInsecureRegistries = %#v, want empty", config.ImageInsecureRegistries)
	}
}

func TestNewConfigRejectsInvalidImageStoreMode(t *testing.T) {
	testNewConfigRejectsInvalidImageStoreMode(t)
}

func testNewConfigRejectsInvalidImageStoreMode(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("IMAGE_STORE_MODE", "podman")

	di := do.New()
	do.ProvideValue(di, slog.Default())
	_, err := NewConfig(di)
	if err == nil {
		t.Fatal("NewConfig returned nil error, want invalid IMAGE_STORE_MODE error")
	}
	if !strings.Contains(err.Error(), "IMAGE_STORE_MODE") || !strings.Contains(err.Error(), "podman") {
		t.Fatalf("error %q does not mention invalid IMAGE_STORE_MODE", err.Error())
	}
}

func TestNewConfigDefaultsDataRootFromXDGDataHome(t *testing.T) {
	testNewConfigDefaultsDataRootFromXDGDataHome(t)
}

func testNewConfigDefaultsDataRootFromXDGDataHome(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("DATA_ROOT", "")
	t.Setenv("DATA_ROOT", "")
	t.Setenv("XDG_DATA_HOME", filepath.Join(root, "xdg-data"))

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	want := filepath.Join(root, "xdg-data", "agent-compose")
	if config.DataRoot != want {
		t.Fatalf("DataRoot = %q, want %q", config.DataRoot, want)
	}
}

func TestNewConfigEnsuresHostDirectoriesExist(t *testing.T) {
	testNewConfigEnsuresHostDirectoriesExist(t)
}

func testNewConfigEnsuresHostDirectoriesExist(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data")
	dockerHostSandboxRoot := filepath.Join(root, "host-sandboxes")
	t.Setenv("DATA_ROOT", dataRoot)
	t.Setenv("DOCKER_HOST_SANDBOX_ROOT", dockerHostSandboxRoot)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	for name, dir := range map[string]string{
		"DataRoot":         config.DataRoot,
		"SandboxRoot":      config.SandboxRoot,
		"BoxliteHome":      config.BoxliteHome,
		"DockerHome":       config.DockerHome,
		"ImageCacheRoot":   config.ImageCacheRoot,
		"MicrosandboxHome": config.MicrosandboxHome,
	} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Fatalf("%s was not created at %s: %v", name, dir, err)
		}
		if !info.IsDir() {
			t.Fatalf("%s path %s is not a directory", name, dir)
		}
	}
	if config.DockerHostSandboxRoot != dockerHostSandboxRoot {
		t.Fatalf("DockerHostSandboxRoot = %q, want %q", config.DockerHostSandboxRoot, dockerHostSandboxRoot)
	}
}

func TestNewConfigPreservesWindowsDockerHostSandboxRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("DOCKER_HOST_SANDBOX_ROOT", `E:/program/agent-compose-main/data/agent-compose/sandboxes`)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	want := `E:/program/agent-compose-main/data/agent-compose/sandboxes`
	if config.DockerHostSandboxRoot != want {
		t.Fatalf("DockerHostSandboxRoot = %q, want %q", config.DockerHostSandboxRoot, want)
	}
}

func TestNewConfigCacheTTL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		want    time.Duration
		wantErr bool
	}{
		{name: "default", want: 168 * time.Hour},
		{name: "disabled", value: "0", want: 0},
		{name: "custom", value: "24h", want: 24 * time.Hour},
		{name: "negative", value: "-1h", wantErr: true},
		{name: "invalid", value: "seven days", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("XDG_DATA_HOME", root)
			t.Setenv("CACHE_TTL", tc.value)
			di := do.New()
			do.ProvideValue(di, slog.Default())
			config, err := NewConfig(di)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("NewConfig CACHE_TTL=%q returned nil error", tc.value)
				}
				return
			}
			if err != nil || config.CacheTTL != tc.want {
				t.Fatalf("NewConfig CACHE_TTL=%q = %v err=%v, want %v", tc.value, config.CacheTTL, err, tc.want)
			}
		})
	}
}

func TestNewConfigCacheTTLAcceptsDeprecatedBoxCacheTTL(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	t.Setenv("BOX_CACHE_TTL", "12h")
	var logs strings.Builder
	di := do.New()
	do.ProvideValue(di, slog.New(slog.NewTextHandler(&logs, nil)))

	config, err := NewConfig(di)
	if err != nil || config.CacheTTL != 12*time.Hour {
		t.Fatalf("NewConfig CacheTTL = %v, err=%v; want 12h", config.CacheTTL, err)
	}
	if !strings.Contains(logs.String(), "using deprecated environment variable") || !strings.Contains(logs.String(), "BOX_CACHE_TTL") {
		t.Fatalf("logs = %q, want BOX_CACHE_TTL deprecation warning", logs.String())
	}
}

func TestNewConfigCacheTTLPrecedesDeprecatedBoxCacheTTL(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	t.Setenv("CACHE_TTL", "2h")
	t.Setenv("BOX_CACHE_TTL", "12h")
	var logs strings.Builder
	di := do.New()
	do.ProvideValue(di, slog.New(slog.NewTextHandler(&logs, nil)))

	config, err := NewConfig(di)
	if err != nil || config.CacheTTL != 2*time.Hour {
		t.Fatalf("NewConfig CacheTTL = %v, err=%v; want 2h", config.CacheTTL, err)
	}
	if !strings.Contains(logs.String(), "deprecated environment variable ignored") || !strings.Contains(logs.String(), "BOX_CACHE_TTL") {
		t.Fatalf("logs = %q, want ignored BOX_CACHE_TTL warning", logs.String())
	}
}

func TestNewConfigPreservesUNCDockerHostSandboxRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("DOCKER_HOST_SANDBOX_ROOT", `\\server\share\agent-compose\sandboxes`)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	want := `\\server\share\agent-compose\sandboxes`
	if config.DockerHostSandboxRoot != want {
		t.Fatalf("DockerHostSandboxRoot = %q, want %q", config.DockerHostSandboxRoot, want)
	}
}

func TestNewConfigRejectsWindowsDockerHostSandboxRootParentSegment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("DOCKER_HOST_SANDBOX_ROOT", `E:\program\..\agent-compose\sandboxes`)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	if _, err := NewConfig(di); err == nil || !strings.Contains(err.Error(), "DOCKER_HOST_SANDBOX_ROOT") {
		t.Fatalf("NewConfig error = %v, want DOCKER_HOST_SANDBOX_ROOT validation error", err)
	}
}

func TestNewConfigRejectsFileDataRoot(t *testing.T) {
	testNewConfigRejectsFileDataRoot(t)
}

func testNewConfigRejectsFileDataRoot(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	dataRoot := filepath.Join(root, "data-file")
	if err := os.WriteFile(dataRoot, []byte("not a dir\n"), 0o644); err != nil {
		t.Fatalf("write data root file: %v", err)
	}
	t.Setenv("DATA_ROOT", dataRoot)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	if _, err := NewConfig(di); err == nil {
		t.Fatalf("expected file data root to fail")
	}
}

func TestDefaultDataRootFallsBackToHome(t *testing.T) {
	testDefaultDataRootFallsBackToHome(t)
}

func testDefaultDataRootFallsBackToHome(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_DATA_HOME", "")

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = "."
	}
	want := filepath.Join(home, ".local", "share", "agent-compose")
	if got := defaultDataRoot(); got != want {
		t.Fatalf("defaultDataRoot = %q, want %q", got, want)
	}
}

func TestBoxDiskSizeGB(t *testing.T) {
	newCfg := func(t *testing.T) *Config {
		t.Helper()
		root := t.TempDir()
		t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
		di := do.New()
		do.ProvideValue(di, slog.Default())
		cfg, err := NewConfig(di)
		if err != nil {
			t.Fatalf("NewConfig returned error: %v", err)
		}
		return cfg
	}

	t.Run("default is 6 for all VM-type drivers", func(t *testing.T) {
		cfg := newCfg(t)
		if cfg.BoxDiskSizeGB != 6 {
			t.Fatalf("BoxDiskSizeGB = %d, want 6 (default)", cfg.BoxDiskSizeGB)
		}
	})

	t.Run("BOX_DISK_SIZE_GB sets the value", func(t *testing.T) {
		t.Setenv("BOX_DISK_SIZE_GB", "11")
		cfg := newCfg(t)
		if cfg.BoxDiskSizeGB != 11 {
			t.Fatalf("BoxDiskSizeGB = %d, want 11", cfg.BoxDiskSizeGB)
		}
	})

	t.Run("invalid value keeps the default", func(t *testing.T) {
		t.Setenv("BOX_DISK_SIZE_GB", "abc")
		cfg := newCfg(t)
		if cfg.BoxDiskSizeGB != 6 {
			t.Fatalf("BoxDiskSizeGB = %d, want 6", cfg.BoxDiskSizeGB)
		}
	})

	t.Run("non-positive value keeps the default", func(t *testing.T) {
		t.Setenv("BOX_DISK_SIZE_GB", "-3")
		cfg := newCfg(t)
		if cfg.BoxDiskSizeGB != 6 {
			t.Fatalf("BoxDiskSizeGB = %d, want 6", cfg.BoxDiskSizeGB)
		}
	})
}

func TestMicrosandboxBindQuotaGB(t *testing.T) {
	newCfg := func(t *testing.T) *Config {
		t.Helper()
		root := t.TempDir()
		t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
		di := do.New()
		do.ProvideValue(di, slog.Default())
		cfg, err := NewConfig(di)
		if err != nil {
			t.Fatalf("NewConfig returned error: %v", err)
		}
		return cfg
	}

	t.Run("defaults to box disk size", func(t *testing.T) {
		cfg := newCfg(t)
		if cfg.MicrosandboxBindQuotaGB != 6 {
			t.Fatalf("MicrosandboxBindQuotaGB = %d, want 6", cfg.MicrosandboxBindQuotaGB)
		}
	})

	t.Run("follows BOX_DISK_SIZE_GB", func(t *testing.T) {
		t.Setenv("BOX_DISK_SIZE_GB", "60")
		cfg := newCfg(t)
		if cfg.MicrosandboxBindQuotaGB != 60 {
			t.Fatalf("MicrosandboxBindQuotaGB = %d, want 60", cfg.MicrosandboxBindQuotaGB)
		}
	})

	t.Run("override sets the value", func(t *testing.T) {
		t.Setenv("BOX_DISK_SIZE_GB", "60")
		t.Setenv("MICROSANDBOX_BIND_QUOTA_GB", "96")
		cfg := newCfg(t)
		if cfg.MicrosandboxBindQuotaGB != 96 {
			t.Fatalf("MicrosandboxBindQuotaGB = %d, want 96", cfg.MicrosandboxBindQuotaGB)
		}
	})

	t.Run("invalid override keeps the inherited default", func(t *testing.T) {
		t.Setenv("BOX_DISK_SIZE_GB", "60")
		t.Setenv("MICROSANDBOX_BIND_QUOTA_GB", "abc")
		cfg := newCfg(t)
		if cfg.MicrosandboxBindQuotaGB != 60 {
			t.Fatalf("MicrosandboxBindQuotaGB = %d, want 60", cfg.MicrosandboxBindQuotaGB)
		}
	})

	t.Run("non-positive override keeps the inherited default", func(t *testing.T) {
		t.Setenv("BOX_DISK_SIZE_GB", "60")
		t.Setenv("MICROSANDBOX_BIND_QUOTA_GB", "0")
		cfg := newCfg(t)
		if cfg.MicrosandboxBindQuotaGB != 60 {
			t.Fatalf("MicrosandboxBindQuotaGB = %d, want 60", cfg.MicrosandboxBindQuotaGB)
		}
	})
}
