package adapters

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/volumes"
	"agent-compose/pkg/workspaces"
)

type LoaderVolumeResolver interface {
	ResolveMounts(ctx context.Context, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) ([]domain.SandboxVolumeMount, []string, error)
}

type LoaderSandboxRunner struct {
	Config           *appconfig.Config
	Store            *sessionstore.Store
	ConfigDB         *configstore.ConfigStore
	workspaceEnsurer workspaces.WorkspaceEnsurer
	Driver           sessions.SandboxDriver
	Cap              capabilities.Provider
	Volumes          LoaderVolumeResolver
	Streams          *sessions.StreamBroker
	Publisher        loaders.ControllerPublisher
	CapTokens        *CapabilitySandboxResolver
	AgentExecutor    *AgentExecutor
	LifecycleLocks   *sessions.LifecycleLocks
}

func NewLoaderSandboxRunner(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, workspaceEnsurer workspaces.WorkspaceEnsurer, driver sessions.SandboxDriver, cap capabilities.Provider, volumeResolver LoaderVolumeResolver, streams *sessions.StreamBroker, publisher loaders.ControllerPublisher, capTokens *CapabilitySandboxResolver, agentExecutor *AgentExecutor, locks ...*sessions.LifecycleLocks) *LoaderSandboxRunner {
	runner := &LoaderSandboxRunner{Config: config, Store: store, ConfigDB: configDB, workspaceEnsurer: workspaceEnsurer, Driver: driver, Cap: cap, Volumes: volumeResolver, Streams: streams, Publisher: publisher, CapTokens: capTokens, AgentExecutor: agentExecutor}
	if len(locks) > 0 {
		runner.LifecycleLocks = locks[0]
	}
	return runner
}

