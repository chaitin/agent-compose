package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/agentcompose/proxy"
	"github.com/chaitin/agent-compose/pkg/cache"
	"github.com/chaitin/agent-compose/pkg/capabilities"
	"github.com/chaitin/agent-compose/pkg/capproxy"
	"github.com/chaitin/agent-compose/pkg/cleanup"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/dashboard"
	"github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/events"
	"github.com/chaitin/agent-compose/pkg/events/webhooks"
	"github.com/chaitin/agent-compose/pkg/imagecache"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/resources"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	storagesqlite "github.com/chaitin/agent-compose/pkg/storage/sqlite"
	"github.com/chaitin/agent-compose/pkg/volumes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func Setup(di do.Injector) {
	Register(di)
	if err := StartBackground(di); err != nil {
		slog.Error("failed to start agent-compose background managers", "error", err)
	}
}

func Register(di do.Injector) {
	RegisterDependencies(di)
	RegisterRoutes(di)
}

func RegisterDependencies(di do.Injector) {
	do.Provide(di, func(do.Injector) (*sandboxes.LifecycleLocks, error) { return sandboxes.NewLifecycleLocks(), nil })
	do.Provide(di, NewDatabase)
	do.Provide(di, NewConfigStore)
	do.Provide(di, NewSandboxStore)
	do.Provide(di, NewWorkspaceProvisioner)
	do.MustAs[*workspaces.Provisioner, workspaces.WorkspaceEnsurer](di)
	do.Provide(di, NewRuntimeProvider)
	do.Provide(di, NewLLMClient)
	do.Provide(di, NewProjectOctoBusTargetResolver)
	do.Provide(di, NewCapabilityProvider)
	do.Provide(di, NewCapabilitySandboxResolver)
	do.Provide(di, NewImageBackends)
	do.Provide(di, NewCacheController)
	do.Provide(di, NewResourceLocator)
	do.Provide(di, NewVolumeManager)
	do.Provide(di, NewCapProxyServer)
	do.Provide(di, schedulers.NewBus)
	do.Provide(di, sandboxes.NewStreamBroker)
	do.Provide(di, NewRunLogHub)
	do.Provide(di, NewEventDispatcher)
	do.Provide(di, NewDashboardOverviewAggregator)
	do.Provide(di, NewDashboardOverviewHub)
	do.Provide(di, schedulers.NewSchedulerEngine)
	do.Provide(di, NewSandboxDriver)
	do.Provide(di, NewCellExecutor)
	do.Provide(di, NewAgentRunner)
	do.Provide(di, NewAgentExecutor)
	do.Provide(di, NewSchedulerCommandExecutor)
	do.Provide(di, NewSchedulerSandboxRunner)
	do.Provide(di, NewSandboxRPCBridge)
	do.Provide(di, NewSchedulerController)
	do.Provide(di, NewRunController)
	do.Provide(di, NewSandboxRunTargetResolver)
	do.Provide(di, NewSandboxRemovalCoordinator)
	do.Provide(di, NewRunCompletionManager)
	do.Provide(di, NewDeletionRecovery)
	do.Provide(di, NewCleanupRunner)
	do.Provide(di, NewRunSupervisor)
	do.Provide(di, NewProjectController)
}

