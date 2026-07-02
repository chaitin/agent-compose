package httpapi

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/labstack/echo/v4"

	agentworkspace "agent-compose/internal/agentcompose/workspace"
)

type WorkspaceConfigStore interface {
	GetWorkspaceConfig(ctx context.Context, workspaceID string) (agentworkspace.Config, error)
}

type FileWorkspaceOpener func(ctx context.Context, workspace agentworkspace.Config) (agentworkspace.FileContent, error)

type WorkspaceAPI struct {
	Store            WorkspaceConfigStore
	OpenFileContent  FileWorkspaceOpener
	UploadLimitBytes int64
}

type WorkspaceFilesResponse struct {
	WorkspaceID string                     `json:"workspace_id"`
	Files       []agentworkspace.FileEntry `json:"files"`
}

func (h WorkspaceAPI) HandleListFiles(c echo.Context) error {
	workspace, content, err := h.loadFileWorkspaceConfig(c.Request().Context(), c.Param("workspaceID"))
	if err != nil {
		return toWorkspaceHTTPError(err)
	}
	defer func() { _ = content.Root.Close() }()
	files, err := agentworkspace.ListFiles(content.Root)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, WorkspaceFilesResponse{WorkspaceID: workspace.ID, Files: files})
}

func (h WorkspaceAPI) HandleUpload(c echo.Context) error {
	limit := h.UploadLimitBytes
	if limit > 0 {
		if c.Request().ContentLength > limit {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "workspace upload exceeds configured limit")
		}
		c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, limit)
	}
	_, content, err := h.loadFileWorkspaceConfig(c.Request().Context(), c.Param("workspaceID"))
	if err != nil {
		return toWorkspaceHTTPError(err)
	}
	defer func() { _ = content.Root.Close() }()
	fileHeader, err := c.FormFile("file")
	if err != nil {
		if strings.Contains(err.Error(), "http: request body too large") {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "workspace upload exceeds configured limit")
		}
		return echo.NewHTTPError(http.StatusBadRequest, "missing form file \"file\"")
	}
	uploadType := strings.ToLower(strings.TrimSpace(c.FormValue("upload_type")))
	targetPath := strings.TrimSpace(c.FormValue("path"))
	switch uploadType {
	case "", "file":
		if err := storeUploadedWorkspaceFile(fileHeader, content.Root, targetPath); err != nil {
			return toWorkspaceUploadHTTPError(err)
		}
	case "archive":
		if err := extractUploadedWorkspaceArchive(fileHeader, content.Root); err != nil {
			return toWorkspaceUploadHTTPError(err)
		}
	default:
		return echo.NewHTTPError(http.StatusBadRequest, fmt.Sprintf("unsupported upload_type %q", uploadType))
	}
	files, err := agentworkspace.ListFiles(content.Root)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	return c.JSON(http.StatusOK, WorkspaceFilesResponse{WorkspaceID: c.Param("workspaceID"), Files: files})
}

func (h WorkspaceAPI) HandleDownload(c echo.Context) error {
	_, content, err := h.loadFileWorkspaceConfig(c.Request().Context(), c.Param("workspaceID"))
	if err != nil {
		return toWorkspaceHTTPError(err)
	}
	defer func() { _ = content.Root.Close() }()
	relPath, err := agentworkspace.CleanRelativePath(c.QueryParam("path"), false)
	if err != nil {
		return echo.NewHTTPError(http.StatusBadRequest, err.Error())
	}
	relPath = filepath.ToSlash(relPath)
	info, err := content.Root.Lstat(relPath)
	if err != nil {
		if os.IsNotExist(err) {
			return echo.NewHTTPError(http.StatusNotFound, err.Error())
		}
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return echo.NewHTTPError(http.StatusBadRequest, "download path must not be a symlink")
	}
	if info.IsDir() {
		return echo.NewHTTPError(http.StatusBadRequest, "download path must be a file")
	}
	file, err := content.Root.Open(relPath)
	if err != nil {
		return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
	}
	defer func() { _ = file.Close() }()
	c.Response().Header().Set(echo.HeaderContentDisposition, fmt.Sprintf("attachment; filename=%q", filepath.Base(relPath)))
	c.Response().Header().Set(echo.HeaderContentType, "application/octet-stream")
	return c.Stream(http.StatusOK, "application/octet-stream", file)
}

func (h WorkspaceAPI) loadFileWorkspaceConfig(ctx context.Context, workspaceID string) (agentworkspace.Config, agentworkspace.FileContent, error) {
	if h.Store == nil {
		return agentworkspace.Config{}, agentworkspace.FileContent{}, fmt.Errorf("workspace store is not configured")
	}
	workspace, err := h.Store.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return agentworkspace.Config{}, agentworkspace.FileContent{}, err
	}
	if strings.ToLower(strings.TrimSpace(workspace.Type)) != "file" {
		return agentworkspace.Config{}, agentworkspace.FileContent{}, fmt.Errorf("workspace config %s is not a file workspace", workspace.ID)
	}
	if h.OpenFileContent == nil {
		return agentworkspace.Config{}, agentworkspace.FileContent{}, fmt.Errorf("file workspace opener is not configured")
	}
	content, err := h.OpenFileContent(ctx, workspace)
	if err != nil {
		return agentworkspace.Config{}, agentworkspace.FileContent{}, err
	}
	return workspace, content, nil
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
	cleanTarget, err := agentworkspace.CleanRelativePath(targetPath, false)
	if err != nil {
		return err
	}
	src, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("open uploaded file: %w", err)
	}
	defer func() { _ = src.Close() }()
	cleanTarget = filepath.ToSlash(cleanTarget)
	if err := agentworkspace.EnsureRootParentDir(contentRoot, cleanTarget); err != nil {
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
	return agentworkspace.ExtractTarArchive(src, contentRoot)
}
