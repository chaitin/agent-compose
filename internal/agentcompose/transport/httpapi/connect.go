package httpapi

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type ConnectService interface {
	agentcomposev1connect.SessionServiceHandler
	agentcomposev1connect.KernelServiceHandler
	agentcomposev1connect.AgentServiceHandler
	agentcomposev1connect.AgentDefinitionServiceHandler
	agentcomposev1connect.LLMServiceHandler
	agentcomposev1connect.ConfigServiceHandler
	agentcomposev1connect.LoaderServiceHandler
	agentcomposev1connect.DashboardServiceHandler
	agentcomposev1connect.CapabilityServiceHandler
	agentcomposev2connect.ProjectServiceHandler
	agentcomposev2connect.RunServiceHandler
	agentcomposev2connect.ExecServiceHandler
	agentcomposev2connect.ImageServiceHandler
}

func RegisterConnectHandlers(app *echo.Echo, service ConnectService) {
	registerConnectHandler(app, agentcomposev1connect.NewSessionServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewKernelServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewAgentServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewAgentDefinitionServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewLLMServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewConfigServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewLoaderServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewDashboardServiceHandler(service))
	registerConnectHandler(app, agentcomposev1connect.NewCapabilityServiceHandler(service))

	registerConnectHandler(app, agentcomposev2connect.NewProjectServiceHandler(service))
	registerConnectHandler(app, agentcomposev2connect.NewRunServiceHandler(service))
	registerConnectHandler(app, agentcomposev2connect.NewExecServiceHandler(service))
	registerConnectHandler(app, agentcomposev2connect.NewImageServiceHandler(service))
}

func registerConnectHandler(app *echo.Echo, path string, handler http.Handler) {
	app.Any(path+"*", echo.WrapHandler(handler))
}
