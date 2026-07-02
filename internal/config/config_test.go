package config

import (
	"encoding/base64"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"
)

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

func testNewConfigParsesEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("HTTP_LISTEN", "127.0.0.1:9000")
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
	t.Setenv("BOX_CACHE_TTL", "2h")
	t.Setenv("GUEST_WORKSPACE", "/workspace")
	t.Setenv("GUEST_HOME", "/home/test")
	t.Setenv("GUEST_STATE_ROOT", "/state")
	t.Setenv("GUEST_RUNTIME_ROOT", "/runtime")
	t.Setenv("GUEST_LOG_ROOT", "/logs")
	t.Setenv("JUPYTER_GUEST_PORT", "9999")
	t.Setenv("JUPYTER_PROXY_BASE", "/agent-compose/jupyter/")
	t.Setenv("SESSION_START_TIMEOUT", "9s")
	t.Setenv("SESSION_STOP_TIMEOUT", "10s")
	t.Setenv("HTTP_BASIC_AUTH", base64.StdEncoding.EncodeToString([]byte("user:pass")))
	t.Setenv("WEBHOOK_BODY_LIMIT_BYTES", "1234")
	t.Setenv("WEBHOOK_QUEUE_RULES_JSON", `[{"name":"repo-a","workers":2,"match":{"topic":"webhook.github.push"}}]`)
	t.Setenv("WEBHOOK_QUEUE_DEFAULT_WORKERS", "6")
	t.Setenv("WORKSPACE_UPLOAD_LIMIT_BYTES", "4321")
	t.Setenv("AUTH_USERNAME", "root")
	t.Setenv("AUTH_PASSWORD", "secret")
	t.Setenv("AUTH_SECRET", "auth-secret")
	t.Setenv("AUTH_SESSION_TTL", "3h")
	t.Setenv("OAUTH_APIKEY", "oauth-client")
	t.Setenv("OAUTH_SECRET", "oauth-secret")
	t.Setenv("OAUTH_SCOPES", "profile,email")
	t.Setenv("OAUTH_CALLBACK_URL", "http://localhost:7410/oauth/callback")

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	if config.HttpListen != "127.0.0.1:9000" || config.RuntimeDriver != RuntimeDriverDocker {
		t.Fatalf("listen/driver = %q/%q", config.HttpListen, config.RuntimeDriver)
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
	if config.BoxDiskSizeGB != 11 || config.BoxCacheTTL != 2*time.Hour {
		t.Fatalf("box disk/cache = %d/%s", config.BoxDiskSizeGB, config.BoxCacheTTL)
	}
	if config.DefaultImage != "box:latest" || config.DockerDefaultImage != "docker:latest" || config.MicrosandboxDefaultImage != "box:latest" {
		t.Fatalf("image config = %#v", config)
	}
	if config.ImageStoreMode != ImageStoreModeOCI || config.ImageCacheRoot != filepath.Join(root, "custom-images") {
		t.Fatalf("image store config = %#v", config)
	}
	if config.JupyterGuestPort != 9999 || config.SessionStartTimeout != 9*time.Second || config.SessionStopTimeout != 10*time.Second {
		t.Fatalf("session config = %#v", config)
	}
	if config.GuestWorkspacePath != "/workspace" || config.GuestHomePath != "/root" || config.GuestStateRoot != "/state" || config.GuestRuntimeRoot != "/runtime" || config.GuestLogRoot != "/logs" {
		t.Fatalf("guest paths = %#v", config)
	}
	if config.HTTPBasicAuth != "user:pass" {
		t.Fatalf("auth = %q", config.HTTPBasicAuth)
	}
	if config.WebhookBodyLimitBytes != 1234 || config.WorkspaceUploadLimitBytes != 4321 {
		t.Fatalf("limits = %d/%d", config.WebhookBodyLimitBytes, config.WorkspaceUploadLimitBytes)
	}
	if config.WebhookQueueRulesJSON == "" || config.WebhookQueueDefaultWorkers != 6 {
		t.Fatalf("webhook queue config = %q/%d", config.WebhookQueueRulesJSON, config.WebhookQueueDefaultWorkers)
	}
	if config.AuthUsername != "root" || config.AuthPassword != "secret" || config.AuthSecret != "auth-secret" || config.AuthSessionTTL != 3*time.Hour {
		t.Fatalf("auth config = %#v", config)
	}
	if config.OAuthAPIKey != "oauth-client" || config.OAuthSecret != "oauth-secret" || config.OAuthCallbackURL != "http://localhost:7410/oauth/callback" {
		t.Fatalf("oauth config = %#v", config)
	}
	if config.OAuthAuthURL != "/oauth2/auth" || config.OAuthTokenURL != "/oauth2/token" || config.OAuthUserInfoURL != "/userinfo" {
		t.Fatalf("oauth endpoint config = %#v", config)
	}
	if config.JupyterProxyBasePath != "/agent-compose/jupyter" {
		t.Fatalf("jupyter proxy base path = %q", config.JupyterProxyBasePath)
	}
	if config.OAuthClientAuthMethod != "client_secret_post" {
		t.Fatalf("oauth client auth method = %q", config.OAuthClientAuthMethod)
	}
	if got := strings.Join(config.OAuthScopes, "|"); got != "profile|email" {
		t.Fatalf("oauth scopes = %q", got)
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
	di := do.New()
	do.ProvideValue(di, slog.Default())
	if _, err := NewConfig(di); err != nil {
		t.Fatalf("NewConfig returned error for default roots: %v", err)
	}

	t.Setenv("RUNTIME_DRIVER", "bad-driver")
	if _, err := NewConfig(di); err == nil {
		t.Fatalf("expected invalid runtime driver to fail")
	}
}

func TestNewConfigDefaultsDaemonListenConfig(t *testing.T) {
	testNewConfigDefaultsDaemonListenConfig(t)
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
	dockerHostSessionRoot := filepath.Join(root, "host-sessions")
	t.Setenv("DATA_ROOT", dataRoot)
	t.Setenv("DOCKER_HOST_SESSION_ROOT", dockerHostSessionRoot)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	for name, dir := range map[string]string{
		"DataRoot":         config.DataRoot,
		"SessionRoot":      config.SessionRoot,
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
	if config.DockerHostSessionRoot != dockerHostSessionRoot {
		t.Fatalf("DockerHostSessionRoot = %q, want %q", config.DockerHostSessionRoot, dockerHostSessionRoot)
	}
}

func TestNewConfigPreservesWindowsDockerHostSessionRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("DOCKER_HOST_SESSION_ROOT", `E:/program/agent-compose-main/data/agent-compose/sessions`)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	want := `E:/program/agent-compose-main/data/agent-compose/sessions`
	if config.DockerHostSessionRoot != want {
		t.Fatalf("DockerHostSessionRoot = %q, want %q", config.DockerHostSessionRoot, want)
	}
}

func TestNewConfigPreservesUNCDockerHostSessionRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("DOCKER_HOST_SESSION_ROOT", `\\server\share\agent-compose\sessions`)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	config, err := NewConfig(di)
	if err != nil {
		t.Fatalf("NewConfig returned error: %v", err)
	}

	want := `\\server\share\agent-compose\sessions`
	if config.DockerHostSessionRoot != want {
		t.Fatalf("DockerHostSessionRoot = %q, want %q", config.DockerHostSessionRoot, want)
	}
}

func TestNewConfigRejectsWindowsDockerHostSessionRootParentSegment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("DATA_ROOT", filepath.Join(root, "data"))
	t.Setenv("DOCKER_HOST_SESSION_ROOT", `E:\program\..\agent-compose\sessions`)

	di := do.New()
	do.ProvideValue(di, slog.Default())
	if _, err := NewConfig(di); err == nil || !strings.Contains(err.Error(), "DOCKER_HOST_SESSION_ROOT") {
		t.Fatalf("NewConfig error = %v, want DOCKER_HOST_SESSION_ROOT validation error", err)
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
