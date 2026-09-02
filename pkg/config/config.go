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
	RuntimeDriverK8s          = "k8s"
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
	SQLiteMaxOpenConns         int
	DataRoot                   string
	SandboxRoot                string
	SandboxRootExplicit        bool
	HttpListen                 string
	DaemonAuthToken            string
	HttpTLSCertFile            string
	HttpTLSKeyFile             string
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
	LLMMaxOutputTokens         int
	CodexRequestMaxRetries     uint64
	CodexStreamMaxRetries      uint64
	CodexStreamIdleTimeout     time.Duration
	RuntimeBaseURL             string
	AgentTimeout               time.Duration
	SchedulerRunTimeout        time.Duration
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
	K8sHome                    string
	K8sKubeconfigPath          string
	K8sNamespace               string
	K8sDefaultImage            string
	K8sRuntimeBaseURL          string
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
	SandboxRetentionTTL        time.Duration
	SandboxArchiveRoot         string
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
	SandboxGracefulStopTimeout time.Duration
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

	sources, err := loadConfigSources(logger, dataRoot)
	if err != nil {
		return nil, err
	}
	warnPublicHTTPListen(logger, sources.DaemonHTTP.HttpListen)

	normalized, err := normalizeConfigPaths(configPathsToNormalize{
		DataRoot:              dataRoot,
		SandboxRoot:           sources.Sandbox.Root,
		SandboxArchiveRoot:    sources.Cleanup.SandboxArchiveRoot,
		BoxliteHome:           sources.Drivers.BoxliteHome,
		BoxliteRuntimeDir:     sources.Drivers.BoxliteRuntimeDir,
		DockerHome:            sources.Drivers.DockerHome,
		DockerHostSandboxRoot: sources.Drivers.DockerHostSandboxRoot,
		MicrosandboxHome:      sources.Drivers.MicrosandboxHome,
		MicrosandboxMSBPath:   sources.Drivers.MicrosandboxMSBPath,
		MicrosandboxLibPath:   sources.Drivers.MicrosandboxLibPath,
		K8sHome:               sources.Drivers.K8sHome,
		ImageCacheRoot:        sources.Images.ImageCacheRoot,
		BoxRootfsPath:         sources.Images.BoxRootfsPath,
	})
	if err != nil {
		return nil, err
	}
	if err := ensureConfigDirsExist(normalized); err != nil {
		return nil, err
	}

	return buildConfig(sources, normalized), nil
}

// configSources bundles every environment-derived config group NewConfig
// assembles into the final *Config.
type configSources struct {
	Database        databaseConfig
	Sandbox         sandboxRootConfigValues
	DaemonHTTP      daemonHTTPConfig
	LLM             llmEnvConfig
	Timeouts        runtimeTimeoutsConfig
	Drivers         driverHomesConfig
	Images          imageEnvConfig
	Resources       sandboxResourceConfig
	Cleanup         cleanupConfigValues
	GuestPaths      *Config
	SandboxTimeouts sandboxTimeoutsConfig
	HTTPLimits      httpLimitsConfig
}

func loadConfigSources(logger *slog.Logger, dataRoot string) (configSources, error) {
	database, err := loadDatabaseConfig(logger, dataRoot)
	if err != nil {
		return configSources{}, err
	}
	sandbox, err := loadSandboxRootConfig(logger, dataRoot)
	if err != nil {
		return configSources{}, err
	}
	daemonHTTP, err := loadDaemonHTTPConfig()
	if err != nil {
		return configSources{}, err
	}
	drivers, err := loadDriverHomesConfig(logger, dataRoot)
	if err != nil {
		return configSources{}, err
	}
	images, err := loadImageEnvConfig(dataRoot)
	if err != nil {
		return configSources{}, err
	}
	resources, err := loadSandboxResourceConfig(logger)
	if err != nil {
		return configSources{}, err
	}
	cleanup, err := loadCleanupConfig(dataRoot)
	if err != nil {
		return configSources{}, err
	}
	guestPaths := &Config{
		GuestWorkspacePath: os.Getenv("GUEST_WORKSPACE"),
		GuestStateRoot:     os.Getenv("GUEST_STATE_ROOT"),
		GuestRuntimeRoot:   os.Getenv("GUEST_RUNTIME_ROOT"),
		GuestLogRoot:       os.Getenv("GUEST_LOG_ROOT"),
	}
	ApplyDefaultGuestPaths(guestPaths)
	sandboxTimeouts, err := loadSandboxTimeoutsConfig(logger)
	if err != nil {
		return configSources{}, err
	}
	return configSources{
		Database:        database,
		Sandbox:         sandbox,
		DaemonHTTP:      daemonHTTP,
		LLM:             loadLLMEnvConfig(logger),
		Timeouts:        loadRuntimeTimeoutsConfig(logger),
		Drivers:         drivers,
		Images:          images,
		Resources:       resources,
		Cleanup:         cleanup,
		GuestPaths:      guestPaths,
		SandboxTimeouts: sandboxTimeouts,
		HTTPLimits:      loadHTTPLimitsConfig(logger),
	}, nil
}

