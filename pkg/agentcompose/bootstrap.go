package agentcompose

import (
	"context"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	"agent-compose/internal/agentcompose/bootstrap"
	"agent-compose/pkg/capproxy"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
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
	do.Provide(di, NewStore)
	do.Provide(di, NewConfigStore)
	do.Provide(di, NewRuntimeProvider)
	do.Provide(di, NewDriver)
	do.Provide(di, NewExecutor)
	do.Provide(di, NewLLMClient)
	do.Provide(di, NewCapabilityProvider)
	do.Provide(di, NewCapProxyServer)
	do.Provide(di, NewLoaderBus)
	do.Provide(di, NewSessionStreamBroker)
	do.Provide(di, NewDashboardOverviewAggregator)
	do.Provide(di, NewDashboardOverviewHub)
	do.Provide(di, NewLoaderEngine)
	do.Provide(di, NewSessionRPCBridge)
	do.Provide(di, NewLoaderManager)
	do.Provide(di, NewService)

	app := do.MustInvoke[*echo.Echo](di)
	service := do.MustInvoke[*Service](di)

	path, handler := agentcomposev1connect.NewSessionServiceHandler(NewSessionHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewKernelServiceHandler(NewKernelHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewAgentServiceHandler(NewAgentHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewAgentDefinitionServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewLLMServiceHandler(NewLLMHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewConfigServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewLoaderServiceHandler(NewLoaderHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewDashboardServiceHandler(NewDashboardHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev1connect.NewCapabilityServiceHandler(NewCapabilityHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))

	path, handler = agentcomposev2connect.NewProjectServiceHandler(service)
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewRunServiceHandler(NewRunHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewExecServiceHandler(NewExecHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))
	path, handler = agentcomposev2connect.NewImageServiceHandler(NewImageHandler(service))
	app.Any(path+"*", echo.WrapHandler(handler))

	registerWebhookRoutes(app, service)
	registerRuntimeLLMFacadeRoutes(app, service)
	registerProxyRoutes(app, service)
	registerWorkspaceRoutes(app, service)
}

func startBackground(di do.Injector) error {
	service := do.MustInvoke[*Service](di)
	return service.StartBackground(do.MustInvoke[context.Context](di), do.MustInvoke[*capproxy.Server](di))
}
