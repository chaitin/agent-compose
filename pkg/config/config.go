package config

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/do/v2"
)

const DefaultWorkspaceUploadLimitBytes int64 = 1 << 30
const defaultGuestHomePath = "/root"

const (
	RuntimeDriverBoxlite      = "boxlite"
	RuntimeDriverDocker       = "docker"
	RuntimeDriverMicrosandbox = "microsandbox"
)

const (
	ImageStoreModeAuto   = "auto"
	ImageStoreModeDocker = "docker"
	ImageStoreModeOCI    = "oci"
)

var BuildVersion = "0"

type Config struct {
	DbAddr                     string
	DbName                     string
	DbTimeout                  time.Duration
	DataRoot                   string
	SessionRoot                string
	HttpListen                 string
	AgentComposeSocket         string
	AgentComposeHost           string
	HttpRoot                   string
	UIRoot                     string
	HTTPBasicAuth              string
	HttpBasicAuth              string
	WebhookBodyLimitBytes      int64
	WebhookQueueRulesJSON      string
	WebhookQueueDefaultWorkers int
	WorkspaceUploadLimitBytes  int64
	AuthUsername               string
	AuthPassword               string
	AuthSecret                 string
	AuthSessionTTL             time.Duration
	OAuthAPIKey                string
	OAuthSecret                string
	OAuthScopes                []string
	OAuthCallbackURL           string
	OAuthAuthURL               string
	OAuthTokenURL              string
	OAuthUserInfoURL           string
	OAuthClientAuthMethod      string
	LLMAPIEndpoint             string
	LLMAPIProtocol             string
	LLMAPIKey                  string
	LLMModel                   string
	LLMTimeout                 time.Duration
	RuntimeBaseURL             string
	AgentTimeout               time.Duration
	LoaderRunTimeout           time.Duration
	RuntimeDriver              string
	BoxliteHome                string
	BoxliteRuntimeDir          string
	DockerHome                 string
	DockerHostSessionRoot      string
	DockerDefaultImage         string
	MicrosandboxHome           string
	MicrosandboxMSBPath        string
	MicrosandboxLibPath        string
	MicrosandboxDefaultImage   string
	MicrosandboxInsecure       []string
	DefaultImage               string
	BoxRootfsPath              string
	ImageRegistry              string
	ImageStoreMode             string
	ImageCacheRoot             string
	ImageInsecureRegistries    []string
	BoxDiskSizeGB              int
	BoxCacheTTL                time.Duration
	GuestWorkspacePath         string
	GuestHomePath              string
	GuestStateRoot             string
	GuestRuntimeRoot           string
	GuestLogRoot               string
	JupyterGuestPort           int
	SessionStartTimeout        time.Duration
	SessionStopTimeout         time.Duration
	JupyterProxyBasePath       string
	CapGRPCListen              string
	CapGRPCTarget              string
	Version                    string
}

