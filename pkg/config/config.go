package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/samber/do/v2"
)

const DefaultWorkspaceUploadLimitBytes int64 = 1 << 30
const DefaultAgentComposeSocketPath = "/var/run/agent-compose.sock"
const DefaultAgentTimeout = 10 * time.Hour
const defaultGuestHomePath = "/root"

const (
	DefaultSandboxCPUs       uint8  = 4
	DefaultSandboxMemoryMiB  uint32 = 4096
	DefaultSandboxDiskSizeGB int32  = 6
)

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
	SandboxRoot                string
	SandboxRootExplicit        bool
	HttpListen                 string
	DaemonAuthToken            string
	AgentComposeSocket         string
	AgentComposeHost           string
	WebhookBodyLimitBytes      int64
	WebhookQueueRulesJSON      string
	WebhookQueueDefaultWorkers int
	WorkspaceUploadLimitBytes  int64
	LLMAPIEndpoint             string
	LLMAPIProtocol             string
	LLMAPIKey                  string
	LLMModel                   string
	LLMTimeout                 time.Duration
	CodexRequestMaxRetries     uint64
	CodexStreamMaxRetries      uint64
	CodexStreamIdleTimeout     time.Duration
	RuntimeBaseURL             string
	AgentTimeout               time.Duration
	LoaderRunTimeout           time.Duration
	RuntimeDriver              string
	BoxliteHome                string
	BoxliteRuntimeDir          string
	DockerHome                 string
	DockerHostSandboxRoot      string
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
	SandboxCPUs                uint8
	SandboxMemoryMiB           uint32
	SandboxDiskSizeGB          int32
	CacheTTL                   time.Duration
	CleanupInterval            time.Duration
	WorkspaceCleanupTTL        time.Duration
	ImageCacheCleanupTTL       time.Duration
	ImagePullTimeout           time.Duration
	GuestWorkspacePath         string
	GuestHomePath              string
	GuestStateRoot             string
	GuestRuntimeRoot           string
	GuestLogRoot               string
	JupyterGuestPort           int
	SandboxStartTimeout        time.Duration
	SandboxStopTimeout         time.Duration
	JupyterReadyTimeout        time.Duration
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

	sandboxRootExplicit := strings.TrimSpace(os.Getenv("SANDBOX_ROOT")) != "" || strings.TrimSpace(os.Getenv("SESSION_ROOT")) != ""
	sandboxRoot, err := envWithLegacy(logger, "SANDBOX_ROOT", "SESSION_ROOT")
	if err != nil {
		return nil, err
	}
	if sandboxRoot == "" {
		legacyRoot := filepath.Join(dataRoot, "sessions")
		if nonEmpty, inspectErr := pathHasEntries(legacyRoot); inspectErr != nil {
			return nil, fmt.Errorf("inspect legacy sessions root %s: %w", legacyRoot, inspectErr)
		} else if nonEmpty {
			sandboxRoot = legacyRoot
			logger.Warn("using deprecated sessions storage root", "path", legacyRoot, "replacement", filepath.Join(dataRoot, "sandboxes"))
		} else {
			sandboxRoot = filepath.Join(dataRoot, "sandboxes")
		}
	}

	httpListen := strings.TrimSpace(os.Getenv("HTTP_LISTEN"))
	if httpListen != "" {
		if err := validateTCPListenAddress("HTTP_LISTEN", httpListen); err != nil {
			return nil, err
		}
	}
	daemonAuthToken := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_AUTH_TOKEN"))
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
	codexRuntime := loadCodexRuntimeConfig(logger, llmTimeout)
	agentTimeout := DefaultAgentTimeout
	if raw := os.Getenv("AGENT_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse AGENT_TIMEOUT", "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("ignored non-positive AGENT_TIMEOUT", "value", raw)
		} else {
			agentTimeout = parsed
		}
	}
	imagePullTimeout := 10 * time.Minute
	if raw := os.Getenv("IMAGE_PULL_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse IMAGE_PULL_TIMEOUT", "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("ignored non-positive IMAGE_PULL_TIMEOUT", "value", raw)
		} else {
			imagePullTimeout = parsed
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
	dockerHostSandboxRoot, err := envWithLegacy(logger, "DOCKER_HOST_SANDBOX_ROOT", "DOCKER_HOST_SESSION_ROOT")
	if err != nil {
		return nil, err
	}

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

	sandboxCPUs := positiveUint8Env(logger, "SANDBOX_CPUS", DefaultSandboxCPUs)
	sandboxMemoryMiB := positiveMemoryMiBEnv(logger, "SANDBOX_MEMORY_MIB", DefaultSandboxMemoryMiB)
	sandboxDiskSizeGB := positiveDiskSizeGBEnv(logger, "SANDBOX_DISK_SIZE_GB", DefaultSandboxDiskSizeGB)
	cacheTTL := 7 * 24 * time.Hour
	cacheTTLRaw, err := envWithLegacy(logger, "CACHE_TTL", "BOX_CACHE_TTL")
	if err != nil {
		return nil, err
	}
	if raw := strings.TrimSpace(cacheTTLRaw); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return nil, fmt.Errorf("parse CACHE_TTL %q: %w", raw, parseErr)
		}
		if parsed < 0 {
			return nil, fmt.Errorf("CACHE_TTL must not be negative")
		}
		cacheTTL = parsed
	}

	cleanupInterval, err := cleanupDurationEnv("CLEANUP_INTERVAL", time.Hour)
	if err != nil {
		return nil, err
	}
	workspaceCleanupTTL, err := cleanupDurationEnv("WORKSPACE_CLEANUP_TTL", 0)
	if err != nil {
		return nil, err
	}
	imageCacheCleanupTTL, err := cleanupDurationEnv("IMAGE_CACHE_CLEANUP_TTL", 0)
	if err != nil {
		return nil, err
	}
	if (workspaceCleanupTTL > 0 || imageCacheCleanupTTL > 0) && cleanupInterval <= 0 {
		return nil, fmt.Errorf("CLEANUP_INTERVAL must be positive when automatic cleanup is enabled")
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
	if raw, err := envWithLegacy(logger, "SANDBOX_START_TIMEOUT", "SESSION_START_TIMEOUT"); err != nil {
		return nil, err
	} else if raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse SANDBOX_START_TIMEOUT", "value", raw, "error", err)
		} else {
			startTimeout = parsed
		}
	}

	stopTimeout := 30 * time.Second
	if raw, err := envWithLegacy(logger, "SANDBOX_STOP_TIMEOUT", "SESSION_STOP_TIMEOUT"); err != nil {
		return nil, err
	} else if raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse SANDBOX_STOP_TIMEOUT", "value", raw, "error", err)
		} else {
			stopTimeout = parsed
		}
	}

	jupyterReadyTimeout := 30 * time.Second
	if raw := os.Getenv("JUPYTER_READY_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse JUPYTER_READY_TIMEOUT", "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("ignoring non-positive JUPYTER_READY_TIMEOUT, using default", "value", raw, "default", jupyterReadyTimeout)
		} else {
			jupyterReadyTimeout = parsed
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

	warnPublicHTTPListen(logger, httpListen)

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
	sandboxRoot = mustAbs(sandboxRoot)
	boxliteHome = mustAbs(boxliteHome)
	boxliteRuntimeDir = mustAbs(boxliteRuntimeDir)
	dockerHome = mustAbs(dockerHome)
	dockerHostSandboxRoot, err = normalizeDockerHostSandboxRoot(dockerHostSandboxRoot)
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
		"SANDBOX_ROOT":      sandboxRoot,
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
		SandboxRoot:                sandboxRoot,
		SandboxRootExplicit:        sandboxRootExplicit,
		HttpListen:                 httpListen,
		DaemonAuthToken:            daemonAuthToken,
		AgentComposeSocket:         agentComposeSocket,
		AgentComposeHost:           agentComposeHost,
		WebhookBodyLimitBytes:      webhookBodyLimitBytes,
		WebhookQueueRulesJSON:      webhookQueueRulesJSON,
		WebhookQueueDefaultWorkers: webhookQueueDefaultWorkers,
		WorkspaceUploadLimitBytes:  workspaceUploadLimitBytes,
		LLMAPIEndpoint:             llmAPIEndpoint,
		LLMAPIProtocol:             llmAPIProtocol,
		LLMAPIKey:                  llmAPIKey,
		LLMModel:                   llmModel,
		LLMTimeout:                 llmTimeout,
		CodexRequestMaxRetries:     codexRuntime.requestMaxRetries,
		CodexStreamMaxRetries:      codexRuntime.streamMaxRetries,
		CodexStreamIdleTimeout:     codexRuntime.streamIdleTimeout,
		RuntimeBaseURL:             runtimeBaseURL,
		AgentTimeout:               agentTimeout,
		LoaderRunTimeout:           loaderRunTimeout,
		RuntimeDriver:              runtimeDriver,
		BoxliteHome:                boxliteHome,
		BoxliteRuntimeDir:          boxliteRuntimeDir,
		DockerHome:                 dockerHome,
		DockerHostSandboxRoot:      dockerHostSandboxRoot,
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
		SandboxCPUs:                sandboxCPUs,
		SandboxMemoryMiB:           sandboxMemoryMiB,
		SandboxDiskSizeGB:          sandboxDiskSizeGB,
		CacheTTL:                   cacheTTL,
		CleanupInterval:            cleanupInterval,
		WorkspaceCleanupTTL:        workspaceCleanupTTL,
		ImageCacheCleanupTTL:       imageCacheCleanupTTL,
		ImagePullTimeout:           imagePullTimeout,
		GuestWorkspacePath:         guestPaths.GuestWorkspacePath,
		GuestHomePath:              guestPaths.GuestHomePath,
		GuestStateRoot:             guestPaths.GuestStateRoot,
		GuestRuntimeRoot:           guestPaths.GuestRuntimeRoot,
		GuestLogRoot:               guestPaths.GuestLogRoot,
		JupyterGuestPort:           jupyterGuestPort,
		SandboxStartTimeout:        startTimeout,
		SandboxStopTimeout:         stopTimeout,
		JupyterReadyTimeout:        jupyterReadyTimeout,
		JupyterProxyBasePath:       jupyterProxyBase,
		CapGRPCListen:              strings.TrimSpace(os.Getenv("CAP_GRPC_LISTEN")),
		CapGRPCTarget:              strings.TrimSpace(os.Getenv("CAP_GRPC_TARGET")),
		Version:                    BuildVersion,
	}, nil
}

