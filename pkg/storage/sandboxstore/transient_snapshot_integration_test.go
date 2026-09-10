package sandboxstore

import (
	"context"
	"errors"
	"os"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func TestIntegrationTransientSnapshotClaimProtectsUnindexedSandbox(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	cfg, err := workspaces.CreateTransientFileSnapshot(ctx, store.config, domain.WorkspaceConfig{ID: "transient", Type: "file", ConfigJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	workspace := &domain.SandboxWorkspace{ID: cfg.ID, Type: cfg.Type, ConfigJSON: cfg.ConfigJSON, SnapshotID: cfg.SnapshotID, SnapshotLease: cfg.SnapshotLease}
	// Stop at the same partial commit boundary where later saveCells/saveEvents
	// errors return nil,error: authoritative metadata exists before index insertion.
	prepared, err := store.prepareSandboxCreateSession(ctx, sandboxCreateSpec{Driver: "docker", WorkspaceID: cfg.ID, Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.saveSandbox(prepared.Session); err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetSandbox(ctx, prepared.Session.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Workspace.SnapshotID != cfg.SnapshotID {
		t.Fatal("storage lost transient owner")
	}
	if err := workspaces.ReleaseUnusedTransientSnapshot(ctx, store.config, store, workspace); err != nil {
		t.Fatal(err)
	}
	root, err := workspaces.FileWorkspaceContentRoot(store.config, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("partial create lost retry source: %v", err)
	}
	cleaner := &workspaces.TransientSnapshotCleaner{Config: store.config, Store: store}
	result, err := cleaner.Clean(ctx, persisted.Summary.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if result.Removed != 0 || result.Skipped != 1 {
		t.Fatalf("recovery removed unindexed source: %#v", result)
	}
	// Removal accessories run before metadata deletion, so even pending sources
	// are released when their sandbox is explicitly removed.
	releaser := workspaces.SnapshotAccessoryReleaser{Config: store.config, Store: store}
	if err := releaser.ReleaseSandboxResources(ctx, persisted.Summary.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted sandbox source remains: %v", err)
	}
}