func RegisterRoutes(di do.Injector) {
	app := do.MustInvoke[*echo.Echo](di)
	schedulerController := do.MustInvoke[*schedulers.Controller](di)

	projectHandler := api.NewProjectHandlerWithAgentModels(api.ProjectHandlerDeps{
		Delegate:         projectControllerDelegate{controller: do.MustInvoke[*projects.Controller](di)},
		Store:            do.MustInvoke[*configstore.ConfigStore](di),
		SchedulerRuntime: schedulerController,
		SchedulerRuns:    schedulerController.SchedulerRuns(),
		AgentModels:      newProjectAgentModelResolver(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*configstore.ConfigStore](di)),
		SandboxDirs:      do.MustInvoke[*sandboxstore.Store](di),
	})
	path, handler := agentcomposev2connect.NewProjectServiceHandler(projectHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	runDelegate := runControllerDelegate{
		controller: do.MustInvoke[*runs.Controller](di),
		supervisor: do.MustInvoke[*RunSupervisor](di),
	}
	runHandler := api.NewRunHandlerWithRunLogHub(runDelegate, do.MustInvoke[*configstore.ConfigStore](di), do.MustInvoke[*runs.RunLogHub](di), do.MustInvoke[*RunSupervisor](di))
	path, handler = agentcomposev2connect.NewRunServiceHandler(runHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	execHandler := api.NewExecHandler(api.ExecHandlerDeps{
		Config:   do.MustInvoke[*appconfig.Config](di),
		Store:    do.MustInvoke[*sandboxstore.Store](di),
		Projects: do.MustInvoke[*configstore.ConfigStore](di),
		Runtime: func(session *domain.Sandbox) (api.ExecRuntime, error) {
			return do.MustInvoke[adapters.RuntimeProvider](di).ForSession(session)
		},
	}, runDelegate).WithLifecycleLocks(do.MustInvoke[*sandboxes.LifecycleLocks](di))
	path, handler = agentcomposev2connect.NewExecServiceHandler(execHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	imageHandler := api.NewImageHandler(do.MustInvoke[*adapters.ImageBackends](di))
	path, handler = agentcomposev2connect.NewImageServiceHandler(imageHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	cacheHandler := api.NewCacheHandler(do.MustInvoke[*cache.Controller](di))
	path, handler = agentcomposev2connect.NewCacheServiceHandler(cacheHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	volumeHandler := api.NewVolumeHandler(do.MustInvoke[*volumes.Manager](di))
	path, handler = agentcomposev2connect.NewVolumeServiceHandler(volumeHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	sandboxHandler := api.NewSandboxHandler(api.SandboxHandlerDeps{
		Delegate:  do.MustInvoke[*adapters.SandboxRPCBridge](di),
		Store:     do.MustInvoke[*sandboxstore.Store](di),
		Remover:   do.MustInvoke[*adapters.SandboxDriver](di),
		Dashboard: do.MustInvoke[*dashboard.Hub](di),
	},
		func(session *domain.Sandbox) (api.SandboxStatsRuntime, error) {
			runtime, err := do.MustInvoke[adapters.RuntimeProvider](di).ForSession(session)
			if err != nil {
				return nil, err
			}
			statsRuntime, ok := runtime.(api.SandboxStatsRuntime)
			if !ok {
				return nil, domain.ClassifyError(domain.ErrUnsupported, "sandbox stats are unsupported by this runtime driver", nil)
			}
			return statsRuntime, nil
		},
	).WithRunTargetResolver(do.MustInvoke[*runs.SandboxRunTargetResolver](di)).WithRemovalCoordinator(do.MustInvoke[*sandboxes.RemovalCoordinator](di))
	path, handler = agentcomposev2connect.NewSandboxServiceHandler(sandboxHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewSettingsServiceHandler(api.NewSettingsV2Handler(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*configstore.ConfigStore](di)))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewDashboardServiceHandler(api.NewDashboardV2Handler(do.MustInvoke[*dashboard.Hub](di)))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewCapabilityServiceHandler(api.NewCapabilityV2Handler(do.MustInvoke[capabilities.Provider](di), capabilityRuntimeConfig{config: do.MustInvoke[*appconfig.Config](di)}))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewLLMServiceHandler(api.NewLLMHandler(do.MustInvoke[*adapters.LLMClient](di)))
	app.Any(path+"*", echo.WrapHandler(handler))
	resourceHandler := api.NewResourceHandler(do.MustInvoke[*resources.Locator](di))
	path, handler = agentcomposev2connect.NewResourceServiceHandler(resourceHandler)
	app.Any(path+"*", echo.WrapHandler(handler))

	registerProxyRoutes(app, di)
	registerWorkspaceRoutes(app, di)
	registerRuntimeLLMFacadeRoutes(app, di)
	registerWebhookRoutes(app, di)
}

func NewSandboxRunTargetResolver(di do.Injector) (*runs.SandboxRunTargetResolver, error) {
	return runs.NewSandboxRunTargetResolver(do.MustInvoke[*configstore.ConfigStore](di))
}

type sandboxRemovalTargetResolver struct {
	resolver *runs.SandboxRunTargetResolver
}

func (r sandboxRemovalTargetResolver) ResolveSandboxTargets(ctx context.Context, items []*domain.Sandbox) (map[string]sandboxes.SandboxOwnershipTarget, error) {
	resolved, err := r.resolver.ResolveBatch(ctx, items)
	if err != nil {
		return nil, err
	}
	out := make(map[string]sandboxes.SandboxOwnershipTarget, len(resolved))
	for id, target := range resolved {
		out[id] = sandboxes.SandboxOwnershipTarget{ProjectID: target.ProjectID, AgentName: target.AgentName}
	}
	return out, nil
}

func NewSandboxRemovalCoordinator(di do.Injector) (*sandboxes.RemovalCoordinator, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	return &sandboxes.RemovalCoordinator{
		SandboxRoot: config.SandboxRoot,
		Store:       do.MustInvoke[*sandboxstore.Store](di),
		Runtime:     do.MustInvoke[*adapters.SandboxDriver](di),
		Targets: sandboxRemovalTargetResolver{
			resolver: do.MustInvoke[*runs.SandboxRunTargetResolver](di),
		},
		Residues: adapters.NewRuntimeResidueManager(config, do.MustInvoke[*adapters.SandboxDriver](di)),
		Locks:    do.MustInvoke[*sandboxes.LifecycleLocks](di),
	}, nil
}

func NewDeletionRecovery(di do.Injector) (*sandboxes.DeletionRecovery, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	return sandboxes.NewDeletionRecoveryWithArchiveRoot(
		do.MustInvoke[*sandboxes.RemovalCoordinator](di),
		config.SandboxArchiveRoot,
		do.MustInvoke[*slog.Logger](di),
	), nil
}

func NewCleanupRunner(di do.Injector) (*cleanup.Runner, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	store := do.MustInvoke[*sandboxstore.Store](di)
	imageCache, err := imagecache.New(imagecache.Config{
		Root: config.ImageCacheRoot, DefaultRegistry: config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return nil, err
	}
	store.SetCacheDependencyLocker(imageCache)
	return &cleanup.Runner{
		Interval: config.CleanupInterval,
		Policies: []cleanup.Policy{
			{TTL: config.WorkspaceCleanupTTL, Cleaner: &sandboxes.WorkspaceCleaner{
				Store: store, Locks: do.MustInvoke[*sandboxes.LifecycleLocks](di),
			}},
			{TTL: config.SandboxRetentionTTL, Cleaner: &sandboxes.SandboxRetentionCleaner{
				Store: store, Locks: do.MustInvoke[*sandboxes.LifecycleLocks](di), ArchiveRoot: config.SandboxArchiveRoot,
				Removal: do.MustInvoke[*sandboxes.RemovalCoordinator](di), SandboxRoot: config.SandboxRoot,
			}},
			{TTL: config.ImageCacheCleanupTTL, Cleaner: &adapters.ImageCacheCleaner{Cache: imageCache, Sandboxes: store, SandboxRoot: config.SandboxRoot}},
		},
	}, nil
}

func StartBackground(di do.Injector) error {
	// Constructing the cleanup runner installs the cache-dependency lock on the
	// sandbox store. Do this before resolving or starting any component that can
	// create a sandbox, so the initial cleanup pass cannot race registration.
	runner := do.MustInvoke[*cleanup.Runner](di)
	ctx := do.MustInvoke[context.Context](di)
	if err := loadModelCatalog(ctx, do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*configstore.ConfigStore](di)); err != nil {
		return fmt.Errorf("load model catalog: %w", err)
	}
	for _, warning := range do.MustInvoke[*adapters.SandboxRPCBridge](di).RecoverStoppedRuntimeReleases(ctx) {
		slog.Warn("failed to recover stopped runtime release", "warning", warning)
	}
	if err := startBackgroundManagers(ctx, backgroundManagersDeps{
		Sandboxes:   do.MustInvoke[*sandboxstore.Store](di),
		ConfigDB:    do.MustInvoke[*configstore.ConfigStore](di),
		Bridge:      do.MustInvoke[*adapters.SandboxRPCBridge](di),
		Schedulers:  do.MustInvoke[*schedulers.Controller](di),
		Events:      do.MustInvoke[*events.Dispatcher](di),
		CapProxy:    do.MustInvoke[*capproxy.Server](di),
		CapTokens:   do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
		Completions: do.MustInvoke[*runs.CompletionManager](di),
	}); err != nil {
		return err
	}
	if err := do.MustInvoke[*sandboxes.DeletionRecovery](di).Start(ctx); err != nil {
		return fmt.Errorf("start sandbox deletion recovery: %w", err)
	}
	runner.Start(ctx)
	return nil
}

func StopBackground(ctx context.Context, di do.Injector) error {
	components := make([]backgroundComponent, 0, 2)
	var setupErrors []error
	if supervisor, err := do.Invoke[*RunSupervisor](di); err != nil {
		setupErrors = append(setupErrors, fmt.Errorf("resolve run supervisor: %w", err))
	} else if err := supervisor.Shutdown(ctx); err != nil {
		setupErrors = append(setupErrors, fmt.Errorf("stop run supervisor: %w", err))
	}
	completion, completionErr := do.Invoke[*runs.CompletionManager](di)
	if completionErr != nil {
		setupErrors = append(setupErrors, fmt.Errorf("resolve project run completion manager: %w", completionErr))
	} else {
		components = append(components, backgroundComponent{name: "project run completion manager", shutdown: completion.Shutdown})
	}

	recovery, recoveryErr := do.Invoke[*sandboxes.DeletionRecovery](di)
	if recoveryErr != nil {
		setupErrors = append(setupErrors, fmt.Errorf("resolve sandbox deletion recovery: %w", recoveryErr))
	} else {
		components = append(components, backgroundComponent{name: "sandbox deletion recovery", shutdown: recovery.Shutdown})
	}
	runner, runnerErr := do.Invoke[*cleanup.Runner](di)
	if runnerErr != nil {
		setupErrors = append(setupErrors, fmt.Errorf("resolve cleanup runner: %w", runnerErr))
	} else {
		components = append(components, backgroundComponent{name: "cleanup runner", shutdown: runner.Shutdown})
	}
	return stopBackgroundComponents(ctx, components, setupErrors...)
}

func NewCapProxyServer(di do.Injector) (*capproxy.Server, error) {
	return adapters.NewCapProxyServer(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
		do.MustInvoke[*adapters.ProjectOctoBusTargetResolver](di),
	), nil
}

func NewImageBackends(di do.Injector) (*adapters.ImageBackends, error) {
	return adapters.NewImageBackends(do.MustInvoke[*appconfig.Config](di))
}

func NewCacheController(di do.Injector) (*cache.Controller, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	_ = do.MustInvoke[*sandboxstore.Store](di)
	_ = do.MustInvoke[*configstore.ConfigStore](di)

	imageCacheRoot := strings.TrimSpace(config.ImageCacheRoot)
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(config.DataRoot, "images")
		config.ImageCacheRoot = imageCacheRoot
	}
	ociCache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return nil, err
	}
	config.ImageCacheRoot = ociCache.Root()

	materializedDependencies := cache.CombinedMaterializedDependencies{
		ownershipMaterializedDependencies{sandboxRoot: config.SandboxRoot},
		cache.MicrosandboxRootfsDependencies{Home: config.MicrosandboxHome, BaseRoot: ociCache.MaterializationRoot()},
	}
	sources := []cache.Source{
		cache.OCISource{Cache: ociCache},
		cache.MaterializedSource{
			Scanner: cache.MaterializedScanner{Cache: ociCache, Dependencies: materializedDependencies},
			Remover: cache.MaterializedRemover{Cache: ociCache, Dependencies: materializedDependencies},
		},
		cache.SkillSource{Root: filepath.Join(config.DataRoot, "skills")},
	}
	sources = append(sources, driver.NewRuntimeCacheSources(config)...)
	return &cache.Controller{Sources: sources, TTL: config.CacheTTL}, nil
}

type ownershipMaterializedDependencies struct {
	sandboxRoot string
}

func (p ownershipMaterializedDependencies) MaterializedDependencies(_ context.Context) ([]cache.MaterializedDependency, []string, error) {
	records, warnings := sandboxes.ListOwnershipRecords(p.sandboxRoot)
	var dependencies []cache.MaterializedDependency
	for _, record := range records {
		for _, dependency := range record.CacheDependencies {
			if dependency.Domain != "runtime-image" || strings.TrimSpace(dependency.Identity) == "" {
				continue
			}
			dependencies = append(dependencies, cache.MaterializedDependency{SandboxID: record.SandboxID, Identity: dependency.Identity, Status: record.LifecycleState})
		}
	}
	return dependencies, warnings, nil
}

func NewResourceLocator(di do.Injector) (*resources.Locator, error) {
	backends := do.MustInvoke[*adapters.ImageBackends](di)
	return resources.NewLocator(
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*sandboxstore.Store](di),
		backends.Auto,
		do.MustInvoke[*cache.Controller](di),
	), nil
}

func NewVolumeManager(di do.Injector) (*volumes.Manager, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	store := do.MustInvoke[*configstore.ConfigStore](di)
	manager := volumes.NewManager(store, volumes.NewLocalDriver(config), volumes.NewK8sDriver(config))
	manager.Sandboxes = do.MustInvoke[*sandboxstore.Store](di)
	return manager, nil
}

func NewRuntimeProvider(di do.Injector) (adapters.RuntimeProvider, error) {
	return adapters.NewRuntimeProvider(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*sandboxstore.Store](di))
}

func NewLLMClient(di do.Injector) (*adapters.LLMClient, error) {
	return adapters.NewLLMClient(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*configstore.ConfigStore](di)), nil
}