func cleanupDurationEnv(name string, defaultValue time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", name, raw, err)
	}
	if parsed < 0 {
		return 0, fmt.Errorf("%s must not be negative", name)
	}
	return parsed, nil
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
	return DefaultAgentComposeSocket()
}

func DefaultAgentComposeSocket() string {
	if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
		return filepath.Join(runtimeDir, "agent-compose.sock")
	}
	return DefaultAgentComposeSocketPath
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

func warnPublicHTTPListen(logger *slog.Logger, httpListen string) {
	if httpListen == "" || isLoopbackListenAddress(httpListen) {
		return
	}
	logger.Warn("HTTP_LISTEN exposes the daemon on a non-loopback address; expose it only on a trusted network or behind the agent-compose-ui server",
		"http_listen", httpListen,
	)
}

func isLoopbackListenAddress(value string) bool {
	host, _, err := net.SplitHostPort(value)
	if err != nil {
		return false
	}
	host = strings.TrimSpace(host)
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return false
	}
	for _, resolved := range ips {
		if !resolved.IsLoopback() {
			return false
		}
	}
	return true
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

func envWithLegacy(logger *slog.Logger, newName, oldName string) (string, error) {
	newValue := os.Getenv(newName)
	oldValue := os.Getenv(oldName)
	if newValue != "" {
		if oldValue != "" {
			logger.Warn("deprecated environment variable ignored because replacement is set", "deprecated", oldName, "replacement", newName)
		}
		return newValue, nil
	}
	if oldValue != "" {
		logger.Warn("using deprecated environment variable", "deprecated", oldName, "replacement", newName)
		return oldValue, nil
	}
	return "", nil
}

