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

	"github.com/chaitin/agent-compose/pkg/capabilities"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/volumes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

type SchedulerVolumeResolver interface {
	ResolveMounts(ctx context.Context, specs []domain.VolumeMountSpec, options volumes.ResolveOptions) ([]domain.SandboxVolumeMount, []string, error)
}

type SchedulerSandboxRunner struct {
	Config               *appconfig.Config
	Store                *sandboxstore.Store
	ConfigDB             *configstore.ConfigStore
	workspaceEnsurer     workspaces.WorkspaceEnsurer
	Driver               sandboxes.SandboxDriver
	Cap                  capabilities.Provider
	Volumes              SchedulerVolumeResolver
	Streams              *sandboxes.StreamBroker
	Publisher            schedulers.ControllerPublisher
	CapTokens            *CapabilitySandboxResolver
	AgentExecutor        *AgentExecutor
	LifecycleLocks       *sandboxes.LifecycleLocks
	inlineWorkspaceLocks *sandboxes.LifecycleLocks
}

// SchedulerSandboxRunnerDeps bundles NewSchedulerSandboxRunner's required
// dependencies.
type SchedulerSandboxRunnerDeps struct {
	Config           *appconfig.Config
	Store            *sandboxstore.Store
	ConfigDB         *configstore.ConfigStore
	WorkspaceEnsurer workspaces.WorkspaceEnsurer
	Driver           sandboxes.SandboxDriver
	Cap              capabilities.Provider
	VolumeResolver   SchedulerVolumeResolver
	Streams          *sandboxes.StreamBroker
	Publisher        schedulers.ControllerPublisher
	CapTokens        *CapabilitySandboxResolver
	AgentExecutor    *AgentExecutor
}

func NewSchedulerSandboxRunner(deps SchedulerSandboxRunnerDeps, locks ...*sandboxes.LifecycleLocks) *SchedulerSandboxRunner {
	runner := &SchedulerSandboxRunner{Config: deps.Config, Store: deps.Store, ConfigDB: deps.ConfigDB, workspaceEnsurer: deps.WorkspaceEnsurer, Driver: deps.Driver, Cap: deps.Cap, Volumes: deps.VolumeResolver, Streams: deps.Streams, Publisher: deps.Publisher, CapTokens: deps.CapTokens, AgentExecutor: deps.AgentExecutor, inlineWorkspaceLocks: sandboxes.NewLifecycleLocks()}
	if len(locks) > 0 {
		runner.LifecycleLocks = locks[0]
	}
	return runner
}

func (r *SchedulerSandboxRunner) Shutdown(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	stopCtx := context.WithoutCancel(ctx)
	session, err := r.Store.GetSandbox(stopCtx, sessionID)
	if err != nil {
		return err
	}
	outcome, stopErr := r.stopLifecycle().StopLoaded(stopCtx, session)
	if stopErr != nil {
		if outcome.DriverStopped && outcome.Sandbox != nil {
			r.publish("agent-compose.session.stopped", schedulers.SessionTopicPayload(outcome.Sandbox, "scheduler"))
		}
		return stopErr
	}
	if !outcome.Changed() || outcome.Sandbox == nil {
		return nil
	}
	r.publish("agent-compose.session.stopped", schedulers.SessionTopicPayload(outcome.Sandbox, "scheduler"))
	return nil
}

func (r *SchedulerSandboxRunner) stopLifecycle() sandboxes.Lifecycle {
	return sandboxes.Lifecycle{
		Config:        r.Config,
		Store:         r.Store,
		Driver:        r.Driver,
		AccessRevoker: r.CapTokens,
		Notifier: sandboxLifecycleNotifier{
			streams: r.Streams,
		},
		Locks: r.LifecycleLocks,
	}
}

// resolvedSchedulerSandboxConfig is the sandbox configuration
// resolveSchedulerSandboxConfig derives from a scheduler + agent request,
// ready to reuse an existing sticky binding or create a new sandbox.
type resolvedSchedulerSandboxConfig struct {
	AgentDefinition   *domain.AgentDefinition
	EffectivePolicy   string
	ForceNew          bool
	ProviderEnvItems  []domain.SandboxEnvVar
	EnvItems          []domain.SandboxEnvVar
	AgentConfig       execution.AgentConfig
	WorkspaceSnapshot *domain.SandboxWorkspace
	WorkspaceID       string
	Driver            string
	GuestImage        string
	VolumeMounts      []domain.SandboxVolumeMount
	VolumeWarnings    []string
	ConfigHash        string
}