func (r *LoaderSandboxRunner) Shutdown(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	unlock := r.LifecycleLocks.Lock(sessionID)
	defer unlock()
	stopCtx := context.WithoutCancel(ctx)
	session, err := r.Store.GetSandbox(stopCtx, sessionID)
	if err != nil {
		return err
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return nil
	}
	if err := r.Driver.StopSandboxVM(stopCtx, session); err != nil {
		return err
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	if err := r.Store.UpdateSandbox(stopCtx, session); err != nil {
		return err
	}
	if r.Streams != nil {
		r.Streams.PublishSandboxUpdated(&session.Summary)
	}
	event := domain.SandboxEvent{ID: uuid.NewString(), Type: "sandbox.stopped", Level: "info", Message: "sandbox stopped", CreatedAt: time.Now().UTC()}
	_ = r.Store.AddEvent(stopCtx, session.Summary.ID, event)
	if r.Streams != nil {
		r.Streams.PublishEventAdded(session.Summary.ID, event)
	}
	r.revokeCapabilitySandbox(session.Summary.ID)
	loaded, err := r.Store.GetSandbox(stopCtx, session.Summary.ID)
	if err != nil {
		return err
	}
	r.publish("agent-compose.session.stopped", loaders.SessionTopicPayload(loaded, "loader"))
	return nil
}

func (r *LoaderSandboxRunner) Ensure(ctx context.Context, loader domain.Loader, request domain.LoaderAgentRequest, titleOverridesSession bool) (*domain.Sandbox, string, error) {
	agentDefinition, err := r.ResolveLoaderAgentDefinition(ctx, loader)
	if err != nil {
		return nil, "", err
	}
	effectivePolicy := domain.NormalizeLoaderSandboxPolicy(loader.Summary.SandboxPolicy)
	if strings.TrimSpace(domain.LoaderAgentSandboxPolicy(request)) != "" {
		effectivePolicy = domain.NormalizeLoaderSandboxPolicy(domain.LoaderAgentSandboxPolicy(request))
	}
	hasOverrides := loaders.AgentRequestOverridesSession(request, titleOverridesSession)
	forceNew := effectivePolicy == domain.LoaderSandboxPolicyNew || hasOverrides
	globalEnvItems, err := r.ConfigDB.ListGlobalEnv(ctx)
	if err != nil {
		return nil, "", err
	}
	var providerEnvItems []domain.SandboxEnvVar
	if agentDefinition != nil {
		providerEnvItems = domain.MergeEnvItems(providerEnvItems, agentDefinition.EnvItems)
	}
	agentConfig := execution.AgentConfig{Provider: domain.NormalizeAgentKind(request.Agent)}
	if agentDefinition != nil {
		agentConfig = execution.AgentConfigFromDefinition(*agentDefinition, domain.DefaultAgentProvider)
		if requestedProvider := domain.NormalizeAgentKind(request.Agent); requestedProvider != "" {
			agentConfig.Provider = requestedProvider
		}
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = domain.NormalizeAgentKind(loader.Summary.DefaultAgent)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = domain.DefaultAgentProvider
	}
	providerEnvItems = domain.MergeEnvItems(providerEnvItems, loader.EnvItems)
	providerEnvItems = domain.MergeEnvItems(providerEnvItems, domain.LoaderAgentSandboxEnv(request))
	envItems := domain.MergeEnvItems(globalEnvItems, providerEnvItems)
	envItems = llms.FilterPersistedRuntimeEnv(envItems)
	workspaceID := r.workspaceID(loader, request, agentDefinition)
	workspaceSnapshot, err := r.workspaceSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	driver, err := r.driver(request, loader, agentDefinition)
	if err != nil {
		return nil, "", err
	}
	if err := validateLoaderRuntimeDriverCompiled(driver); err != nil {
		return nil, "", err
	}
	guestImage := r.guestImage(request, loader, agentDefinition, driver)
	volumeMounts, volumeWarnings, err := r.resolveVolumeMounts(ctx, loader, request, agentDefinition)
	if err != nil {
		return nil, "", err
	}
	baseConfigHash, err := loaderSandboxConfigHash(loader)
	if err != nil {
		return nil, "", err
	}
	configHash, err := loaderRequestSandboxConfigHash(baseConfigHash, request, agentDefinition, providerEnvItems, envItems, workspaceSnapshot, driver, guestImage, volumeMounts)
	if err != nil {
		return nil, "", err
	}
	var previousBinding *domain.LoaderBinding
	if !forceNew {
		if session, eventType, reused, binding, err := r.reuseCompatibleLoaderBinding(ctx, loader, request.BindingTriggerID, configHash); err != nil {
			return nil, "", err
		} else if reused {
			return session, eventType, nil
		} else {
			previousBinding = binding
		}
	}

	capabilityVars, capabilityTags := capabilities.BuildGatewaySandboxVars(capabilities.ProxyTarget(r.Cap), loader.Summary.CapsetIDs)
	envItems = domain.MergeEnvItems(envItems, capabilityVars)
	origin := "loader"
	if strings.TrimSpace(loader.Summary.ManagedProjectID) != "" {
		origin = "scheduler"
	}
	tags := []domain.SandboxTag{
		{Name: "origin", Value: origin},
		{Name: "loader_id", Value: loader.Summary.ID},
		{Name: "loader_name", Value: loader.Summary.Name},
		{Name: domain.AgentSandboxTagProvider, Value: agentConfig.Provider},
	}
	if origin == "scheduler" {
		projectID := strings.TrimSpace(loader.Summary.ManagedProjectID)
		tags = append(tags,
			domain.SandboxTag{Name: "project", Value: projectID},
			domain.SandboxTag{Name: "project_id", Value: projectID},
			domain.SandboxTag{Name: "agent", Value: loader.Summary.ManagedAgentName},
			domain.SandboxTag{Name: "scheduler_id", Value: loader.Summary.ManagedSchedulerID},
		)
	}
	tags = append(tags, capabilityTags...)
	title := firstNonEmpty(strings.TrimSpace(request.Title), strings.TrimSpace(loader.Summary.Name), domain.DefaultLoaderName(time.Now().UTC()))
	if agentDefinition != nil {
		tags = append(tags,
			domain.SandboxTag{Name: domain.AgentSandboxTagSource, Value: domain.AgentSandboxTagSourceVal},
			domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: agentDefinition.ID},
			domain.SandboxTag{Name: domain.AgentSandboxTagName, Value: agentDefinition.Name},
		)
	}
	session, err := r.Store.CreateSandboxWithOptions(ctx, title, "", driver, guestImage, workspaceID, domain.SandboxTypeScript+":"+loader.Summary.ID, workspaceSnapshot, envItems, tags, sessionstore.CreateSandboxOptions{
		JupyterEnabled: request.JupyterEnabled,
		VolumeMounts:   volumeMounts,
	})
	if err != nil {
		return nil, "", err
	}
	llms.SetSandboxProviderEnvItems(session, providerEnvItems)
	if request.PullPolicy != "" {
		session.Summary.PullPolicy = request.PullPolicy
		if err := r.Store.UpdateSandbox(ctx, session); err != nil {
			return nil, "", fmt.Errorf("persist sandbox pull policy: %w", err)
		}
	}
	r.recordVolumeWarnings(ctx, session.Summary.ID, volumeWarnings)
	if err := r.workspaceEnsurer.Ensure(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = r.Store.UpdateSandbox(ctx, session)
		return nil, "", err
	}
	writeCapabilityGuide(ctx, r.Cap, r.Store, r.Streams, session, loader.Summary.CapsetIDs)
	if r.AgentExecutor == nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = r.Store.UpdateSandbox(ctx, session)
		return nil, "", fmt.Errorf("agent executor is required")
	}
	if err := r.AgentExecutor.PrepareSandboxAgentEnvironment(ctx, session, agentConfig, agentDefinition); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = r.Store.UpdateSandbox(ctx, session)
		return nil, "", err
	}
	if err := r.Driver.StartSandboxVM(ctx, session); err != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = r.Store.UpdateSandbox(ctx, session)
		return nil, "", err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := r.Store.UpdateSandbox(ctx, session); err != nil {
		return nil, "", err
	}
	if r.Streams != nil {
		r.Streams.PublishSandboxUpdated(&session.Summary)
	}
	event := domain.SandboxEvent{ID: uuid.NewString(), Type: "sandbox.created", Level: "info", Message: fmt.Sprintf("sandbox started with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = r.Store.AddEvent(ctx, session.Summary.ID, event)
	if r.Streams != nil {
		r.Streams.PublishEventAdded(session.Summary.ID, event)
	}
	if effectivePolicy == domain.LoaderSandboxPolicySticky && !forceNew {
		claimed, err := r.bindLoaderSandbox(ctx, loader, request.BindingTriggerID, session.Summary.ID, configHash, previousBinding)
		if err != nil {
			_ = r.Shutdown(ctx, session.Summary.ID)
			return nil, "", fmt.Errorf("persist loader sticky sandbox binding: %w", err)
		}
		if !claimed {
			if err := r.Shutdown(ctx, session.Summary.ID); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, "", fmt.Errorf("retire unclaimed loader sticky sandbox: %w", err)
			}
			winner, eventType, reused, err := r.reuseWinningLoaderBinding(ctx, loader.Summary.ID, request.BindingTriggerID, configHash)
			if err != nil {
				return nil, "", fmt.Errorf("reuse concurrently claimed loader sticky sandbox: %w", err)
			}
			if reused {
				return winner, eventType, nil
			}
			return nil, "", fmt.Errorf("loader sticky sandbox binding changed concurrently")
		}
	}
	loaded, err := r.Store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	domain.RestoreSandboxTransientFields(loaded, session)
	r.indexCapabilitySandbox(loaded)
	r.publish("agent-compose.session.created", map[string]any{
		"sandboxId":     loaded.Summary.ID,
		"title":         loaded.Summary.Title,
		"driver":        loaded.Summary.Driver,
		"triggerSource": loaded.Summary.TriggerSource,
		"source":        "loader",
		"loaderId":      loader.Summary.ID,
	})
	return loaded, "loader.sandbox.created", nil
}