func NewSandboxDriver(di do.Injector) (*adapters.SandboxDriver, error) {
	return adapters.NewSandboxDriver(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sandboxstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
	), nil
}

func NewCellExecutor(di do.Injector) (*adapters.CellExecutor, error) {
	return adapters.NewCellExecutor(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sandboxstore.Store](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
		do.MustInvoke[*sandboxes.StreamBroker](di),
	), nil
}

func NewAgentRunner(di do.Injector) (*adapters.AgentRunner, error) {
	return adapters.NewAgentRunner(adapters.AgentRunnerDeps{
		Config:   do.MustInvoke[*appconfig.Config](di),
		Store:    do.MustInvoke[*sandboxstore.Store](di),
		ConfigDB: do.MustInvoke[*configstore.ConfigStore](di),
		Agents:   do.MustInvoke[*configstore.ConfigStore](di),
		Runtimes: do.MustInvoke[adapters.RuntimeProvider](di),
	}), nil
}

func NewAgentExecutor(di do.Injector) (*adapters.AgentExecutor, error) {
	return adapters.NewAgentExecutor(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sandboxstore.Store](di),
		do.MustInvoke[*sandboxes.StreamBroker](di),
		do.MustInvoke[*adapters.AgentRunner](di),
	), nil
}

func NewSchedulerCommandExecutor(di do.Injector) (*adapters.SchedulerCommandExecutor, error) {
	return adapters.NewSchedulerCommandExecutor(adapters.SchedulerCommandExecutorDeps{
		Config:   do.MustInvoke[*appconfig.Config](di),
		Store:    do.MustInvoke[*sandboxstore.Store](di),
		ConfigDB: do.MustInvoke[*configstore.ConfigStore](di),
		Runtimes: do.MustInvoke[adapters.RuntimeProvider](di),
		Streams:  do.MustInvoke[*sandboxes.StreamBroker](di),
	}), nil
}

