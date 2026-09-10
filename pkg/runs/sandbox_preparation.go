package runs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/capabilities"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/images"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/volumes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func (c *Controller) prepareProjectRun(ctx context.Context, run domain.ProjectRunRecord, requestEnv []*agentcomposev2.EnvVarSpec) (Preparation, error) {
	return PrepareProjectRun(ctx, PreparationDeps{Store: c.configDB, Resolver: projectRunWorkspaceResolver{controller: c}}, run, requestEnv)
}

func resolveRunJupyterOptions(base sandboxstore.CreateSandboxOptions, override *agentcomposev2.RunJupyterSpec) (sandboxstore.CreateSandboxOptions, error) {
	result := base
	if override == nil {
		return result, nil
	}
	if override.GetGuestPort() > 65535 {
		return sandboxstore.CreateSandboxOptions{}, fmt.Errorf("%w: jupyter guest_port must be 0 or a valid TCP port between 1 and 65535", ErrInvalidRequest)
	}
	if override.GetEnabled() || override.GetExpose() {
		result.JupyterEnabled = true
	}
	if override.GetGuestPort() != 0 {
		result.JupyterGuestPort = int(override.GetGuestPort())
	}
	if override.GetExpose() {
		result.JupyterExpose = true
	}
	return result, nil
}