func pathHasEntries(path string) (bool, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
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

func normalizeDockerHostSandboxRoot(path string) (string, error) {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return "", nil
	}
	if isWindowsHostPath(trimmed) {
		if hasParentPathSegment(trimmed) {
			return "", fmt.Errorf("invalid DOCKER_HOST_SANDBOX_ROOT %q: parent path segments are not allowed", path)
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

func positiveUint8Env(logger *slog.Logger, name string, defaultValue uint8) uint8 {
	return uint8(positiveUintEnv(logger, name, uint64(defaultValue), 8))
}

func positiveMemoryMiBEnv(logger *slog.Logger, name string, defaultValue uint32) uint32 {
	// BoxLite accepts memory through a signed C int, so the shared value must
	// fit both that API and Microsandbox's uint32 option.
	return uint32(positiveUintEnv(logger, name, uint64(defaultValue), 31))
}

func positiveDiskSizeGBEnv(logger *slog.Logger, name string, defaultValue int32) int32 {
	// Microsandbox exposes the bind quota in MiB as uint32. Restrict the GiB
	// value so multiplying it by 1024 cannot wrap.
	return int32(positiveUintEnv(logger, name, uint64(defaultValue), 22))
}

func positiveUintEnv(logger *slog.Logger, name string, defaultValue uint64, bitSize int) uint64 {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return defaultValue
	}
	parsed, err := strconv.ParseUint(raw, 10, bitSize)
	if err != nil || parsed == 0 {
		logger.Warn("failed to parse positive integer environment variable", "name", name, "value", raw, "error", err)
		return defaultValue
	}
	return parsed
}