func (r *SchedulerSandboxRunner) resolveSchedulerSandboxConfig(ctx context.Context, scheduler domain.Scheduler, request domain.SchedulerAgentRequest, titleOverridesSession bool) (resolvedSchedulerSandboxConfig, error) {
	agentDefinition, err := r.ResolveSchedulerAgentDefinition(ctx, scheduler)
	if err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	effectivePolicy := schedulers.NormalizeSandboxPolicy(scheduler.Summary.SandboxPolicy)
	if strings.TrimSpace(schedulers.AgentSandboxPolicy(request)) != "" {
		effectivePolicy = schedulers.NormalizeSandboxPolicy(schedulers.AgentSandboxPolicy(request))
	}
	hasOverrides := schedulers.AgentRequestOverridesSession(request, titleOverridesSession)
	forceNew := effectivePolicy == domain.SchedulerSandboxPolicyNew || hasOverrides
	globalEnvItems, err := r.ConfigDB.ListGlobalEnv(ctx)
	if err != nil {
		return resolvedSchedulerSandboxConfig{}, err
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
		agentConfig.Provider = domain.NormalizeAgentKind(scheduler.Summary.DefaultAgent)
	}
	if agentConfig.Provider == "" {
		agentConfig.Provider = domain.DefaultAgentProvider
	}
	providerEnvItems = domain.MergeEnvItems(providerEnvItems, scheduler.EnvItems)
	providerEnvItems = domain.MergeEnvItems(providerEnvItems, schedulers.AgentSandboxEnv(request))
	envItems := domain.MergeEnvItems(globalEnvItems, providerEnvItems)
	envItems = llms.FilterPersistedRuntimeEnv(envItems)
	driver, err := r.driver(request, scheduler, agentDefinition)
	if err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	workspaceID := r.workspaceID(scheduler, request, agentDefinition)
	workspaceSnapshot, workspaceID, err := r.resolveWorkspaceSnapshot(ctx, request, agentDefinition, workspaceID, driver)
	if err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	if err := workspaces.ValidateWorkspaceRuntimeDriver(workspaceSnapshot, driver); err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	if err := validateSchedulerRuntimeDriverCompiled(driver); err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	guestImage := r.guestImage(request, scheduler, agentDefinition, driver)
	volumeMounts, volumeWarnings, err := r.resolveVolumeMounts(ctx, scheduler, request, agentDefinition, driver)
	if err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	baseConfigHash, err := schedulerSandboxConfigHash(scheduler)
	if err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	configHash, err := schedulerRequestSandboxConfigHash(schedulerRequestSandboxConfigHashRequest{
		BaseHash:         baseConfigHash,
		Request:          request,
		AgentDefinition:  agentDefinition,
		ProviderEnvItems: providerEnvItems,
		EnvItems:         envItems,
		Workspace:        workspaceSnapshot,
		Driver:           driver,
		GuestImage:       guestImage,
		VolumeMounts:     volumeMounts,
	})
	if err != nil {
		return resolvedSchedulerSandboxConfig{}, err
	}
	return resolvedSchedulerSandboxConfig{
		AgentDefinition:   agentDefinition,
		EffectivePolicy:   effectivePolicy,
		ForceNew:          forceNew,
		ProviderEnvItems:  providerEnvItems,
		EnvItems:          envItems,
		AgentConfig:       agentConfig,
		WorkspaceSnapshot: workspaceSnapshot,
		WorkspaceID:       workspaceID,
		Driver:            driver,
		GuestImage:        guestImage,
		VolumeMounts:      volumeMounts,
		VolumeWarnings:    volumeWarnings,
		ConfigHash:        configHash,
	}, nil
}

