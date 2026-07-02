package agentcompose

import (
	"context"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	appshell "agent-compose/internal/agentcompose/app"
	"agent-compose/internal/agentcompose/bootstrap"
	"agent-compose/internal/agentcompose/transport/connectv1"
	"agent-compose/internal/agentcompose/transport/connectv2"
	"agent-compose/internal/capproxy"
)

func init() {
	bootstrap.Configure(bootstrap.Hooks{
		Register:        register,
		StartBackground: startBackground,
	})
}

func bootstrapSetup(di do.Injector) {
	bootstrap.Setup(di)
}

func bootstrapRegister(di do.Injector) {
	bootstrap.Register(di)
}

func bootstrapStartBackground(di do.Injector) error {
	return bootstrap.StartBackground(di)
}

func register(di do.Injector) {
	appshell.Register(di, appshell.Providers[
		*Store,
		*ConfigStore,
		RuntimeProvider,
		Driver,
		*Executor,
		*LLMClient,
		capabilityIntegration,
		*capproxy.Server,
		*LoaderBus,
		*SessionStreamBroker,
		*DashboardOverviewAggregator,
		*DashboardOverviewHub,
		LoaderEngine,
		*SessionRPCBridge,
		*LoaderManager,
		*Service,
	]{
		Store:                       NewStore,
		ConfigStore:                 NewConfigStore,
		RuntimeProvider:             NewRuntimeProvider,
		Driver:                      NewDriver,
		Executor:                    NewExecutor,
		LLMClient:                   NewLLMClient,
		CapabilityProvider:          NewCapabilityProvider,
		CapProxy:                    NewCapProxyServer,
		LoaderBus:                   NewLoaderBus,
		SessionStreamBroker:         NewSessionStreamBroker,
		DashboardOverviewAggregator: NewDashboardOverviewAggregator,
		DashboardOverviewHub:        NewDashboardOverviewHub,
		LoaderEngine:                NewLoaderEngine,
		SessionRPCBridge:            NewSessionRPCBridge,
		LoaderManager:               NewLoaderManager,
		Service:                     NewService,
		RegisterRoutes:              registerRoutes,
	})
}

func registerRoutes(app *echo.Echo, service *Service) {
	connectv1.RegisterHandlers(app, service)
	connectv2.RegisterHandlers(app, service)
	registerWebhookRoutes(app, service)
	registerRuntimeLLMFacadeRoutes(app, service)
	registerProxyRoutes(app, service)
	registerWorkspaceRoutes(app, service)
}

func startBackground(di do.Injector) error {
	return appshell.StartBackground(di, appshell.BackgroundOptions[*Service, *capproxy.Server]{
		Start: func(service *Service, ctx context.Context, capProxy *capproxy.Server) error {
			return service.StartBackground(ctx, capProxy)
		},
	})
}