func (r *LoaderSandboxRunner) Load(ctx context.Context, sessionID string) (*domain.Sandbox, error) {
	return r.Store.GetSandbox(ctx, sessionID)
}

func (r *LoaderSandboxRunner) LoadOrResume(ctx context.Context, sessionID string) (*domain.Sandbox, string, error) {
	unlock := r.LifecycleLocks.Lock(sessionID)
	defer unlock()
	return r.loadOrResumeLocked(ctx, sessionID)
}

func (r *LoaderSandboxRunner) loadOrResumeLocked(ctx context.Context, sessionID string) (*domain.Sandbox, string, error) {
	session, err := r.Store.GetSandbox(ctx, sessionID)
	if err != nil {
		return nil, "", err
	}
	if session.Summary.VMStatus == domain.VMStatusRunning {
		return session, "", nil
	}
	if session.Summary.VMStatus == domain.VMStatusDeleting {
		return nil, "", fmt.Errorf("sandbox %s is being deleted", sessionID)
	}
	if validator, ok := r.Driver.(sessions.SandboxRuntimeValidator); ok {
		if err := validator.ValidateSandboxRuntime(session); err != nil {
			return nil, "", err
		}
	}
	if err := r.workspaceEnsurer.Ensure(ctx, session); err != nil {
		return nil, "", err
	}
	vmState, err := r.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	if vmState.StartedAt.IsZero() {
		if err := r.AgentExecutor.PrepareSandboxAgentEnvironmentFromTags(ctx, session); err != nil {
			return nil, "", err
		}
	}
	writeCapabilityGuide(ctx, r.Cap, r.Store, r.Streams, session, capabilities.SandboxCapsets(session))
	if err := r.Driver.StartSandboxVM(ctx, session); err != nil {
		return nil, "", err
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := r.Store.UpdateSandbox(ctx, session); err != nil {
		return nil, "", err
	}
	if r.Streams != nil {
		r.Streams.PublishSandboxUpdated(&session.Summary)
	}
	event := domain.SandboxEvent{ID: uuid.NewString(), Type: "sandbox.resumed", Level: "info", Message: fmt.Sprintf("sandbox resumed with %s driver using guest image %s", session.Summary.Driver, session.Summary.GuestImage), CreatedAt: time.Now().UTC()}
	_ = r.Store.AddEvent(ctx, session.Summary.ID, event)
	if r.Streams != nil {
		r.Streams.PublishEventAdded(session.Summary.ID, event)
	}
	loaded, err := r.Store.GetSandbox(ctx, session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	domain.RestoreSandboxTransientFields(loaded, session)
	r.indexCapabilitySandbox(loaded)
	r.publish("agent-compose.session.resumed", map[string]any{
		"sandboxId": loaded.Summary.ID,
		"title":     loaded.Summary.Title,
		"driver":    loaded.Summary.Driver,
		"source":    "loader",
	})
	return loaded, "loader.sandbox.resumed", nil
}

func (r *LoaderSandboxRunner) indexCapabilitySandbox(session *domain.Sandbox) {
	if r != nil && r.CapTokens != nil {
		r.CapTokens.IndexSandbox(session)
	}
}

func (r *LoaderSandboxRunner) revokeCapabilitySandbox(sandboxID string) {
	if r != nil && r.CapTokens != nil {
		r.CapTokens.RevokeSandbox(sandboxID)
	}
}

func (r *LoaderSandboxRunner) ResolveLoaderAgentDefinition(ctx context.Context, loader domain.Loader) (*domain.AgentDefinition, error) {
	agentID := strings.TrimSpace(loader.Summary.AgentID)
	if agentID == "" {
		return nil, nil
	}
	agent, err := r.ConfigDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("loader agent definition %s: %w", agentID, err)
	}
	if !agent.Enabled {
		return nil, fmt.Errorf("loader agent definition %s is disabled", agentID)
	}
	return &agent, nil
}

func (r *LoaderSandboxRunner) workspaceID(loader domain.Loader, request domain.LoaderAgentRequest, agentDefinition *domain.AgentDefinition) string {
	workspaceID := firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID))
	if agentDefinition != nil {
		workspaceID = firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(loader.Summary.WorkspaceID), strings.TrimSpace(agentDefinition.WorkspaceID))
	}
	return workspaceID
}

