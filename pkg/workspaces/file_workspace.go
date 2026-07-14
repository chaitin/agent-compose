package workspaces

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime/multipart"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

const FileWorkspaceContentDirName = "content"

type FileWorkspaceConfig struct {
	Root string `json:"root,omitempty"`
}

type FileWorkspaceContent struct {
	AbsRoot string
	RelRoot string
	Root    *os.Root
}

type fileWorkspace struct {
	config    *appconfig.Config
	workspace domain.WorkspaceConfig
}

func PrepareFileWorkspace(config *appconfig.Config, session *domain.Sandbox, workspace domain.WorkspaceConfig) error {
	return fileWorkspace{config: config, workspace: workspace}.Prepare(context.Background(), session)
}

func (w fileWorkspace) Prepare(_ context.Context, session *domain.Sandbox) error {
	workspaceRoot := strings.TrimSpace(session.Summary.WorkspacePath)
	if workspaceRoot == "" {
		return domain.ClassifyError(domain.ErrRequired, fmt.Sprintf("session %s missing workspace path", session.Summary.ID), nil)
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return fmt.Errorf("prepare workspace %s failed: create workspace root: %w", w.workspace.Name, err)
	}
	content, err := OpenFileWorkspaceContent(w.config, w.workspace)
	if err != nil {
		return err
	}
	defer func() { _ = content.Root.Close() }()
	if err := CopyRootDirectoryContents(content.Root, workspaceRoot); err != nil {
		return fmt.Errorf("prepare workspace %s failed: copy file workspace content: %w", w.workspace.Name, err)
	}
	return nil
}

func FileWorkspaceContentRoot(config *appconfig.Config, workspace domain.WorkspaceConfig) (string, error) {
	workspaceID := strings.TrimSpace(workspace.ID)
	if workspaceID == "" {
		return "", domain.ClassifyError(domain.ErrRequired, "workspace config id is required for file workspace", nil)
	}
	var cfg FileWorkspaceConfig
	trimmedConfig := strings.TrimSpace(workspace.ConfigJSON)
	if trimmedConfig != "" && trimmedConfig != "{}" {
		if err := json.Unmarshal([]byte(trimmedConfig), &cfg); err != nil {
			return "", fmt.Errorf("decode workspace config %s: %w", workspace.ID, err)
		}
	}
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		return DefaultFileWorkspaceContentRoot(config, workspaceID)
	}
	if !filepath.IsAbs(root) {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace config %s has invalid file workspace root %q", workspace.ID, root), nil)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace config %s has invalid file workspace root %q", workspace.ID, root), err)
	}
	expectedRoot, err := DefaultFileWorkspaceContentRoot(config, workspaceID)
	if err != nil {
		return "", err
	}
	if cleanRoot != expectedRoot {
		if cleanRoot == legacyContainerFileWorkspaceContentRoot(workspaceID) {
			return expectedRoot, nil
		}
		return "", fmt.Errorf("workspace config %s has file workspace root %q, want %q", workspace.ID, cleanRoot, expectedRoot)
	}
	return cleanRoot, nil
}

func legacyContainerFileWorkspaceContentRoot(workspaceID string) string {
	return filepath.Join(string(filepath.Separator), "data", "workspaces", workspaceID, FileWorkspaceContentDirName)
}

func ValidateFileWorkspaceConfig(config *appconfig.Config, workspaceID, configJSON string) (string, error) {
	return FileWorkspaceContentRoot(config, domain.WorkspaceConfig{
		ID:         strings.TrimSpace(workspaceID),
		Type:       "file",
		ConfigJSON: configJSON,
	})
}

func OpenFileWorkspaceContent(config *appconfig.Config, workspace domain.WorkspaceConfig) (FileWorkspaceContent, error) {
	absRoot, err := FileWorkspaceContentRoot(config, workspace)
	if err != nil {
		return FileWorkspaceContent{}, err
	}
	workspaceID := strings.TrimSpace(workspace.ID)
	relRoot, err := FileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return FileWorkspaceContent{}, err
	}
	dataRoot, err := OpenFileWorkspaceDataRoot(config)
	if err != nil {
		return FileWorkspaceContent{}, err
	}
	defer func() { _ = dataRoot.Close() }()
	for _, dir := range []string{"workspaces", filepath.ToSlash(filepath.Join("workspaces", workspaceID)), relRoot} {
		if err := EnsureRootDir(dataRoot, dir); err != nil {
			return FileWorkspaceContent{}, err
		}
	}
	contentRoot, err := dataRoot.OpenRoot(relRoot)
	if err != nil {
		return FileWorkspaceContent{}, fmt.Errorf("open file workspace content root: %w", err)
	}
	return FileWorkspaceContent{AbsRoot: absRoot, RelRoot: relRoot, Root: contentRoot}, nil
}

