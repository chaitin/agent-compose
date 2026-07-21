package workspaces

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	domain "agent-compose/pkg/model"
)

const (
	workspaceProvisioningStateDir = "workspace-provisioning"
	workspaceProvisioningAttempt  = "attempt-"
)

type provisioningFileSystem interface {
	MkdirAll(string, fs.FileMode) error
	MkdirTemp(string, string) (string, error)
	Lstat(string) (fs.FileInfo, error)
	ReadDir(string) ([]os.DirEntry, error)
	RemoveAll(string) error
	Rename(string, string) error
}

type osProvisioningFileSystem struct{}

func (osProvisioningFileSystem) MkdirAll(path string, perm fs.FileMode) error {
	return os.MkdirAll(path, perm)
}

func (osProvisioningFileSystem) MkdirTemp(dir, pattern string) (string, error) {
	return os.MkdirTemp(dir, pattern)
}

func (osProvisioningFileSystem) Lstat(path string) (fs.FileInfo, error) {
	return os.Lstat(path)
}

func (osProvisioningFileSystem) ReadDir(path string) ([]os.DirEntry, error) {
	return os.ReadDir(path)
}

func (osProvisioningFileSystem) RemoveAll(path string) error {
	return os.RemoveAll(path)
}

func (osProvisioningFileSystem) Rename(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func (p *Provisioner) provisionPending(ctx context.Context, sandbox *domain.Sandbox) error {
	paths, err := p.resolveProvisioningPaths(sandbox)
	if err != nil {
		return p.persistProvisioningFailure(ctx, sandbox, err)
	}
	attemptPath, err := p.createProvisioningAttempt(paths)
	if err != nil {
		return p.persistProvisioningFailure(ctx, sandbox, err)
	}

	staged := cloneSandboxForProvisioning(sandbox)
	staged.Summary.WorkspacePath = attemptPath
	if err := p.materializer.Materialize(ctx, staged); err != nil {
		cleanupErr := p.cleanupProvisioningAttempt(attemptPath)
		return p.persistProvisioningFailure(ctx, sandbox, errors.Join(
			fmt.Errorf("materialize workspace for sandbox %s: %w", sandbox.Summary.ID, err),
			cleanupErr,
		))
	}

	if err := p.promoteProvisioningAttempt(attemptPath, paths.workspace); err != nil {
		cleanupErr := p.cleanupProvisioningAttempt(attemptPath)
		return p.persistProvisioningFailure(ctx, sandbox, errors.Join(err, cleanupErr))
	}
	if strings.TrimSpace(sandbox.WorkspaceID) == "" {
		sandbox.WorkspaceID = strings.TrimSpace(staged.WorkspaceID)
	}
	if err := domain.TransitionSandboxWorkspaceProvisioning(
		sandbox,
		domain.SandboxWorkspaceProvisioningStatusReady,
	); err != nil {
		return err
	}
	if err := p.sandboxes.UpdateSandbox(ctx, sandbox); err != nil {
		return fmt.Errorf("persist ready workspace provisioning for sandbox %s: %w", sandbox.Summary.ID, err)
	}
	return nil
}

func (p *Provisioner) createProvisioningAttempt(paths provisioningPaths) (string, error) {
	if err := p.requireProvisioningDirectory(paths.sandboxRoot, "sandbox root"); err != nil {
		return "", err
	}
	if err := p.ensureProvisioningDirectory(paths.stateRoot, "sandbox state root"); err != nil {
		return "", err
	}
	if err := p.ensureProvisioningDirectory(paths.stagingRoot, "workspace provisioning staging root"); err != nil {
		return "", err
	}
	entries, err := p.filesystem.ReadDir(paths.stagingRoot)
	if err != nil {
		return "", fmt.Errorf("read workspace provisioning staging root %s: %w", paths.stagingRoot, err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), workspaceProvisioningAttempt) {
			continue
		}
		attemptPath := filepath.Join(paths.stagingRoot, entry.Name())
		if err := p.filesystem.RemoveAll(attemptPath); err != nil {
			return "", fmt.Errorf("remove stale workspace provisioning attempt %s: %w", attemptPath, err)
		}
	}
	attemptPath, err := p.filesystem.MkdirTemp(paths.stagingRoot, workspaceProvisioningAttempt)
	if err != nil {
		return "", fmt.Errorf("create workspace provisioning attempt in %s: %w", paths.stagingRoot, err)
	}
	return attemptPath, nil
}

func (p *Provisioner) promoteProvisioningAttempt(attemptPath, workspacePath string) error {
	workspacePath = filepath.Clean(strings.TrimSpace(workspacePath))
	if err := p.filesystem.RemoveAll(workspacePath); err != nil {
		return fmt.Errorf("remove incomplete workspace %s before promotion: %w", workspacePath, err)
	}
	if err := p.filesystem.Rename(attemptPath, workspacePath); err != nil {
		return fmt.Errorf("promote workspace provisioning attempt %s to %s: %w", attemptPath, workspacePath, err)
	}
	return nil
}