func NewConfig(di do.Injector) (*Config, error) {
	logger := do.MustInvoke[*slog.Logger](di)

	dataRoot := os.Getenv("DATA_ROOT")
	if dataRoot == "" {
		dataRoot = defaultDataRoot()
	}

	dbPath := filepath.Join(dataRoot, "data.db")

	dbName := "agent_compose"

	dbTimeout := 16 * time.Second
	if raw := os.Getenv("DB_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse database timeout duration", "dbTimeoutStr", raw)
		} else if parsed > time.Millisecond {
			dbTimeout = parsed
			logger.Info("dbTimeout updated", "dbTimeout", dbTimeout)
		}
	}

	sessionRoot := filepath.Join(dataRoot, "sessions")

	httpRoot := os.Getenv("HTTP_ROOT")
	uiRoot := os.Getenv("UI_ROOT")

	httpListen := strings.TrimSpace(os.Getenv("HTTP_LISTEN"))
	if httpListen != "" {
		if err := validateTCPListenAddress("HTTP_LISTEN", httpListen); err != nil {
			return nil, err
		}
	}
	agentComposeSocket, err := resolveAgentComposeSocket(os.Getenv("AGENT_COMPOSE_SOCKET"))
	if err != nil {
		return nil, err
	}
	agentComposeHost := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_HOST"))
	if agentComposeHost != "" {
		if err := validateAgentComposeHost(agentComposeHost); err != nil {
			return nil, err
		}
	}

	llmAPIEndpoint := os.Getenv("LLM_API_ENDPOINT")
	llmAPIProtocol := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_API_PROTOCOL")))
	llmAPIKey := getenvFirst("LLM_API_KEY", "OPENAI_API_KEY")

	llmModel := os.Getenv("LLM_MODEL")
	runtimeBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("AGENT_COMPOSE_RUNTIME_BASE_URL")), "/")

	llmTimeout := 60 * time.Second
	if raw := os.Getenv("LLM_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse LLM_TIMEOUT", "value", raw, "error", err)
		} else {
			llmTimeout = parsed
		}
	}
	agentTimeout := 10 * time.Minute
	if raw := os.Getenv("AGENT_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse AGENT_TIMEOUT", "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("ignored non-positive AGENT_TIMEOUT", "value", raw)
		} else {
			agentTimeout = parsed
		}
	}
	loaderRunTimeout := 20 * time.Minute
	if raw := os.Getenv("LOADER_RUN_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse LOADER_RUN_TIMEOUT", "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("ignored non-positive LOADER_RUN_TIMEOUT", "value", raw)
		} else {
			loaderRunTimeout = parsed
		}
	}
	runtimeDriver := os.Getenv("RUNTIME_DRIVER")
	if runtimeDriver == "" {
		runtimeDriver = RuntimeDriverDocker
	}
	runtimeDriver = resolveRuntimeDriver(runtimeDriver)
	if err := validateRuntimeDriver(runtimeDriver); err != nil {
		return nil, err
	}

	boxliteHome := os.Getenv("BOXLITE_HOME")
	if boxliteHome == "" {
		boxliteHome = filepath.Join(dataRoot, "boxlite")
	}

	boxliteRuntimeDir := os.Getenv("BOXLITE_RUNTIME_DIR")
	if boxliteRuntimeDir == "" {
		boxliteRuntimeDir = filepath.Join(".", "build", "boxlite", "runtime")
	}

	dockerHome := os.Getenv("DOCKER_HOME")
	if dockerHome == "" {
		dockerHome = filepath.Join(dataRoot, "docker")
	}
	dockerHostSessionRoot := os.Getenv("DOCKER_HOST_SESSION_ROOT")

	microsandboxHome := getenvFirst("MICROSANDBOX_HOME", "MSB_HOME")
	if microsandboxHome == "" {
		microsandboxHome = filepath.Join(dataRoot, "microsandbox")
	}

	microsandboxMSBPath := getenvFirst("MICROSANDBOX_MSB_PATH", "MSB_PATH")
	if microsandboxMSBPath == "" {
		microsandboxMSBPath = filepath.Join(".", "build", "microsandbox", "bin", "msb")
	}
	microsandboxLibPath := os.Getenv("MICROSANDBOX_LIB_PATH")
	if microsandboxLibPath == "" {
		microsandboxLibPath = filepath.Join(".", "build", "microsandbox", "lib", "libmicrosandbox_go_ffi.so")
	}

	defaultImage := os.Getenv("DEFAULT_IMAGE")
	if defaultImage == "" {
		defaultImage = "debian:bookworm-slim"
	}

	microsandboxDefaultImage := os.Getenv("MICROSANDBOX_DEFAULT_IMAGE")
	if microsandboxDefaultImage == "" {
		microsandboxDefaultImage = defaultImage
	}
	dockerDefaultImage := os.Getenv("DOCKER_DEFAULT_IMAGE")
	if dockerDefaultImage == "" {
		dockerDefaultImage = defaultImage
	}
	microsandboxInsecure := splitAndTrimEnv(os.Getenv("MICROSANDBOX_INSECURE_REGISTRIES"))

	boxRootfsPath := os.Getenv("BOX_ROOTFS_PATH")

	imageRegistry := os.Getenv("IMAGE_REGISTRY")
	if imageRegistry == "" {
		imageRegistry = "docker.io"
	}
	imageStoreMode := strings.ToLower(strings.TrimSpace(os.Getenv("IMAGE_STORE_MODE")))
	if imageStoreMode == "" {
		imageStoreMode = ImageStoreModeAuto
	}
	if err := validateImageStoreMode(imageStoreMode); err != nil {
		return nil, err
	}
	imageCacheRoot := os.Getenv("IMAGE_CACHE_ROOT")
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(dataRoot, "images")
	}
	imageInsecureRegistries := splitAndTrimEnv(os.Getenv("IMAGE_INSECURE_REGISTRIES"))

	boxDiskSizeGB := 6
	if raw := os.Getenv("BOX_DISK_SIZE_GB"); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed <= 0 {
			logger.Warn("failed to parse BOX_DISK_SIZE_GB", "value", raw, "error", err)
		} else {
			boxDiskSizeGB = parsed
		}
	}

	boxCacheTTL := 7 * 24 * time.Hour
	if raw := os.Getenv("BOX_CACHE_TTL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse BOX_CACHE_TTL", "value", raw, "error", err)
		} else {
			boxCacheTTL = parsed
		}
	}

	guestPaths := &Config{
		GuestWorkspacePath: os.Getenv("GUEST_WORKSPACE"),
		GuestStateRoot:     os.Getenv("GUEST_STATE_ROOT"),
		GuestRuntimeRoot:   os.Getenv("GUEST_RUNTIME_ROOT"),
		GuestLogRoot:       os.Getenv("GUEST_LOG_ROOT"),
	}
	ApplyDefaultGuestPaths(guestPaths)

	jupyterGuestPort := 8888
	if raw := os.Getenv("JUPYTER_GUEST_PORT"); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed <= 0 {
			logger.Warn("failed to parse JUPYTER_GUEST_PORT", "value", raw, "error", err)
		} else {
			jupyterGuestPort = parsed
		}
	}

	startTimeout := 30 * time.Minute
	if raw := os.Getenv("SESSION_START_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse SESSION_START_TIMEOUT", "value", raw, "error", err)
		} else {
			startTimeout = parsed
		}
	}

	stopTimeout := 30 * time.Second
	if raw := os.Getenv("SESSION_STOP_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse SESSION_STOP_TIMEOUT", "value", raw, "error", err)
		} else {
			stopTimeout = parsed
		}
	}

	basicAuth := ""
	if raw := os.Getenv("HTTP_BASIC_AUTH"); raw != "" {
		if decoded, err := base64.StdEncoding.DecodeString(raw); err != nil {
			logger.Warn("failed to decode HTTP_BASIC_AUTH", "error", err)
		} else {
			basicAuth = string(decoded)
		}
	}

	webhookBodyLimitBytes := int64(1 << 20)
	if raw := os.Getenv("WEBHOOK_BODY_LIMIT_BYTES"); raw != "" {
		var parsed int64
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed <= 0 {
			logger.Warn("failed to parse WEBHOOK_BODY_LIMIT_BYTES", "value", raw, "error", err)
		} else {
			webhookBodyLimitBytes = parsed
		}
	}
	webhookQueueRulesJSON := strings.TrimSpace(os.Getenv("WEBHOOK_QUEUE_RULES_JSON"))
	webhookQueueDefaultWorkers := 8
	if raw := os.Getenv("WEBHOOK_QUEUE_DEFAULT_WORKERS"); raw != "" {
		var parsed int
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed < 0 {
			logger.Warn("failed to parse WEBHOOK_QUEUE_DEFAULT_WORKERS", "value", raw, "error", err)
		} else {
			webhookQueueDefaultWorkers = parsed
		}
	}
	workspaceUploadLimitBytes := DefaultWorkspaceUploadLimitBytes
	if raw := os.Getenv("WORKSPACE_UPLOAD_LIMIT_BYTES"); raw != "" {
		var parsed int64
		if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed <= 0 {
			logger.Warn("failed to parse WORKSPACE_UPLOAD_LIMIT_BYTES", "value", raw, "error", err)
		} else {
			workspaceUploadLimitBytes = parsed
		}
	}

	authUsername := os.Getenv("AUTH_USERNAME")
	if authUsername == "" {
		authUsername = "admin"
	}
	authPassword := os.Getenv("AUTH_PASSWORD")
	authSecret := os.Getenv("AUTH_SECRET")
	authSessionTTL := 24 * time.Hour
	if raw := os.Getenv("AUTH_SESSION_TTL"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse AUTH_SESSION_TTL", "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("AUTH_SESSION_TTL must be positive", "value", raw)
		} else {
			authSessionTTL = parsed
		}
	}
	oauthAPIKey := os.Getenv("OAUTH_APIKEY")
	oauthSecret := os.Getenv("OAUTH_SECRET")
	oauthScopes := splitAndTrimEnv(os.Getenv("OAUTH_SCOPES"))
	if len(oauthScopes) == 0 {
		oauthScopes = []string{"profile"}
	}
	oauthCallbackURL := os.Getenv("OAUTH_CALLBACK_URL")
	oauthBaseURL := strings.TrimRight(os.Getenv("OAUTH_BASE_URL"), "/")
	oauthAuthURL := os.Getenv("OAUTH_AUTH_URL")
	if oauthAuthURL == "" {
		oauthAuthURL = oauthBaseURL + "/oauth2/auth"
	}
	oauthTokenURL := os.Getenv("OAUTH_TOKEN_URL")
	if oauthTokenURL == "" {
		oauthTokenURL = oauthBaseURL + "/oauth2/token"
	}
	oauthUserInfoURL := os.Getenv("OAUTH_USERINFO_URL")
	if oauthUserInfoURL == "" {
		oauthUserInfoURL = oauthBaseURL + "/userinfo"
	}
	oauthClientAuthMethod := strings.ToLower(strings.TrimSpace(os.Getenv("OAUTH_CLIENT_AUTH_METHOD")))
	if oauthClientAuthMethod == "" {
		oauthClientAuthMethod = "client_secret_post"
	}

	jupyterProxyBase := strings.TrimSpace(os.Getenv("JUPYTER_PROXY_BASE"))
	if jupyterProxyBase == "" {
		jupyterProxyBase = "/jupyter"
	}
	if !strings.HasPrefix(jupyterProxyBase, "/") {
		jupyterProxyBase = "/" + jupyterProxyBase
	}
	jupyterProxyBase = strings.TrimRight(jupyterProxyBase, "/")
	if jupyterProxyBase == "" {
		jupyterProxyBase = "/jupyter"
	}

	dataRoot = mustAbs(dataRoot)
	sessionRoot = mustAbs(sessionRoot)
	httpRoot = mustAbs(httpRoot)
	uiRoot = mustAbs(uiRoot)
	boxliteHome = mustAbs(boxliteHome)
	boxliteRuntimeDir = mustAbs(boxliteRuntimeDir)
	dockerHome = mustAbs(dockerHome)
	dockerHostSessionRoot, err = normalizeDockerHostSessionRoot(dockerHostSessionRoot)
	if err != nil {
		return nil, err
	}
	microsandboxHome = mustAbs(microsandboxHome)
	microsandboxMSBPath = mustAbs(microsandboxMSBPath)
	microsandboxLibPath = mustAbs(microsandboxLibPath)
	imageCacheRoot = mustAbs(imageCacheRoot)
	if boxRootfsPath != "" {
		boxRootfsPath = mustAbs(boxRootfsPath)
	}

	dirs := map[string]string{
		"DATA_ROOT":         dataRoot,
		"SESSION_ROOT":      sessionRoot,
		"BOXLITE_HOME":      boxliteHome,
		"DOCKER_HOME":       dockerHome,
		"IMAGE_CACHE_ROOT":  imageCacheRoot,
		"MICROSANDBOX_HOME": microsandboxHome,
	}
	for name, dir := range dirs {
		if err := ensureDirExists(dir); err != nil {
			return nil, fmt.Errorf("ensure %s exists: %w", name, err)
		}
	}

	return &Config{
		DbAddr:                     dbPath,
		DbName:                     dbName,
		DbTimeout:                  dbTimeout,
		DataRoot:                   dataRoot,
		SessionRoot:                sessionRoot,
		HttpListen:                 httpListen,
		AgentComposeSocket:         agentComposeSocket,
		AgentComposeHost:           agentComposeHost,
		HttpRoot:                   httpRoot,
		UIRoot:                     uiRoot,
		HTTPBasicAuth:              basicAuth,
		HttpBasicAuth:              basicAuth,
		WebhookBodyLimitBytes:      webhookBodyLimitBytes,
		WebhookQueueRulesJSON:      webhookQueueRulesJSON,
		WebhookQueueDefaultWorkers: webhookQueueDefaultWorkers,
		WorkspaceUploadLimitBytes:  workspaceUploadLimitBytes,
		AuthUsername:               authUsername,
		AuthPassword:               authPassword,
		AuthSecret:                 authSecret,
		AuthSessionTTL:             authSessionTTL,
		OAuthAPIKey:                oauthAPIKey,
		OAuthSecret:                oauthSecret,
		OAuthScopes:                oauthScopes,
		OAuthCallbackURL:           oauthCallbackURL,
		OAuthAuthURL:               oauthAuthURL,
		OAuthTokenURL:              oauthTokenURL,
		OAuthUserInfoURL:           oauthUserInfoURL,
		OAuthClientAuthMethod:      oauthClientAuthMethod,
		LLMAPIEndpoint:             llmAPIEndpoint,
		LLMAPIProtocol:             llmAPIProtocol,
		LLMAPIKey:                  llmAPIKey,
		LLMModel:                   llmModel,
		LLMTimeout:                 llmTimeout,
		RuntimeBaseURL:             runtimeBaseURL,
		AgentTimeout:               agentTimeout,
		LoaderRunTimeout:           loaderRunTimeout,
		RuntimeDriver:              runtimeDriver,
		BoxliteHome:                boxliteHome,
		BoxliteRuntimeDir:          boxliteRuntimeDir,
		DockerHome:                 dockerHome,
		DockerHostSessionRoot:      dockerHostSessionRoot,
		DockerDefaultImage:         dockerDefaultImage,
		MicrosandboxHome:           microsandboxHome,
		MicrosandboxMSBPath:        microsandboxMSBPath,
		MicrosandboxLibPath:        microsandboxLibPath,
		MicrosandboxDefaultImage:   microsandboxDefaultImage,
		MicrosandboxInsecure:       microsandboxInsecure,
		DefaultImage:               defaultImage,
		BoxRootfsPath:              boxRootfsPath,
		ImageRegistry:              imageRegistry,
		ImageStoreMode:             imageStoreMode,
		ImageCacheRoot:             imageCacheRoot,
		ImageInsecureRegistries:    imageInsecureRegistries,
		BoxDiskSizeGB:              boxDiskSizeGB,
		BoxCacheTTL:                boxCacheTTL,
		GuestWorkspacePath:         guestPaths.GuestWorkspacePath,
		GuestHomePath:              guestPaths.GuestHomePath,
		GuestStateRoot:             guestPaths.GuestStateRoot,
		GuestRuntimeRoot:           guestPaths.GuestRuntimeRoot,
		GuestLogRoot:               guestPaths.GuestLogRoot,
		JupyterGuestPort:           jupyterGuestPort,
		SessionStartTimeout:        startTimeout,
		SessionStopTimeout:         stopTimeout,
		JupyterProxyBasePath:       jupyterProxyBase,
		CapGRPCListen:              strings.TrimSpace(os.Getenv("CAP_GRPC_LISTEN")),
		CapGRPCTarget:              strings.TrimSpace(os.Getenv("CAP_GRPC_TARGET")),
		Version:                    BuildVersion,
	}, nil
}