func ensureConfigDirsExist(normalized configPathsToNormalize) error {
	dirs := map[string]string{
		"DATA_ROOT":         normalized.DataRoot,
		"SANDBOX_ROOT":      normalized.SandboxRoot,
		"BOXLITE_HOME":      normalized.BoxliteHome,
		"DOCKER_HOME":       normalized.DockerHome,
		"IMAGE_CACHE_ROOT":  normalized.ImageCacheRoot,
		"MICROSANDBOX_HOME": normalized.MicrosandboxHome,
		"K8S_HOME":          normalized.K8sHome,
	}
	for name, dir := range dirs {
		if err := ensureDirExists(dir); err != nil {
			return fmt.Errorf("ensure %s exists: %w", name, err)
		}
	}
	return nil
}

func buildConfig(sources configSources, normalized configPathsToNormalize) *Config {
	database, sandbox, daemonHTTP := sources.Database, sources.Sandbox, sources.DaemonHTTP
	llm, timeouts, drivers := sources.LLM, sources.Timeouts, sources.Drivers
	images, resources, cleanup := sources.Images, sources.Resources, sources.Cleanup
	guestPaths, sandboxTimeouts, httpLimits := sources.GuestPaths, sources.SandboxTimeouts, sources.HTTPLimits
	return &Config{
		DbAddr:                     database.DbAddr,
		DbName:                     database.DbName,
		DbTimeout:                  database.DbTimeout,
		SQLiteMaxOpenConns:         database.SQLiteMaxOpenConns,
		DataRoot:                   normalized.DataRoot,
		SandboxRoot:                normalized.SandboxRoot,
		SandboxRootExplicit:        sandbox.Explicit,
		HttpListen:                 daemonHTTP.HttpListen,
		DaemonAuthToken:            daemonHTTP.DaemonAuthToken,
		HttpTLSCertFile:            daemonHTTP.HttpTLSCertFile,
		HttpTLSKeyFile:             daemonHTTP.HttpTLSKeyFile,
		AgentComposeSocket:         daemonHTTP.AgentComposeSocket,
		AgentComposeHost:           daemonHTTP.AgentComposeHost,
		WebhookBodyLimitBytes:      httpLimits.WebhookBodyLimitBytes,
		WebhookQueueRulesJSON:      httpLimits.WebhookQueueRulesJSON,
		WebhookQueueDefaultWorkers: httpLimits.WebhookQueueDefaultWorkers,
		WorkspaceUploadLimitBytes:  httpLimits.WorkspaceUploadLimitBytes,
		LLMAPIEndpoint:             llm.LLMAPIEndpoint,
		LLMAPIProtocol:             llm.LLMAPIProtocol,
		LLMAPIKey:                  llm.LLMAPIKey,
		LLMModel:                   llm.LLMModel,
		LLMTimeout:                 llm.LLMTimeout,
		LLMMaxOutputTokens:         llm.LLMMaxOutputTokens,
		CodexRequestMaxRetries:     llm.CodexRequestMaxRetries,
		CodexStreamMaxRetries:      llm.CodexStreamMaxRetries,
		CodexStreamIdleTimeout:     llm.CodexStreamIdleTimeout,
		RuntimeBaseURL:             llm.RuntimeBaseURL,
		AgentTimeout:               timeouts.AgentTimeout,
		SchedulerRunTimeout:        timeouts.SchedulerRunTimeout,
		RuntimeDriver:              drivers.RuntimeDriver,
		BoxliteHome:                normalized.BoxliteHome,
		BoxliteRuntimeDir:          normalized.BoxliteRuntimeDir,
		DockerHome:                 normalized.DockerHome,
		DockerHostSandboxRoot:      normalized.DockerHostSandboxRoot,
		DockerDefaultImage:         images.DockerDefaultImage,
		MicrosandboxHome:           normalized.MicrosandboxHome,
		MicrosandboxMSBPath:        normalized.MicrosandboxMSBPath,
		MicrosandboxLibPath:        normalized.MicrosandboxLibPath,
		MicrosandboxDefaultImage:   images.MicrosandboxDefaultImage,
		MicrosandboxInsecure:       images.MicrosandboxInsecure,
		K8sHome:                    normalized.K8sHome,
		K8sKubeconfigPath:          drivers.K8sKubeconfigPath,
		K8sNamespace:               drivers.K8sNamespace,
		K8sDefaultImage:            images.K8sDefaultImage,
		K8sRuntimeBaseURL:          drivers.K8sRuntimeBaseURL,
		DefaultImage:               images.DefaultImage,
		BoxRootfsPath:              normalized.BoxRootfsPath,
		ImageRegistry:              images.ImageRegistry,
		ImageStoreMode:             images.ImageStoreMode,
		ImageCacheRoot:             normalized.ImageCacheRoot,
		ImageInsecureRegistries:    images.ImageInsecureRegistries,
		SandboxCPUs:                resources.SandboxCPUs,
		SandboxMemoryMiB:           resources.SandboxMemoryMiB,
		SandboxDiskSizeGB:          resources.SandboxDiskSizeGB,
		CacheTTL:                   resources.CacheTTL,
		CleanupInterval:            cleanup.CleanupInterval,
		WorkspaceCleanupTTL:        cleanup.WorkspaceCleanupTTL,
		SandboxRetentionTTL:        cleanup.SandboxRetentionTTL,
		SandboxArchiveRoot:         normalized.SandboxArchiveRoot,
		ImageCacheCleanupTTL:       cleanup.ImageCacheCleanupTTL,
		ImagePullTimeout:           timeouts.ImagePullTimeout,
		GuestWorkspacePath:         guestPaths.GuestWorkspacePath,
		GuestHomePath:              guestPaths.GuestHomePath,
		GuestStateRoot:             guestPaths.GuestStateRoot,
		GuestRuntimeRoot:           guestPaths.GuestRuntimeRoot,
		GuestLogRoot:               guestPaths.GuestLogRoot,
		JupyterGuestPort:           sandboxTimeouts.JupyterGuestPort,
		SandboxStartTimeout:        sandboxTimeouts.StartTimeout,
		SandboxStopTimeout:         sandboxTimeouts.StopTimeout,
		SandboxGracefulStopTimeout: sandboxTimeouts.GracefulStopTimeout,
		JupyterReadyTimeout:        sandboxTimeouts.JupyterReadyTimeout,
		JupyterProxyBasePath:       sandboxTimeouts.JupyterProxyBase,
		CapGRPCListen:              strings.TrimSpace(os.Getenv("CAP_GRPC_LISTEN")),
		CapGRPCTarget:              strings.TrimSpace(os.Getenv("CAP_GRPC_TARGET")),
		Version:                    BuildVersion,
	}
}

