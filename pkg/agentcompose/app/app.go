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

	"agent-compose/pkg/agentcompose/adapters"
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/agentcompose/proxy"
	"agent-compose/pkg/cache"
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
	"agent-compose/pkg/cleanup"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/driver"
	"agent-compose/pkg/events"
	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/resources"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	storagesqlite "agent-compose/pkg/storage/sqlite"
	"agent-compose/pkg/volumes"
	"agent-compose/pkg/workspaces"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
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
	do.Provide(di, func(do.Injector) (*sessions.LifecycleLocks, error) { return sessions.NewLifecycleLocks(), nil })
	do.Provide(di, NewDatabase)
	do.Provide(di, NewConfigStore)
	do.Provide(di, NewSandboxStore)
	do.Provide(di, NewWorkspaceProvisioner)
	do.MustAs[*workspaces.Provisioner, workspaces.WorkspaceEnsurer](di)
	do.Provide(di, NewRuntimeProvider)
	do.Provide(di, NewLLMClient)
	do.Provide(di, NewCapabilityProvider)
	do.Provide(di, NewCapabilitySandboxResolver)
	do.Provide(di, NewImageBackends)
	do.Provide(di, NewCacheController)
	do.Provide(di, NewResourceLocator)
	do.Provide(di, NewVolumeManager)
	do.Provide(di, NewCapProxyServer)
	do.Provide(di, loaders.NewBus)
	do.Provide(di, sessions.NewStreamBroker)
	do.Provide(di, NewRunLogHub)
	do.Provide(di, NewEventDispatcher)
	do.Provide(di, NewDashboardOverviewAggregator)
	do.Provide(di, NewDashboardOverviewHub)
	do.Provide(di, loaders.NewLoaderEngine)
	do.Provide(di, NewSandboxDriver)
	do.Provide(di, NewCellExecutor)
	do.Provide(di, NewAgentRunner)
	do.Provide(di, NewAgentExecutor)
	do.Provide(di, NewLoaderCommandExecutor)
	do.Provide(di, NewLoaderSandboxRunner)
	do.Provide(di, NewSandboxRPCBridge)
	do.Provide(di, NewLoaderController)
	do.Provide(di, NewRunController)
	do.Provide(di, NewSandboxRunTargetResolver)
	do.Provide(di, NewSandboxRemovalCoordinator)
	do.Provide(di, NewCleanupRunner)
	do.Provide(di, NewRunSupervisor)
	do.Provide(di, NewProjectController)
}

func RegisterRoutes(di do.Injector) {
	app := do.MustInvoke[*echo.Echo](di)

	projectHandler := api.NewProjectHandler(
		projectControllerDelegate{controller: do.MustInvoke[*projects.Controller](di)},
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*loaders.Controller](di),
	)
	path, handler := agentcomposev2connect.NewProjectServiceHandler(projectHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	runDelegate := runControllerDelegate{
		controller: do.MustInvoke[*runs.Controller](di),
		supervisor: do.MustInvoke[*RunSupervisor](di),
	}
	runHandler := api.NewRunHandlerWithRunLogHub(runDelegate, do.MustInvoke[*configstore.ConfigStore](di), do.MustInvoke[*runs.RunLogHub](di), do.MustInvoke[*RunSupervisor](di))
	path, handler = agentcomposev2connect.NewRunServiceHandler(runHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	execHandler := api.NewExecHandler(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		func(session *domain.Sandbox) (api.ExecRuntime, error) {
			return do.MustInvoke[adapters.RuntimeProvider](di).ForSession(session)
		},
		runDelegate,
	).WithLifecycleLocks(do.MustInvoke[*sessions.LifecycleLocks](di))
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
	sandboxHandler := api.NewSandboxHandler(
		do.MustInvoke[*adapters.SandboxRPCBridge](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*adapters.SandboxDriver](di),
		do.MustInvoke[*dashboard.Hub](di),
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
	).WithRunTargetResolver(do.MustInvoke[*runs.SandboxRunTargetResolver](di)).WithRemovalCoordinator(do.MustInvoke[*sessions.RemovalCoordinator](di))
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

func (r sandboxRemovalTargetResolver) ResolveSandboxTargets(ctx context.Context, sandboxes []*domain.Sandbox) (map[string]sessions.SandboxOwnershipTarget, error) {
	resolved, err := r.resolver.ResolveBatch(ctx, sandboxes)
	if err != nil {
		return nil, err
	}
	out := make(map[string]sessions.SandboxOwnershipTarget, len(resolved))
	for id, target := range resolved {
		out[id] = sessions.SandboxOwnershipTarget{ProjectID: target.ProjectID, AgentName: target.AgentName}
	}
	return out, nil
}

func NewSandboxRemovalCoordinator(di do.Injector) (*sessions.RemovalCoordinator, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	return &sessions.RemovalCoordinator{
		SandboxRoot: config.SandboxRoot,
		Store:       do.MustInvoke[*sessionstore.Store](di),
		Runtime:     do.MustInvoke[*adapters.SandboxDriver](di),
		Targets: sandboxRemovalTargetResolver{
			resolver: do.MustInvoke[*runs.SandboxRunTargetResolver](di),
		},
		Residues: adapters.NewRuntimeResidueManager(config, do.MustInvoke[*adapters.SandboxDriver](di)),
		Locks:    do.MustInvoke[*sessions.LifecycleLocks](di),
	}, nil
}

func NewCleanupRunner(di do.Injector) (*cleanup.Runner, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	store := do.MustInvoke[*sessionstore.Store](di)
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
			{TTL: config.WorkspaceCleanupTTL, Cleaner: &sessions.WorkspaceCleaner{Store: store, Locks: do.MustInvoke[*sessions.LifecycleLocks](di)}},
			{TTL: config.ImageCacheCleanupTTL, Cleaner: &adapters.ImageCacheCleaner{Cache: imageCache, Sandboxes: store, SandboxRoot: config.SandboxRoot}},
		},
	}, nil
}

