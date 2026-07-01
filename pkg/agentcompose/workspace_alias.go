package agentcompose

import (
	appconfig "agent-compose/pkg/config"

	"github.com/labstack/echo/v4"

	"agent-compose/pkg/workspaces"
)

type WorkspaceService = workspaces.Service
type gitWorkspaceConfig = workspaces.GitWorkspaceConfig

func fileWorkspaceContentRoot(config *appconfig.Config, workspace WorkspaceConfig) (string, error) {
	return workspaces.FileWorkspaceContentRoot(config, workspace)
}

func moveWorkspaceEntry(src, dst string) error {
	return workspaces.MoveWorkspaceEntry(src, dst)
}

func registerWorkspaceRoutes(app *echo.Echo, service *Service) {
	workspaces.RegisterRoutes(app, workspaces.NewService(service.config, service.configDB))
}

func toWorkspaceUploadHTTPError(err error) error {
	return workspaces.ToWorkspaceUploadHTTPError(err)
}

func toWorkspaceHTTPError(err error) error {
	return workspaces.ToWorkspaceHTTPError(err)
}