// databaseConfig holds the sqlite connection settings NewConfig derives from
// the environment.
type databaseConfig struct {
	DbAddr             string
	DbName             string
	DbTimeout          time.Duration
	SQLiteMaxOpenConns int
}

func loadDatabaseConfig(logger *slog.Logger, dataRoot string) (databaseConfig, error) {
	dbTimeout := 16 * time.Second
	if raw := os.Getenv("DB_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse database timeout duration", "dbTimeoutStr", raw)
		} else if parsed > time.Millisecond {
			dbTimeout = parsed
			logger.Info("dbTimeout updated", "dbTimeout", dbTimeout)
		}
	}
	sqliteMaxOpenConns, err := sqliteMaxOpenConnsFromEnvironment()
	if err != nil {
		return databaseConfig{}, err
	}
	return databaseConfig{
		DbAddr:             filepath.Join(dataRoot, "data.db"),
		DbName:             "agent_compose",
		DbTimeout:          dbTimeout,
		SQLiteMaxOpenConns: sqliteMaxOpenConns,
	}, nil
}

// sandboxRootConfigValues is the resolved sandbox storage root, along with
// whether it was set explicitly (vs. defaulted or migrated from the legacy
// sessions root).
type sandboxRootConfigValues struct {
	Explicit bool
	Root     string
}

