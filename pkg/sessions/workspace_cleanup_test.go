package sessions_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
)

func TestWorkspaceCleanerReclaimsStoppedSandboxAndBlocksProvisioning(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store, sandbox := newWorkspaceCleanupSandbox(t, now.Add(-48*time.Hour))
	workspaceFile := filepath.Join(sandbox.Summary.WorkspacePath, "result.txt")
	if err := os.WriteFile(workspaceFile, []byte("result"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleaner := &sessions.WorkspaceCleaner{Store: store, Locks: sessions.NewLifecycleLocks(), Now: func() time.Time { return now }}
	result, err := cleaner.Clean(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 1 || result.Removed != 1 || result.Failed != 0 {
		t.Fatalf("cleanup result = %#v", result)
	}
	if _, err := os.Stat(sandbox.Summary.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("workspace still exists: %v", err)
	}
	reclaimed, err := store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed.WorkspaceReclamation == nil || reclaimed.WorkspaceReclamation.State != domain.SandboxWorkspaceReclamationStateReclaimed || reclaimed.WorkspaceReclamation.CompletedAt.IsZero() {
		t.Fatalf("workspace reclamation = %#v", reclaimed.WorkspaceReclamation)
	}
	provisioner := workspaces.NewProvisionerWithMaterializer(store, noopWorkspaceMaterializer{})
	if err := provisioner.Ensure(ctx, reclaimed); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("Ensure error = %v, want failed precondition", err)
	}
	if err := store.RemoveSandbox(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("RemoveSandbox after reclamation: %v", err)
	}
}

func TestWorkspaceCleanerRejectsSymlinkWorkspace(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store, sandbox := newWorkspaceCleanupSandbox(t, now.Add(-48*time.Hour))
	external := t.TempDir()
	if err := os.RemoveAll(sandbox.Summary.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, sandbox.Summary.WorkspacePath); err != nil {
		t.Fatal(err)
	}
	cleaner := &sessions.WorkspaceCleaner{Store: store, Locks: sessions.NewLifecycleLocks(), Now: func() time.Time { return now }}
	result, err := cleaner.Clean(context.Background(), now.Add(-24*time.Hour))
	if err == nil || result.Failed != 1 {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	loaded, loadErr := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if loaded.WorkspaceReclamation != nil || domain.SandboxWorkspaceUnavailable(loaded) {
		t.Fatalf("unsafe path committed reclamation intent: %#v", loaded.WorkspaceReclamation)
	}
	if _, err := os.Stat(external); err != nil {
		t.Fatalf("external target was affected: %v", err)
	}
}

func TestWorkspaceCleanerRequiresRecordedStop(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store, sandbox := newWorkspaceCleanupSandbox(t, time.Time{})
	cleaner := &sessions.WorkspaceCleaner{Store: store, Locks: sessions.NewLifecycleLocks(), Now: func() time.Time { return now }}
	result, err := cleaner.Clean(context.Background(), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 0 || result.Removed != 0 {
		t.Fatalf("cleanup without recorded stop = %#v", result)
	}
	if _, err := os.Stat(sandbox.Summary.WorkspacePath); err != nil {
		t.Fatalf("workspace without recorded stop was removed: %v", err)
	}
	loaded, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WorkspaceReclamation != nil {
		t.Fatalf("cleanup without recorded stop persisted intent: %#v", loaded.WorkspaceReclamation)
	}
}

func TestWorkspaceCleanerDoesNotReclaimFailedSandboxWithStaleStop(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store, sandbox := newWorkspaceCleanupSandbox(t, now.Add(-48*time.Hour))
	sandbox.Summary.VMStatus = domain.VMStatusFailed
	if err := store.UpdateSandbox(context.Background(), sandbox); err != nil {
		t.Fatal(err)
	}

	cleaner := &sessions.WorkspaceCleaner{Store: store, Locks: sessions.NewLifecycleLocks(), Now: func() time.Time { return now }}
	result, err := cleaner.Clean(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 0 || result.Removed != 0 {
		t.Fatalf("cleanup failed sandbox with stale stop = %#v", result)
	}
	if _, err := os.Stat(sandbox.Summary.WorkspacePath); err != nil {
		t.Fatalf("failed sandbox workspace was removed: %v", err)
	}
	loaded, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.WorkspaceReclamation != nil {
		t.Fatalf("failed sandbox persisted reclamation intent: %#v", loaded.WorkspaceReclamation)
	}
}

func TestWorkspaceCleanerRequiresStopAfterLatestStart(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store, sandbox := newWorkspaceCleanupSandbox(t, now.Add(-48*time.Hour))
	vmState, err := store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	vmState.StartedAt = now.Add(-time.Hour)
	if err := store.SaveVMState(sandbox.Summary.ID, vmState); err != nil {
		t.Fatal(err)
	}

	cleaner := &sessions.WorkspaceCleaner{Store: store, Locks: sessions.NewLifecycleLocks(), Now: func() time.Time { return now }}
	result, err := cleaner.Clean(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 0 || result.Removed != 0 {
		t.Fatalf("cleanup stale stop before latest start = %#v", result)
	}
	if _, err := os.Stat(sandbox.Summary.WorkspacePath); err != nil {
		t.Fatalf("workspace with stale stop marker was removed: %v", err)
	}
}

func TestWorkspaceCleanerRequiresStopAfterLatestStartAttempt(t *testing.T) {
	now := time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)
	store, sandbox := newWorkspaceCleanupSandbox(t, now.Add(-48*time.Hour))
	vmState, err := store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	vmState.StartAttemptedAt = now.Add(-time.Hour)
	if err := store.SaveVMState(sandbox.Summary.ID, vmState); err != nil {
		t.Fatal(err)
	}

	cleaner := &sessions.WorkspaceCleaner{Store: store, Locks: sessions.NewLifecycleLocks(), Now: func() time.Time { return now }}
	result, err := cleaner.Clean(context.Background(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if result.Matched != 0 || result.Removed != 0 {
		t.Fatalf("cleanup stop before latest start attempt = %#v", result)
	}
	if _, err := os.Stat(sandbox.Summary.WorkspacePath); err != nil {
		t.Fatalf("workspace with newer start attempt was removed: %v", err)
	}
}

func newWorkspaceCleanupSandbox(t *testing.T, stoppedAt time.Time) (*sessionstore.Store, *domain.Sandbox) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), RuntimeDriver: "docker",
		DefaultImage: "guest:latest", DockerDefaultImage: "guest:latest", JupyterProxyBasePath: "/jupyter",
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	sandbox, err := store.CreateSandbox(context.Background(), "cleanup", "", "docker", "guest:latest", "", "test", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusStopped
	if err := store.UpdateSandbox(context.Background(), sandbox); err != nil {
		t.Fatal(err)
	}
	vmState, err := store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	vmState.StoppedAt = stoppedAt
	if err := store.SaveVMState(sandbox.Summary.ID, vmState); err != nil {
		t.Fatal(err)
	}
	return store, sandbox
}

type noopWorkspaceMaterializer struct{}

func (noopWorkspaceMaterializer) Materialize(context.Context, *domain.Sandbox) error { return nil }