//nolint:funlen // a manual mutex lock/unlock spans the reuse-attempt and fallthrough-to-create paths, driverValidated/volumesResolved flags are set in one phase and read in another, and a recursive self-call handles concurrent sticky-binding claims; splitting would move this coupling across a function boundary rather than remove it.
func (c *Controller) ensureProjectRunSandbox(ctx context.Context, run domain.ProjectRunRecord, prepared Preparation, req RunAgentRequest) (SandboxResult, error) {
	defer func() {
		if c == nil || c.config == nil {
			return
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := workspaces.ReleaseUnusedTransientSnapshot(cleanupCtx, c.config, c.store, prepared.Workspace); err != nil {
			slog.Warn("failed to release prepared workspace snapshot", "error", err)
		}
	}()
	if c == nil || c.config == nil || c.store == nil || c.driver == nil {
		return SandboxResult{}, fmt.Errorf("sandbox runtime dependencies are required")
	}
	jupyterOptions, err := resolveRunJupyterOptions(prepared.SandboxOptions, req.Jupyter)
	if err != nil {
		return SandboxResult{}, err
	}
	stickySchedulerID := strings.TrimSpace(req.StickyBindingSchedulerID)
	stickyTriggerID := strings.TrimSpace(req.StickyBindingTriggerID)
	driver, err := driverpkg.ResolveSandboxRuntimeDriver(run.Driver, c.config.RuntimeDriver)
	if err != nil {
		return SandboxResult{}, err
	}
	driverValidated := false
	if stickySchedulerID == "" || strings.TrimSpace(req.StickyBindingConfigHash) != "" {
		if err := c.validateSandboxRuntimeDriver(driver); err != nil {
			return SandboxResult{}, err
		}
		driverValidated = true
	}
	guestImage := driverpkg.ResolveSandboxGuestImage(run.ImageRef, driverpkg.DefaultGuestImageForDriver(c.config, driver))
	var volumeMounts []domain.SandboxVolumeMount
	var volumeWarnings []string
	volumesResolved := false
	if strings.TrimSpace(req.SandboxID) == "" && strings.TrimSpace(req.StickyBindingConfigHash) != "" {
		volumeMounts, volumeWarnings, err = c.resolveProjectRunVolumeMounts(ctx, driver, prepared, req)
		if err != nil {
			return SandboxResult{}, err
		}
		jupyterOptions.VolumeMounts = volumeMounts
		volumesResolved = true
	}
	stickyConfigHash, err := stickyProjectRunConfigHash(req.StickyBindingConfigHash, run, prepared, stickySandboxSpec{Driver: driver, GuestImage: guestImage, VolumeMounts: volumeMounts, Jupyter: jupyterOptions})
	if err != nil {
		return SandboxResult{}, fmt.Errorf("hash sticky project sandbox configuration: %w", err)
	}
	tags := SandboxTags(run)
	agentConfig := execution.AgentConfig{Provider: domain.DefaultAgentProvider}
	if prepared.AgentDefinition != nil {
		agentConfig = execution.AgentConfigFromDefinition(*prepared.AgentDefinition, domain.DefaultAgentProvider)
		tags = append(tags,
			domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: prepared.AgentDefinition.ID},
			domain.SandboxTag{Name: domain.AgentSandboxTagName, Value: prepared.AgentDefinition.Name},
		)
	}
	tags = append(tags, domain.SandboxTag{Name: domain.AgentSandboxTagProvider, Value: agentConfig.Provider})
	capabilityVars, capabilityTags := capabilities.BuildGatewaySandboxVars(capabilities.ProxyTarget(c.cap), prepared.CapsetIDs)
	tags = append(tags, capabilityTags...)
	trustedHeaders := domain.TrustedHeadersFromContext(ctx)
	bindingStore, hasBindingStore := c.configDB.(stickyBindingStore)
	var previousStickyBinding *domain.SchedulerBinding
	boundSandbox := false
	warnings := []string(nil)
	if stickySchedulerID != "" && strings.TrimSpace(req.SandboxID) == "" {
		if !hasBindingStore {
			return SandboxResult{}, fmt.Errorf("sticky sandbox binding store is required")
		}
		sandboxID, binding, bindingWarnings, err := c.resolveStickySchedulerBinding(ctx, bindingStore, stickyBindingKey{SchedulerID: stickySchedulerID, TriggerID: stickyTriggerID, ConfigHash: stickyConfigHash})
		if err != nil {
			return SandboxResult{}, err
		}
		warnings = append(warnings, bindingWarnings...)
		previousStickyBinding = binding
		if sandboxID != "" {
			req.SandboxID = sandboxID
			boundSandbox = true
		}
	}
	if sandboxID := strings.TrimSpace(req.SandboxID); sandboxID != "" {
		unlock := c.lifecycleLocks.Lock(sandboxID)
		locked := true
		defer func() {
			if locked {
				unlock()
			}
		}()
		if len(req.Volumes) > 0 {
			return SandboxResult{}, fmt.Errorf("%w: run volumes cannot be combined with an existing sandbox", ErrInvalidRequest)
		}
		if boundSandbox && previousStickyBinding != nil {
			current, found, err := bindingStore.GetSchedulerBinding(ctx, stickySchedulerID, stickyTriggerID)
			if err != nil {
				return SandboxResult{}, fmt.Errorf("revalidate sticky sandbox binding: %w", err)
			}
			if !found || !schedulers.SchedulerBindingsMatch(current, *previousStickyBinding) {
				return SandboxResult{}, fmt.Errorf("sticky sandbox binding changed concurrently")
			}
		}
		sandbox, err := c.store.GetSandbox(ctx, sandboxID)
		if err != nil {
			if !boundSandbox {
				return SandboxResult{}, fmt.Errorf("load sandbox %s: %w", sandboxID, err)
			}
			if !driverValidated {
				if validateErr := c.validateSandboxRuntimeDriver(driver); validateErr != nil {
					return SandboxResult{}, validateErr
				}
				driverValidated = true
			}
			retiring := schedulers.RetiringSchedulerBinding(*previousStickyBinding, stickyConfigHash)
			claimed, claimErr := bindingStore.CompareAndSwapSchedulerBinding(ctx, previousStickyBinding, retiring)
			if claimErr != nil {
				return SandboxResult{}, fmt.Errorf("claim unavailable sticky sandbox %s retirement: %w", sandboxID, claimErr)
			}
			if !claimed {
				return SandboxResult{}, fmt.Errorf("sticky sandbox binding changed concurrently")
			}
			previousStickyBinding = &retiring
			warnings = append(warnings, fmt.Sprintf("sticky sandbox %s is unavailable; creating a replacement", sandboxID))
			unlock()
			locked = false
		} else {
			if err := validateProjectRunSandboxOwnership(sandbox, run); err != nil {
				return SandboxResult{}, err
			}
			if pendingRunID, pending, err := c.pendingCompletionForSandbox(ctx, sandboxID); err != nil {
				return SandboxResult{}, err
			} else if pending && pendingRunID != run.RunID {
				return SandboxResult{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("sandbox %s has pending completion for run %s", sandboxID, pendingRunID), nil)
			}
			if sandbox.Summary.VMStatus == domain.VMStatusDeleting {
				return SandboxResult{Sandbox: sandbox}, fmt.Errorf("sandbox %s is being deleted", sandboxID)
			}
			driver, err := driverpkg.ResolveSandboxRuntimeDriver(sandbox.Summary.Driver, c.config.RuntimeDriver)
			if err != nil {
				return SandboxResult{}, err
			}
			if err := c.validateSandboxRuntimeDriver(driver); err != nil {
				return SandboxResult{Sandbox: sandbox}, err
			}
			if _, err := NewCoordinator(c.configDB, projects.StableProjectRunID).BindSandbox(ctx, run.RunID, sandboxID, false); err != nil {
				return SandboxResult{Sandbox: sandbox}, fmt.Errorf("bind reused sandbox to project run: %w", err)
			}
			if sandbox.Summary.VMStatus != domain.VMStatusRunning {
				if err := c.applyJupyterOptionsToSandbox(sandbox, jupyterOptions); err != nil {
					return SandboxResult{Sandbox: sandbox}, err
				}
				guestImage := driverpkg.ResolveSandboxGuestImage(sandbox.Summary.GuestImage, driverpkg.DefaultGuestImageForDriver(c.config, driver))
				if err := images.EnsureDriverImage(ctx, c.config, c.images, images.EnsureRequest{
					Driver:      driver,
					ImageRef:    guestImage,
					ProjectName: run.ProjectName,
					AgentName:   run.AgentName,
				}); err != nil {
					return SandboxResult{Sandbox: sandbox}, err
				}
			}
			sandbox.EnvItems = domain.MergeEnvItems(sandbox.EnvItems, capabilityVars)
			sandbox.Summary.Tags = MergeSandboxTags(sandbox.Summary.Tags, tags)
			if err := c.startProjectRunSandbox(ctx, sandbox, sandboxStartEvent{Type: "sandbox.resumed", Message: "sandbox resumed for project run"}, trustedHeaders); err != nil {
				return SandboxResult{Sandbox: sandbox}, err
			}
			return SandboxResult{Sandbox: sandbox, Warnings: warnings}, nil
		}
	}

	workspaceID := ""
	if prepared.Workspace != nil {
		workspaceID = strings.TrimSpace(prepared.Workspace.ID)
	}
	if !driverValidated {
		if err := c.validateSandboxRuntimeDriver(driver); err != nil {
			return SandboxResult{}, err
		}
	}
	if !volumesResolved {
		volumeMounts, volumeWarnings, err = c.resolveProjectRunVolumeMounts(ctx, driver, prepared, req)
		if err != nil {
			return SandboxResult{}, err
		}
		jupyterOptions.VolumeMounts = volumeMounts
	}
	if err := images.EnsureDriverImage(ctx, c.config, c.images, images.EnsureRequest{
		Driver:      driver,
		ImageRef:    guestImage,
		ProjectName: run.ProjectName,
		AgentName:   run.AgentName,
	}); err != nil {
		return SandboxResult{}, err
	}
	if driverK8s := driverK8sOptionsFromAgentDefinition(prepared.AgentDefinition); driverK8s != nil {
		jupyterOptions.DriverK8sContext = driverK8s.Context
		jupyterOptions.DriverK8sNamespace = driverK8s.Namespace
	}
	sandbox, err := c.store.CreateSandboxWithOptions(ctx,
		SandboxTitle(run),
		"",
		driver,
		guestImage,
		workspaceID,
		domain.SandboxTypeManual,
		prepared.Workspace,
		domain.MergeEnvItems(prepared.EnvItems, capabilityVars),
		tags,
		jupyterOptions,
	)
	if err != nil {
		return SandboxResult{}, err
	}
	if _, err := NewCoordinator(c.configDB, projects.StableProjectRunID).BindSandbox(ctx, run.RunID, sandbox.Summary.ID, true); err != nil {
		bindErr := fmt.Errorf("bind created sandbox to project run: %w", err)
		if c.removal == nil {
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, bindErr
		}
		_, removeErr := c.removal.Remove(context.WithoutCancel(ctx), sandbox.Summary.ID, true)
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, errors.Join(bindErr, removeErr)
	}
	llms.SetSandboxProviderEnvItems(sandbox, prepared.ProviderEnvItems)
	if err := c.ensureProjectRunSandboxWorkspace(ctx, sandbox); err != nil {
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, err
	}
	if c.executor == nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("agent executor is required")
	}
	if err := c.executor.PrepareSandboxAgentEnvironment(ctx, sandbox, agentConfig, prepared.AgentDefinition); err != nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, err
	}
	if err := c.startProjectRunSandboxRuntime(ctx, sandbox, sandboxStartEvent{Type: "sandbox.created", Message: "sandbox started for project run"}, trustedHeaders); err != nil {
		return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, err
	}
	if stickySchedulerID != "" {
		if !hasBindingStore {
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("sticky sandbox binding store is required")
		}
		claimed, err := bindingStore.CompareAndSwapSchedulerBinding(ctx, previousStickyBinding, domain.SchedulerBinding{SchedulerID: stickySchedulerID, TriggerID: stickyTriggerID, SandboxID: sandbox.Summary.ID, SandboxConfigHash: stickyConfigHash})
		if err != nil {
			if stopErr := c.stopProjectRunSandbox(ctx, sandbox); stopErr != nil {
				return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, errors.Join(fmt.Errorf("persist sticky sandbox binding: %w", err), fmt.Errorf("retire unbound sticky sandbox: %w", stopErr))
			}
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("persist sticky sandbox binding: %w", err)
		}
		if !claimed {
			if err := c.stopProjectRunSandbox(ctx, sandbox); err != nil {
				return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("retire unclaimed sticky sandbox: %w", err)
			}
			winner, compatible, err := loadCompatibleStickySchedulerBinding(ctx, bindingStore, stickyBindingKey{SchedulerID: stickySchedulerID, TriggerID: stickyTriggerID, ConfigHash: stickyConfigHash})
			if err != nil {
				return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("load concurrently claimed sticky sandbox: %w", err)
			}
			if compatible {
				reuseRequest := req
				reuseRequest.SandboxID = winner.SandboxID
				reuseRequest.Volumes = nil
				reuseRequest.StickyBindingSchedulerID = ""
				reuseRequest.StickyBindingTriggerID = ""
				reuseRequest.StickyBindingConfigHash = ""
				result, reuseErr := c.ensureProjectRunSandbox(ctx, run, prepared, reuseRequest)
				result.Warnings = append(append(warnings, volumeWarnings...), result.Warnings...)
				if reuseErr != nil {
					return result, fmt.Errorf("reuse concurrently claimed sticky sandbox: %w", reuseErr)
				}
				return result, nil
			}
			return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, fmt.Errorf("sticky sandbox binding changed concurrently")
		}
	}
	volumeWarnings = append(warnings, volumeWarnings...)
	return SandboxResult{Sandbox: sandbox, Created: true, Warnings: volumeWarnings}, nil
}