func loadSandboxRootConfig(logger *slog.Logger, dataRoot string) (sandboxRootConfigValues, error) {
	sandboxRootExplicit := strings.TrimSpace(os.Getenv("SANDBOX_ROOT")) != "" || strings.TrimSpace(os.Getenv("SESSION_ROOT")) != ""
	sandboxRoot, err := envWithLegacy(logger, "SANDBOX_ROOT", "SESSION_ROOT")
	if err != nil {
		return sandboxRootConfigValues{}, err
	}
	sandboxRoot = strings.TrimSpace(sandboxRoot)
	if sandboxRoot == "" {
		legacyRoot := filepath.Join(dataRoot, "sessions")
		if nonEmpty, inspectErr := pathHasEntries(legacyRoot); inspectErr != nil {
			return sandboxRootConfigValues{}, fmt.Errorf("inspect legacy sessions root %s: %w", legacyRoot, inspectErr)
		} else if nonEmpty {
			sandboxRoot = legacyRoot
			logger.Warn("using deprecated sessions storage root", "path", legacyRoot, "replacement", filepath.Join(dataRoot, "sandboxes"))
		} else {
			sandboxRoot = filepath.Join(dataRoot, "sandboxes")
		}
	}
	return sandboxRootConfigValues{Explicit: sandboxRootExplicit, Root: sandboxRoot}, nil
}

// daemonHTTPConfig holds the daemon's own listen/auth settings, as opposed to
// the LLM or scheduler HTTP endpoints it talks to.
type daemonHTTPConfig struct {
	HttpListen         string
	DaemonAuthToken    string
	HttpTLSCertFile    string
	HttpTLSKeyFile     string
	AgentComposeSocket string
	AgentComposeHost   string
}

func loadDaemonHTTPConfig() (daemonHTTPConfig, error) {
	httpListen := strings.TrimSpace(os.Getenv("HTTP_LISTEN"))
	if httpListen != "" {
		if err := validateTCPListenAddress("HTTP_LISTEN", httpListen); err != nil {
			return daemonHTTPConfig{}, err
		}
	}
	daemonAuthToken := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_AUTH_TOKEN"))
	httpTLSCertFile := strings.TrimSpace(os.Getenv("HTTP_TLS_CERT_FILE"))
	httpTLSKeyFile := strings.TrimSpace(os.Getenv("HTTP_TLS_KEY_FILE"))
	if (httpTLSCertFile == "") != (httpTLSKeyFile == "") {
		return daemonHTTPConfig{}, errors.New("HTTP_TLS_CERT_FILE and HTTP_TLS_KEY_FILE must be configured together")
	}
	if httpListen != "" && !isLoopbackListenAddress(httpListen) {
		if daemonAuthToken == "" {
			return daemonHTTPConfig{}, errors.New("non-loopback HTTP_LISTEN requires AGENT_COMPOSE_AUTH_TOKEN")
		}
		if httpTLSCertFile == "" {
			return daemonHTTPConfig{}, errors.New("non-loopback HTTP_LISTEN requires HTTP_TLS_CERT_FILE and HTTP_TLS_KEY_FILE")
		}
	}
	agentComposeSocket, err := resolveAgentComposeSocket(os.Getenv("AGENT_COMPOSE_SOCKET"))
	if err != nil {
		return daemonHTTPConfig{}, err
	}
	agentComposeHost := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_HOST"))
	if agentComposeHost != "" {
		if err := validateAgentComposeHost(agentComposeHost); err != nil {
			return daemonHTTPConfig{}, err
		}
	}
	return daemonHTTPConfig{
		HttpListen:         httpListen,
		DaemonAuthToken:    daemonAuthToken,
		HttpTLSCertFile:    httpTLSCertFile,
		HttpTLSKeyFile:     httpTLSKeyFile,
		AgentComposeSocket: agentComposeSocket,
		AgentComposeHost:   agentComposeHost,
	}, nil
}

// llmEnvConfig holds the LLM endpoint/credentials and the Codex runtime
// retry/timeout settings derived from it.
type llmEnvConfig struct {
	LLMAPIEndpoint         string
	LLMAPIProtocol         string
	LLMAPIKey              string
	LLMModel               string
	LLMMaxOutputTokens     int
	RuntimeBaseURL         string
	LLMTimeout             time.Duration
	CodexRequestMaxRetries uint64
	CodexStreamMaxRetries  uint64
	CodexStreamIdleTimeout time.Duration
}

