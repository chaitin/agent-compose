package workspaces

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sources"
)

const GitWorkspaceTempDirName = ".agent-compose-git-clone"

type GitWorkspaceConfig struct {
	sources.Source
	Target string `json:"target,omitempty"`
}

type gitWorkspace struct {
	workspace domain.WorkspaceConfig
}

func PrepareGitWorkspace(ctx context.Context, session *domain.Sandbox, workspace domain.WorkspaceConfig) error {
	return gitWorkspace{workspace: workspace}.Prepare(ctx, session)
}

func (w gitWorkspace) Prepare(ctx context.Context, session *domain.Sandbox) error {
	var cfg GitWorkspaceConfig
	if err := json.Unmarshal([]byte(w.workspace.ConfigJSON), &cfg); err != nil {
		return fmt.Errorf("decode workspace config %s: %w", w.workspace.ID, err)
	}
	cfg.Source = cfg.Normalized()
	if cfg.Provider == "" {
		cfg.Provider = sources.ProviderGit
	}
	if cfg.URL == "" {
		return fmt.Errorf("workspace config %s missing git url", w.workspace.ID)
	}
	target, err := NormalizeWorkspaceTarget(w.workspace.ID, cfg.Target)
	if err != nil {
		return err
	}
	workspaceRoot := strings.TrimSpace(session.Summary.WorkspacePath)
	if workspaceRoot == "" {
		return fmt.Errorf("session %s missing workspace path", session.Summary.ID)
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return fmt.Errorf("prepare workspace %s failed: create workspace root: %w", w.workspace.Name, err)
	}
	if err := cleanupGitCloneTempDir(workspaceRoot); err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", w.workspace.Name, err)
	}
	if target == "." {
		if err := cloneGitWorkspaceRoot(ctx, workspaceRoot, cfg.Source); err != nil {
			return fmt.Errorf("prepare workspace %s failed: %w", w.workspace.Name, err)
		}
		return nil
	}
	clonePath := filepath.Join(workspaceRoot, target)
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		return fmt.Errorf("prepare workspace %s failed: create clone parent: %w", w.workspace.Name, err)
	}
	if _, err := (sources.GitClient{}).Checkout(ctx, cfg.Source, clonePath); err != nil {
		return fmt.Errorf("prepare workspace %s failed: %w", w.workspace.Name, err)
	}
	return nil
}

func NormalizeWorkspaceTarget(workspaceID, raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ".", nil
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("workspace config %s has invalid target %q", workspaceID, trimmed)
	}
	clean := filepath.Clean(trimmed)
	if clean == "." {
		return ".", nil
	}
	parentPrefix := ".." + string(filepath.Separator)
	if clean == ".." || strings.HasPrefix(clean, parentPrefix) {
		return "", fmt.Errorf("workspace config %s has invalid target %q", workspaceID, trimmed)
	}
	return clean, nil
}

func cleanupGitCloneTempDir(workspaceRoot string) error {
	tempDir := filepath.Join(workspaceRoot, GitWorkspaceTempDirName)
	if err := os.RemoveAll(tempDir); err != nil {
		return fmt.Errorf("cleanup temp git clone dir: %w", err)
	}
	return nil
}

func cloneGitWorkspaceRoot(ctx context.Context, workspaceRoot string, source sources.Source) error {
	tempDir := filepath.Join(workspaceRoot, GitWorkspaceTempDirName)
	if _, err := (sources.GitClient{}).Checkout(ctx, source, tempDir); err != nil {
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
		if err := MoveWorkspaceEntry(src, dst); err != nil {
			return err
		}
	}
	if err := os.RemoveAll(tempDir); err != nil {
		return fmt.Errorf("remove temp git clone dir: %w", err)
	}
	return nil
}

func MoveWorkspaceEntry(src, dst string) error {
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
		if err := MoveWorkspaceEntry(childSrc, childDst); err != nil {
			return err
		}
	}
	if err := os.Remove(src); err != nil {
		return fmt.Errorf("remove merged workspace directory %s: %w", src, err)
	}
	return nil
}
