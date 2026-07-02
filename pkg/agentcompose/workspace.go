package agentcompose

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	agentworkspace "agent-compose/internal/agentcompose/workspace"
	appconfig "agent-compose/pkg/config"
)

const gitWorkspaceTempDirName = agentworkspace.GitTempDirName

const fileWorkspaceContentDirName = agentworkspace.FileContentDirName

type gitWorkspaceConfig = agentworkspace.GitConfig
type fileWorkspaceConfig = agentworkspace.FileConfig
type fileWorkspaceContent = agentworkspace.FileContent
type workspaceFileEntry = agentworkspace.FileEntry

func prepareSessionWorkspace(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session) error {
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

func fileWorkspaceContentRoot(config *appconfig.Config, workspace WorkspaceConfig) (string, error) {
	return agentworkspace.FileContentRoot(config, workspace)
}

func validateFileWorkspaceConfig(config *appconfig.Config, workspaceID, configJSON string) (string, error) {
	return agentworkspace.ValidateFileConfig(config, workspaceID, configJSON)
}

func openFileWorkspaceContent(config *appconfig.Config, workspace WorkspaceConfig) (fileWorkspaceContent, error) {
	return agentworkspace.OpenFileContent(config, workspace)
}

func fileWorkspaceContentRelRoot(workspaceID string) (string, error) {
	return agentworkspace.FileContentRelRoot(workspaceID)
}

func openFileWorkspaceDataRoot(config *appconfig.Config) (*os.Root, error) {
	return agentworkspace.OpenFileDataRoot(config)
}

func ensureRootDir(root *os.Root, relPath string) error {
	return agentworkspace.EnsureRootDir(root, relPath)
}

func ensureRootParentDir(root *os.Root, relPath string) error {
	return agentworkspace.EnsureRootParentDir(root, relPath)
}

func copyRootDirectoryContents(srcRoot *os.Root, dstDir string) error {
	return agentworkspace.CopyRootDirectoryContents(srcRoot, dstDir)
}

func copyRootWorkspaceEntry(srcRoot *os.Root, relPath, dst string) error {
	return agentworkspace.CopyRootEntry(srcRoot, relPath, dst)
}

func extractWorkspaceTarArchive(src io.Reader, dstRoot *os.Root) error {
	return agentworkspace.ExtractTarArchive(src, dstRoot)
}

func cleanWorkspaceRelativePath(raw string, allowEmpty bool) (string, error) {
	return agentworkspace.CleanRelativePath(raw, allowEmpty)
}

func listWorkspaceFiles(contentRoot *os.Root) ([]workspaceFileEntry, error) {
	return agentworkspace.ListFiles(contentRoot)
}

func defaultFileWorkspaceConfigJSON(config *appconfig.Config, workspaceID string) string {
	return agentworkspace.DefaultFileConfigJSON(config, workspaceID)
}

func defaultFileWorkspaceContentRoot(config *appconfig.Config, workspaceID string) (string, error) {
	return agentworkspace.DefaultFileContentRoot(config, workspaceID)
}

func normalizeGitCloneTarget(workspaceID, raw string) (string, error) {
	return agentworkspace.NormalizeGitCloneTarget(workspaceID, raw)
}

func cleanupGitCloneTempDir(workspaceRoot string) error {
	return agentworkspace.CleanupGitTempDir(workspaceRoot)
}

func hostWorkspaceInitialized(workspaceRoot string) (bool, error) {
	return agentworkspace.HostInitialized(workspaceRoot)
}

func cloneGitWorkspaceRoot(ctx context.Context, workspaceRoot, cloneURL string, cfg gitWorkspaceConfig) error {
	return agentworkspace.CloneRoot(ctx, workspaceRoot, cloneURL, cfg)
}

func promoteClonedWorkspaceRoot(tempDir, workspaceRoot string) error {
	return agentworkspace.PromoteClonedRoot(tempDir, workspaceRoot)
}

func moveWorkspaceEntry(src, dst string) error {
	return agentworkspace.MoveEntry(src, dst)
}

func gitClone(ctx context.Context, cloneURL string, cfg gitWorkspaceConfig, clonePath string) error {
	return agentworkspace.GitClone(ctx, cloneURL, cfg, clonePath)
}

func gitCloneArgs(cloneURL string, cfg gitWorkspaceConfig, clonePath string) []string {
	return agentworkspace.GitCloneArgs(cloneURL, cfg, clonePath)
}

func gitCheckoutCommit(ctx context.Context, clonePath string, cfg gitWorkspaceConfig) error {
	return agentworkspace.GitCheckoutCommit(ctx, clonePath, cfg)
}

func gitCommitFetchArgs(commit string) []string {
	return agentworkspace.GitCommitFetchArgs(commit)
}

func gitDeepenFetchArgs(unshallow bool) []string {
	return agentworkspace.GitDeepenFetchArgs(unshallow)
}

func runGitCommand(ctx context.Context, dir, action string, args ...string) error {
	return agentworkspace.RunGitCommand(ctx, dir, action, args...)
}

func applyGitCredentials(cloneURL string, cfg gitWorkspaceConfig) string {
	return agentworkspace.ApplyGitCredentials(cloneURL, cfg)
}