func FileWorkspaceContentRelRoot(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return "", domain.ClassifyError(domain.ErrRequired, "workspace config id is required for file workspace", nil)
	}
	if filepath.IsAbs(workspaceID) || workspaceID == "." || workspaceID == ".." || workspaceID != filepath.Base(workspaceID) {
		return "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf("workspace config id %q is not a valid path segment", workspaceID), nil)
	}
	return filepath.ToSlash(filepath.Join("workspaces", workspaceID, FileWorkspaceContentDirName)), nil
}

func OpenFileWorkspaceDataRoot(config *appconfig.Config) (*os.Root, error) {
	dataRootPath, err := filepath.Abs(strings.TrimSpace(config.DataRoot))
	if err != nil {
		return nil, fmt.Errorf("resolve data root: %w", err)
	}
	if err := os.MkdirAll(dataRootPath, 0o755); err != nil {
		return nil, fmt.Errorf("create data root: %w", err)
	}
	info, err := os.Lstat(dataRootPath)
	if err != nil {
		return nil, fmt.Errorf("stat data root: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("data root %s is a symlink", dataRootPath)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("data root %s is not a directory", dataRootPath)
	}
	dataRoot, err := os.OpenRoot(dataRootPath)
	if err != nil {
		return nil, fmt.Errorf("open data root: %w", err)
	}
	return dataRoot, nil
}

func EnsureRootDir(root *os.Root, relPath string) error {
	cleanPath, err := CleanRelativePath(relPath, false)
	if err != nil {
		return err
	}
	cleanPath = filepath.ToSlash(cleanPath)
	info, err := root.Lstat(cleanPath)
	if err != nil {
		if os.IsNotExist(err) {
			if err := root.Mkdir(cleanPath, 0o755); err != nil && !os.IsExist(err) {
				return fmt.Errorf("create root directory %s: %w", cleanPath, err)
			}
			info, err = root.Lstat(cleanPath)
			if err != nil {
				return fmt.Errorf("stat created root directory %s: %w", cleanPath, err)
			}
		} else {
			return fmt.Errorf("stat root directory %s: %w", cleanPath, err)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("root directory %s is a symlink", cleanPath)
	}
	if !info.IsDir() {
		return fmt.Errorf("root path %s is not a directory", cleanPath)
	}
	return nil
}

func EnsureRootParentDir(root *os.Root, relPath string) error {
	cleanPath, err := CleanRelativePath(relPath, false)
	if err != nil {
		return err
	}
	parent := filepath.ToSlash(filepath.Dir(cleanPath))
	if parent == "." {
		return nil
	}
	current := ""
	for _, part := range strings.Split(parent, "/") {
		if part == "" || part == "." {
			continue
		}
		if current == "" {
			current = part
		} else {
			current = current + "/" + part
		}
		if err := EnsureRootDir(root, current); err != nil {
			return err
		}
	}
	return nil
}

func CopyRootDirectoryContents(srcRoot *os.Root, dstDir string) error {
	entries, err := fs.ReadDir(srcRoot.FS(), ".")
	if err != nil {
		return fmt.Errorf("read source workspace dir: %w", err)
	}
	for _, entry := range entries {
		if err := copyRootWorkspaceEntry(srcRoot, entry.Name(), filepath.Join(dstDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyRootWorkspaceEntry(srcRoot *os.Root, relPath, dst string) error {
	cleanPath, err := CleanRelativePath(relPath, false)
	if err != nil {
		return err
	}
	cleanPath = filepath.ToSlash(cleanPath)
	info, err := srcRoot.Lstat(cleanPath)
	if err != nil {
		return fmt.Errorf("stat source workspace entry %s: %w", cleanPath, err)
	}
	switch mode := info.Mode(); {
	case mode.IsDir():
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove destination workspace directory %s: %w", dst, err)
		}
		if err := os.MkdirAll(dst, mode.Perm()); err != nil {
			return fmt.Errorf("create destination workspace directory %s: %w", dst, err)
		}
		entries, err := fs.ReadDir(srcRoot.FS(), cleanPath)
		if err != nil {
			return fmt.Errorf("read source workspace directory %s: %w", cleanPath, err)
		}
		for _, entry := range entries {
			if err := copyRootWorkspaceEntry(srcRoot, filepath.ToSlash(filepath.Join(cleanPath, entry.Name())), filepath.Join(dst, entry.Name())); err != nil {
				return err
			}
		}
		return nil
	case mode.Type() == os.ModeSymlink:
		return fmt.Errorf("file workspace symlink %s is not supported", cleanPath)
	default:
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return fmt.Errorf("create destination workspace file parent %s: %w", filepath.Dir(dst), err)
		}
		if err := os.RemoveAll(dst); err != nil {
			return fmt.Errorf("remove destination workspace file %s: %w", dst, err)
		}
		srcFile, err := srcRoot.Open(cleanPath)
		if err != nil {
			return fmt.Errorf("open source workspace file %s: %w", cleanPath, err)
		}
		defer func() { _ = srcFile.Close() }()
		dstFile, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode.Perm())
		if err != nil {
			return fmt.Errorf("create destination workspace file %s: %w", dst, err)
		}
		defer func() { _ = dstFile.Close() }()
		if _, err := io.Copy(dstFile, srcFile); err != nil {
			return fmt.Errorf("copy workspace file %s to %s: %w", cleanPath, dst, err)
		}
		return nil
	}
}

func ExtractWorkspaceTarArchive(src io.Reader, dstRoot *os.Root) error {
	reader := tar.NewReader(src)
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		relPath := filepath.Clean(strings.TrimSpace(header.Name))
		if relPath == "." || relPath == "" {
			continue
		}
		if filepath.IsAbs(relPath) {
			return fmt.Errorf("tar entry %q must be relative", header.Name)
		}
		if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
			return fmt.Errorf("tar entry %q escapes workspace root", header.Name)
		}
		relPath = filepath.ToSlash(relPath)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := EnsureRootParentDir(dstRoot, relPath); err != nil {
				return err
			}
			if err := EnsureRootDir(dstRoot, relPath); err != nil {
				return fmt.Errorf("create workspace archive dir %s: %w", relPath, err)
			}
		case tar.TypeReg:
			if err := EnsureRootParentDir(dstRoot, relPath); err != nil {
				return fmt.Errorf("create workspace archive parent %s: %w", filepath.Dir(relPath), err)
			}
			if err := dstRoot.RemoveAll(relPath); err != nil {
				return fmt.Errorf("remove workspace archive file target %s: %w", relPath, err)
			}
			file, err := dstRoot.OpenFile(relPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, header.FileInfo().Mode().Perm())
			if err != nil {
				return fmt.Errorf("create workspace archive file %s: %w", relPath, err)
			}
			if _, err := io.Copy(file, reader); err != nil {
				_ = file.Close()
				return fmt.Errorf("write workspace archive file %s: %w", relPath, err)
			}
			if err := file.Close(); err != nil {
				return fmt.Errorf("close workspace archive file %s: %w", relPath, err)
			}
		case tar.TypeSymlink, tar.TypeLink:
			return fmt.Errorf("unsupported tar entry type %q for %s", string(header.Typeflag), relPath)
		default:
			return fmt.Errorf("unsupported tar entry type %q for %s", string(header.Typeflag), relPath)
		}
	}
}

