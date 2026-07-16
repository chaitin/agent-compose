package workspaces

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"

	"golang.org/x/sync/singleflight"
)

type SandboxStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
	UpdateSandbox(context.Context, *domain.Sandbox) error
}

type SandboxPathResolver interface {
	SandboxDir(string) string
}

type WorkspaceMaterializer interface {
	Materialize(context.Context, *domain.Sandbox) error
}

// WorkspaceEnsurer is the lifecycle-facing workspace dependency. Callers use
// this narrow interface instead of reaching provider materializers directly.
type WorkspaceEnsurer interface {
	Ensure(context.Context, *domain.Sandbox) error
}

type Provisioner struct {
	sandboxes    SandboxStore
	paths        SandboxPathResolver
	materializer WorkspaceMaterializer
	filesystem   provisioningFileSystem
	group        singleflight.Group
}

var _ WorkspaceEnsurer = (*Provisioner)(nil)

func NewProvisioner(config *appconfig.Config, workspaces WorkspaceConfigStore, sandboxes SandboxStore) *Provisioner {
	return NewProvisionerWithMaterializer(sandboxes, sessionWorkspaceMaterializer{
		config:     config,
		workspaces: workspaces,
	})
}

func NewProvisionerWithMaterializer(sandboxes SandboxStore, materializer WorkspaceMaterializer) *Provisioner {
	paths, _ := sandboxes.(SandboxPathResolver)
	return &Provisioner{
		sandboxes:    sandboxes,
		paths:        paths,
		materializer: materializer,
		filesystem:   osProvisioningFileSystem{},
	}
}

func (p *Provisioner) Ensure(ctx context.Context, sandbox *domain.Sandbox) error {
	if p == nil {
		return fmt.Errorf("%w: workspace provisioner is nil", domain.ErrInvalidArgument)
	}
	if sandbox == nil {
		return fmt.Errorf("%w: sandbox is nil", domain.ErrInvalidArgument)
	}
	sandboxID := strings.TrimSpace(sandbox.Summary.ID)
	if sandboxID == "" {
		return fmt.Errorf("%w: sandbox id is required", domain.ErrRequired)
	}
	if p.sandboxes == nil {
		return fmt.Errorf("%w: sandbox store is required", domain.ErrRequired)
	}

	result := p.group.DoChan(sandboxID, func() (any, error) {
		loaded, err := p.sandboxes.GetSandbox(ctx, sandboxID)
		if err != nil {
			return nil, fmt.Errorf("reload sandbox %s before workspace provisioning: %w", sandboxID, err)
		}
		if err := validateProvisioningSandbox(loaded, sandboxID); err != nil {
			return nil, err
		}
		return nil, p.ensureLoaded(ctx, loaded)
	})
	var sharedErr error
	select {
	case <-ctx.Done():
		return ctx.Err()
	case completed := <-result:
		sharedErr = completed.Err
	}

	loaded, err := p.sandboxes.GetSandbox(ctx, sandboxID)
	if err != nil {
		reloadErr := fmt.Errorf("reload sandbox %s after workspace provisioning: %w", sandboxID, err)
		return errors.Join(sharedErr, reloadErr)
	}
	if err := validateProvisioningSandbox(loaded, sandboxID); err != nil {
		return errors.Join(sharedErr, err)
	}
	domain.RestoreSandboxTransientFields(loaded, sandbox)
	*sandbox = *loaded
	return sharedErr
}

func (p *Provisioner) ensureLoaded(ctx context.Context, sandbox *domain.Sandbox) error {
	if domain.SandboxWorkspaceUnavailable(sandbox) {
		return domain.ClassifyError(domain.ErrFailedPrecondition, "sandbox workspace was reclaimed and cannot be provisioned", nil)
	}
	if !sandboxHasWorkspace(sandbox) {
		return nil
	}
	if strings.TrimSpace(sandbox.Summary.WorkspacePath) == "" {
		return fmt.Errorf("%w: sandbox %s workspace path is required", domain.ErrRequired, sandbox.Summary.ID)
	}
	if sandbox.WorkspaceProvisioning == nil {
		sandbox.WorkspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
			Version:   domain.SandboxWorkspaceProvisioningVersion,
			Status:    domain.SandboxWorkspaceProvisioningStatusReady,
			UpdatedAt: time.Now().UTC(),
		}
		return p.sandboxes.UpdateSandbox(ctx, sandbox)
	}
	if err := domain.ValidateSandboxWorkspaceProvisioning(sandbox.WorkspaceProvisioning); err != nil {
		return err
	}

	switch sandbox.WorkspaceProvisioning.Status {
	case domain.SandboxWorkspaceProvisioningStatusReady:
		return nil
	case domain.SandboxWorkspaceProvisioningStatusFailed:
		if err := domain.TransitionSandboxWorkspaceProvisioning(
			sandbox,
			domain.SandboxWorkspaceProvisioningStatusPending,
		); err != nil {
			return err
		}
		if err := p.sandboxes.UpdateSandbox(ctx, sandbox); err != nil {
			return fmt.Errorf("persist pending workspace provisioning for sandbox %s: %w", sandbox.Summary.ID, err)
		}
	case domain.SandboxWorkspaceProvisioningStatusPending:
		// Continue below.
	default:
		return fmt.Errorf(
			"%w: unsupported workspace provisioning status %q",
			domain.ErrInvalidArgument,
			sandbox.WorkspaceProvisioning.Status,
		)
	}

	if p.materializer == nil {
		return fmt.Errorf("%w: workspace materializer is required", domain.ErrRequired)
	}
	return p.provisionPending(ctx, sandbox)
}

func validateProvisioningSandbox(sandbox *domain.Sandbox, expectedID string) error {
	if sandbox == nil {
		return fmt.Errorf("%w: sandbox store returned nil sandbox", domain.ErrInvalidArgument)
	}
	if sandboxID := strings.TrimSpace(sandbox.Summary.ID); sandboxID == "" {
		return fmt.Errorf("%w: persisted sandbox id is required", domain.ErrRequired)
	} else if sandboxID != expectedID {
		return fmt.Errorf(
			"%w: sandbox store returned id %q for %q",
			domain.ErrInvalidArgument,
			sandboxID,
			expectedID,
		)
	}
	return nil
}

func sandboxHasWorkspace(sandbox *domain.Sandbox) bool {
	return sandbox.Workspace != nil || strings.TrimSpace(sandbox.WorkspaceID) != ""
}

type sessionWorkspaceMaterializer struct {
	config     *appconfig.Config
	workspaces WorkspaceConfigStore
}

func (m sessionWorkspaceMaterializer) Materialize(ctx context.Context, sandbox *domain.Sandbox) error {
	return materializeSessionWorkspace(ctx, m.config, m.workspaces, sandbox)
}