func (r *LoaderSandboxRunner) workspaceSnapshot(ctx context.Context, workspaceID string) (*domain.SandboxWorkspace, error) {
	if workspaceID == "" {
		return nil, nil
	}
	workspaceConfig, err := r.ConfigDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return toSandboxWorkspaceSnapshot(workspaceConfig), nil
}

func (r *LoaderSandboxRunner) driver(request domain.LoaderAgentRequest, loader domain.Loader, agentDefinition *domain.AgentDefinition) (string, error) {
	driverValue := firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver))
	if agentDefinition != nil {
		driverValue = firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(loader.Summary.Driver), strings.TrimSpace(agentDefinition.Driver))
	}
	return driverpkg.ResolveSandboxRuntimeDriver(driverValue, r.Config.RuntimeDriver)
}

func validateLoaderRuntimeDriverCompiled(driver string) error {
	err := driverpkg.ValidateCompiledRuntimeDriver(driver)
	if errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		return domain.ClassifyError(domain.ErrUnsupported, "", err)
	}
	return err
}

func (r *LoaderSandboxRunner) guestImage(request domain.LoaderAgentRequest, loader domain.Loader, agentDefinition *domain.AgentDefinition, driver string) string {
	agentGuestImage := ""
	if agentDefinition != nil {
		agentGuestImage = agentDefinition.GuestImage
	}
	return driverpkg.ResolveSandboxGuestImage(request.GuestImage, loader.Summary.GuestImage, agentGuestImage, driverpkg.DefaultGuestImageForDriver(r.Config, driver))
}

