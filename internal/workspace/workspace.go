package workspace

import (
	appconfig "agent-compose/pkg/config"
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
	"strings"
)

const gitWorkspaceTempDirName = ".agent-compose-git-clone"

const fileWorkspaceContentDirName = "content"
const FileWorkspaceContentDirName = fileWorkspaceContentDirName

type gitWorkspaceConfig struct {
	URL         string `json:"url"`
	Branch      string `json:"branch,omitempty"`
	Commit      string `json:"commit,omitempty"`
	Credential  string `json:"credential,omitempty"`
	Username    string `json:"username,omitempty"`
	Password    string `json:"password,omitempty"`
	CloneTarget string `json:"path,omitempty"`
}

type GitWorkspaceConfig = gitWorkspaceConfig

type fileWorkspaceConfig struct {
	Root string `json:"root,omitempty"`
}

type FileWorkspaceConfig = fileWorkspaceConfig

type fileWorkspaceContent struct {
	AbsRoot string
	RelRoot string
	Root    *os.Root
}

type FileWorkspaceContent = fileWorkspaceContent

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB ConfigStore, session *Session) error {
	workspaceID := strings.TrimSpace(session.WorkspaceID)
	if session.Workspace != nil && strings.TrimSpace(session.Workspace.ID) != "" {
		workspace := WorkspaceConfig{
			ID:         strings.TrimSpace(session.Workspace.ID),
			Name:       session.Workspace.Name,
			Type:       session.Workspace.Type,
			ConfigJSON: session.Workspace.ConfigJSON,
		}
		if workspaceID == "" {
			session.WorkspaceID = workspace.ID
		}
		return prepareWorkspaceConfig(ctx, config, session, workspace)
	}
	if workspaceID == "" {
		return nil
	}
	workspace, err := configDB.GetWorkspaceConfig(ctx, workspaceID)
	if err != nil {
		return err
	}
	return prepareWorkspaceConfig(ctx, config, session, workspace)
}

func PrepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB ConfigStore, session *Session) error {
	return prepareSessionWorkspace(ctx, config, configDB, session)
}

func prepareWorkspaceConfig(ctx context.Context, config *appconfig.Config, session *Session, workspace WorkspaceConfig) error {
	switch strings.ToLower(strings.TrimSpace(workspace.Type)) {
	case "git":
		return prepareGitWorkspace(ctx, session, workspace)
	case "file":
		return prepareFileWorkspace(config, session, workspace)
	default:
		return fmt.Errorf("unsupported workspace type %q", workspace.Type)
	}
}

func prepareFileWorkspace(config *appconfig.Config, session *Session, workspace WorkspaceConfig) error {
	workspaceRoot := strings.TrimSpace(session.Summary.WorkspacePath)
	if workspaceRoot == "" {
		return fmt.Errorf("session %s missing workspace path", session.Summary.ID)
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return fmt.Errorf("prepare workspace %s failed: create workspace root: %w", workspace.Name, err)
	}
	content, err := openFileWorkspaceContent(config, workspace)
	if err != nil {
		return err
	}
	defer func() { _ = content.Root.Close() }()
	if err := copyRootDirectoryContents(content.Root, workspaceRoot); err != nil {
		return fmt.Errorf("prepare workspace %s failed: copy file workspace content: %w", workspace.Name, err)
	}
	return nil
}

func fileWorkspaceContentRoot(config *appconfig.Config, workspace WorkspaceConfig) (string, error) {
	workspaceID := strings.TrimSpace(workspace.ID)
	if workspaceID == "" {
		return "", fmt.Errorf("workspace config id is required for file workspace")
	}
	var cfg fileWorkspaceConfig
	trimmedConfig := strings.TrimSpace(workspace.ConfigJSON)
	if trimmedConfig != "" && trimmedConfig != "{}" {
		if err := json.Unmarshal([]byte(trimmedConfig), &cfg); err != nil {
			return "", fmt.Errorf("decode workspace config %s: %w", workspace.ID, err)
		}
	}
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		return defaultFileWorkspaceContentRoot(config, workspaceID)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("workspace config %s has invalid file workspace root %q", workspace.ID, root)
	}
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("workspace config %s has invalid file workspace root %q", workspace.ID, root)
	}
	expectedRoot, err := defaultFileWorkspaceContentRoot(config, workspaceID)
	if err != nil {
		return "", err
	}
	if cleanRoot != expectedRoot {
		return "", fmt.Errorf("workspace config %s has file workspace root %q, want %q", workspace.ID, cleanRoot, expectedRoot)
	}
	return cleanRoot, nil
}

