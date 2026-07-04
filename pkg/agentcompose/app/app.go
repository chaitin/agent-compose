package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
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
	"agent-compose/pkg/events"
	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
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
	do.Provide(di, NewRuntimeProvider)
	do.Provide(di, NewLLMClient)
	do.Provide(di, NewCapabilityProvider)
	do.Provide(di, NewImageBackends)
	do.Provide(di, NewCapProxyServer)
	do.Provide(di, loaders.NewBus)
	do.Provide(di, sessions.NewStreamBroker)
	do.Provide(di, NewEventDispatcher)
	do.Provide(di, NewDashboardOverviewAggregator)
	do.Provide(di, NewDashboardOverviewHub)
	do.Provide(di, loaders.NewLoaderEngine)
	do.Provide(di, NewSessionDriver)
	do.Provide(di, NewCellExecutor)
	do.Provide(di, NewAgentRunner)
	do.Provide(di, NewAgentExecutor)
	do.Provide(di, NewLoaderCommandExecutor)
	do.Provide(di, NewLoaderSessionRunner)
	do.Provide(di, NewSessionRPCBridge)
	do.Provide(di, NewLoaderController)
	do.Provide(di, NewRunController)
	do.Provide(di, NewRunSupervisor)
	do.Provide(di, NewProjectController)
}