func (p *Provisioner) cleanupProvisioningAttempt(attemptPath string) error {
	if strings.TrimSpace(attemptPath) == "" {
		return nil
	}
	if err := p.filesystem.RemoveAll(attemptPath); err != nil {
		return fmt.Errorf("cleanup workspace provisioning attempt %s: %w", attemptPath, err)
	}
	return nil
}

func (p *Provisioner) persistProvisioningFailure(ctx context.Context, sandbox *domain.Sandbox, cause error) error {
	result := cause
	if err := domain.TransitionSandboxWorkspaceProvisioning(
		sandbox,
		domain.SandboxWorkspaceProvisioningStatusFailed,
	); err != nil {
		return errors.Join(result, fmt.Errorf("transition sandbox %s workspace provisioning to failed: %w", sandbox.Summary.ID, err))
	}
	if err := p.sandboxes.UpdateSandbox(ctx, sandbox); err != nil {
		result = errors.Join(result, fmt.Errorf("persist failed workspace provisioning for sandbox %s: %w", sandbox.Summary.ID, err))
	}
	return result
}

type provisioningPaths struct {
	sandboxRoot string
	workspace   string
	stateRoot   string
	stagingRoot string
}

func (p *Provisioner) resolveProvisioningPaths(sandbox *domain.Sandbox) (provisioningPaths, error) {
	if p.paths == nil {
		return provisioningPaths{}, fmt.Errorf("%w: sandbox path resolver is required", domain.ErrRequired)
	}
	rawSandboxRoot := strings.TrimSpace(p.paths.SandboxDir(sandbox.Summary.ID))
	if rawSandboxRoot == "" {
		return provisioningPaths{}, fmt.Errorf("%w: sandbox %s root is required", domain.ErrRequired, sandbox.Summary.ID)
	}
	sandboxRoot, err := filepath.Abs(filepath.Clean(rawSandboxRoot))
	if err != nil {
		return provisioningPaths{}, fmt.Errorf("%w: resolve sandbox %s root: %v", domain.ErrInvalidArgument, sandbox.Summary.ID, err)
	}
	rawWorkspacePath := strings.TrimSpace(sandbox.Summary.WorkspacePath)
	if rawWorkspacePath == "" {
		return provisioningPaths{}, fmt.Errorf("%w: sandbox %s workspace path is required", domain.ErrRequired, sandbox.Summary.ID)
	}
	workspacePath, err := filepath.Abs(filepath.Clean(rawWorkspacePath))
	if err != nil {
		return provisioningPaths{}, fmt.Errorf("%w: resolve sandbox %s workspace path: %v", domain.ErrInvalidArgument, sandbox.Summary.ID, err)
	}
	expectedWorkspace := filepath.Join(sandboxRoot, "workspace")
	if workspacePath != expectedWorkspace {
		return provisioningPaths{}, fmt.Errorf(
			"%w: sandbox %s workspace path %q is outside authoritative path %q",
			domain.ErrInvalidArgument,
			sandbox.Summary.ID,
			workspacePath,
			expectedWorkspace,
		)
	}
	stateRoot := filepath.Join(sandboxRoot, "state")
	return provisioningPaths{
		sandboxRoot: sandboxRoot,
		workspace:   expectedWorkspace,
		stateRoot:   stateRoot,
		stagingRoot: filepath.Join(stateRoot, workspaceProvisioningStateDir),
	}, nil
}

func (p *Provisioner) requireProvisioningDirectory(path, label string) error {
	info, err := p.filesystem.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s %s: %w", label, path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s %s is not a real directory", domain.ErrInvalidArgument, label, path)
	}
	return nil
}

func (p *Provisioner) ensureProvisioningDirectory(path, label string) error {
	info, err := p.filesystem.Lstat(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("inspect %s %s: %w", label, path, err)
		}
		if err := p.filesystem.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("create %s %s: %w", label, path, err)
		}
		info, err = p.filesystem.Lstat(path)
		if err != nil {
			return fmt.Errorf("inspect created %s %s: %w", label, path, err)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s %s is not a real directory", domain.ErrInvalidArgument, label, path)
	}
	return nil
}

func cloneSandboxForProvisioning(sandbox *domain.Sandbox) *domain.Sandbox {
	clone := *sandbox
	clone.Summary.Tags = append([]domain.SandboxTag(nil), sandbox.Summary.Tags...)
	if sandbox.Workspace != nil {
		workspace := *sandbox.Workspace
		clone.Workspace = &workspace
	}
	if sandbox.WorkspaceProvisioning != nil {
		provisioning := *sandbox.WorkspaceProvisioning
		clone.WorkspaceProvisioning = &provisioning
	}
	clone.EnvItems = append([]domain.SandboxEnvVar(nil), sandbox.EnvItems...)
	clone.VolumeMounts = append([]domain.SandboxVolumeMount(nil), sandbox.VolumeMounts...)
	clone.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.RuntimeEnvItems...)
	clone.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.ProviderEnvItems...)
	clone.ExecutionProviderEnvItems = append([]domain.SandboxEnvVar(nil), sandbox.ExecutionProviderEnvItems...)
	return &clone
}
