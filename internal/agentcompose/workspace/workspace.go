package workspace

import (
	"archive/tar"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	appconfig "agent-compose/internal/config"
)

const GitTempDirName = ".agent-compose-git-clone"

const FileContentDirName = "content"

type Config struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	Type       string    `json:"type"`
	ConfigJSON string    `json:"config_json"`
	Comment    string    `json:"comment,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Snapshot struct {
	ID         string `json:"id"`
	Name       string `json:"name,omitempty"`
	Type       string `json:"type,omitempty"`
	ConfigJSON string `json:"config_json,omitempty"`
}

type GitConfig struct {
	URL         string `json:"url"`
	Branch      string `json:"branch,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Credential  string `json:"credential,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	CloneTarget string `json:"path,omitempty"`
}

type FileConfig struct {
	Root string `json:"root,omitempty"`
}

type FileContent struct {
	AbsRoot string
	RelRoot string
	Root    *os.Root
}

type FileEntry struct {
	Path      string `json:"path"`
	Dir       bool   `json:"dir"`
	Size      int64  `json:"size"`
	UpdatedAt string `json:"updated_at"`
}

func SnapshotFromConfig(item Config) *Snapshot {
	return &Snapshot{
		ID:         item.ID,
		Name:       item.Name,
		Type:       item.Type,
		ConfigJSON: item.ConfigJSON,
	}
}

func FileContentRoot(config *appconfig.Config, workspace Config) (string, error) {
	workspaceID := strings.TrimSpace(workspace.ID)
	if workspaceID == "" {
		return "", fmt.Errorf("workspace config id is required for file workspace")
	}
	var cfg FileConfig
	trimmedConfig := strings.TrimSpace(workspace.ConfigJSON)
	if trimmedConfig != "" && trimmedConfig != "{}" {
		if err := json.Unmarshal([]byte(trimmedConfig), &cfg); err != nil {
			return "", fmt.Errorf("decode workspace config %s: %w", workspace.ID, err)
		}
	}
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		return DefaultFileContentRoot(config, workspaceID)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("workspace config %s has invalid file workspace root %q", workspace.ID, root)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("workspace config %s has invalid file workspace root %q", workspace.ID, root)
	}
	expectedRoot, err := DefaultFileContentRoot(config, workspaceID)
	if err != nil {
		return "", err
	}
	if cleanRoot != expectedRoot {
		return "", fmt.Errorf("workspace config %s has file workspace root %q, want %q", workspace.ID, cleanRoot, expectedRoot)
	}
	return cleanRoot, nil
}

func ValidateFileConfig(config *appconfig.Config, workspaceID, configJSON string) (string, error) {
	return FileContentRoot(config, Config{
		ID:         strings.TrimSpace(workspaceID),
		Type:       "file",
		ConfigJSON: configJSON,
	})
}

func OpenFileContent(config *appconfig.Config, workspace Config) (FileContent, error) {
	absRoot, err := FileContentRoot(config, workspace)
	if err != nil {
		return FileContent{}, err
	}
	workspaceID := strings.TrimSpace(workspace.ID)
	relRoot, err := FileContentRelRoot(workspaceID)
	if err != nil {
		return FileContent{}, err
	}
	dataRoot, err := OpenFileDataRoot(config)
	if err != nil {
		return FileContent{}, err
	}
	defer func() { _ = dataRoot.Close() }()
	for _, dir := range []string{"workspaces", filepath.ToSlash(filepath.Join("workspaces", workspaceID)), relRoot} {
		if err := EnsureRootDir(dataRoot, dir); err != nil {
			return FileContent{}, err
		}
	}
	contentRoot, err := dataRoot.OpenRoot(relRoot)
	if err != nil {
		return FileContent{}, fmt.Errorf("open file workspace content root: %w", err)
	}
	return FileContent{AbsRoot: absRoot, RelRoot: relRoot, Root: contentRoot}, nil
}

func FileContentRelRoot(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return "", fmt.Errorf("workspace config id is required for file workspace")
	}
	if filepath.IsAbs(workspaceID) || workspaceID == "." || workspaceID == ".." || workspaceID != filepath.Base(workspaceID) {
		return "", fmt.Errorf("workspace config id %q is not a valid path segment", workspaceID)
	}
	return filepath.ToSlash(filepath.Join("workspaces", workspaceID, FileContentDirName)), nil
}

