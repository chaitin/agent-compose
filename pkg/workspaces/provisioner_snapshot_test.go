package workspaces

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type snapshotReadyFailureStore struct {
	*provisionerStagingStore
	failReady  bool
	sourcePath string
}

func (s *snapshotReadyFailureStore) UpdateSandbox(ctx context.Context, sandbox *domain.Sandbox) error {
	if sandbox.WorkspaceProvisioning.Status == domain.SandboxWorkspaceProvisioningStatusReady {
		if _, err := os.Stat(s.sourcePath); err != nil {
			return errors.Join(errors.New("snapshot released before Ready persistence"), err)
		}
		if s.failReady {
			return os.ErrPermission
		}
	}
	return s.provisionerStagingStore.UpdateSandbox(ctx, sandbox)
}

func TestProvisionerSnapshotReleaseRequiresDurableReadyAndPreservesRetry(t *testing.T) {
	ctx := context.Background()
	config := &appconfig.Config{DataRoot: t.TempDir()}
	_, workspace, source := newTransientSnapshotFixture(t, config)
	sandboxRoot := t.TempDir()
	sandbox := newProvisionerStagingSandbox("snapshot-ready", filepath.Join(sandboxRoot, "workspace"), domain.SandboxWorkspaceProvisioningStatusPending)
	sandbox.Workspace = workspace
	sandbox.WorkspaceID = workspace.ID
	if err := ClaimTransientSnapshot(ctx, config, workspace, sandbox.Summary.ID); err != nil {
		t.Fatal(err)
	}
	store := &snapshotReadyFailureStore{provisionerStagingStore: newProvisionerStagingStore(sandbox), failReady: true, sourcePath: source}
	provisioner := NewProvisioner(config, nil, store)
	if err := provisioner.Ensure(ctx, sandbox); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("Ready persistence error = %v", err)
	}
	if err := ReleaseUnusedTransientSnapshot(ctx, config, store, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("failed Ready persistence lost retry source: %v", err)
	}
	store.failReady = false
	if err := provisioner.Ensure(ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Ready source retained: %v", err)
	}
	output := filepath.Join(sandbox.Summary.WorkspacePath, "input.txt")
	got, err := os.ReadFile(output)
	if err != nil || string(got) != "original" {
		t.Fatalf("retry contents = %q %v", got, err)
	}
	if err := os.WriteFile(output, []byte("user edit"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := provisioner.Ensure(ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(output)
	if err != nil || string(got) != "user edit" {
		t.Fatalf("Ready resume changed edits: %q %v", got, err)
	}
}

func TestOwnedSnapshotAndStagingCleanupHandlesReadOnlyDirectories(t *testing.T) {
	ctx := context.Background()
	config := &appconfig.Config{DataRoot: t.TempDir()}
	_, workspace, root := newTransientSnapshotFixture(t, config)
	outside := t.TempDir()
	sentinel := filepath.Join(outside, "keep")
	writeProvisionerFileStateFile(t, sentinel, "outside", 0o400)
	readonly := filepath.Join(root, "readonly")
	writeProvisionerFileStateFile(t, filepath.Join(readonly, "file"), "read only", 0o400)
	if err := os.Symlink(outside, filepath.Join(readonly, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readonly, 0o500); err != nil {
		t.Fatal(err)
	}
	if err := ReleaseTransientSnapshot(ctx, config, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only generation remains: %v", err)
	}
	staging := filepath.Join(t.TempDir(), "attempt")
	writeProvisionerFileStateFile(t, filepath.Join(staging, "nested", "file"), "staging", 0o400)
	if err := os.Chmod(filepath.Join(staging, "nested"), 0o500); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(staging, 0o500); err != nil {
		t.Fatal(err)
	}
	provisioner := &Provisioner{filesystem: osProvisioningFileSystem{}}
	if err := provisioner.cleanupProvisioningAttempt(staging); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only staging remains: %v", err)
	}
	info, err := os.Stat(sentinel)
	if err != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("cleanup touched symlink target: %v %v", info, err)
	}
	t.Logf("owned read-only cleanup validated with effective uid %d", os.Geteuid())
}