func Setup(di do.Injector) {
	do.Provide(di, NewConfig)
}

func resolveAgentComposeSocket(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = defaultAgentComposeSocket()
	}
	if value == "" {
		return "", fmt.Errorf("AGENT_COMPOSE_SOCKET is empty")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("invalid AGENT_COMPOSE_SOCKET %q: path contains NUL byte", value)
	}
	return mustAbs(value), nil
}

func defaultAgentComposeSocket() string {
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "agent-compose.sock")
	}
	return filepath.Join(os.TempDir(), fmt.Sprintf("agent-compose-%d.sock", os.Getuid()))
}

func validateTCPListenAddress(name, value string) error {
	host, port, err := net.SplitHostPort(value)
	if err != nil {
		return fmt.Errorf("invalid %s %q: expected host:port: %w", name, value, err)
	}
	if strings.TrimSpace(port) == "" {
		return fmt.Errorf("invalid %s %q: port is required", name, value)
	}
	if _, err := net.LookupPort("tcp", port); err != nil {
		return fmt.Errorf("invalid %s %q: invalid port %q: %w", name, value, port, err)
	}
	if strings.Contains(host, "/") {
		return fmt.Errorf("invalid %s %q: host must not contain a path", name, value)
	}
	return nil
}

func validateAgentComposeHost(value string) error {
	parsed, err := url.Parse(value)
	if err != nil {
		return fmt.Errorf("invalid AGENT_COMPOSE_HOST %q: %w", value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("invalid AGENT_COMPOSE_HOST %q: scheme must be http or https", value)
	}
	if parsed.Host == "" {
		return fmt.Errorf("invalid AGENT_COMPOSE_HOST %q: host is required", value)
	}
	return nil
}

func ApplyDefaultGuestPaths(config *Config) {
	if config.GuestWorkspacePath == "" {
		config.GuestWorkspacePath = "/workspace"
	}
	config.GuestHomePath = defaultGuestHomePath
	if config.GuestStateRoot == "" {
		config.GuestStateRoot = "/data/state"
	}
	if config.GuestRuntimeRoot == "" {
		config.GuestRuntimeRoot = "/data/runtime"
	}
	if config.GuestLogRoot == "" {
		config.GuestLogRoot = "/data/logs"
	}
}

func resolveRuntimeDriver(value string) string {
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

func validateRuntimeDriver(value string) error {
	switch resolveRuntimeDriver(value) {
	case RuntimeDriverBoxlite, RuntimeDriverDocker, RuntimeDriverMicrosandbox:
		return nil
	default:
		return fmt.Errorf("unsupported agent-compose runtime driver %q", strings.TrimSpace(value))
	}
}

func validateImageStoreMode(value string) error {
	switch value {
	case ImageStoreModeAuto, ImageStoreModeDocker, ImageStoreModeOCI:
		return nil
	default:
		return fmt.Errorf("unsupported IMAGE_STORE_MODE %q: expected auto, docker, or oci", strings.TrimSpace(value))
	}
}

func defaultDataRoot() string {
	userDataHome := os.Getenv("XDG_DATA_HOME")
	if userDataHome == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			home = "."
		}
		userDataHome = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(userDataHome, "agent-compose")
}

func getenvFirst(keys ...string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return ""
}

func mustAbs(path string) string {
	if path == "" {
		return ""
	}
	resolved, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return resolved
}

func normalizeDockerHostSessionRoot(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if isWindowsHostPath(trimmed) {
		if hasParentPathSegment(trimmed) {
			return "", fmt.Errorf("invalid DOCKER_HOST_SESSION_ROOT %q: parent path segments are not allowed", path)
		}
		return trimmed, nil
	}
	return mustAbs(trimmed), nil
}

func isWindowsHostPath(path string) bool {
	if strings.HasPrefix(path, `\\`) {
		return true
	}
	if len(path) < 3 {
		return false
	}
	drive := path[0]
	if (drive < 'A' || drive > 'Z') && (drive < 'a' || drive > 'z') {
		return false
	}
	return path[1] == ':' && (path[2] == '\\' || path[2] == '/')
}

func hasParentPathSegment(path string) bool {
	for _, part := range strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	}) {
		if part == ".." {
			return true
		}
	}
	return false
}

func ensureDirExists(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	return nil
}

func splitAndTrimEnv(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ';' || r == '\n'
	})
	items := make([]string, 0, len(parts))
	for _, item := range parts {
		trimmed := strings.TrimSpace(item)
		if trimmed == "" {
			continue
		}
		items = append(items, trimmed)
	}
	if len(items) == 0 {
		return nil
	}
	return items
}
