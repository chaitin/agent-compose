package agentcompose

import (
	appconfig "agent-compose/internal/config"
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"

	"agent-compose/internal/agentcompose/transport/httpapi"
)

type workspaceFilesResponse struct {
	WorkspaceID string               `json:"workspace_id"`
	Files       []workspaceFileEntry `json:"files"`
}

func registerWorkspaceRoutes(app *echo.Echo, service *Service) {
	httpapi.RegisterWorkspaceRoutes(app, httpapi.WorkspaceHandlers{
		HandleListFiles: service.handleWorkspaceListFiles,
		HandleUpload:    service.handleWorkspaceUpload,
		HandleDownload:  service.handleWorkspaceDownload,
	})
}

func (s *Service) handleWorkspaceListFiles(c echo.Context) error {
	return s.workspaceHTTPAPI().HandleListFiles(c)
}

func (s *Service) handleWorkspaceUpload(c echo.Context) error {
	return s.workspaceHTTPAPI().HandleUpload(c)
}

func (s *Service) handleWorkspaceDownload(c echo.Context) error {
	return s.workspaceHTTPAPI().HandleDownload(c)
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

func toWorkspaceUploadHTTPError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "http: request body too large") {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "workspace upload exceeds configured limit")
	}
	return echo.NewHTTPError(http.StatusBadRequest, err.Error())
}

func (s *Service) loadFileWorkspaceConfig(ctx context.Context, workspaceID string) (WorkspaceConfig, fileWorkspaceContent, error) {
	workspace, err := s.configDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return WorkspaceConfig{}, fileWorkspaceContent{}, err
	}
	if strings.ToLower(strings.TrimSpace(workspace.Type)) != "file" {
		return WorkspaceConfig{}, fileWorkspaceContent{}, fmt.Errorf("workspace config %s is not a file workspace", workspace.ID)
	}
	content, err := openFileWorkspaceContent(s.config, workspace)
	if err != nil {
		return WorkspaceConfig{}, fileWorkspaceContent{}, err
	}
	return workspace, content, nil
}

func toWorkspaceHTTPError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "not found"):
		return echo.NewHTTPError(http.StatusNotFound, message)
	case strings.Contains(message, "not a file workspace"), strings.Contains(message, "invalid"), strings.Contains(message, "missing"):
		return echo.NewHTTPError(http.StatusBadRequest, message)
	default:
		return echo.NewHTTPError(http.StatusInternalServerError, message)
	}
}

func storeUploadedWorkspaceFile(fileHeader *multipart.FileHeader, contentRoot *os.Root, targetPath string) error {
	if targetPath == "" {
		targetPath = fileHeader.Filename
	}
	cleanTarget, err := cleanWorkspaceRelativePath(targetPath, false)
	if err != nil {
		return err
	}
	src, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("open uploaded file: %w", err)
	}
	defer func() { _ = src.Close() }()
	cleanTarget = filepath.ToSlash(cleanTarget)
	if err := ensureRootParentDir(contentRoot, cleanTarget); err != nil {
		return fmt.Errorf("create upload target parent: %w", err)
	}
	if err := contentRoot.RemoveAll(cleanTarget); err != nil {
		return fmt.Errorf("remove upload target file: %w", err)
	}
	dst, err := contentRoot.OpenFile(cleanTarget, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return fmt.Errorf("create upload target file: %w", err)
	}
	defer func() { _ = dst.Close() }()
	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("write upload target file: %w", err)
	}
	return nil
}

func extractUploadedWorkspaceArchive(fileHeader *multipart.FileHeader, contentRoot *os.Root) error {
	src, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("open uploaded archive: %w", err)
	}
	defer func() { _ = src.Close() }()
	return extractWorkspaceTarArchive(src, contentRoot)
}