func loadLLMEnvConfig(logger *slog.Logger) llmEnvConfig {
	llmAPIEndpoint := os.Getenv("LLM_API_ENDPOINT")
	llmAPIProtocol := strings.ToLower(strings.TrimSpace(os.Getenv("LLM_API_PROTOCOL")))
	llmAPIKey := getenvFirst("LLM_API_KEY", "OPENAI_API_KEY")

	llmModel := os.Getenv("LLM_MODEL")
	llmMaxOutputTokens := envPositiveInt(logger, "LLM_MAX_OUTPUT_TOKENS")
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
	return llmEnvConfig{
		LLMAPIEndpoint:         llmAPIEndpoint,
		LLMAPIProtocol:         llmAPIProtocol,
		LLMAPIKey:              llmAPIKey,
		LLMModel:               llmModel,
		LLMMaxOutputTokens:     llmMaxOutputTokens,
		RuntimeBaseURL:         runtimeBaseURL,
		LLMTimeout:             llmTimeout,
		CodexRequestMaxRetries: codexRuntime.requestMaxRetries,
		CodexStreamMaxRetries:  codexRuntime.streamMaxRetries,
		CodexStreamIdleTimeout: codexRuntime.streamIdleTimeout,
	}
}

// runtimeTimeoutsConfig holds the agent/image-pull/scheduler-run timeouts
// that gate how long the daemon waits on runtime operations.
type runtimeTimeoutsConfig struct {
	AgentTimeout        time.Duration
	ImagePullTimeout    time.Duration
	SchedulerRunTimeout time.Duration
}

func loadRuntimeTimeoutsConfig(logger *slog.Logger) runtimeTimeoutsConfig {
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
	schedulerRunTimeout := 20 * time.Minute
	schedulerRunTimeoutName := "SCHEDULER_RUN_TIMEOUT"
	rawSchedulerRunTimeout := os.Getenv(schedulerRunTimeoutName)
	if rawSchedulerRunTimeout == "" {
		schedulerRunTimeoutName = "LOADER_RUN_TIMEOUT"
		rawSchedulerRunTimeout = os.Getenv(schedulerRunTimeoutName)
		if rawSchedulerRunTimeout != "" {
			logger.Warn("LOADER_RUN_TIMEOUT is deprecated; use SCHEDULER_RUN_TIMEOUT")
		}
	}
	if raw := rawSchedulerRunTimeout; raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse "+schedulerRunTimeoutName, "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("ignored non-positive "+schedulerRunTimeoutName, "value", raw)
		} else {
			schedulerRunTimeout = parsed
		}
	}
	return runtimeTimeoutsConfig{
		AgentTimeout:        agentTimeout,
		ImagePullTimeout:    imagePullTimeout,
		SchedulerRunTimeout: schedulerRunTimeout,
	}
}

// driverHomesConfig holds the selected runtime driver and each backend's
// home/lib paths before path normalization.
type driverHomesConfig struct {
	RuntimeDriver         string
	BoxliteHome           string
	BoxliteRuntimeDir     string
	DockerHome            string
	DockerHostSandboxRoot string
	MicrosandboxHome      string
	MicrosandboxMSBPath   string
	MicrosandboxLibPath   string
	K8sHome               string
	K8sKubeconfigPath     string
	K8sNamespace          string
	K8sRuntimeBaseURL     string
}