func (r *LoaderSandboxRunner) resolveVolumeMounts(ctx context.Context, loader domain.Loader, request domain.LoaderAgentRequest, agentDefinition *domain.AgentDefinition) ([]domain.SandboxVolumeMount, []string, error) {
	specs, err := mergeLoaderVolumeMountSpecs(agentDefinitionVolumes(agentDefinition), loader.Volumes, request.Volumes)
	if err != nil {
		return nil, nil, err
	}
	if len(specs) == 0 {
		return nil, nil, nil
	}
	if r.Volumes == nil {
		return nil, nil, fmt.Errorf("volume resolver is required")
	}
	projectVolumes, err := r.loaderProjectVolumes(ctx, loader)
	if err != nil {
		return nil, nil, err
	}
	projectRoot, err := r.loaderProjectRoot(ctx, loader)
	if err != nil {
		return nil, nil, err
	}
	return r.Volumes.ResolveMounts(ctx, specs, volumes.ResolveOptions{
		ProjectRoot:    projectRoot,
		ProjectVolumes: projectVolumes,
	})
}

func (r *LoaderSandboxRunner) loaderProjectVolumes(ctx context.Context, loader domain.Loader) (map[string]domain.VolumeRecord, error) {
	projectID := strings.TrimSpace(loader.Summary.ManagedProjectID)
	if projectID == "" {
		return nil, nil
	}
	if r.ConfigDB == nil {
		return nil, fmt.Errorf("config store is required")
	}
	projectVolumes, err := r.ConfigDB.ListProjectVolumes(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list loader project volumes %s: %w", projectID, err)
	}
	return projectVolumes, nil
}

func (r *LoaderSandboxRunner) loaderProjectRoot(ctx context.Context, loader domain.Loader) (string, error) {
	projectID := strings.TrimSpace(loader.Summary.ManagedProjectID)
	if projectID == "" {
		return "", nil
	}
	if r.ConfigDB == nil {
		return "", fmt.Errorf("config store is required")
	}
	project, err := r.ConfigDB.GetProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("get loader project %s: %w", projectID, err)
	}
	return loaderProjectRoot(project), nil
}

func loaderProjectRoot(project domain.ProjectRecord) string {
	sourcePath := strings.TrimSpace(project.SourcePath)
	if sourcePath == "" {
		return ""
	}
	info, err := os.Stat(sourcePath)
	if err == nil && info.IsDir() {
		return sourcePath
	}
	return filepath.Dir(sourcePath)
}

func mergeLoaderVolumeMountSpecs(groups ...[]domain.VolumeMountSpec) ([]domain.VolumeMountSpec, error) {
	var merged []domain.VolumeMountSpec
	byTarget := make(map[string]int)
	for _, group := range groups {
		normalized, err := domain.NormalizeVolumeMountSpecs(group)
		if err != nil {
			return nil, err
		}
		for _, spec := range normalized {
			target := filepath.Clean(spec.Target)
			if index, ok := byTarget[target]; ok {
				merged[index] = spec
				continue
			}
			byTarget[target] = len(merged)
			merged = append(merged, spec)
		}
	}
	return merged, nil
}

func agentDefinitionVolumes(agentDefinition *domain.AgentDefinition) []domain.VolumeMountSpec {
	if agentDefinition == nil {
		return nil
	}
	return agentDefinition.Volumes
}

func (r *LoaderSandboxRunner) recordVolumeWarnings(ctx context.Context, sessionID string, warnings []string) {
	if r == nil || r.Store == nil || len(warnings) == 0 {
		return
	}
	for _, warning := range warnings {
		event := domain.SandboxEvent{ID: uuid.NewString(), Type: "sandbox.volume.warning", Level: "warn", Message: warning, CreatedAt: time.Now().UTC()}
		_ = r.Store.AddEvent(ctx, sessionID, event)
		if r.Streams != nil {
			r.Streams.PublishEventAdded(sessionID, event)
		}
	}
}

func (r *LoaderSandboxRunner) publish(topic string, payload map[string]any) {
	if r.Publisher != nil {
		_ = r.Publisher.Publish(domain.LoaderTopicEvent{
			Topic:     strings.TrimSpace(topic),
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		})
	}
}
