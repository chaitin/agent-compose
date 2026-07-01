package workspaces

import (
	"context"
	"os"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
	"github.com/labstack/echo/v4"
)

type Session = model.Session
type SessionSummary = model.SessionSummary
type SessionWorkspace = model.SessionWorkspace
type WorkspaceConfig = model.WorkspaceConfig
type ConfigStore = storage.ConfigStore

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *Session) error {
	return PrepareSessionWorkspace(ctx, config, configDB, session)
}

func defaultFileWorkspaceConfigJSON(config *appconfig.Config, workspaceID string) string {
	return DefaultFileWorkspaceConfigJSON(config, workspaceID)
}

func fileWorkspaceContentRoot(config *appconfig.Config, workspace WorkspaceConfig) (string, error) {
	return FileWorkspaceContentRoot(config, workspace)
}

func openFileWorkspaceContent(config *appconfig.Config, workspace WorkspaceConfig) (FileWorkspaceContent, error) {
	return OpenFileWorkspaceContent(config, workspace)
}

func fileWorkspaceContentRelRoot(workspaceID string) (string, error) {
	return FileWorkspaceContentRelRoot(workspaceID)
}

func normalizeGitCloneTarget(workspaceID, raw string) (string, error) {
	return NormalizeGitCloneTarget(workspaceID, raw)
}

func copyRootDirectoryContents(srcRoot *os.Root, dstDir string) error {
	return CopyRootDirectoryContents(srcRoot, dstDir)
}

func registerWorkspaceRoutes(app *echo.Echo, service *Service) {
	RegisterRoutes(app, service)
}