// createSchedulerSandbox creates the sandbox for a resolved scheduler sandbox
// config: builds tags/title, creates the sandbox, persists provider env items
// and pull policy, and records any volume-resolution warnings.
func (r *SchedulerSandboxRunner) createSchedulerSandbox(ctx context.Context, scheduler domain.Scheduler, request domain.SchedulerAgentRequest, cfg resolvedSchedulerSandboxConfig) (*domain.Sandbox, error) {
	capabilityVars, capabilityTags := capabilities.BuildGatewaySandboxVars(capabilities.ProxyTarget(r.Cap), scheduler.Summary.CapsetIDs)
	envItems := domain.MergeEnvItems(cfg.EnvItems, capabilityVars)
	tags := []domain.SandboxTag{
		{Name: "origin", Value: "scheduler"},
		{Name: "scheduler_id", Value: scheduler.Summary.ID},
		{Name: "scheduler_name", Value: scheduler.Summary.Name},
		{Name: domain.AgentSandboxTagProvider, Value: cfg.AgentConfig.Provider},
	}
	if strings.TrimSpace(scheduler.Summary.ProjectID) != "" {
		projectID := strings.TrimSpace(scheduler.Summary.ProjectID)
		tags = append(tags,
			domain.SandboxTag{Name: "project", Value: projectID},
			domain.SandboxTag{Name: "project_id", Value: projectID},
			domain.SandboxTag{Name: "agent", Value: scheduler.Summary.AgentName},
		)
	}
	tags = append(tags, capabilityTags...)
	title := firstNonEmpty(strings.TrimSpace(request.Title), strings.TrimSpace(scheduler.Summary.Name), schedulers.DefaultName(time.Now().UTC()))
	agentDefinition := cfg.AgentDefinition
	if agentDefinition != nil {
		tags = append(tags,
			domain.SandboxTag{Name: domain.AgentSandboxTagSource, Value: domain.AgentSandboxTagSourceVal},
			domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: agentDefinition.ID},
			domain.SandboxTag{Name: domain.AgentSandboxTagName, Value: agentDefinition.Name},
		)
	}
	session, err := r.Store.CreateSandboxWithOptions(ctx, title, "", cfg.Driver, cfg.GuestImage, cfg.WorkspaceID, domain.SandboxTypeScript+":"+scheduler.Summary.ID, cfg.WorkspaceSnapshot, envItems, tags, sandboxstore.CreateSandboxOptions{
		JupyterEnabled:       request.JupyterEnabled,
		VolumeMounts:         cfg.VolumeMounts,
		StoppedRuntimePolicy: stoppedRuntimePolicyFromAgentDefinition(agentDefinition),
	})
	if err != nil {
		return nil, err
	}
	llms.SetSandboxProviderEnvItems(session, cfg.ProviderEnvItems)
	if request.PullPolicy != "" {
		session.Summary.PullPolicy = request.PullPolicy
		if err := r.Store.UpdateSandbox(ctx, session); err != nil {
			return nil, fmt.Errorf("persist sandbox pull policy: %w", err)
		}
	}
	r.recordVolumeWarnings(ctx, session.Summary.ID, cfg.VolumeWarnings)
	return session, nil
}