func NewSchedulerSandboxRunner(di do.Injector) (*adapters.SchedulerSandboxRunner, error) {
	return adapters.NewSchedulerSandboxRunner(adapters.SchedulerSandboxRunnerDeps{
		Config:           do.MustInvoke[*appconfig.Config](di),
		Store:            do.MustInvoke[*sandboxstore.Store](di),
		ConfigDB:         do.MustInvoke[*configstore.ConfigStore](di),
		WorkspaceEnsurer: do.MustInvoke[workspaces.WorkspaceEnsurer](di),
		Driver:           do.MustInvoke[*adapters.SandboxDriver](di),
		Cap:              do.MustInvoke[capabilities.Provider](di),
		VolumeResolver:   do.MustInvoke[*volumes.Manager](di),
		Streams:          do.MustInvoke[*sandboxes.StreamBroker](di),
		Publisher:        do.MustInvoke[*schedulers.Bus](di),
		CapTokens:        do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
		AgentExecutor:    do.MustInvoke[*adapters.AgentExecutor](di),
	}, do.MustInvoke[*sandboxes.LifecycleLocks](di)), nil
}

func NewSandboxRPCBridge(di do.Injector) (*adapters.SandboxRPCBridge, error) {
	dashboard, _ := do.Invoke[*dashboard.Hub](di)
	return adapters.NewSandboxRPCBridge(adapters.SandboxRPCBridgeDeps{
		Config:           do.MustInvoke[*appconfig.Config](di),
		Store:            do.MustInvoke[*sandboxstore.Store](di),
		ConfigDB:         do.MustInvoke[*configstore.ConfigStore](di),
		WorkspaceEnsurer: do.MustInvoke[workspaces.WorkspaceEnsurer](di),
		Driver:           do.MustInvoke[*adapters.SandboxDriver](di),
		Runtimes:         do.MustInvoke[adapters.RuntimeProvider](di),
		Bus:              do.MustInvoke[*schedulers.Bus](di),
		Streams:          do.MustInvoke[*sandboxes.StreamBroker](di),
		Cap:              do.MustInvoke[capabilities.Provider](di),
		CapTokens:        do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
		Dashboard:        dashboard,
		AgentExecutor:    do.MustInvoke[*adapters.AgentExecutor](di),
	}, do.MustInvoke[*sandboxes.LifecycleLocks](di)), nil
}