func RegisterRoutes(di do.Injector) {
	app := do.MustInvoke[*echo.Echo](di)

	sessionHandler := api.NewSessionHandler(
		do.MustInvoke[*adapters.SessionRPCBridge](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*sessions.StreamBroker](di),
	)
	path, handler := agentcomposev1connect.NewSessionServiceHandler(sessionHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	kernelHandler := api.NewKernelHandler(
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*adapters.CellExecutor](di),
		do.MustInvoke[*loaders.Bus](di),
	)
	path, handler = agentcomposev1connect.NewKernelServiceHandler(kernelHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	agentHandler := api.NewAgentHandler(
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.AgentExecutor](di),
		do.MustInvoke[*loaders.Bus](di),
	)
	path, handler = agentcomposev1connect.NewAgentServiceHandler(agentHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	agentDefinitionHandler := api.NewAgentDefinitionHandler(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.SessionRPCBridge](di),
		do.MustInvoke[*sessions.StreamBroker](di),
	)
	path, handler = agentcomposev1connect.NewAgentDefinitionServiceHandler(agentDefinitionHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	llmHandler := api.NewLLMHandler(do.MustInvoke[*adapters.LLMClient](di))
	path, handler = agentcomposev1connect.NewLLMServiceHandler(llmHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	configHandler := api.NewConfigHandler(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*configstore.ConfigStore](di))
	path, handler = agentcomposev1connect.NewConfigServiceHandler(configHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	loaderHandler := api.NewLoaderHandler(do.MustInvoke[*loaders.Controller](di), do.MustInvoke[*configstore.ConfigStore](di))
	path, handler = agentcomposev1connect.NewLoaderServiceHandler(loaderHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	dashboardHandler := api.NewDashboardHandler(do.MustInvoke[*dashboard.Hub](di))
	path, handler = agentcomposev1connect.NewDashboardServiceHandler(dashboardHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	capabilityHandler := api.NewCapabilityHandler(
		do.MustInvoke[capabilities.Provider](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		capabilityRuntimeConfig{config: do.MustInvoke[*appconfig.Config](di)},
	)
	path, handler = agentcomposev1connect.NewCapabilityServiceHandler(capabilityHandler)
	app.Any(path+"*", echo.WrapHandler(handler))

	projectHandler := api.NewProjectHandler(projectControllerDelegate{controller: do.MustInvoke[*projects.Controller](di)}, do.MustInvoke[*configstore.ConfigStore](di))
	path, handler = agentcomposev2connect.NewProjectServiceHandler(projectHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	runDelegate := runControllerDelegate{
		controller: do.MustInvoke[*runs.Controller](di),
		supervisor: do.MustInvoke[*RunSupervisor](di),
	}
	runHandler := api.NewRunHandler(runDelegate, do.MustInvoke[*configstore.ConfigStore](di), do.MustInvoke[*RunSupervisor](di))
	path, handler = agentcomposev2connect.NewRunServiceHandler(runHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	execHandler := api.NewExecHandler(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		func(session *domain.Session) (api.ExecRuntime, error) {
			return do.MustInvoke[adapters.RuntimeProvider](di).ForSession(session)
		},
	)
	path, handler = agentcomposev2connect.NewExecServiceHandler(execHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	imageHandler := api.NewImageHandler(do.MustInvoke[*adapters.ImageBackends](di))
	path, handler = agentcomposev2connect.NewImageServiceHandler(imageHandler)
	app.Any(path+"*", echo.WrapHandler(handler))
	sandboxHandler := api.NewSandboxHandler(
		do.MustInvoke[*adapters.SessionRPCBridge](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*dashboard.Hub](di),
		func(session *domain.Session) (api.SandboxStatsRuntime, error) {
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
	)
	path, handler = agentcomposev2connect.NewSandboxServiceHandler(sandboxHandler)
	app.Any(path+"*", echo.WrapHandler(handler))

	registerProxyRoutes(app, di)
	registerWorkspaceRoutes(app, di)
	registerRuntimeLLMFacadeRoutes(app, di)
	registerWebhookRoutes(app, di)
}

func StartBackground(di do.Injector) error {
	return startBackgroundManagers(
		do.MustInvoke[context.Context](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.SessionRPCBridge](di),
		do.MustInvoke[*loaders.Controller](di),
		do.MustInvoke[*events.Dispatcher](di),
		do.MustInvoke[*capproxy.Server](di),
	)
}

func NewCapProxyServer(di do.Injector) (*capproxy.Server, error) {
	return adapters.NewCapProxyServer(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		adapters.NewCapabilitySessionResolver(do.MustInvoke[*sessionstore.Store](di)),
	), nil
}

func NewImageBackends(di do.Injector) (*adapters.ImageBackends, error) {
	return adapters.NewImageBackends(do.MustInvoke[*appconfig.Config](di))
}

func NewRuntimeProvider(di do.Injector) (adapters.RuntimeProvider, error) {
	return adapters.NewRuntimeProvider(do.MustInvoke[*appconfig.Config](di))
}

func NewLLMClient(di do.Injector) (*adapters.LLMClient, error) {
	return adapters.NewLLMClient(do.MustInvoke[*appconfig.Config](di), do.MustInvoke[*configstore.ConfigStore](di)), nil
}

func NewSessionDriver(di do.Injector) (*adapters.SessionDriver, error) {
	return adapters.NewSessionDriver(
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

func NewLoaderSessionRunner(di do.Injector) (*adapters.LoaderSessionRunner, error) {
	return adapters.NewLoaderSessionRunner(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.SessionDriver](di),
		do.MustInvoke[capabilities.Provider](di),
		do.MustInvoke[*sessions.StreamBroker](di),
		do.MustInvoke[*loaders.Bus](di),
	), nil
}

func NewSessionRPCBridge(di do.Injector) (*adapters.SessionRPCBridge, error) {
	dashboard, _ := do.Invoke[*dashboard.Hub](di)
	return adapters.NewSessionRPCBridge(
		do.MustInvoke[*appconfig.Config](di),
		do.MustInvoke[*sessionstore.Store](di),
		do.MustInvoke[*configstore.ConfigStore](di),
		do.MustInvoke[*adapters.SessionDriver](di),
		do.MustInvoke[adapters.RuntimeProvider](di),
		do.MustInvoke[*loaders.Bus](di),
		do.MustInvoke[*sessions.StreamBroker](di),
		do.MustInvoke[capabilities.Provider](di),
		dashboard,
	), nil
}

func NewConfigStore(di do.Injector) (*configstore.ConfigStore, error) {
	return configstore.NewConfigStore(di)
}

func NewCapabilityProvider(di do.Injector) (capabilities.Provider, error) {
	conf := do.MustInvoke[*appconfig.Config](di)
	return adapters.NewCapabilityProvider(do.MustInvoke[*configstore.ConfigStore](di), conf.CapGRPCTarget), nil
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

func registerProxyRoutes(app *echo.Echo, di do.Injector) {
	sessions := do.MustInvoke[*adapters.SessionRPCBridge](di)
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
		Tokens:   configDB,
		Sessions: do.MustInvoke[*sessionstore.Store](di),
		ResolveTarget: func(ctx context.Context, requestedModel, providerID string) (llms.ResolvedTarget, error) {
			return configstore.ResolveRuntimeLLMTarget(ctx, config, configDB, requestedModel, providerID)
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