type FileEntry struct {
	Path      string `json:"path"`
	Dir       bool   `json:"dir"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

func ListFiles(contentRoot *os.Root) ([]FileEntry, error) {
	items := make([]FileEntry, 0)
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
		items = append(items, FileEntry{
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

func CleanRelativePath(raw string, allowEmpty bool) (string, error) {
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

func StoreUploadedFile(fileHeader *multipart.FileHeader, contentRoot *os.Root, targetPath string) error {
	if targetPath == "" {
		targetPath = fileHeader.Filename
	}
	cleanTarget, err := CleanRelativePath(targetPath, false)
	if err != nil {
		return err
	}
	src, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("open uploaded file: %w", err)
	}
	defer func() { _ = src.Close() }()
	cleanTarget = filepath.ToSlash(cleanTarget)
	if err := EnsureRootParentDir(contentRoot, cleanTarget); err != nil {
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

func ExtractUploadedArchive(fileHeader *multipart.FileHeader, contentRoot *os.Root) error {
	src, err := fileHeader.Open()
	if err != nil {
		return fmt.Errorf("open uploaded archive: %w", err)
	}
	defer func() { _ = src.Close() }()
	return ExtractWorkspaceTarArchive(src, contentRoot)
}

func DefaultFileConfigJSON(config *appconfig.Config, workspaceID string) string {
	root, err := DefaultFileWorkspaceContentRoot(config, workspaceID)
	if err != nil {
		root = filepath.Join(config.DataRoot, "workspaces", strings.TrimSpace(workspaceID), FileWorkspaceContentDirName)
	}
	payload, _ := json.Marshal(FileWorkspaceConfig{Root: root})
	return string(payload)
}

func DefaultFileWorkspaceContentRoot(config *appconfig.Config, workspaceID string) (string, error) {
	root := filepath.Join(config.DataRoot, "workspaces", strings.TrimSpace(workspaceID), FileWorkspaceContentDirName)
	return filepath.Abs(root)
}