func StartBackground(di do.Injector) error {
	// Constructing the cleanup runner installs the cache-dependency lock on the
	// sandbox store. Do this before resolving or starting any component that can
	// create a sandbox, so the initial cleanup pass cannot race registration.
	runner := do.MustInvoke[*cleanup.Runner](di)
	for _, warning := range do.MustInvoke[*sessions.RemovalCoordinator](di).Recover(do.MustInvoke[context.Context](di)) {
		slog.Warn("failed to recover sandbox deletion", "warning", warning)
	}
	if err := syncLegacyDefaultProject(do.MustInvoke[context.Context](di), do.MustInvoke[*projects.Controller](di)); err != nil {
		slog.Warn("failed to sync legacy v1 agents into the default project", "error", err)
	}
	ctx := do.MustInvoke[context.Context](di)
	if err := startBackgroundManagers(
		ctx,
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.SandboxRPCBridge](di),
		do.MustInvoke[*loaders.Controller](di),
		do.MustInvoke[*events.Dispatcher](di),
		do.MustInvoke[*capproxy.Server](di),
		do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
	); err != nil {
		return err
	}
	runner.Start(ctx)
	return nil
}

func StopBackground(ctx context.Context, di do.Injector) error {
	runner, err := do.Invoke[*cleanup.Runner](di)
	if err != nil {
		return err
	}
	return runner.Shutdown(ctx)
}

func NewCapProxyServer(di do.Injector) (*capproxy.Server, error) {
	return adapters.NewCapProxyServer(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
	), nil
}

func NewImageBackends(di do.Injector) (*adapters.ImageBackends, error) {
	return adapters.NewImageBackends(do.MustInvoke[*appconfig.Config](di))
}

func NewCacheController(di do.Injector) (*cache.Controller, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	_ = do.MustInvoke[*sessionstore.Store](di)
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
	records, warnings := sessions.ListOwnershipRecords(p.sandboxRoot)
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
		do.MustInvoke[*sessionstore.Store](di),
		backends.Auto,
		do.MustInvoke[*cache.Controller](di),
	), nil
}

func NewVolumeManager(di do.Injector) (*volumes.Manager, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	store := do.MustInvoke[*configstore.ConfigStore](di)
	manager := volumes.NewManager(store, volumes.NewLocalDriver(config))
	manager.Sandboxes = do.MustInvoke[*sessionstore.Store](di)
	return manager, nil
}

func NewRuntimeProvider(di do.Injector) (adapters.RuntimeProvider, error) {
	return adapters.NewRuntimeProvider(do.MustInvoke[*appconfig.Config](di))
}

func NewLLMClient(di do.Injector) (*adapters.LLMClient, error) {
	return adapters.NewLLMClient(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*configstore.ConfigStore](di)), nil
}

func NewSandboxDriver(di do.Injector) (*adapters.SandboxDriver, error) {
	return adapters.NewSandboxDriver(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
	), nil
}

func NewCellExecutor(di do.Injector) (*adapters.CellExecutor, error) {
	return adapters.NewCellExecutor(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
		do.MustInvoke[*sessions.StreamBroker](di),
	), nil
}

func NewAgentRunner(di do.Injector) (*adapters.AgentRunner, error) {
	return adapters.NewAgentRunner(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
	), nil
}

func NewAgentExecutor(di do.Injector) (*adapters.AgentExecutor, error) {
	return adapters.NewAgentExecutor(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*sessions.StreamBroker](di),
		do.MustInvoke[*adapters.AgentRunner](di),
	), nil
}

