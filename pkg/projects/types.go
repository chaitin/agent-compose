package projects

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"agent-compose/pkg/bus"
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/executor"
	"agent-compose/pkg/images"
	"agent-compose/pkg/llm"
	"agent-compose/pkg/loaders"
	"agent-compose/pkg/model"
	"agent-compose/pkg/runtimes"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage"
	"agent-compose/pkg/workspaces"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

const (
	VMStatusRunning = model.VMStatusRunning
	VMStatusStopped = model.VMStatusStopped
	VMStatusFailed  = model.VMStatusFailed

	SessionTypeManual = model.SessionTypeManual

	ProjectRunStatusPending   = model.ProjectRunStatusPending
	ProjectRunStatusRunning   = model.ProjectRunStatusRunning
	ProjectRunStatusSucceeded = model.ProjectRunStatusSucceeded
	ProjectRunStatusFailed    = model.ProjectRunStatusFailed
	ProjectRunStatusCanceled  = model.ProjectRunStatusCanceled

	ProjectRunSourceManual    = model.ProjectRunSourceManual
	ProjectRunSourceScheduler = model.ProjectRunSourceScheduler
	ProjectRunSourceAPI       = model.ProjectRunSourceAPI

	LoaderRuntimeScheduler = model.LoaderRuntimeScheduler

	LoaderTriggerKindInterval = model.LoaderTriggerKindInterval
	LoaderTriggerKindEvent    = model.LoaderTriggerKindEvent
	LoaderTriggerKindTimeout  = model.LoaderTriggerKindTimeout
	LoaderTriggerKindCron     = model.LoaderTriggerKindCron

	LoaderSessionPolicyNew      = model.LoaderSessionPolicyNew
	LoaderConcurrencyPolicySkip = model.LoaderConcurrencyPolicySkip
	defaultAgentProvider        = model.DefaultAgentProvider
)

type Store = storage.Store
type ConfigStore = storage.ConfigStore
type ProjectRecord = storage.ProjectRecord
type ProjectRevisionRecord = storage.ProjectRevisionRecord
type ProjectAgentRecord = storage.ProjectAgentRecord
type ProjectSchedulerRecord = storage.ProjectSchedulerRecord
type ProjectRunRecord = storage.ProjectRunRecord
type ProjectListOptions = storage.ProjectListOptions
type ProjectRunListOptions = storage.ProjectRunListOptions

type Session = model.Session
type SessionSummary = model.SessionSummary
type SessionListOptions = model.SessionListOptions
type SessionTag = model.SessionTag
type SessionEnvVar = model.SessionEnvVar
type SessionEvent = model.SessionEvent
type SessionWorkspace = model.SessionWorkspace
type NotebookCell = model.NotebookCell
type ExecChunk = model.ExecChunk
type AgentDefinition = model.AgentDefinition
type WorkspaceConfig = model.WorkspaceConfig
type Loader = model.Loader
type LoaderSummary = model.LoaderSummary
type LoaderTrigger = model.LoaderTrigger
type LoaderValidationResult = loaders.LoaderValidationResult
type LoaderTopicEvent = bus.LoaderTopicEvent
type Executor = executor.Executor
type ExecuteAgentRequest = executor.ExecuteAgentRequest
type AgentExecutionStream = executor.AgentExecutionStream
type SessionStreamBroker = sessions.SessionStreamBroker
type ImageInspectRequest = images.ImageInspectRequest
type ImagePullRequest = images.ImagePullRequest
type fileWorkspaceContent = workspaces.FileWorkspaceContent
type gitWorkspaceConfig = workspaces.GitWorkspaceConfig

type Driver = runtimes.Driver
type CapabilityProvider = capabilities.Provider
type DashboardOverviewHub = dashboard.DashboardOverviewHub

type Service struct {
	config    *appconfig.Config
	store     *Store
	configDB  *ConfigStore
	driver    Driver
	executor  *executor.Executor
	images    images.ImageBackend
	loaders   *loaders.LoaderManager
	cap       CapabilityProvider
	bus       *bus.LoaderBus
	streams   *sessions.SessionStreamBroker
	dashboard *DashboardOverviewHub
}

type ServiceDeps struct {
	Config    *appconfig.Config
	Store     *Store
	ConfigDB  *ConfigStore
	Driver    Driver
	Executor  *executor.Executor
	Images    images.ImageBackend
	Loaders   *loaders.LoaderManager
	Cap       CapabilityProvider
	Bus       *bus.LoaderBus
	Streams   *sessions.SessionStreamBroker
	Dashboard *DashboardOverviewHub
}

