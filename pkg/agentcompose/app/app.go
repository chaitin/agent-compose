package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	"agent-compose/pkg/agentcompose/adapters"
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/agentcompose/proxy"
	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capproxy"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/driver"
	"agent-compose/pkg/events"
	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/imagecache"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/networks"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/resources"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/runtimecache"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
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
	do.Provide(di, sessionstore.NewStore)
	do.Provide(di, NewConfigStore)
	do.Provide(di, NewWorkspaceProvisioner)
	do.MustAs[*workspaces.Provisioner, workspaces.WorkspaceEnsurer](di)
	do.Provide(di, NewRuntimeProvider)
	do.Provide(di, NewSandboxNetworkManager)
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
	do.Provide(di, NewRunSupervisor)
	do.Provide(di, NewProjectController)
}

func RegisterRoutes(di do.Injector) {
	app := do.MustInvoke[*echo.Echo](di)

	projectHandler := api.NewProjectHandler(projectControllerDelegate{controller: do.MustInvoke[*projects.Controller](di)}, do.MustInvoke[*configstore.ConfigStore](di))
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
	)
	path, handler = agentcomposev2connect.NewExecServiceHandler(execHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	imageHandler := api.NewImageHandler(do.MustInvoke[*adapters.ImageBackends](di))
	path, handler = agentcomposev2connect.NewImageServiceHandler(imageHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	cacheHandler := api.NewCacheHandler(do.MustInvoke[*runtimecache.Controller](di))
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
	).WithRunTargetResolver(do.MustInvoke[*runs.SandboxRunTargetResolver](di))
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

func StartBackground(di do.Injector) error {
	if err := syncLegacyDefaultProject(do.MustInvoke[context.Context](di), do.MustInvoke[*projects.Controller](di)); err != nil {
		slog.Warn("failed to sync legacy v1 agents into the default project", "error", err)
	}
	return startBackgroundManagers(
		do.MustInvoke[context.Context](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.SandboxRPCBridge](di),
		do.MustInvoke[*loaders.Controller](di),
		do.MustInvoke[*events.Dispatcher](di),
		do.MustInvoke[*capproxy.Server](di),
		do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
	)
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

func NewCacheController(di do.Injector) (*runtimecache.Controller, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	_ = do.MustInvoke[*sessionstore.Store](di)
	_ = do.MustInvoke[*configstore.ConfigStore](di)

	imageCacheRoot := strings.TrimSpace(config.ImageCacheRoot)
	if imageCacheRoot == "" {
		imageCacheRoot = filepath.Join(config.DataRoot, "images")
		config.ImageCacheRoot = imageCacheRoot
	}
	cache, err := imagecache.New(imagecache.Config{
		Root:               imageCacheRoot,
		DefaultRegistry:    config.ImageRegistry,
		InsecureRegistries: config.ImageInsecureRegistries,
	})
	if err != nil {
		return nil, err
	}
	config.ImageCacheRoot = cache.Root()

	sources := []runtimecache.Source{
		runtimecache.MaterializedSource{
			Scanner: runtimecache.MaterializedScanner{Cache: cache},
			Remover: runtimecache.MaterializedRemover{Cache: cache},
		},
	}
	sources = append(sources, driver.NewRuntimeCacheSources(config)...)
	return &runtimecache.Controller{Sources: sources}, nil
}

func NewResourceLocator(di do.Injector) (*resources.Locator, error) {
	backends := do.MustInvoke[*adapters.ImageBackends](di)
	return resources.NewLocator(
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*sessionstore.Store](di),
		backends.Auto,
		do.MustInvoke[*runtimecache.Controller](di),
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
	driver := adapters.NewSandboxDriver(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
	)
	driver.NetworkPreparer = do.MustInvoke[*networks.Manager](di)
	return driver, nil
}

func NewSandboxNetworkManager(di do.Injector) (*networks.Manager, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	return &networks.Manager{
		Infrastructure: adapters.NewDockerNetworkInfrastructure(),
		Ports: adapters.StorePortAllocator{
			Store: do.MustInvoke[*sessionstore.Store](di),
		},
		DockerPublishAddress:  config.NetworkDockerPublishAddress,
		RuntimePublishAddress: config.NetworkRuntimePublishAddress,
	}, nil
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
	), nil
}

func NewConfigStore(di do.Injector) (*configstore.ConfigStore, error) {
	return configstore.NewConfigStore(di)
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
		Client: &http.Client{Timeout: config.LLMTimeout},
	})
}

func registerWebhookRoutes(app *echo.Echo, di do.Injector) {
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{
		Store:            do.MustInvoke[*configstore.ConfigStore](di),
		WebhookBodyLimit: do.MustInvoke[*appconfig.Config](di).WebhookBodyLimitBytes,
	})
}