func NewDatabase(di do.Injector) (*storagesqlite.Database, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	if err := os.MkdirAll(config.DataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create agent-compose data root: %w", err)
	}
	return storagesqlite.OpenWithMaxOpenConns(config.DbAddr, config.DbTimeout, config.EffectiveSQLiteMaxOpenConns())
}

func NewConfigStore(di do.Injector) (*configstore.ConfigStore, error) {
	database := do.MustInvoke[*storagesqlite.Database](di)
	return configstore.FromDB(database.DB()), nil
}

func NewSandboxStore(di do.Injector) (*sandboxstore.Store, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	database := do.MustInvoke[*storagesqlite.Database](di)
	resolver := sandboxProjectProjectionResolver{resolver: do.MustInvoke[*runs.SandboxRunTargetResolver](di)}
	return sandboxstore.NewWithDatabase(config, database.DB(), resolver)
}

type sandboxProjectProjectionResolver struct {
	resolver *runs.SandboxRunTargetResolver
}

func (r sandboxProjectProjectionResolver) ResolveSandboxProjectIDs(ctx context.Context, sandboxes []*domain.Sandbox) (map[string]string, error) {
	resolved, err := r.resolver.ResolveBatch(ctx, sandboxes)
	if err != nil {
		return nil, err
	}
	projectIDs := make(map[string]string, len(resolved))
	for sandboxID, target := range resolved {
		projectIDs[sandboxID] = target.ProjectID
	}
	return projectIDs, nil
}

