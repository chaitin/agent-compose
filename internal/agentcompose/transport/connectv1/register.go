package connectv1

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
)

type Service interface {
	agentcomposev1connect.SessionServiceHandler
	agentcomposev1connect.KernelServiceHandler
	agentcomposev1connect.AgentServiceHandler
	agentcomposev1connect.AgentDefinitionServiceHandler
	agentcomposev1connect.LLMServiceHandler
	agentcomposev1connect.ConfigServiceHandler
	agentcomposev1connect.LoaderServiceHandler
	agentcomposev1connect.DashboardServiceHandler
	agentcomposev1connect.CapabilityServiceHandler
}

func RegisterHandlers(app *echo.Echo, service Service) {
	registerConnectHandler(app, agentcomposev1connect.NewSessionServiceHandler(NewSessionHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewKernelServiceHandler(NewKernelHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewAgentServiceHandler(NewAgentHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewAgentDefinitionServiceHandler(NewAgentDefinitionHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewLLMServiceHandler(NewLLMHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewConfigServiceHandler(NewConfigHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewLoaderServiceHandler(NewLoaderHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewDashboardServiceHandler(NewDashboardHandler(service)))
	registerConnectHandler(app, agentcomposev1connect.NewCapabilityServiceHandler(NewCapabilityHandler(service)))
}

func registerConnectHandler(app *echo.Echo, path string, handler http.Handler) {
	app.Any(path+"*", echo.WrapHandler(handler))
}