func (c *Controller) pendingCompletionForSandbox(ctx context.Context, sandboxID string) (string, bool, error) {
	store, ok := c.configDB.(CompletionStore)
	if !ok {
		return "", false, nil
	}
	return store.ProjectRunCompletionForSandbox(ctx, sandboxID)
}

func (c *Controller) validateSandboxRuntimeDriver(driver string) error {
	err := driverpkg.ValidateCompiledRuntimeDriver(driver)
	if errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) {
		return domain.ClassifyError(domain.ErrUnsupported, "", err)
	}
	return err
}

func (c *Controller) resolveProjectRunVolumeMounts(ctx context.Context, driver string, prepared Preparation, req RunAgentRequest) ([]domain.SandboxVolumeMount, []string, error) {
	specs := prepared.Volumes
	if len(req.Volumes) > 0 {
		specs = req.Volumes
	}
	if len(specs) == 0 {
		return nil, nil, nil
	}
	if err := volumes.ValidateDriverMountSpecs(driver, specs); err != nil {
		return nil, nil, err
	}
	if c.volumes == nil {
		return nil, nil, fmt.Errorf("volume resolver is required")
	}
	mounts, warnings, err := c.volumes.ResolveMounts(ctx, specs, volumes.ResolveOptions{
		ProjectRoot:    prepared.ProjectRoot,
		ProjectVolumes: prepared.ProjectVolumes,
	})
	if err != nil {
		return nil, nil, err
	}
	if err := volumes.ValidateResolvedDriverMounts(driver, mounts); err != nil {
		return nil, nil, err
	}
	return mounts, warnings, nil
}

