package agentcompose

import (
	"context"

	"github.com/labstack/echo/v4"

	"agent-compose/internal/agentcompose/transport/httpapi"
	appconfig "agent-compose/internal/config"
)

func registerWorkspaceRoutes(app *echo.Echo, service *Service) {
	api := service.workspaceHTTPAPI()
	httpapi.RegisterWorkspaceRoutes(app, httpapi.WorkspaceHandlers{
		HandleListFiles: api.HandleListFiles,
		HandleUpload:    api.HandleUpload,
		HandleDownload:  api.HandleDownload,
	})
}

func (s *Service) workspaceHTTPAPI() httpapi.WorkspaceAPI {
	limit := s.config.WorkspaceUploadLimitBytes
	if limit <= 0 {
		limit = appconfig.DefaultWorkspaceUploadLimitBytes
	}
	return httpapi.WorkspaceAPI{
		Store:            s.configDB,
		UploadLimitBytes: limit,
		OpenFileContent: func(ctx context.Context, workspace WorkspaceConfig) (fileWorkspaceContent, error) {
			return openFileWorkspaceContent(s.config, workspace)
		},
	}
}
