package workspaces

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
	"google.golang.org/protobuf/types/known/emptypb"

	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type workspaceFileEntry struct {
	Path      string `json:"path"`
	Dir       bool   `json:"dir"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

type workspaceFilesResponse struct {
	WorkspaceID string               `json:"workspace_id"`
	Files       []workspaceFileEntry `json:"files"`
}

type Service struct {
	config   *appconfig.Config
	configDB *storage.ConfigStore
}

func NewService(config *appconfig.Config, configDB *storage.ConfigStore) *Service {
	return &Service{config: config, configDB: configDB}
}

func RegisterRoutes(app *echo.Echo, service *Service) {
	base := "/api/agent-compose/workspaces"
	app.GET(base+"/:workspaceID/files", func(c echo.Context) error {
		workspace, content, err := service.loadFileWorkspaceConfig(c.Request().Context(), c.Param("workspaceID"))
		if err != nil {
			return toWorkspaceHTTPError(err)
		}
		defer func() { _ = content.Root.Close() }()
		files, err := listWorkspaceFiles(content.Root)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, workspaceFilesResponse{WorkspaceID: workspace.ID, Files: files})
	})
	app.POST(base+"/:workspaceID/upload", func(c echo.Context) error {
		limit := service.config.WorkspaceUploadLimitBytes
		if limit <= 0 {
			limit = appconfig.DefaultWorkspaceUploadLimitBytes
		}
		if c.Request().ContentLength > limit {
			return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "workspace upload exceeds configured limit")
		}
		c.Request().Body = http.MaxBytesReader(c.Response(), c.Request().Body, limit)
		_, content, err := service.loadFileWorkspaceConfig(c.Request().Context(), c.Param("workspaceID"))
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
		files, err := listWorkspaceFiles(content.Root)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, err.Error())
		}
		return c.JSON(http.StatusOK, workspaceFilesResponse{WorkspaceID: c.Param("workspaceID"), Files: files})
	})
	app.GET(base+"/:workspaceID/download", func(c echo.Context) error {
		_, content, err := service.loadFileWorkspaceConfig(c.Request().Context(), c.Param("workspaceID"))
		if err != nil {
			return toWorkspaceHTTPError(err)
		}
		defer func() { _ = content.Root.Close() }()
		relPath, err := cleanWorkspaceRelativePath(c.QueryParam("path"), false)
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
	})
}

func toWorkspaceUploadHTTPError(err error) error {
	return ToWorkspaceUploadHTTPError(err)
}

func ToWorkspaceUploadHTTPError(err error) error {
	if err == nil {
		return nil
	}
	if strings.Contains(err.Error(), "http: request body too large") {
		return echo.NewHTTPError(http.StatusRequestEntityTooLarge, "workspace upload exceeds configured limit")
	}
	return echo.NewHTTPError(http.StatusBadRequest, err.Error())
}

func (s *Service) loadFileWorkspaceConfig(ctx context.Context, workspaceID string) (model.WorkspaceConfig, fileWorkspaceContent, error) {
	workspace, err := s.configDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return model.WorkspaceConfig{}, fileWorkspaceContent{}, err
	}
	if strings.ToLower(strings.TrimSpace(workspace.Type)) != "file" {
		return model.WorkspaceConfig{}, fileWorkspaceContent{}, fmt.Errorf("workspace config %s is not a file workspace", workspace.ID)
	}
	content, err := openFileWorkspaceContent(s.config, workspace)
	if err != nil {
		return model.WorkspaceConfig{}, fileWorkspaceContent{}, err
	}
	return workspace, content, nil
}

func (s *Service) ListWorkspaceConfigs(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[agentcomposev1.ListWorkspaceConfigsResponse], error) {
	_ = req
	items, err := s.configDB.ListWorkspaceConfigs(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &agentcomposev1.ListWorkspaceConfigsResponse{}
	for _, item := range items {
		resp.Workspaces = append(resp.Workspaces, toProtoWorkspaceConfig(item))
	}
	return connect.NewResponse(resp), nil
}

func (s *Service) CreateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.CreateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	configJSON := strings.TrimSpace(req.Msg.GetConfigJson())
	workspaceType := strings.ToLower(strings.TrimSpace(req.Msg.GetType()))
	workspaceID := ""
	if workspaceType == "file" {
		workspaceID = uuid.NewString()
		configJSON = DefaultFileWorkspaceConfigJSON(s.config, workspaceID)
		if _, err := ValidateFileWorkspaceConfig(s.config, workspaceID, configJSON); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		if err := s.checkFileWorkspaceContentCreatable(workspaceID); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	item, err := s.configDB.CreateWorkspaceConfig(ctx, model.WorkspaceConfig{
		ID:         workspaceID,
		Name:       req.Msg.GetName(),
		Type:       workspaceType,
		ConfigJSON: configJSON,
		Comment:    req.Msg.GetComment(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if workspaceType == "file" {
		if err := s.createFileWorkspaceContent(item.ID, item.ConfigJSON); err != nil {
			deleteErr := s.configDB.DeleteWorkspaceConfig(ctx, item.ID)
			if deleteErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create file workspace content: %w; rollback workspace config: %v", err, deleteErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&agentcomposev1.WorkspaceConfigResponse{Workspace: toProtoWorkspaceConfig(item)}), nil
}

func (s *Service) UpdateWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.UpdateWorkspaceConfigRequest]) (*connect.Response[agentcomposev1.WorkspaceConfigResponse], error) {
	configJSON := strings.TrimSpace(req.Msg.GetConfigJson())
	workspaceType := strings.ToLower(strings.TrimSpace(req.Msg.GetType()))
	previous, err := s.configDB.GetWorkspaceConfig(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if workspaceType == "file" {
		configJSON = DefaultFileWorkspaceConfigJSON(s.config, req.Msg.GetWorkspaceId())
		if _, err := ValidateFileWorkspaceConfig(s.config, req.Msg.GetWorkspaceId(), configJSON); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}
	wasFile := strings.EqualFold(strings.TrimSpace(previous.Type), "file")
	if workspaceType == "file" {
		if err := s.checkFileWorkspaceContentCreatable(req.Msg.GetWorkspaceId()); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	} else if wasFile {
		if err := s.checkFileWorkspaceContentRemovable(previous); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	item, err := s.configDB.UpdateWorkspaceConfig(ctx, model.WorkspaceConfig{
		ID:         req.Msg.GetWorkspaceId(),
		Name:       req.Msg.GetName(),
		Type:       workspaceType,
		ConfigJSON: configJSON,
		Comment:    req.Msg.GetComment(),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if workspaceType == "file" {
		if err := s.createFileWorkspaceContent(item.ID, item.ConfigJSON); err != nil {
			_, rollbackErr := s.configDB.UpdateWorkspaceConfig(ctx, previous)
			if rollbackErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	} else if wasFile {
		if err := s.removeFileWorkspaceContent(previous); err != nil {
			_, rollbackErr := s.configDB.UpdateWorkspaceConfig(ctx, previous)
			if rollbackErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&agentcomposev1.WorkspaceConfigResponse{Workspace: toProtoWorkspaceConfig(item)}), nil
}

func (s *Service) DeleteWorkspaceConfig(ctx context.Context, req *connect.Request[agentcomposev1.WorkspaceConfigIDRequest]) (*connect.Response[emptypb.Empty], error) {
	workspace, err := s.configDB.GetWorkspaceConfig(ctx, req.Msg.GetWorkspaceId())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		if err := s.checkFileWorkspaceContentRemovable(workspace); err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	if err := s.configDB.DeleteWorkspaceConfig(ctx, req.Msg.GetWorkspaceId()); err != nil {
		if strings.Contains(err.Error(), "referenced by") {
			return nil, connect.NewError(connect.CodeFailedPrecondition, err)
		}
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.EqualFold(strings.TrimSpace(workspace.Type), "file") {
		if err := s.removeFileWorkspaceContent(workspace); err != nil {
			_, rollbackErr := s.configDB.CreateWorkspaceConfig(ctx, workspace)
			if rollbackErr != nil {
				return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("remove file workspace content: %w; rollback workspace config: %v", err, rollbackErr))
			}
			return nil, connect.NewError(connect.CodeInternal, err)
		}
	}
	return connect.NewResponse(&emptypb.Empty{}), nil
}

func (s *Service) createFileWorkspaceContent(workspaceID, configJSON string) error {
	content, err := openFileWorkspaceContent(s.config, model.WorkspaceConfig{
		ID:         workspaceID,
		Type:       "file",
		ConfigJSON: configJSON,
	})
	if err != nil {
		return err
	}
	return content.Root.Close()
}

func (s *Service) checkFileWorkspaceContentCreatable(workspaceID string) error {
	relRoot, err := fileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return err
	}
	dataRoot, err := openFileWorkspaceDataRoot(s.config)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	for _, dir := range []string{"workspaces", filepath.ToSlash(filepath.Join("workspaces", strings.TrimSpace(workspaceID))), relRoot} {
		info, err := dataRoot.Lstat(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("file workspace path %s is a symlink", dir)
		}
		if !info.IsDir() {
			return fmt.Errorf("file workspace path %s is not a directory", dir)
		}
	}
	return nil
}

func (s *Service) checkFileWorkspaceContentRemovable(workspace model.WorkspaceConfig) error {
	dataRoot, _, err := s.fileWorkspaceContentRemovalTarget(workspace)
	if err != nil {
		return err
	}
	return dataRoot.Close()
}

func (s *Service) removeFileWorkspaceContent(workspace model.WorkspaceConfig) error {
	dataRoot, relRoot, err := s.fileWorkspaceContentRemovalTarget(workspace)
	if err != nil {
		return err
	}
	defer func() { _ = dataRoot.Close() }()
	return dataRoot.RemoveAll(relRoot)
}

func (s *Service) fileWorkspaceContentRemovalTarget(workspace model.WorkspaceConfig) (*os.Root, string, error) {
	dataRoot, err := openFileWorkspaceDataRoot(s.config)
	if err != nil {
		return nil, "", err
	}
	relRoot, err := fileWorkspaceContentRelRoot(workspace.ID)
	if err != nil {
		_ = dataRoot.Close()
		return nil, "", err
	}
	info, err := dataRoot.Lstat(relRoot)
	if err != nil && !os.IsNotExist(err) {
		_ = dataRoot.Close()
		return nil, "", err
	}
	if err == nil && info.Mode()&os.ModeSymlink != 0 {
		_ = dataRoot.Close()
		return nil, "", fmt.Errorf("file workspace content root %s is a symlink", relRoot)
	}
	return dataRoot, relRoot, nil
}

func toWorkspaceHTTPError(err error) error {
	return ToWorkspaceHTTPError(err)
}

func ToWorkspaceHTTPError(err error) error {
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

func listWorkspaceFiles(contentRoot *os.Root) ([]workspaceFileEntry, error) {
	items := make([]workspaceFileEntry, 0)
	err := fs.WalkDir(contentRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		relPath := filepath.ToSlash(path)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("workspace file %s is a symlink", relPath)
		}
		info, err := contentRoot.Lstat(relPath)
		if err != nil {
			return err
		}
		items = append(items, workspaceFileEntry{
			Path:      filepath.ToSlash(relPath),
			Dir:       entry.IsDir(),
			Size:      info.Size(),
			UpdatedAt: info.ModTime().UTC().Format(time.RFC3339Nano),
		})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list workspace files: %w", err)
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].Path < items[j].Path
	})
	return items, nil
}

func cleanWorkspaceRelativePath(raw string, allowEmpty bool) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("workspace path is required")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("workspace path %q must be relative", trimmed)
	}
	clean := filepath.Clean(trimmed)
	if clean == "." {
		if allowEmpty {
			return "", nil
		}
		return "", fmt.Errorf("workspace path is required")
	}
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("workspace path %q escapes workspace root", trimmed)
	}
	return clean, nil
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

func DefaultFileWorkspaceConfigJSON(config *appconfig.Config, workspaceID string) string {
	root, err := defaultFileWorkspaceContentRoot(config, workspaceID)
	if err != nil {
		root = filepath.Join(config.DataRoot, "workspaces", strings.TrimSpace(workspaceID), fileWorkspaceContentDirName)
	}
	payload, _ := json.Marshal(fileWorkspaceConfig{Root: root})
	return string(payload)
}

func toProtoWorkspaceConfig(item model.WorkspaceConfig) *agentcomposev1.WorkspaceConfig {
	return &agentcomposev1.WorkspaceConfig{
		Id:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJson: item.ConfigJSON,
		Comment:    item.Comment,
		CreatedAt:  item.CreatedAt.Format(time.RFC3339Nano),
		UpdatedAt:  item.UpdatedAt.Format(time.RFC3339Nano),
	}
}

func defaultFileWorkspaceContentRoot(config *appconfig.Config, workspaceID string) (string, error) {
	root := filepath.Join(config.DataRoot, "workspaces", strings.TrimSpace(workspaceID), fileWorkspaceContentDirName)
	return filepath.Abs(root)
}