func FileWorkspaceContentRoot(config *appconfig.Config, workspace WorkspaceConfig) (string, error) {
	return fileWorkspaceContentRoot(config, workspace)
}

func validateFileWorkspaceConfig(config *appconfig.Config, workspaceID, configJSON string) (string, error) {
	return fileWorkspaceContentRoot(config, WorkspaceConfig{
		ID:         strings.TrimSpace(workspaceID),
		Type:       "file",
		ConfigJSON: configJSON,
	})
}

func ValidateFileWorkspaceConfig(config *appconfig.Config, workspaceID, configJSON string) (string, error) {
	return validateFileWorkspaceConfig(config, workspaceID, configJSON)
}

func openFileWorkspaceContent(config *appconfig.Config, workspace WorkspaceConfig) (fileWorkspaceContent, error) {
	absRoot, err := fileWorkspaceContentRoot(config, workspace)
	if err != nil {
		return fileWorkspaceContent{}, err
	}
	workspaceID := strings.TrimSpace(workspace.ID)
	relRoot, err := fileWorkspaceContentRelRoot(workspaceID)
	if err != nil {
		return fileWorkspaceContent{}, err
	}
	dataRoot, err := openFileWorkspaceDataRoot(config)
	if err != nil {
		return fileWorkspaceContent{}, err
	}
	defer func() { _ = dataRoot.Close() }()
	for _, dir := range []string{"workspaces", filepath.ToSlash(filepath.Join("workspaces", workspaceID)), relRoot} {
		if err := ensureRootDir(dataRoot, dir); err != nil {
			return fileWorkspaceContent{}, err
		}
	}
	contentRoot, err := dataRoot.OpenRoot(relRoot)
	if err != nil {
		return fileWorkspaceContent{}, fmt.Errorf("open file workspace content root: %w", err)
	}
	return fileWorkspaceContent{AbsRoot: absRoot, RelRoot: relRoot, Root: contentRoot}, nil
}

func OpenFileWorkspaceContent(config *appconfig.Config, workspace WorkspaceConfig) (FileWorkspaceContent, error) {
	return openFileWorkspaceContent(config, workspace)
}

func fileWorkspaceContentRelRoot(workspaceID string) (string, error) {
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return "", fmt.Errorf("workspace config id is required for file workspace")
	}
	if filepath.IsAbs(workspaceID) || workspaceID == "." || workspaceID == ".." || workspaceID != filepath.Base(workspaceID) {
		return "", fmt.Errorf("workspace config id %q is not a valid path segment", workspaceID)
	}
	return filepath.ToSlash(filepath.Join("workspaces", workspaceID, fileWorkspaceContentDirName)), nil
}