func NewService(deps ServiceDeps) *Service {
	return &Service{
		config:    deps.Config,
		store:     deps.Store,
		configDB:  deps.ConfigDB,
		driver:    deps.Driver,
		executor:  deps.Executor,
		images:    deps.Images,
		loaders:   deps.Loaders,
		cap:       deps.Cap,
		bus:       deps.Bus,
		streams:   deps.Streams,
		dashboard: deps.Dashboard,
	}
}

func (s *Service) RunProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest) (ProjectRunRecord, error, error) {
	return s.runProjectAgent(ctx, msg, nil)
}

func StableProjectID(name, sourcePath string) (string, error) {
	return storage.StableProjectID(name, sourcePath)
}

func StableManagedAgentID(projectID, agentName string) (string, error) {
	return storage.StableManagedAgentID(projectID, agentName)
}

func StableProjectSchedulerID(projectID, agentName, schedulerName string) (string, error) {
	return storage.StableProjectSchedulerID(projectID, agentName, schedulerName)
}

func StableManagedLoaderID(projectID, agentName, schedulerName string) (string, error) {
	return storage.StableManagedLoaderID(projectID, agentName, schedulerName)
}

func StableManagedTriggerID(projectID, agentName, schedulerName, triggerName string, triggerIndex int) (string, error) {
	return storage.StableManagedTriggerID(projectID, agentName, schedulerName, triggerName, triggerIndex)
}

func StableProjectRunID(projectID, agentName, source, idempotencyKey string) (string, error) {
	return storage.StableProjectRunID(projectID, agentName, source, idempotencyKey)
}

func StableReadableID(prefix, readable, seed string) string {
	return storage.StableReadableID(prefix, readable, seed)
}

func NewProjectRecordFromSpec(spec *compose.NormalizedProjectSpec, sourcePath string) (ProjectRecord, error) {
	return storage.NewProjectRecordFromSpec(spec, sourcePath)
}

func NewProjectAgentRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectAgentRecord, error) {
	return storage.NewProjectAgentRecordFromSpec(projectID, revision, agent)
}

func NewProjectSchedulerRecordFromSpec(projectID string, revision int64, agent compose.NormalizedAgentSpec) (ProjectSchedulerRecord, bool, error) {
	return storage.NewProjectSchedulerRecordFromSpec(projectID, revision, agent)
}

func normalizeProjectRunStatus(status string) string {
	return storage.NormalizeProjectRunStatus(status)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func hostSessionDir(session *Session) string {
	return filepath.Dir(session.Summary.WorkspacePath)
}

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	return model.NormalizeSessionEnvItems(items)
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	return model.MergeSessionEnvItems(globalItems, sessionItems)
}

func normalizeCapsetIDs(ids []string) []string {
	return capabilities.NormalizeCapsetIDs(ids)
}

func filterPersistedRuntimeEnv(items []SessionEnvVar) []SessionEnvVar {
	return llm.FilterPersistedRuntimeEnv(items)
}

func toSessionWorkspaceSnapshot(item WorkspaceConfig) *SessionWorkspace {
	return &SessionWorkspace{ID: item.ID, Name: item.Name, Type: item.Type, ConfigJSON: item.ConfigJSON}
}

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session) error {
	return workspaces.PrepareSessionWorkspace(ctx, config, configDB, session)
}

func prepareStreamingHeaders(headers http.Header) {
	if headers == nil {
		return
	}
	headers.Set("Cache-Control", "no-cache, no-transform")
	headers.Set("X-Accel-Buffering", "no")
}

func validateFileWorkspaceConfig(config *appconfig.Config, workspaceID, configJSON string) (string, error) {
	return workspaces.ValidateFileWorkspaceConfig(config, workspaceID, configJSON)
}

func defaultFileWorkspaceConfigJSON(config *appconfig.Config, workspaceID string) string {
	return workspaces.DefaultFileWorkspaceConfigJSON(config, workspaceID)
}

func openFileWorkspaceContent(config *appconfig.Config, workspace WorkspaceConfig) (fileWorkspaceContent, error) {
	return workspaces.OpenFileWorkspaceContent(config, workspace)
}

func fileWorkspaceContentRelRoot(workspaceID string) (string, error) {
	return workspaces.FileWorkspaceContentRelRoot(workspaceID)
}

func openFileWorkspaceDataRoot(config *appconfig.Config) (*os.Root, error) {
	return workspaces.OpenFileWorkspaceDataRoot(config)
}

func normalizeGitCloneTarget(workspaceID, raw string) (string, error) {
	return workspaces.NormalizeGitCloneTarget(workspaceID, raw)
}

func copyRootDirectoryContents(srcRoot *os.Root, dstDir string) error {
	return workspaces.CopyRootDirectoryContents(srcRoot, dstDir)
}

func buildCapabilityGatewaySessionVars(publicTarget string, capsetIDs []string) ([]SessionEnvVar, []SessionTag) {
	return capabilities.BuildGatewaySessionVars(publicTarget, capsetIDs)
}