func (r *SchedulerSandboxRunner) Ensure(ctx context.Context, scheduler domain.Scheduler, request domain.SchedulerAgentRequest, titleOverridesSession bool) (*domain.Sandbox, string, error) {
	cfg, err := r.resolveSchedulerSandboxConfig(ctx, scheduler, request, titleOverridesSession)
	if err != nil {
		return nil, "", err
	}
	var previousBinding *domain.SchedulerBinding
	if !cfg.ForceNew {
		if session, eventType, reused, binding, err := r.reuseCompatibleSchedulerBinding(ctx, scheduler, request.BindingTriggerID, cfg.ConfigHash); err != nil {
			return nil, "", err
		} else if reused {
			return session, eventType, nil
		} else {
			previousBinding = binding
		}
	}

	session, err := r.createSchedulerSandbox(ctx, scheduler, request, cfg)
	if err != nil {
		return nil, "", err
	}
	workspaceSnapshot, agentDefinition, agentConfig := cfg.WorkspaceSnapshot, cfg.AgentDefinition, cfg.AgentConfig
	effectivePolicy, forceNew, configHash := cfg.EffectivePolicy, cfg.ForceNew, cfg.ConfigHash
	ensureErr := func() error {
		unlock := r.fileWorkspaceReadLock(workspaceSnapshot)
		defer unlock()
		return r.workspaceEnsurer.Ensure(ctx, session)
	}()
	if ensureErr != nil {
		session.Summary.VMStatus = domain.VMStatusFailed
		_ = r.Store.UpdateSandbox(ctx, session)
		return nil, "", ensureErr
	}
	runs.WriteCapabilityGuide(ctx, runs.CapabilityGuideDeps{
		Provider:       r.Cap,
		Store:          r.Store,
		Streams:        r.Streams,
		Config:         r.Config,
		WriteGuestFile: r.AgentExecutor.GuestFileWriterFor(session),
	}, session, scheduler.Summary.CapsetIDs)
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
	if effectivePolicy == domain.SchedulerSandboxPolicySticky && !forceNew {
		claimed, err := r.bindSchedulerSandbox(ctx, domain.SchedulerBinding{
			SchedulerID:       scheduler.Summary.ID,
			TriggerID:         request.BindingTriggerID,
			SandboxID:         session.Summary.ID,
			SandboxConfigHash: configHash,
		}, previousBinding)
		if err != nil {
			_ = r.Shutdown(ctx, session.Summary.ID)
			return nil, "", fmt.Errorf("persist scheduler sticky sandbox binding: %w", err)
		}
		if !claimed {
			if err := r.Shutdown(ctx, session.Summary.ID); err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, "", fmt.Errorf("retire unclaimed scheduler sticky sandbox: %w", err)
			}
			winner, eventType, reused, err := r.reuseWinningSchedulerBinding(ctx, scheduler.Summary.ID, request.BindingTriggerID, configHash)
			if err != nil {
				return nil, "", fmt.Errorf("reuse concurrently claimed scheduler sticky sandbox: %w", err)
			}
			if reused {
				return winner, eventType, nil
			}
			return nil, "", fmt.Errorf("scheduler sticky sandbox binding changed concurrently")
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
		"source":        "scheduler",
		"schedulerId":   scheduler.Summary.ID,
	})
	return loaded, "scheduler.sandbox.created", nil
}

func (r *SchedulerSandboxRunner) Load(ctx context.Context, sessionID string) (*domain.Sandbox, error) {
	return r.Store.GetSandbox(ctx, sessionID)
}

func (r *SchedulerSandboxRunner) LoadOrResume(ctx context.Context, sessionID string) (*domain.Sandbox, string, error) {
	unlock := r.LifecycleLocks.Lock(sessionID)
	defer unlock()
	return r.loadOrResumeLocked(ctx, sessionID)
}

func (r *SchedulerSandboxRunner) loadOrResumeLocked(ctx context.Context, sessionID string) (*domain.Sandbox, string, error) {
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
	if validator, ok := r.Driver.(sandboxes.SandboxRuntimeValidator); ok {
		if err := validator.ValidateSandboxRuntime(session); err != nil {
			return nil, "", err
		}
	}
	ensureErr := func() error {
		unlock := r.fileWorkspaceReadLock(session.Workspace)
		defer unlock()
		return r.workspaceEnsurer.Ensure(ctx, session)
	}()
	if ensureErr != nil {
		return nil, "", ensureErr
	}
	vmState, err := r.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, "", err
	}
	runs.WriteCapabilityGuide(ctx, runs.CapabilityGuideDeps{
		Provider:       r.Cap,
		Store:          r.Store,
		Streams:        r.Streams,
		Config:         r.Config,
		WriteGuestFile: r.AgentExecutor.GuestFileWriterFor(session),
	}, session, capabilities.SandboxCapsets(session))
	if vmState.StartedAt.IsZero() || sandboxes.RuntimeReleaseIntentional(session) {
		if err := r.AgentExecutor.PrepareSandboxAgentEnvironmentFromTags(ctx, session); err != nil {
			return nil, "", err
		}
	}
	if err := r.Driver.StartSandboxVM(ctx, session); err != nil {
		return nil, "", err
	}
	session.StoppedRuntime = nil
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
		"source":    "scheduler",
	})
	return loaded, "scheduler.sandbox.resumed", nil
}

func (r *SchedulerSandboxRunner) indexCapabilitySandbox(session *domain.Sandbox) {
	if r != nil && r.CapTokens != nil {
		r.CapTokens.IndexSandbox(session, nil)
	}
}

