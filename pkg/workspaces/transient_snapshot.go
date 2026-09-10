package workspaces

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/google/uuid"
)

const transientSnapshotDirectory = "workspace-snapshots"

type transientSnapshotOwner struct {
	Version     int       `json:"version"`
	SnapshotID  string    `json:"snapshot_id"`
	WorkspaceID string    `json:"workspace_id"`
	SandboxID   string    `json:"sandbox_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	Released    bool      `json:"released,omitempty"`
}

// CreateTransientFileSnapshot allocates a generation separate from Settings
// presets. Its marker is written before content, so interrupted copies remain
// identifiable without treating directory names as evidence of ownership.
func CreateTransientFileSnapshot(ctx context.Context, config *appconfig.Config, workspace domain.WorkspaceConfig) (domain.WorkspaceConfig, error) {
	if err := ctx.Err(); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	if workspace.Type != "file" || workspace.SnapshotID != "" || workspace.SnapshotLease != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("transient snapshots require a fresh file workspace")
	}
	if _, err := FileWorkspaceContentRoot(config, workspace); err != nil {
		return domain.WorkspaceConfig{}, err
	}
	root, err := openTransientSnapshotRoot(config)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	defer func() { _ = root.Close() }()
	workspace.SnapshotID = uuid.NewString()
	lease, acquired, err := acquireTransientSnapshotLease(root, workspace.SnapshotID, true)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	if !acquired {
		return domain.WorkspaceConfig{}, fmt.Errorf("fresh snapshot lease is busy")
	}
	workspace.SnapshotLease = lease
	created := false
	defer func() {
		if !created {
			_ = lease.Close()
			_ = root.Remove(workspace.SnapshotID + ".lease")
		}
	}()

	owner := transientSnapshotOwner{Version: 1, SnapshotID: workspace.SnapshotID, WorkspaceID: workspace.ID, CreatedAt: time.Now().UTC()}
	payload, err := json.Marshal(owner)
	if err != nil {
		return domain.WorkspaceConfig{}, err
	}
	marker, err := root.OpenFile(workspace.SnapshotID+".json", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("create transient snapshot ownership: %w", err)
	}
	_, writeErr := marker.Write(payload)
	if err := errors.Join(writeErr, marker.Close()); err != nil {
		_ = root.Remove(workspace.SnapshotID + ".json")
		return domain.WorkspaceConfig{}, fmt.Errorf("write transient snapshot ownership: %w", err)
	}
	for _, directory := range []string{workspace.SnapshotID, filepath.Join(workspace.SnapshotID, FileWorkspaceContentDirName)} {
		if err := EnsureRootDir(root, directory); err != nil {
			cleanup := ReleaseTransientSnapshot(context.WithoutCancel(ctx), config, &domain.SandboxWorkspace{ID: workspace.ID, Type: "file", SnapshotID: workspace.SnapshotID, SnapshotLease: workspace.SnapshotLease})
			return domain.WorkspaceConfig{}, errors.Join(err, cleanup)
		}
	}
	created = true
	return workspace, nil
}

func openTransientSnapshotRoot(config *appconfig.Config) (*os.Root, error) {
	root, err := OpenFileWorkspaceDataRoot(config)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if err := EnsureRootDir(root, transientSnapshotDirectory); err != nil {
		return nil, err
	}
	return root.OpenRoot(transientSnapshotDirectory)
}

func readTransientSnapshotOwner(root *os.Root, snapshotID, workspaceID string) (transientSnapshotOwner, error) {
	parsed, err := uuid.Parse(snapshotID)
	if err != nil || parsed.String() != snapshotID {
		return transientSnapshotOwner{}, fmt.Errorf("invalid transient snapshot id %q", snapshotID)
	}
	info, err := root.Lstat(snapshotID + ".json")
	if err != nil {
		return transientSnapshotOwner{}, err
	}
	if !info.Mode().IsRegular() {
		return transientSnapshotOwner{}, fmt.Errorf("transient snapshot ownership must be a regular file")
	}
	payload, err := root.ReadFile(snapshotID + ".json")
	if err != nil {
		return transientSnapshotOwner{}, err
	}
	var owner transientSnapshotOwner
	if err := json.Unmarshal(payload, &owner); err != nil {
		return transientSnapshotOwner{}, fmt.Errorf("decode transient snapshot ownership: %w", err)
	}
	if owner.Version != 1 || owner.SnapshotID != snapshotID || owner.WorkspaceID == "" || owner.CreatedAt.IsZero() || (workspaceID != "" && owner.WorkspaceID != workspaceID) {
		return transientSnapshotOwner{}, fmt.Errorf("transient snapshot ownership does not match workspace")
	}
	generation, err := root.Lstat(snapshotID)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return transientSnapshotOwner{}, err
	}
	if err != nil || !generation.IsDir() || generation.Mode()&os.ModeSymlink != 0 {
		return owner, nil
	}
	claim := filepath.Join(snapshotID, "consumer")
	if info, err := root.Lstat(claim); err == nil {
		if !info.Mode().IsRegular() {
			return transientSnapshotOwner{}, fmt.Errorf("snapshot consumer must be a regular file")
		}
		consumer, err := root.ReadFile(claim)
		if err != nil {
			return transientSnapshotOwner{}, err
		}
		if len(consumer) == 0 || (owner.SandboxID != "" && owner.SandboxID != string(consumer)) {
			return transientSnapshotOwner{}, fmt.Errorf("snapshot consumer does not match ownership")
		}
		owner.SandboxID = string(consumer)
	} else if !errors.Is(err, os.ErrNotExist) {
		return transientSnapshotOwner{}, err
	}
	return owner, nil
}

func openTransientFileContent(config *appconfig.Config, workspace domain.WorkspaceConfig) (FileWorkspaceContent, error) {
	if workspace.Type != "file" {
		return FileWorkspaceContent{}, fmt.Errorf("transient snapshot requires a file workspace")
	}
	if mount, err := DecodeFileWorkspaceMount(workspace.ConfigJSON); err != nil {
		return FileWorkspaceContent{}, err
	} else if mount != nil {
		return FileWorkspaceContent{}, fmt.Errorf("mount workspace cannot reference a transient snapshot")
	}
	root, err := openTransientSnapshotRoot(config)
	if err != nil {
		return FileWorkspaceContent{}, err
	}
	defer func() { _ = root.Close() }()
	if _, err := readTransientSnapshotOwner(root, workspace.SnapshotID, workspace.ID); err != nil {
		return FileWorkspaceContent{}, err
	}
	rel := filepath.Join(workspace.SnapshotID, FileWorkspaceContentDirName)
	for _, directory := range []string{workspace.SnapshotID, rel} {
		info, err := root.Lstat(directory)
		if err != nil {
			return FileWorkspaceContent{}, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return FileWorkspaceContent{}, fmt.Errorf("transient snapshot content must be a real directory")
		}
	}
	content, err := root.OpenRoot(rel)
	if err != nil {
		return FileWorkspaceContent{}, err
	}
	abs, err := filepath.Abs(filepath.Join(config.DataRoot, transientSnapshotDirectory, rel))
	if err != nil {
		_ = content.Close()
		return FileWorkspaceContent{}, err
	}
	return FileWorkspaceContent{Root: content, AbsRoot: abs, RelRoot: filepath.Join(transientSnapshotDirectory, rel)}, nil
}

// ReleaseTransientSnapshot is idempotent and only removes a matching internal
// ownership record and its generation. Settings presets have no SnapshotID.
func ReleaseTransientSnapshot(ctx context.Context, config *appconfig.Config, workspace *domain.SandboxWorkspace) error {
	if workspace == nil || workspace.SnapshotID == "" {
		return nil
	}
	if err := CloseTransientSnapshotLease(workspace); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if workspace.Type != "file" {
		return fmt.Errorf("transient snapshot belongs to a non-file workspace")
	}
	root, err := openTransientSnapshotRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	// UUID generations are never reused. GC only opens existing lock inodes;
	// removing the marker before the lease file prevents lock-path recreation.
	lease, acquired, err := acquireTransientSnapshotLease(root, workspace.SnapshotID, false)
	if err != nil {
		return err
	}
	if !acquired {
		return nil
	}
	defer func() { _ = lease.Close() }()
	owner, err := readTransientSnapshotOwner(root, workspace.SnapshotID, workspace.ID)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return releaseTransientSnapshotLocked(root, owner)
}

func releaseTransientSnapshotLocked(root *os.Root, owner transientSnapshotOwner) error {
	owner.Released = true
	if info, statErr := root.Lstat(owner.SnapshotID); statErr == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
		if err := writeTransientSnapshotOwner(root, owner); err != nil {
			return err
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := removeOwnedRootDirectory(root, owner.SnapshotID); err != nil {
		return fmt.Errorf("remove transient snapshot content: %w", err)
	}
	if err := root.Remove(owner.SnapshotID + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Remove(owner.SnapshotID + ".lease"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// WorkspaceForConfigurationHash excludes physical generation identity while
// retaining all semantic workspace fields, including the logical workspace ID.
func WorkspaceForConfigurationHash(workspace *domain.SandboxWorkspace) *domain.SandboxWorkspace {
	if workspace == nil {
		return nil
	}
	copy := *workspace
	copy.SnapshotID = ""
	copy.SnapshotLease = nil
	return &copy
}

func transientSnapshotContentRoot(config *appconfig.Config, id string) (string, error) {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed.String() != id {
		return "", fmt.Errorf("invalid transient snapshot id %q", id)
	}
	return filepath.Abs(filepath.Join(config.DataRoot, transientSnapshotDirectory, id, FileWorkspaceContentDirName))
}

func writeTransientSnapshotOwner(root *os.Root, owner transientSnapshotOwner) error {
	payload, err := json.Marshal(owner)
	if err != nil {
		return err
	}
	temporary := filepath.Join(owner.SnapshotID, ".owner-"+uuid.NewString())
	defer func() { _ = root.Remove(temporary) }()
	if err := root.WriteFile(temporary, payload, 0o600); err != nil {
		return err
	}
	return root.Rename(temporary, owner.SnapshotID+".json")
}

// ClaimTransientSnapshot records the single consumer before sandbox metadata is
// written. A later create error can then resolve the precise durable record,
// including a record that has not yet reached the sandbox index.
func ClaimTransientSnapshot(ctx context.Context, config *appconfig.Config, workspace *domain.SandboxWorkspace, sandboxID string) error {
	if workspace == nil || workspace.SnapshotID == "" {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if sandboxID == "" {
		return fmt.Errorf("transient snapshot consumer is required")
	}
	root, err := openTransientSnapshotRoot(config)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	owner, err := readTransientSnapshotOwner(root, workspace.SnapshotID, workspace.ID)
	if err != nil {
		return err
	}
	if owner.Released || (owner.SandboxID != "" && owner.SandboxID != sandboxID) {
		return fmt.Errorf("transient snapshot already has a consumer")
	}
	// Publish the immutable consumer atomically. Hardlinks here only publish a
	// tiny ownership record, never writable workspace content.
	temporary := filepath.Join(workspace.SnapshotID, ".claim-"+uuid.NewString())
	defer func() { _ = root.Remove(temporary) }()
	if err := root.WriteFile(temporary, []byte(sandboxID), 0o600); err != nil {
		return err
	}
	if err := root.Link(temporary, filepath.Join(workspace.SnapshotID, "consumer")); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
		current, readErr := readTransientSnapshotOwner(root, workspace.SnapshotID, workspace.ID)
		if readErr != nil {
			return readErr
		}
		if current.SandboxID != sandboxID {
			return fmt.Errorf("transient snapshot already has a consumer")
		}
	}
	return nil
}