func NewWorkspaceProvisioner(di do.Injector) (*workspaces.Provisioner, error) {
	return workspaces.NewProvisioner(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*sandboxstore.Store](di),
	), nil
}

func NewCapabilityProvider(di do.Injector) (capabilities.Provider, error) {
	conf := do.MustInvoke[*appconfig.Config](di)
	return adapters.NewCapabilityProvider(
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.ProjectOctoBusTargetResolver](di),
		conf.CapGRPCTarget,
	), nil
}

func NewProjectOctoBusTargetResolver(di do.Injector) (*adapters.ProjectOctoBusTargetResolver, error) {
	return adapters.NewProjectOctoBusTargetResolver(do.MustInvoke[*configstore.ConfigStore](di)), nil
}

func NewCapabilitySandboxResolver(di do.Injector) (*adapters.CapabilitySandboxResolver, error) {
	return adapters.NewCapabilitySandboxResolver(do.MustInvoke[*sandboxstore.Store](di)), nil
}

func NewEventDispatcher(di do.Injector) (*events.Dispatcher, error) {
	return events.NewDispatcher(
		do.MustInvoke[context.Context](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*schedulers.Bus](di),
	), nil
}

type capabilityRuntimeConfig struct {
	config *appconfig.Config
}

func (c capabilityRuntimeConfig) CapProxyListen() string {
	if c.config == nil {
		return ""
	}
	return c.config.CapGRPCListen
}