func (r *SchedulerSandboxRunner) ResolveSchedulerAgentDefinition(ctx context.Context, scheduler domain.Scheduler) (*domain.AgentDefinition, error) {
	agentID := strings.TrimSpace(scheduler.Summary.AgentID)
	if agentID == "" {
		return nil, nil
	}
	agent, err := r.ConfigDB.GetAgentDefinition(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("scheduler agent definition %s: %w", agentID, err)
	}
	if !agent.Enabled {
		return nil, fmt.Errorf("scheduler agent definition %s is disabled", agentID)
	}
	return &agent, nil
}

func (r *SchedulerSandboxRunner) workspaceID(scheduler domain.Scheduler, request domain.SchedulerAgentRequest, agentDefinition *domain.AgentDefinition) string {
	workspaceID := firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(scheduler.Summary.WorkspaceID))
	if agentDefinition != nil {
		workspaceID = firstNonEmpty(strings.TrimSpace(request.WorkspaceID), strings.TrimSpace(scheduler.Summary.WorkspaceID), strings.TrimSpace(agentDefinition.WorkspaceID))
	}
	return workspaceID
}

// resolveWorkspaceSnapshot resolves the sandbox workspace snapshot for a
// scheduler run. request.WorkspaceID is an explicit session override (e.g.
// scheduler.agent(prompt, { workspaceId }) or scheduler.shell/exec) and is
// always treated as a Settings-managed workspace_config preset id. Absent
// that override, an agent's yaml `workspace:` declaration is resolved
// inline instead of being looked up as a preset (see issue #599: the yaml
// `name` label was never a real preset id, so that lookup always failed).
func (r *SchedulerSandboxRunner) resolveWorkspaceSnapshot(ctx context.Context, request domain.SchedulerAgentRequest, agentDefinition *domain.AgentDefinition, workspaceID, driver string) (*domain.SandboxWorkspace, string, error) {
	if strings.TrimSpace(request.WorkspaceID) == "" {
		if spec := agentDefinitionInlineWorkspace(agentDefinition); spec != nil {
			snapshot, resolvedID, err := r.inlineWorkspaceSnapshot(ctx, agentDefinition, spec, driver)
			if err != nil {
				return nil, "", err
			}
			return snapshot, resolvedID, nil
		}
	}
	if workspaceID == "" {
		return nil, "", nil
	}
	snapshot, err := r.workspaceSnapshot(ctx, workspaceID)
	if err != nil {
		return nil, "", err
	}
	return snapshot, workspaceID, nil
}

// fileWorkspaceReadLock holds the same per-workspace-id lock
// materializeInlineFileWorkspace uses while resetting and recopying its
// shared content directory (workspaces/<id>/content under the data root).
// workspaceEnsurer.Ensure reads that same shared directory (see
// pkg/workspaces file workspace Prepare) to populate the sandbox's own
// workspace path, at both sandbox creation (Ensure) and resume
// (loadOrResumeLocked) time. Without holding this lock across that read, a
// concurrent Ensure call for the same workspace id could RemoveAll/recopy
// the shared directory while this read is in flight, surfacing as ENOENT or
// partial content copied into the sandbox. Settings-managed file presets
// share this lock key too; their content is static outside of Settings
// edits, so the extra serialization there is harmless.
func (r *SchedulerSandboxRunner) fileWorkspaceReadLock(workspace *domain.SandboxWorkspace) func() {
	if workspace == nil || workspace.Type != "file" || strings.TrimSpace(workspace.ID) == "" {
		return func() {}
	}
	return r.inlineWorkspaceLocks.Lock(workspace.ID)
}

func (r *SchedulerSandboxRunner) workspaceSnapshot(ctx context.Context, workspaceID string) (*domain.SandboxWorkspace, error) {
	if workspaceID == "" {
		return nil, nil
	}
	workspaceConfig, err := r.ConfigDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return nil, err
	}
	return toSandboxWorkspaceSnapshot(workspaceConfig), nil
}

func (r *SchedulerSandboxRunner) driver(request domain.SchedulerAgentRequest, scheduler domain.Scheduler, agentDefinition *domain.AgentDefinition) (string, error) {
	driverValue := firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(scheduler.Summary.Driver))
	if agentDefinition != nil {
		driverValue = firstNonEmpty(strings.TrimSpace(request.Driver), strings.TrimSpace(scheduler.Summary.Driver), strings.TrimSpace(agentDefinition.Driver))
	}
	return driverpkg.ResolveSandboxRuntimeDriver(driverValue, r.Config.RuntimeDriver)
}

