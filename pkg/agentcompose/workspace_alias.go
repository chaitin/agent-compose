package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"os"

	"github.com/labstack/echo/v4"

	"agent-compose/pkg/workspaces"
)

type WorkspaceService = workspaces.Service
type fileWorkspaceContent = workspaces.FileWorkspaceContent
type gitWorkspaceConfig = workspaces.GitWorkspaceConfig

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session) error {
	return workspaces.PrepareSessionWorkspace(ctx, config, configDB, session)
}

func validateFileWorkspaceConfig(config *appconfig.Config, workspaceID, configJSON string) (string, error) {
	return workspaces.ValidateFileWorkspaceConfig(config, workspaceID, configJSON)
}

func defaultFileWorkspaceConfigJSON(config *appconfig.Config, workspaceID string) string {
	return workspaces.DefaultFileWorkspaceConfigJSON(config, workspaceID)
}

func fileWorkspaceContentRoot(config *appconfig.Config, workspace WorkspaceConfig) (string, error) {
	return workspaces.FileWorkspaceContentRoot(config, workspace)
}

func openFileWorkspaceContent(config *appconfig.Config, workspace WorkspaceConfig) (fileWorkspaceContent, error) {
	return workspaces.OpenFileWorkspaceContent(config, workspace)
}

func fileWorkspaceContentRelRoot(workspaceID string) (string, error) {
	return workspaces.FileWorkspaceContentRelRoot(workspaceID)
}

func openFileWorkspaceDataRoot(config *appconfig.Config) (*os.Root, error) {
	return workspaces.OpenFileWorkspaceDataRoot(config)
}

func normalizeGitCloneTarget(workspaceID, raw string) (string, error) {
	return workspaces.NormalizeGitCloneTarget(workspaceID, raw)
}

func copyRootDirectoryContents(srcRoot *os.Root, dstDir string) error {
	return workspaces.CopyRootDirectoryContents(srcRoot, dstDir)
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