func loadDriverHomesConfig(logger *slog.Logger, dataRoot string) (driverHomesConfig, error) {
	runtimeDriver := os.Getenv("RUNTIME_DRIVER")
	if runtimeDriver == "" {
		runtimeDriver = RuntimeDriverDocker
	}
	runtimeDriver = resolveRuntimeDriver(runtimeDriver)
	if err := validateRuntimeDriver(runtimeDriver); err != nil {
		return driverHomesConfig{}, err
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
		return driverHomesConfig{}, err
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

	k8sHome := os.Getenv("K8S_HOME")
	if k8sHome == "" {
		k8sHome = filepath.Join(dataRoot, "k8s")
	}
	// K8sKubeconfigPath is left empty unless K8S_KUBECONFIG is set, so the k8s
	// driver falls back to client-go's own default loading rules (KUBECONFIG
	// env, then ~/.kube/config), the same resolution kubectl uses. KUBECONFIG
	// itself is deliberately not read here: it becomes clientcmd's
	// ExplicitPath below (k8sRuntime.client), which only accepts a single
	// file, while KUBECONFIG's own documented form is a PATH-list-separator-
	// delimited list of files to merge - the default loading rules already
	// handle that correctly, so reading it into ExplicitPath here would only
	// break the multi-file case.
	k8sKubeconfigPath := strings.TrimSpace(os.Getenv("K8S_KUBECONFIG"))
	k8sNamespace := strings.TrimSpace(os.Getenv("K8S_NAMESPACE"))
	if k8sNamespace == "" {
		k8sNamespace = "default"
	}
	// K8sRuntimeBaseURL overrides AGENT_COMPOSE_RUNTIME_BASE_URL for k8s
	// sandboxes only. It's needed whenever the daemon's own reachable
	// address differs from what a Pod should call back to - for example a
	// Tailscale Cluster Egress Service's in-cluster DNS name when the
	// daemon isn't deployed inside the cluster (see
	// docs/design/k8s_pod_runtime_driver_design.md §2.2). Left empty by
	// default so k8s sandboxes fall back to the daemon-wide value, same as
	// every other driver.
	k8sRuntimeBaseURL := strings.TrimRight(strings.TrimSpace(os.Getenv("K8S_RUNTIME_BASE_URL")), "/")

	return driverHomesConfig{
		RuntimeDriver:         runtimeDriver,
		BoxliteHome:           boxliteHome,
		BoxliteRuntimeDir:     boxliteRuntimeDir,
		DockerHome:            dockerHome,
		DockerHostSandboxRoot: dockerHostSandboxRoot,
		MicrosandboxHome:      microsandboxHome,
		K8sRuntimeBaseURL:     k8sRuntimeBaseURL,
		MicrosandboxMSBPath:   microsandboxMSBPath,
		MicrosandboxLibPath:   microsandboxLibPath,
		K8sHome:               k8sHome,
		K8sKubeconfigPath:     k8sKubeconfigPath,
		K8sNamespace:          k8sNamespace,
	}, nil
}

// imageEnvConfig holds the default guest images and image cache/registry
// settings.
type imageEnvConfig struct {
	DefaultImage             string
	MicrosandboxDefaultImage string
	DockerDefaultImage       string
	K8sDefaultImage          string
	MicrosandboxInsecure     []string
	BoxRootfsPath            string
	ImageRegistry            string
	ImageStoreMode           string
	ImageCacheRoot           string
	ImageInsecureRegistries  []string
}

func loadImageEnvConfig(dataRoot string) (imageEnvConfig, error) {
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
	k8sDefaultImage := os.Getenv("K8S_DEFAULT_IMAGE")
	if k8sDefaultImage == "" {
		k8sDefaultImage = defaultImage
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
		return imageEnvConfig{}, err
	}
	imageCacheRoot := os.Getenv("IMAGE_CACHE_ROOT")
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(dataRoot, "images")
	}
	imageInsecureRegistries := splitAndTrimEnv(os.Getenv("IMAGE_INSECURE_REGISTRIES"))

	return imageEnvConfig{
		DefaultImage:             defaultImage,
		MicrosandboxDefaultImage: microsandboxDefaultImage,
		DockerDefaultImage:       dockerDefaultImage,
		K8sDefaultImage:          k8sDefaultImage,
		MicrosandboxInsecure:     microsandboxInsecure,
		BoxRootfsPath:            boxRootfsPath,
		ImageRegistry:            imageRegistry,
		ImageStoreMode:           imageStoreMode,
		ImageCacheRoot:           imageCacheRoot,
		ImageInsecureRegistries:  imageInsecureRegistries,
	}, nil
}

// sandboxResourceConfig holds the default sandbox VM sizing and the image
// cache TTL.
type sandboxResourceConfig struct {
	SandboxCPUs       uint8
	SandboxMemoryMiB  uint32
	SandboxDiskSizeGB int32
	CacheTTL          time.Duration
}

