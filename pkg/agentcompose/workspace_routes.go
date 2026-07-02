package agentcompose

import (
	"github.com/labstack/echo/v4"

	"agent-compose/pkg/workspaces"
)

func registerWorkspaceRoutes(app *echo.Echo, service *Service) {
	workspaces.RegisterRoutes(app, workspaces.NewService(service.config, service.configDB))
}