func (c *Controller) applyJupyterOptionsToSandbox(sandbox *domain.Sandbox, options sandboxstore.CreateSandboxOptions) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	proxyState, err := c.store.GetProxyState(sandbox.Summary.ID)
	if err != nil {
		return err
	}
	if !options.JupyterEnabled && !options.JupyterExpose && options.JupyterGuestPort == 0 {
		return nil
	}
	proxyState.Enabled = proxyState.Enabled || options.JupyterEnabled || options.JupyterExpose
	proxyState.Exposed = proxyState.Exposed || options.JupyterExpose
	if options.JupyterGuestPort != 0 {
		proxyState.GuestPort = options.JupyterGuestPort
	}
	if proxyState.Enabled {
		if proxyState.GuestPort == 0 {
			proxyState.GuestPort = c.config.JupyterGuestPort
		}
		driver, err := driverpkg.ResolveSandboxRuntimeDriver(sandbox.Summary.Driver, c.config.RuntimeDriver)
		if err != nil {
			return err
		}
		if driver != driverpkg.RuntimeDriverDocker && proxyState.HostPort == 0 {
			hostPort, err := c.store.AllocateHostPortForJupyter()
			if err != nil {
				return err
			}
			proxyState.HostPort = hostPort
		}
		if strings.TrimSpace(proxyState.Token) == "" {
			proxyState.Token = uuid.NewString()
		}
		if strings.TrimSpace(proxyState.JupyterURL) == "" {
			proxyState.JupyterURL = proxyState.ProxyPath
		}
	}
	return c.store.SaveProxyState(sandbox.Summary.ID, proxyState)
}

