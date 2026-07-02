package connectv2

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type Service interface {
	agentcomposev2connect.ProjectServiceHandler
	agentcomposev2connect.RunServiceHandler
	agentcomposev2connect.ExecServiceHandler
	agentcomposev2connect.ImageServiceHandler
}

func RegisterHandlers(app *echo.Echo, service Service) {
	registerConnectHandler(app, agentcomposev2connect.NewProjectServiceHandler(NewProjectHandler(service)))
	registerConnectHandler(app, agentcomposev2connect.NewRunServiceHandler(NewRunHandler(service)))
	registerConnectHandler(app, agentcomposev2connect.NewExecServiceHandler(NewExecHandler(service)))
	registerConnectHandler(app, agentcomposev2connect.NewImageServiceHandler(NewImageHandler(service)))
}

func registerConnectHandler(app *echo.Echo, path string, handler http.Handler) {
	app.Any(path+"*", echo.WrapHandler(handler))
}