func FileWorkspaceContentRelRoot(workspaceID string) (string, error) {
	return fileWorkspaceContentRelRoot(workspaceID)
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

func defaultFileWorkspaceContentRoot(config *appconfig.Config, workspaceID string) (string, error) {
	root := filepath.Join(config.DataRoot, "workspaces", strings.TrimSpace(workspaceID), fileWorkspaceContentDirName)
	return filepath.Abs(root)
}

func openFileWorkspaceDataRoot(config *appconfig.Config) (*os.Root, error) {
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

func OpenFileWorkspaceDataRoot(config *appconfig.Config) (*os.Root, error) {
	return openFileWorkspaceDataRoot(config)
}

func ensureRootDir(root *os.Root, relPath string) error {
	cleanPath, err := cleanWorkspaceRelativePath(relPath, false)
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

func ensureRootParentDir(root *os.Root, relPath string) error {
	cleanPath, err := cleanWorkspaceRelativePath(relPath, false)
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
		if err := ensureRootDir(root, current); err != nil {
			return err
		}
	}
	return nil
}

func EnsureRootParentDir(root *os.Root, relPath string) error {
	return ensureRootParentDir(root, relPath)
}

func copyRootDirectoryContents(srcRoot *os.Root, dstDir string) error {
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

func CopyRootDirectoryContents(srcRoot *os.Root, dstDir string) error {
	return copyRootDirectoryContents(srcRoot, dstDir)
}

func copyRootWorkspaceEntry(srcRoot *os.Root, relPath, dst string) error {
	cleanPath, err := cleanWorkspaceRelativePath(relPath, false)
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

func extractWorkspaceTarArchive(src io.Reader, dstRoot *os.Root) error {
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
			if err := ensureRootParentDir(dstRoot, relPath); err != nil {
				return err
			}
			if err := ensureRootDir(dstRoot, relPath); err != nil {
				return fmt.Errorf("create workspace archive dir %s: %w", relPath, err)
			}
		case tar.TypeReg:
			if err := ensureRootParentDir(dstRoot, relPath); err != nil {
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

func ExtractWorkspaceTarArchive(src io.Reader, dstRoot *os.Root) error {
	return extractWorkspaceTarArchive(src, dstRoot)
}

func prepareGitWorkspace(ctx context.Context, session *Session, workspace WorkspaceConfig) error {
	var cfg gitWorkspaceConfig
	if err := json.Unmarshal([]byte(workspace.ConfigJSON), &cfg); err != nil {
		return fmt.Errorf("decode workspace config %s: %w", workspace.ID, err)
	}
	cloneURL := strings.TrimSpace(cfg.URL)
	if cloneURL == "" {
		return fmt.Errorf("workspace config %s missing git url", workspace.ID)
	}
	cloneURL = applyGitCredentials(cloneURL, cfg)
	cloneTarget, err := normalizeGitCloneTarget(workspace.ID, cfg.CloneTarget)
	if err != nil {
		return err
	}
	workspaceRoot := strings.TrimSpace(session.Summary.WorkspacePath)
	if workspaceRoot == "" {
		return fmt.Errorf("session %s missing workspace path", session.Summary.ID)
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return fmt.Errorf("prepare workspace %s failed: create workspace root: %w", workspace.Name, err)
	}
	if err := cleanupGitCloneTempDir(workspaceRoot); err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", workspace.Name, err)
	}
	initialized, err := hostWorkspaceInitialized(workspaceRoot)
	if err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", workspace.Name, err)
	}
	if initialized {
		return nil
	}
	if cloneTarget == "." {
		if err := cloneGitWorkspaceRoot(ctx, workspaceRoot, cloneURL, cfg); err != nil {
			return fmt.Errorf("prepare workspace %s failed: %w", workspace.Name, err)
		}
		return nil
	}
	clonePath := filepath.Join(workspaceRoot, cloneTarget)
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		return fmt.Errorf("prepare workspace %s failed: create clone parent: %w", workspace.Name, err)
	}
	if err := gitClone(ctx, cloneURL, cfg, clonePath); err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", workspace.Name, err)
	}
	if err := gitCheckoutCommit(ctx, clonePath, cfg); err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", workspace.Name, err)
	}
	return nil
}

func normalizeGitCloneTarget(workspaceID, raw string) (string, error) {
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

func NormalizeGitCloneTarget(workspaceID, raw string) (string, error) {
	return normalizeGitCloneTarget(workspaceID, raw)
}

func cleanupGitCloneTempDir(workspaceRoot string) error {
	tempDir := filepath.Join(workspaceRoot, gitWorkspaceTempDirName)
	if err := os.RemoveAll(tempDir); err != nil {
		return fmt.Errorf("cleanup temp git clone dir: %w", err)
	}
	return nil
}

func hostWorkspaceInitialized(workspaceRoot string) (bool, error) {
	entries, err := os.ReadDir(workspaceRoot)
	if err != nil {
		return false, fmt.Errorf("read workspace root: %w", err)
	}
	for _, entry := range entries {
		switch entry.Name() {
		case ".agent-compose", gitWorkspaceTempDirName:
			continue
		}
		return true, nil
	}
	return false, nil
}

func cloneGitWorkspaceRoot(ctx context.Context, workspaceRoot, cloneURL string, cfg gitWorkspaceConfig) error {
	tempDir := filepath.Join(workspaceRoot, gitWorkspaceTempDirName)
	if err := gitClone(ctx, cloneURL, cfg, tempDir); err != nil {
		return err
	}
	if err := gitCheckoutCommit(ctx, tempDir, cfg); err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}
	if err := promoteClonedWorkspaceRoot(tempDir, workspaceRoot); err != nil {
		_ = os.RemoveAll(tempDir)
		return err
	}
	return nil
}

func promoteClonedWorkspaceRoot(tempDir, workspaceRoot string) error {
	entries, err := os.ReadDir(tempDir)
	if err != nil {
		return fmt.Errorf("read temp git clone dir: %w", err)
	}
	for _, entry := range entries {
		src := filepath.Join(tempDir, entry.Name())
		dst := filepath.Join(workspaceRoot, entry.Name())
		if err := moveWorkspaceEntry(src, dst); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(tempDir); err != nil {
		return fmt.Errorf("remove temp git clone dir: %w", err)
	}
	return nil
}

func moveWorkspaceEntry(src, dst string) error {
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
		if err := moveWorkspaceEntry(childSrc, childDst); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove merged workspace directory %s: %w", src, err)
	}
	return nil
}

func gitClone(ctx context.Context, cloneURL string, cfg gitWorkspaceConfig, clonePath string) error {
	return runGitCommand(ctx, "", "git clone", gitCloneArgs(cloneURL, cfg, clonePath)...)
}

func gitCloneArgs(cloneURL string, cfg gitWorkspaceConfig, clonePath string) []string {
	args := []string{"clone", "--depth", "1"}
	if branch := strings.TrimSpace(cfg.Branch); branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, cloneURL, clonePath)
	return args
}