// sandboxStartEvent is the sandbox-start event recorded when a project run's
// sandbox transitions to running, either freshly created or resumed.
type sandboxStartEvent struct {
	Type    string
	Message string
}

func (c *Controller) startProjectRunSandbox(ctx context.Context, sandbox *domain.Sandbox, event sandboxStartEvent, trustedHeaders []domain.TrustedHeader) error {
	if sandbox == nil {
		return fmt.Errorf("sandbox is required")
	}
	if sandbox.Summary.VMStatus == domain.VMStatusDeleting {
		return fmt.Errorf("sandbox %s is being deleted", sandbox.Summary.ID)
	}
	if err := c.ensureProjectRunSandboxWorkspace(ctx, sandbox); err != nil {
		return err
	}
	if err := c.prepareFreshStartAgentEnvironment(ctx, sandbox); err != nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return err
	}
	return c.startProjectRunSandboxRuntime(ctx, sandbox, event, trustedHeaders)
}

func (c *Controller) ensureProjectRunSandboxWorkspace(ctx context.Context, sandbox *domain.Sandbox) error {
	if err := c.workspaceEnsurer.Ensure(ctx, sandbox); err != nil {
		sandbox.Summary.VMStatus = domain.VMStatusFailed
		_ = c.store.UpdateSandbox(ctx, sandbox)
		return err
	}
	return nil
}

func (c *Controller) prepareFreshStartAgentEnvironment(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox.Summary.VMStatus == domain.VMStatusRunning {
		return nil
	}
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return err
	}
	if !vmState.StartedAt.IsZero() && !sandboxes.RuntimeReleaseIntentional(sandbox) {
		return nil
	}
	if c.executor == nil {
		return fmt.Errorf("agent executor is required")
	}
	return c.executor.PrepareSandboxAgentEnvironmentFromTags(ctx, sandbox)
}

func (c *Controller) startProjectRunSandboxRuntime(ctx context.Context, sandbox *domain.Sandbox, event sandboxStartEvent, trustedHeaders []domain.TrustedHeader) error {
	WriteCapabilityGuide(ctx, CapabilityGuideDeps{
		Provider:       c.cap,
		Store:          c.store,
		Streams:        c.streams,
		Config:         c.config,
		WriteGuestFile: c.sandboxGuestFileWriter(sandbox),
	}, sandbox, capabilities.SandboxCapsets(sandbox))
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		if err := c.driver.StartSandboxVM(ctx, sandbox); err != nil {
			sandbox.Summary.VMStatus = domain.VMStatusFailed
			_ = c.store.UpdateSandbox(ctx, sandbox)
			return err
		}
	}
	sandbox.StoppedRuntime = nil
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := c.store.UpdateSandbox(ctx, sandbox); err != nil {
		return err
	}
	c.publishProjectRunSandboxStarted(ctx, sandbox, event.Type, event.Message)
	loaded, err := c.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return err
	}
	domain.RestoreSandboxTransientFields(loaded, sandbox)
	*sandbox = *loaded
	if c.capTokens != nil {
		c.capTokens.IndexSandbox(loaded, trustedHeaders)
	}
	return nil
}

func (c *Controller) publishProjectRunSandboxStarted(ctx context.Context, sandbox *domain.Sandbox, eventType, message string) {
	if c.streams != nil {
		c.streams.PublishSandboxUpdated(&sandbox.Summary)
	}
	if c.dashboard != nil {
		c.dashboard.Notify("sandbox_updated")
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     "info",
		Message:   message,
		CreatedAt: time.Now().UTC(),
	}
	_ = c.store.AddEvent(ctx, sandbox.Summary.ID, event)
	if c.streams != nil {
		c.streams.PublishEventAdded(sandbox.Summary.ID, event)
	}
	if c.bus != nil {
		topic := "agent-compose.sandbox.created"
		if eventType == "sandbox.resumed" {
			topic = "agent-compose.sandbox.resumed"
		}
		c.bus.Publish(domain.SchedulerTopicEvent{
			Topic:     topic,
			Payload:   schedulers.SessionTopicPayload(sandbox, "project-run"),
			CreatedAt: time.Now().UTC(),
		})
	}
}
