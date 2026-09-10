package workspaces

import (
	"context"
	"fmt"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ensureMounted prepares only sandbox-owned directories. External sources never
// pass through copy staging, promotion, or recursive cleanup.
func (p *Provisioner) ensureMounted(ctx context.Context, sandbox *domain.Sandbox, mount FileWorkspaceMountConfig) error {
	if err := ValidateWorkspaceRuntimeDriver(sandbox.Workspace, sandbox.Summary.Driver); err != nil {
		return err
	}
	if sandbox.WorkspaceProvisioning == nil {
		sandbox.WorkspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    domain.SandboxWorkspaceProvisioningStatusPending,
			UpdatedAt: time.Now().UTC(),
		}
		if err := p.sandboxes.UpdateSandbox(ctx, sandbox); err != nil {
			return fmt.Errorf("persist pending mounted workspace: %w", err)
		}
	}
	if err := domain.ValidateSandboxWorkspaceProvisioning(sandbox.WorkspaceProvisioning); err != nil {
		return err
	}
	if sandbox.WorkspaceProvisioning.Status == domain.SandboxWorkspaceProvisioningStatusReady {
		// Readiness persists, but the caller may have moved or deleted the live
		// source since the last runtime was created. Never replace it with copy.
		return p.prepareMountedWorkspace(ctx, sandbox, mount)
	}
	if sandbox.WorkspaceProvisioning.Status == domain.SandboxWorkspaceProvisioningStatusFailed {
		if err := domain.TransitionSandboxWorkspaceProvisioning(sandbox, domain.SandboxWorkspaceProvisioningStatusPending); err != nil {
			return err
		}
		if err := p.sandboxes.UpdateSandbox(ctx, sandbox); err != nil {
			return fmt.Errorf("persist mounted workspace retry: %w", err)
		}
	}
	if err := p.prepareMountedWorkspace(ctx, sandbox, mount); err != nil {
		return p.persistProvisioningFailure(ctx, sandbox, err)
	}
	if err := domain.TransitionSandboxWorkspaceProvisioning(sandbox, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
		return err
	}
	if err := p.sandboxes.UpdateSandbox(ctx, sandbox); err != nil {
		return fmt.Errorf("persist ready mounted workspace: %w", err)
	}
	return nil
}

func (p *Provisioner) prepareMountedWorkspace(ctx context.Context, sandbox *domain.Sandbox, mount FileWorkspaceMountConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := ValidateFileWorkspaceMount(mount); err != nil {
		return err
	}
	paths, err := p.resolveProvisioningPaths(sandbox)
	if err != nil {
		return err
	}
	if err := p.requireProvisioningDirectory(paths.sandboxRoot, "sandbox root"); err != nil {
		return err
	}
	if err := p.ensureProvisioningDirectory(paths.workspace, "sandbox workspace"); err != nil {
		return err
	}
	return ctx.Err()
}