func loadSandboxResourceConfig(logger *slog.Logger) (sandboxResourceConfig, error) {
	sandboxCPUs := positiveUint8Env(logger, "SANDBOX_CPUS", DefaultSandboxCPUs)
	sandboxMemoryMiB := positiveMemoryMiBEnv(logger, "SANDBOX_MEMORY_MIB", DefaultSandboxMemoryMiB)
	sandboxDiskSizeGB := positiveDiskSizeGBEnv(logger, "SANDBOX_DISK_SIZE_GB", DefaultSandboxDiskSizeGB)
	cacheTTL := 7 * 24 * time.Hour
	cacheTTLRaw, err := envWithLegacy(logger, "CACHE_TTL", "BOX_CACHE_TTL")
	if err != nil {
		return sandboxResourceConfig{}, err
	}
	if raw := strings.TrimSpace(cacheTTLRaw); raw != "" {
		parsed, parseErr := time.ParseDuration(raw)
		if parseErr != nil {
			return sandboxResourceConfig{}, fmt.Errorf("parse CACHE_TTL %q: %w", raw, parseErr)
		}
		if parsed < 0 {
			return sandboxResourceConfig{}, fmt.Errorf("CACHE_TTL must not be negative")
		}
		cacheTTL = parsed
	}
	return sandboxResourceConfig{
		SandboxCPUs:       sandboxCPUs,
		SandboxMemoryMiB:  sandboxMemoryMiB,
		SandboxDiskSizeGB: sandboxDiskSizeGB,
		CacheTTL:          cacheTTL,
	}, nil
}

// cleanupConfigValues holds the automatic cleanup interval and each
// resource's retention TTL.
type cleanupConfigValues struct {
	CleanupInterval      time.Duration
	WorkspaceCleanupTTL  time.Duration
	SandboxRetentionTTL  time.Duration
	SandboxArchiveRoot   string
	ImageCacheCleanupTTL time.Duration
}

func loadCleanupConfig(dataRoot string) (cleanupConfigValues, error) {
	cleanupInterval, err := cleanupDurationEnv("CLEANUP_INTERVAL", time.Hour)
	if err != nil {
		return cleanupConfigValues{}, err
	}
	workspaceCleanupTTL, err := cleanupDurationEnv("WORKSPACE_CLEANUP_TTL", 0)
	if err != nil {
		return cleanupConfigValues{}, err
	}
	sandboxRetentionTTL, err := cleanupDurationEnv("SANDBOX_RETENTION_TTL", 0)
	if err != nil {
		return cleanupConfigValues{}, err
	}
	sandboxArchiveRoot := strings.TrimSpace(os.Getenv("SANDBOX_ARCHIVE_ROOT"))
	if sandboxArchiveRoot == "" {
		sandboxArchiveRoot = filepath.Join(dataRoot, "archives", "sandboxes")
	}
	imageCacheCleanupTTL, err := cleanupDurationEnv("IMAGE_CACHE_CLEANUP_TTL", 0)
	if err != nil {
		return cleanupConfigValues{}, err
	}
	if (workspaceCleanupTTL > 0 || sandboxRetentionTTL > 0 || imageCacheCleanupTTL > 0) && cleanupInterval <= 0 {
		return cleanupConfigValues{}, fmt.Errorf("CLEANUP_INTERVAL must be positive when automatic cleanup is enabled")
	}
	return cleanupConfigValues{
		CleanupInterval:      cleanupInterval,
		WorkspaceCleanupTTL:  workspaceCleanupTTL,
		SandboxRetentionTTL:  sandboxRetentionTTL,
		SandboxArchiveRoot:   sandboxArchiveRoot,
		ImageCacheCleanupTTL: imageCacheCleanupTTL,
	}, nil
}

// sandboxTimeoutsConfig holds the sandbox lifecycle timeouts and the
// Jupyter guest port/proxy settings.
type sandboxTimeoutsConfig struct {
	StartTimeout        time.Duration
	StopTimeout         time.Duration
	GracefulStopTimeout time.Duration
	JupyterGuestPort    int
	JupyterReadyTimeout time.Duration
	JupyterProxyBase    string
}