func NewLoaderCommandExecutor(di do.Injector) (*adapters.LoaderCommandExecutor, error) {
	return adapters.NewLoaderCommandExecutor(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
		do.MustInvoke[*sessions.StreamBroker](di),
	), nil
}

func NewLoaderSandboxRunner(di do.Injector) (*adapters.LoaderSandboxRunner, error) {
	return adapters.NewLoaderSandboxRunner(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[workspaces.WorkspaceEnsurer](di),
		do.MustInvoke[*adapters.SandboxDriver](di),
		do.MustInvoke[capabilities.Provider](di),
		do.MustInvoke[*volumes.Manager](di),
		do.MustInvoke[*sessions.StreamBroker](di),
		do.MustInvoke[*loaders.Bus](di),
		do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
		do.MustInvoke[*sessions.LifecycleLocks](di),
	), nil
}

func NewSandboxRPCBridge(di do.Injector) (*adapters.SandboxRPCBridge, error) {
	dashboard, _ := do.Invoke[*dashboard.Hub](di)
	return adapters.NewSandboxRPCBridge(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[workspaces.WorkspaceEnsurer](di),
		do.MustInvoke[*adapters.SandboxDriver](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
		do.MustInvoke[*loaders.Bus](di),
		do.MustInvoke[*sessions.StreamBroker](di),
		do.MustInvoke[capabilities.Provider](di),
		do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
		dashboard,
		do.MustInvoke[*sessions.LifecycleLocks](di),
	), nil
}

func NewDatabase(di do.Injector) (*storagesqlite.Database, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	if err := os.MkdirAll(config.DataRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create agent-compose data root: %w", err)
	}
	return storagesqlite.Open(config.DbAddr, config.DbTimeout)
}

func NewConfigStore(di do.Injector) (*configstore.ConfigStore, error) {
	database := do.MustInvoke[*storagesqlite.Database](di)
	return configstore.FromDB(database.DB()), nil
}

func NewSandboxStore(di do.Injector) (*sessionstore.Store, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	database := do.MustInvoke[*storagesqlite.Database](di)
	return sessionstore.NewWithDatabase(config, database.DB())
}

func NewWorkspaceProvisioner(di do.Injector) (*workspaces.Provisioner, error) {
	return workspaces.NewProvisioner(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*sessionstore.Store](di),
	), nil
}

func NewCapabilityProvider(di do.Injector) (capabilities.Provider, error) {
	conf := do.MustInvoke[*appconfig.Config](di)
	return adapters.NewCapabilityProvider(do.MustInvoke[*configstore.ConfigStore](di), conf.CapGRPCTarget), nil
}

func NewCapabilitySandboxResolver(di do.Injector) (*adapters.CapabilitySandboxResolver, error) {
	return adapters.NewCapabilitySandboxResolver(do.MustInvoke[*sessionstore.Store](di)), nil
}

func NewEventDispatcher(di do.Injector) (*events.Dispatcher, error) {
	return events.NewDispatcher(
		do.MustInvoke[context.Context](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*loaders.Bus](di),
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
	return dashboard.NewAggregator(do.MustInvoke[*sessionstore.Store](di), do.MustInvoke[*configstore.ConfigStore](di)), nil
}

func NewDashboardOverviewHub(di do.Injector) (*dashboard.Hub, error) {
	return dashboard.NewHub(do.MustInvoke[context.Context](di), do.MustInvoke[*dashboard.Aggregator](di), 250*time.Millisecond), nil
}

func NewRunLogHub(do.Injector) (*runs.RunLogHub, error) {
	return runs.NewRunLogHub(), nil
}

func registerProxyRoutes(app *echo.Echo, di do.Injector) {
	sessions := do.MustInvoke[*adapters.SandboxRPCBridge](di)
	proxy.RegisterJupyterRoutes(app, proxy.JupyterOptions{
		BasePath: do.MustInvoke[*appconfig.Config](di).JupyterProxyBasePath,
		Store:    do.MustInvoke[*sessionstore.Store](di),
		EnsureReady: func(ctx context.Context, sessionID string) (domain.ProxyState, error) {
			return sessions.EnsureSessionProxyReady(ctx, sessionID)
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
		Sandboxes: do.MustInvoke[*sessionstore.Store](di),
		ResolveTarget: func(ctx context.Context, requestedModel, providerID string) (llms.ResolvedTarget, error) {
			return llms.ResolveRuntimeLLMTarget(ctx, config, configDB, requestedModel, providerID)
		},
		Client: proxy.NewRuntimeLLMHTTPClient(config.LLMTimeout),
	})
}

func registerWebhookRoutes(app *echo.Echo, di do.Injector) {
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{
		Store:            do.MustInvoke[*configstore.ConfigStore](di),
		WebhookBodyLimit: do.MustInvoke[*appconfig.Config](di).WebhookBodyLimitBytes,
	})
}
