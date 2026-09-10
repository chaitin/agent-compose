package sandboxes_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func TestIntegrationWorkspaceMountRetentionPreservesExternalSource(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	store, sandbox := newWorkspaceCleanupSandbox(t, now.Add(-48*time.Hour))
	source := t.TempDir()
	marker := filepath.Join(source, "external.txt")
	if err := os.WriteFile(marker, []byte("external source"), 0o640); err != nil {
		t.Fatal(err)
	}
	raw, err := workspaces.NewFileWorkspaceMountConfig(domain.ProjectRecord{SourcePath: source}, ".", "reference", true)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.Workspace = &domain.SandboxWorkspace{ID: "mounted", Type: "file", ConfigJSON: raw}
	sandbox.WorkspaceProvisioning = &domain.SandboxWorkspaceProvisioning{
		Version: domain.SandboxWorkspaceProvisioningVersion, Status: domain.SandboxWorkspaceProvisioningStatusReady,
	}
	if err := store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Workspace == nil || loaded.Workspace.ConfigJSON != raw || loaded.Summary.WorkspacePath != sandbox.Summary.WorkspacePath {
		t.Fatalf("mounted workspace snapshot did not survive reload: %#v", loaded)
	}
	if err := workspaces.NewProvisionerWithMaterializer(store, nil).Ensure(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	cleaner := &sandboxes.WorkspaceCleaner{Store: store, Locks: sandboxes.NewLifecycleLocks(), Now: func() time.Time { return now }}
	result, err := cleaner.Clean(ctx, now.Add(-24*time.Hour))
	if err != nil || result.Removed != 1 {
		t.Fatalf("mounted workspace reclaim: %#v, %v", result, err)
	}
	loaded, err = store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := workspaces.NewProvisionerWithMaterializer(store, nil).Ensure(ctx, loaded); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("reclaimed mounted workspace resumed: %v", err)
	}
	if err := store.RemoveSandbox(ctx, sandbox.Summary.ID); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "external source" {
		t.Fatalf("external source modified by retention/removal: %q, %v", got, err)
	}
	if _, err := os.Stat(sandbox.Summary.WorkspacePath); !os.IsNotExist(err) {
		t.Fatalf("owned workspace survived cleanup: %v", err)
	}
}