func loadSandboxTimeoutsConfig(logger *slog.Logger) (sandboxTimeoutsConfig, error) {
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
		return sandboxTimeoutsConfig{}, err
	} else if raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse SANDBOX_START_TIMEOUT", "value", raw, "error", err)
		} else {
			startTimeout = parsed
		}
	}

	stopTimeout := 30 * time.Second
	if raw, err := envWithLegacy(logger, "SANDBOX_STOP_TIMEOUT", "SESSION_STOP_TIMEOUT"); err != nil {
		return sandboxTimeoutsConfig{}, err
	} else if raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse SANDBOX_STOP_TIMEOUT", "value", raw, "error", err)
		} else {
			stopTimeout = parsed
		}
	}
	gracefulStopTimeout := 10 * time.Second
	if raw := os.Getenv("SANDBOX_GRACEFUL_STOP_TIMEOUT"); raw != "" {
		if parsed, err := time.ParseDuration(raw); err != nil {
			logger.Warn("failed to parse SANDBOX_GRACEFUL_STOP_TIMEOUT", "value", raw, "error", err)
		} else if parsed <= 0 {
			logger.Warn("SANDBOX_GRACEFUL_STOP_TIMEOUT must be positive", "value", raw)
		} else {
			gracefulStopTimeout = parsed
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

	return sandboxTimeoutsConfig{
		StartTimeout:        startTimeout,
		StopTimeout:         stopTimeout,
		GracefulStopTimeout: gracefulStopTimeout,
		JupyterGuestPort:    jupyterGuestPort,
		JupyterReadyTimeout: jupyterReadyTimeout,
		JupyterProxyBase:    jupyterProxyBase,
	}, nil
}

// httpLimitsConfig holds the webhook/workspace-upload body size and queue
// worker limits.
type httpLimitsConfig struct {
	WebhookBodyLimitBytes      int64
	WebhookQueueRulesJSON      string
	WebhookQueueDefaultWorkers int
	WorkspaceUploadLimitBytes  int64
}

func loadHTTPLimitsConfig(logger *slog.Logger) httpLimitsConfig {
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
	return httpLimitsConfig{
		WebhookBodyLimitBytes:      webhookBodyLimitBytes,
		WebhookQueueRulesJSON:      webhookQueueRulesJSON,
		WebhookQueueDefaultWorkers: webhookQueueDefaultWorkers,
		WorkspaceUploadLimitBytes:  workspaceUploadLimitBytes,
	}
}

// configPathsToNormalize bundles the filesystem paths NewConfig resolves to
// absolute form (and, for a couple of them, extra normalization) once every
// env-derived value is known.
type configPathsToNormalize struct {
	DataRoot              string
	SandboxRoot           string
	SandboxArchiveRoot    string
	BoxliteHome           string
	BoxliteRuntimeDir     string
	DockerHome            string
	DockerHostSandboxRoot string
	MicrosandboxHome      string
	MicrosandboxMSBPath   string
	MicrosandboxLibPath   string
	K8sHome               string
	ImageCacheRoot        string
	BoxRootfsPath         string
}

func normalizeConfigPaths(paths configPathsToNormalize) (configPathsToNormalize, error) {
	paths.DataRoot = mustAbs(paths.DataRoot)
	paths.SandboxRoot = mustAbs(paths.SandboxRoot)
	paths.SandboxArchiveRoot = mustAbs(paths.SandboxArchiveRoot)
	if err := validateSandboxArchiveRoot(paths.SandboxRoot, paths.SandboxArchiveRoot); err != nil {
		return configPathsToNormalize{}, err
	}
	paths.BoxliteHome = mustAbs(paths.BoxliteHome)
	paths.BoxliteRuntimeDir = mustAbs(paths.BoxliteRuntimeDir)
	paths.DockerHome = mustAbs(paths.DockerHome)
	dockerHostSandboxRoot, err := normalizeDockerHostSandboxRoot(paths.DockerHostSandboxRoot)
	if err != nil {
		return configPathsToNormalize{}, err
	}
	paths.DockerHostSandboxRoot = dockerHostSandboxRoot
	paths.MicrosandboxHome = mustAbs(paths.MicrosandboxHome)
	paths.MicrosandboxMSBPath = mustAbs(paths.MicrosandboxMSBPath)
	paths.MicrosandboxLibPath = mustAbs(paths.MicrosandboxLibPath)
	paths.K8sHome = mustAbs(paths.K8sHome)
	paths.ImageCacheRoot = mustAbs(paths.ImageCacheRoot)
	if paths.BoxRootfsPath != "" {
		paths.BoxRootfsPath = mustAbs(paths.BoxRootfsPath)
	}
	return paths, nil
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
	case RuntimeDriverK8s, "kubernetes", "pod":
		return RuntimeDriverK8s
	default:
		return strings.ToLower(strings.TrimSpace(value))
	}
}

func validateRuntimeDriver(value string) error {
	switch resolveRuntimeDriver(value) {
	case RuntimeDriverBoxlite, RuntimeDriverDocker, RuntimeDriverMicrosandbox, RuntimeDriverK8s:
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