func OpenFileDataRoot(config *appconfig.Config) (*os.Root, error) {
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
		if err := CopyRootEntry(srcRoot, entry.Name(), filepath.Join(dstDir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

func CopyRootEntry(srcRoot *os.Root, relPath, dst string) error {
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
			if err := CopyRootEntry(srcRoot, filepath.ToSlash(filepath.Join(cleanPath, entry.Name())), filepath.Join(dst, entry.Name())); err != nil {
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

func ExtractTarArchive(src io.Reader, dstRoot *os.Root) error {
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

func DefaultFileConfigJSON(config *appconfig.Config, workspaceID string) string {
	root, err := DefaultFileContentRoot(config, workspaceID)
	if err != nil {
		root = filepath.Join(config.DataRoot, "workspaces", strings.TrimSpace(workspaceID), FileContentDirName)
	}
	payload, _ := json.Marshal(FileConfig{Root: root})
	return string(payload)
}

func DefaultFileContentRoot(config *appconfig.Config, workspaceID string) (string, error) {
	root := filepath.Join(config.DataRoot, "workspaces", strings.TrimSpace(workspaceID), FileContentDirName)
	return filepath.Abs(root)
}

func NormalizeGitCloneTarget(workspaceID, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ".", nil
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("workspace config %s has invalid clone path %q", workspaceID, trimmed)
	}
	clean := filepath.Clean(trimmed)
	if clean == "." {
		return ".", nil
	}
	parentPrefix := ".." + string(filepath.Separator)
	if clean == ".." || strings.HasPrefix(clean, parentPrefix) {
		return "", fmt.Errorf("workspace config %s has invalid clone path %q", workspaceID, trimmed)
	}
	return clean, nil
}

func CleanupGitTempDir(workspaceRoot string) error {
	tempDir := filepath.Join(workspaceRoot, GitTempDirName)
	if err := os.RemoveAll(tempDir); err != nil {
		return fmt.Errorf("cleanup temp git clone dir: %w", err)
	}
	return nil
}

func HostInitialized(workspaceRoot string) (bool, error) {
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return false, fmt.Errorf("read workspace root: %w", err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case ".agent-compose", GitTempDirName:
			continue
		}
		return true, nil
	}
	return false, nil
}

func CloneRoot(ctx context.Context, workspaceRoot, cloneURL string, cfg GitConfig) error {
	tempDir := filepath.Join(workspaceRoot, GitTempDirName)
	if err := GitClone(ctx, cloneURL, cfg, tempDir); err != nil {
		return err
	}
	if err := GitCheckoutCommit(ctx, tempDir, cfg); err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}
	if err := PromoteClonedRoot(tempDir, workspaceRoot); err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}
	return nil
}

func PromoteClonedRoot(tempDir, workspaceRoot string) error {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return fmt.Errorf("read temp git clone dir: %w", err)
	}
	for _, entry := range entries {
		src := filepath.Join(tempDir, entry.Name())
		dst := filepath.Join(workspaceRoot, entry.Name())
		if err := MoveEntry(src, dst); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(tempDir); err != nil {
		return fmt.Errorf("remove temp git clone dir: %w", err)
	}
	return nil
}

func MoveEntry(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	srcInfo, err := os.Lstat(src)
	if err != nil {
		return fmt.Errorf("stat source workspace entry %s: %w", src, err)
	}
	dstInfo, err := os.Lstat(dst)
	if err != nil {
		return fmt.Errorf("move workspace entry %s to %s: %w", src, dst, err)
	}
	if !srcInfo.IsDir() || !dstInfo.IsDir() {
		return fmt.Errorf("move workspace entry %s to %s: destination already exists", src, dst)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return fmt.Errorf("read source workspace directory %s: %w", src, err)
	}
	for _, entry := range entries {
		childSrc := filepath.Join(src, entry.Name())
		childDst := filepath.Join(dst, entry.Name())
		if err := MoveEntry(childSrc, childDst); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove merged workspace directory %s: %w", src, err)
	}
	return nil
}

func GitClone(ctx context.Context, cloneURL string, cfg GitConfig, clonePath string) error {
	return RunGitCommand(ctx, "", "git clone", GitCloneArgs(cloneURL, cfg, clonePath)...)
}

func GitCloneArgs(cloneURL string, cfg GitConfig, clonePath string) []string {
	args := []string{"clone", "--depth", "1"}
	if branch := strings.TrimSpace(cfg.Branch); branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, cloneURL, clonePath)
	return args
}

func GitCheckoutCommit(ctx context.Context, clonePath string, cfg GitConfig) error {
	commit := strings.TrimSpace(cfg.Commit)
	if commit == "" {
		return nil
	}
	if err := RunGitCommand(ctx, clonePath, "git fetch", GitCommitFetchArgs(commit)...); err == nil {
		return RunGitCommand(ctx, clonePath, "git checkout", "checkout", "FETCH_HEAD")
	}
	if err := RunGitCommand(ctx, clonePath, "git fetch", GitDeepenFetchArgs(true)...); err != nil {
		if err := RunGitCommand(ctx, clonePath, "git fetch", GitDeepenFetchArgs(false)...); err != nil {
			return err
		}
	}
	return RunGitCommand(ctx, clonePath, "git checkout", "checkout", commit)
}

func GitCommitFetchArgs(commit string) []string {
	return []string{"fetch", "--depth", "1", "origin", commit}
}

func GitDeepenFetchArgs(unshallow bool) []string {
	args := []string{"fetch"}
	if unshallow {
		args = append(args, "--unshallow")
	}
	return append(args, "--tags", "origin", "+refs/heads/*:refs/remotes/origin/*")
}

func RunGitCommand(ctx context.Context, dir, action string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if strings.TrimSpace(dir) != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}
	message := strings.TrimSpace(string(output))
	if message == "" {
		message = err.Error()
	}
	return fmt.Errorf("%s failed: %s", action, message)
}

func ApplyGitCredentials(cloneURL string, cfg GitConfig) string {
	trimmedURL := strings.TrimSpace(cloneURL)
	if trimmedURL == "" {
		return ""
	}
	credential := strings.TrimSpace(cfg.Credential)
	if credential == "" {
		user := strings.TrimSpace(cfg.Username)
		pass := strings.TrimSpace(cfg.Password)
		if user != "" || pass != "" {
			credential = url.QueryEscape(user) + ":" + url.QueryEscape(pass)
		}
	}
	if credential == "" {
		return trimmedURL
	}
	if strings.Contains(trimmedURL, "@") {
		return trimmedURL
	}
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(trimmedURL, prefix) {
			return prefix + credential + "@" + strings.TrimPrefix(trimmedURL, prefix)
		}
	}
	return trimmedURL
}