func writeCapabilityGuide(ctx context.Context, provider CapabilityProvider, store *Store, streams *sessions.SessionStreamBroker, session *Session, capsetIDs []string) {
	capabilities.WriteGuide(ctx, provider, store, streams, session, capsetIDs)
}

func capabilityGatewayProxyTarget(provider CapabilityProvider) string {
	return capabilities.GatewayProxyTarget(provider)
}

func sessionCapabilityCapsets(session *Session) []string {
	return capabilities.SessionCapabilityCapsets(session)
}

func restoreSessionTransientFields(dst, src *Session) {
	model.RestoreSessionTransientFields(dst, src)
}

func normalizeAgentDefinition(item AgentDefinition, assignDefaults bool) (AgentDefinition, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Description = strings.TrimSpace(item.Description)
	item.Provider = model.NormalizeAgentProvider(item.Provider)
	if item.Provider == "" && assignDefaults {
		item.Provider = defaultAgentProvider
	}
	item.Model = strings.TrimSpace(item.Model)
	item.SystemPrompt = strings.TrimSpace(item.SystemPrompt)
	item.Driver = strings.TrimSpace(item.Driver)
	item.GuestImage = strings.TrimSpace(item.GuestImage)
	item.WorkspaceID = strings.TrimSpace(item.WorkspaceID)
	item.CapsetIDs = normalizeCapsetIDs(item.CapsetIDs)
	item.ManagedProjectID = strings.TrimSpace(item.ManagedProjectID)
	item.ManagedAgentName = strings.TrimSpace(item.ManagedAgentName)
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	if item.ID == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition id is required")
	}
	if item.Name == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition name is required")
	}
	if item.Provider == "" {
		return AgentDefinition{}, fmt.Errorf("agent definition provider is required")
	}
	if item.Provider != "codex" && item.Provider != "claude" && item.Provider != "gemini" && item.Provider != "opencode" {
		return AgentDefinition{}, fmt.Errorf("agent definition provider %q is not supported", item.Provider)
	}
	if !loaders.JSONObjectDocument(item.ConfigJSON) {
		return AgentDefinition{}, fmt.Errorf("agent definition config_json must be a JSON object")
	}
	if item.ManagedProjectID == "" {
		item.ManagedProjectRevision = 0
		item.ManagedAgentName = ""
	} else {
		if item.ManagedAgentName == "" {
			return AgentDefinition{}, fmt.Errorf("managed agent name is required")
		}
		if item.ManagedProjectRevision < 0 {
			return AgentDefinition{}, fmt.Errorf("managed project revision cannot be negative")
		}
	}
	item.EnvItems = normalizeEnvItems(item.EnvItems)
	return item, nil
}

func agentExecutionConfigFromDefinition(agent AgentDefinition, fallbackProvider string) agentExecutionConfig {
	return loaders.AgentExecutionConfigFromDefinition(agent, fallbackProvider)
}

type agentExecutionConfig = loaders.AgentExecutionConfig

func loaderCronSpecJSON(expr, timezone string) (string, error) {
	return loaders.LoaderCronSpecJSON(expr, timezone)
}

func marshalJSONCompact(value any) (string, error) {
	return loaders.MarshalJSONCompact(value)
}

func sessionTopicPayload(session *Session, source string) map[string]any {
	return loaders.SessionTopicPayload(session, source)
}

type driverImageEnsureRequest struct {
	Driver      string
	ImageRef    string
	ProjectName string
	AgentName   string
}

func (s *Service) ensureProjectAgentImages(ctx context.Context, projectName string, agents []ProjectAgentRecord) error {
	if s == nil || s.config == nil {
		return fmt.Errorf("image ensure config is required")
	}
	for _, agent := range agents {
		driver, err := driverpkg.ResolveSessionRuntimeDriver(agent.Driver, s.config.RuntimeDriver)
		if err != nil {
			return fmt.Errorf("ensure image for project %s agent %s: %w", projectName, agent.AgentName, err)
		}
		imageRef := driverpkg.ResolveSessionGuestImage(agent.Image, driverpkg.DefaultGuestImageForDriver(s.config, driver))
		if err := s.ensureDriverImage(ctx, driverImageEnsureRequest{
			Driver:      driver,
			ImageRef:    imageRef,
			ProjectName: projectName,
			AgentName:   agent.AgentName,
		}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ensureDriverImage(ctx context.Context, req driverImageEnsureRequest) error {
	if s == nil {
		return images.EnsureDriverImage(ctx, nil, nil, images.DriverImageEnsureRequest(req))
	}
	return images.EnsureDriverImage(ctx, s.config, s.images, images.DriverImageEnsureRequest(req))
}
