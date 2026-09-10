package workspaces

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/cleanup"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type SnapshotSandboxReader interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
}

// ReleaseUnusedTransientSnapshot runs after creation/preparation has returned.
// The ownership marker also covers partially committed, not-yet-indexed creates.
func ReleaseUnusedTransientSnapshot(ctx context.Context, config *appconfig.Config, store SnapshotSandboxReader, workspace *domain.SandboxWorkspace) error {
	if workspace == nil || workspace.SnapshotID == "" {
		return nil
	}
	if err := CloseTransientSnapshotLease(workspace); err != nil {
		return err
	}
	root, err := openTransientSnapshotRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	owner, err := readTransientSnapshotOwner(root, workspace.SnapshotID, workspace.ID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if owner.SandboxID != "" {
		if store == nil {
			return fmt.Errorf("sandbox reference reader is required")
		}
		sandbox, err := store.GetSandbox(ctx, owner.SandboxID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && snapshotRequiredBySandbox(sandbox, workspace.SnapshotID) {
			return nil
		}
	}
	return ReleaseTransientSnapshot(ctx, config, workspace)
}

func snapshotRequiredBySandbox(sandbox *domain.Sandbox, snapshotID string) bool {
	if sandbox == nil || sandbox.Workspace == nil || sandbox.Workspace.SnapshotID != snapshotID {
		return false
	}
	return sandbox.WorkspaceProvisioning == nil || domain.ValidateSandboxWorkspaceProvisioning(sandbox.WorkspaceProvisioning) != nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady
}

// TransientSnapshotCleaner only collects generations whose active preparation
// lease can be acquired, then reloads authoritative sandbox metadata.
type TransientSnapshotCleaner struct {
	Config *appconfig.Config
	Store  SnapshotSandboxReader
}

func (*TransientSnapshotCleaner) Name() string { return "transient workspace snapshots" }

func (c *TransientSnapshotCleaner) Clean(ctx context.Context, _ time.Time) (cleanup.Result, error) {
	var result cleanup.Result
	if c.Config == nil || c.Store == nil {
		return result, fmt.Errorf("snapshot cleaner dependencies are required")
	}
	if _, err := os.Lstat(filepath.Join(c.Config.DataRoot, transientSnapshotDirectory)); errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	root, err := openTransientSnapshotRoot(c.Config)
	if err != nil {
		return result, err
	}
	defer func() { _ = root.Close() }()
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return result, err
	}
	if len(entries) == 0 {
		return result, nil
	}
	var failures []error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, errors.Join(append(failures, err)...)
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")

		removed, err := c.cleanGeneration(ctx, root, id)
		if err != nil {
			result.Failed++
			failures = append(failures, err)
			continue
		}
		if removed {
			result.Matched++
			result.Removed++
		} else {
			result.Skipped++
		}

	}
	return result, errors.Join(failures...)
}

// SnapshotAccessoryReleaser participates in the existing deletion journal's
// accessory stage, after the runtime is removed and before sandbox data is
// deleted. Failed/pending generations are no longer needed at that point.
type SnapshotAccessoryReleaser struct {
	Config *appconfig.Config
	Store  SandboxStore
}

func (r SnapshotAccessoryReleaser) ReleaseSandboxResources(ctx context.Context, sandboxID string) error {
	sandbox, err := r.Store.GetSandbox(ctx, sandboxID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return ReleaseTransientSnapshot(ctx, r.Config, sandbox.Workspace)
}

func (c *TransientSnapshotCleaner) cleanGeneration(ctx context.Context, root *os.Root, id string) (bool, error) {
	lease, acquired, err := acquireTransientSnapshotLease(root, id, false)
	if err != nil {
		return false, err
	}
	if !acquired {
		return false, nil
	}
	defer func() { _ = lease.Close() }()
	owner, err := readTransientSnapshotOwner(root, id, "")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if owner.SandboxID != "" {
		sandbox, err := c.Store.GetSandbox(ctx, owner.SandboxID)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
		if err == nil && snapshotRequiredBySandbox(sandbox, id) {
			return false, nil
		}
	}
	if err := releaseTransientSnapshotLocked(root, owner); err != nil {
		return false, err
	}
	return true, nil
}