func validateSchedulerRuntimeDriverCompiled(driver string) error {
	err := driverpkg.ValidateCompiledRuntimeDriver(driver)
	if errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		return domain.ClassifyError(domain.ErrUnsupported, "", err)
	}
	return err
}

func (r *SchedulerSandboxRunner) guestImage(request domain.SchedulerAgentRequest, scheduler domain.Scheduler, agentDefinition *domain.AgentDefinition, driver string) string {
	agentGuestImage := ""
	if agentDefinition != nil {
		agentGuestImage = agentDefinition.GuestImage
	}
	return driverpkg.ResolveSandboxGuestImage(request.GuestImage, scheduler.Summary.GuestImage, agentGuestImage, driverpkg.DefaultGuestImageForDriver(r.Config, driver))
}

func (r *SchedulerSandboxRunner) resolveVolumeMounts(ctx context.Context, scheduler domain.Scheduler, request domain.SchedulerAgentRequest, agentDefinition *domain.AgentDefinition, driver string) ([]domain.SandboxVolumeMount, []string, error) {
	specs, err := mergeSchedulerVolumeMountSpecs(agentDefinitionVolumes(agentDefinition), scheduler.Volumes, request.Volumes)
	if err != nil {
		return nil, nil, err
	}
	if len(specs) == 0 {
		return nil, nil, nil
	}
	if err := volumes.ValidateDriverMountSpecs(driver, specs); err != nil {
		return nil, nil, err
	}
	if r.Volumes == nil {
		return nil, nil, fmt.Errorf("volume resolver is required")
	}
	projectVolumes, err := r.schedulerProjectVolumes(ctx, scheduler)
	if err != nil {
		return nil, nil, err
	}
	projectRoot, err := r.schedulerProjectRoot(ctx, scheduler)
	if err != nil {
		return nil, nil, err
	}
	mounts, warnings, err := r.Volumes.ResolveMounts(ctx, specs, volumes.ResolveOptions{
		ProjectRoot:    projectRoot,
		ProjectVolumes: projectVolumes,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := volumes.ValidateResolvedDriverMounts(driver, mounts); err != nil {
		return nil, nil, err
	}
	return mounts, warnings, nil
}

func (r *SchedulerSandboxRunner) schedulerProjectVolumes(ctx context.Context, scheduler domain.Scheduler) (map[string]domain.VolumeRecord, error) {
	projectID := strings.TrimSpace(scheduler.Summary.ProjectID)
	if projectID == "" {
		return nil, nil
	}
	if r.ConfigDB == nil {
		return nil, fmt.Errorf("config store is required")
	}
	projectVolumes, err := r.ConfigDB.ListProjectVolumes(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("list scheduler project volumes %s: %w", projectID, err)
	}
	return projectVolumes, nil
}

func (r *SchedulerSandboxRunner) schedulerProjectRoot(ctx context.Context, scheduler domain.Scheduler) (string, error) {
	projectID := strings.TrimSpace(scheduler.Summary.ProjectID)
	if projectID == "" {
		return "", nil
	}
	if r.ConfigDB == nil {
		return "", fmt.Errorf("config store is required")
	}
	project, err := r.ConfigDB.GetProject(ctx, projectID)
	if err != nil {
		return "", fmt.Errorf("get scheduler project %s: %w", projectID, err)
	}
	return schedulerProjectRoot(project), nil
}

func schedulerProjectRoot(project domain.ProjectRecord) string {
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

func mergeSchedulerVolumeMountSpecs(groups ...[]domain.VolumeMountSpec) ([]domain.VolumeMountSpec, error) {
	var merged []domain.VolumeMountSpec
	byTarget := make(map[string]int)
	for _, group := range groups {
		normalized, err := volumes.NormalizeMountSpecs(group)
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

func (r *SchedulerSandboxRunner) recordVolumeWarnings(ctx context.Context, sessionID string, warnings []string) {
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

func (r *SchedulerSandboxRunner) publish(topic string, payload map[string]any) {
	if r.Publisher != nil {
		_ = r.Publisher.Publish(domain.SchedulerTopicEvent{
			Topic:     strings.TrimSpace(topic),
			Payload:   payload,
			CreatedAt: time.Now().UTC(),
		})
	}
}