func gitCheckoutCommit(ctx context.Context, clonePath string, cfg gitWorkspaceConfig) error {
	commit := strings.TrimSpace(cfg.Commit)
	if commit == "" {
		return nil
	}
	// Fast path: fetch only the requested object so the clone stays shallow.
	// Works when commit is a full SHA or an exact ref name and the server allows
	// fetching it directly (e.g. uploadpack.allowReachableSHA1InWant).
	if err := runGitCommand(ctx, clonePath, "git fetch", gitCommitFetchArgs(commit)...); err == nil {
		return runGitCommand(ctx, clonePath, "git checkout", "checkout", "FETCH_HEAD")
	}
	// Fallback: the object could not be fetched directly. This happens for an
	// abbreviated SHA (git treats it as a ref name), a commit that is not a ref
	// tip, or a server that rejects by-SHA fetches. Deepen all branches and tags
	// so any reachable commit, abbreviated SHA, or tag resolves locally.
	if err := runGitCommand(ctx, clonePath, "git fetch", gitDeepenFetchArgs(true)...); err != nil {
		// --unshallow only applies to a shallow repo; retry without it.
		if err := runGitCommand(ctx, clonePath, "git fetch", gitDeepenFetchArgs(false)...); err != nil {
			return err
		}
	}
	return runGitCommand(ctx, clonePath, "git checkout", "checkout", commit)
}

func gitCommitFetchArgs(commit string) []string {
	return []string{"fetch", "--depth", "1", "origin", commit}
}

func gitDeepenFetchArgs(unshallow bool) []string {
	args := []string{"fetch"}
	if unshallow {
		args = append(args, "--unshallow")
	}
	return append(args, "--tags", "origin", "+refs/heads/*:refs/remotes/origin/*")
}

func runGitCommand(ctx context.Context, dir, action string, args ...string) error {
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

func applyGitCredentials(cloneURL string, cfg gitWorkspaceConfig) string {
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