func NewDashboardOverviewAggregator(di do.Injector) (*dashboard.Aggregator, error) {
	return dashboard.NewAggregator(do.MustInvoke[*sandboxstore.Store](di), do.MustInvoke[*configstore.ConfigStore](di)), nil
}

func NewDashboardOverviewHub(di do.Injector) (*dashboard.Hub, error) {
	return dashboard.NewHub(do.MustInvoke[context.Context](di), do.MustInvoke[*dashboard.Aggregator](di), 250*time.Millisecond), nil
}

func NewRunLogHub(do.Injector) (*runs.RunLogHub, error) {
	return runs.NewRunLogHub(), nil
}

func registerProxyRoutes(app *echo.Echo, di do.Injector) {
	bridge := do.MustInvoke[*adapters.SandboxRPCBridge](di)
	proxy.RegisterJupyterRoutes(app, proxy.JupyterOptions{
		BasePath: do.MustInvoke[*appconfig.Config](di).JupyterProxyBasePath,
		Store:    do.MustInvoke[*sandboxstore.Store](di),
		EnsureReady: func(ctx context.Context, sandboxID string) (domain.ProxyState, error) {
			return bridge.EnsureSessionProxyReady(ctx, sandboxID)
		},
	})
}

func registerWorkspaceRoutes(app *echo.Echo, di do.Injector) {
	config := do.MustInvoke[*appconfig.Config](di)
	configDB := do.MustInvoke[*configstore.ConfigStore](di)
	proxy.RegisterWorkspaceRoutes(app, proxy.WorkspaceOptions{
		UploadLimitBytes: config.WorkspaceUploadLimitBytes,
		Load: func(ctx context.Context, workspaceID string) (domain.WorkspaceConfig, workspaces.FileWorkspaceContent, error) {
			workspace, err := configDB.GetWorkspaceConfig(ctx, workspaceID)
			if err != nil {
				return domain.WorkspaceConfig{}, workspaces.FileWorkspaceContent{}, err
			}
			if strings.ToLower(strings.TrimSpace(workspace.Type)) != "file" {
				return domain.WorkspaceConfig{}, workspaces.FileWorkspaceContent{}, domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace config %s is not a file workspace", workspace.ID), nil)
			}
			content, err := workspaces.OpenFileWorkspaceContent(config, workspace)
			if err != nil {
				return domain.WorkspaceConfig{}, workspaces.FileWorkspaceContent{}, err
			}
			return workspace, content, nil
		},
	})
}

func registerRuntimeLLMFacadeRoutes(app *echo.Echo, di do.Injector) {
	config := do.MustInvoke[*appconfig.Config](di)
	configDB := do.MustInvoke[*configstore.ConfigStore](di)
	proxy.RegisterRuntimeLLMFacadeRoutes(app, proxy.RuntimeLLMOptions{
		Tokens:    configDB,
		Sandboxes: do.MustInvoke[*sandboxstore.Store](di),
		ResolveTarget: func(ctx context.Context, requestedModel, providerID string) (llms.ResolvedTarget, error) {
			return llms.ResolveRuntimeLLMTarget(ctx, config, configDB, requestedModel, providerID)
		},
		Client:          proxy.NewRuntimeLLMHTTPClient(config.LLMTimeout),
		MaxOutputTokens: config.LLMMaxOutputTokens,
	})
}

func registerWebhookRoutes(app *echo.Echo, di do.Injector) {
	configDB := do.MustInvoke[*configstore.ConfigStore](di)
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{
		Store:            configDB,
		QueryStore:       configDB,
		Sandboxes:        do.MustInvoke[*sandboxstore.Store](di),
		WebhookBodyLimit: do.MustInvoke[*appconfig.Config](di).WebhookBodyLimitBytes,
		RunStopper:       do.MustInvoke[*RunSupervisor](di),
	})
}
