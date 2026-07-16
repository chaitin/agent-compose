package sessions

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"agent-compose/pkg/cleanup"
	domain "agent-compose/pkg/model"
)

type WorkspaceCleanupStore interface {
	ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error)
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
	GetVMState(string) (domain.VMState, error)
	UpdateSandbox(context.Context, *domain.Sandbox) error
	AddEvent(context.Context, string, domain.SandboxEvent) error
	SandboxDir(string) string
}

type WorkspaceCleaner struct {
	Store WorkspaceCleanupStore
	Locks *LifecycleLocks
	Now   func() time.Time
}

func (c *WorkspaceCleaner) Name() string { return "sandbox-workspace" }

func (c *WorkspaceCleaner) Clean(ctx context.Context, cutoff time.Time) (cleanup.Result, error) {
	if c == nil || c.Store == nil {
		return cleanup.Result{}, fmt.Errorf("workspace cleaner store is not configured")
	}
	listed, err := c.Store.ListSandboxes(ctx, domain.SandboxListOptions{Limit: 1 << 30})
	if err != nil {
		return cleanup.Result{}, err
	}
	result := cleanup.Result{}
	var joined error
	for _, sandbox := range listed.Sandboxes {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(joined, err)
		}
		matched, removed, err := c.cleanSandbox(ctx, sandbox.Summary.ID, cutoff)
		if err != nil {
			if matched {
				result.Matched++
			}
			result.Failed++
			joined = errors.Join(joined, fmt.Errorf("reclaim workspace for sandbox %s: %w", sandbox.Summary.ID, err))
			continue
		}
		if !matched {
			result.Skipped++
			continue
		}
		result.Matched++
		if removed {
			result.Removed++
		}
	}
	return result, joined
}

func (c *WorkspaceCleaner) cleanSandbox(ctx context.Context, sandboxID string, cutoff time.Time) (bool, bool, error) {
	unlock := c.Locks.Lock(sandboxID)
	defer unlock()

	sandbox, err := c.Store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return false, false, err
	}
	if sandbox.WorkspaceReclamation != nil && sandbox.WorkspaceReclamation.State == domain.SandboxWorkspaceReclamationStateReclaimed {
		return false, false, nil
	}
	retrying := sandbox.WorkspaceReclamation != nil && sandbox.WorkspaceReclamation.State == domain.SandboxWorkspaceReclamationStateReclaiming
	if sandbox.WorkspaceReclamation != nil && !retrying {
		return false, false, fmt.Errorf("unknown workspace reclamation state %q", sandbox.WorkspaceReclamation.State)
	}
	if !retrying {
		eligibleAt, ok, err := c.workspaceEligibleAt(sandbox)
		if err != nil || !ok || eligibleAt.After(cutoff) {
			return false, false, err
		}
		if _, err := c.safeWorkspacePath(sandbox); err != nil {
			return true, false, err
		}
		now := c.now()
		sandbox.WorkspaceReclamation = &domain.SandboxWorkspaceReclamation{
			State: domain.SandboxWorkspaceReclamationStateReclaiming, StartedAt: now,
		}
		if err := c.Store.UpdateSandbox(ctx, sandbox); err != nil {
			return true, false, fmt.Errorf("persist reclamation intent: %w", err)
		}
	}

	// Validate again after persisting intent so an external path replacement
	// cannot turn the destructive operation into an unsafe deletion.
	workspacePath, err := c.safeWorkspacePath(sandbox)
	if err == nil {
		err = os.RemoveAll(workspacePath)
	}
	if err != nil {
		sandbox.WorkspaceReclamation.LastError = err.Error()
		// The persisted reclaiming intent remains the recovery boundary even if
		// recording the latest retry error also fails.
		_ = c.Store.UpdateSandbox(ctx, sandbox)
		return true, false, err
	}
	sandbox.WorkspaceReclamation.State = domain.SandboxWorkspaceReclamationStateReclaimed
	sandbox.WorkspaceReclamation.CompletedAt = c.now()
	sandbox.WorkspaceReclamation.LastError = ""
	if err := c.Store.UpdateSandbox(ctx, sandbox); err != nil {
		return true, false, fmt.Errorf("persist reclaimed workspace: %w", err)
	}
	// Reclamation state is the durable audit record; an event append failure
	// must not make a completed, irreversible deletion retry as available.
	_ = c.Store.AddEvent(ctx, sandboxID, domain.SandboxEvent{
		ID: uuid.NewString(), Type: "sandbox.workspace_reclaimed", Level: "info",
		Message: "sandbox workspace was reclaimed by retention policy", CreatedAt: c.now(),
	})
	return true, true, nil
}

func (c *WorkspaceCleaner) workspaceEligibleAt(sandbox *domain.Sandbox) (time.Time, bool, error) {
	if sandbox == nil {
		return time.Time{}, false, nil
	}
	if sandbox.Summary.VMStatus != domain.VMStatusStopped {
		return time.Time{}, false, nil
	}
	vmState, err := c.Store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return time.Time{}, false, err
	}
	if vmState.StoppedAt.IsZero() {
		return time.Time{}, false, nil
	}
	if !vmState.StartedAt.IsZero() && vmState.StoppedAt.Before(vmState.StartedAt) {
		return time.Time{}, false, nil
	}
	if !vmState.StartAttemptedAt.IsZero() && vmState.StoppedAt.Before(vmState.StartAttemptedAt) {
		return time.Time{}, false, nil
	}
	return vmState.StoppedAt.UTC(), true, nil
}

func (c *WorkspaceCleaner) safeWorkspacePath(sandbox *domain.Sandbox) (string, error) {
	expected, err := filepath.Abs(filepath.Join(c.Store.SandboxDir(sandbox.Summary.ID), "workspace"))
	if err != nil {
		return "", err
	}
	actual, err := filepath.Abs(strings.TrimSpace(sandbox.Summary.WorkspacePath))
	if err != nil {
		return "", err
	}
	if actual != expected {
		return "", fmt.Errorf("workspace path %q is outside its authoritative sandbox", actual)
	}
	info, err := os.Lstat(actual)
	if os.IsNotExist(err) {
		return actual, nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("workspace path %q is not a safe directory", actual)
	}
	return actual, nil
}

func (c *WorkspaceCleaner) now() time.Time {
	if c.Now != nil {
		return c.Now().UTC()
	}
	return time.Now().UTC()
}
