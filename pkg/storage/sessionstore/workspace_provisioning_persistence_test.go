package sessionstore

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestWorkspaceProvisioningPersistenceSurvivesSaveGetAndStoreRebuild(t *testing.T) {
	ctx := context.Background()
	sandboxRoot := filepath.Join(t.TempDir(), "sandboxes")
	store := newWorkspaceProvisioningPersistenceStore(t, sandboxRoot)

	sandbox, err := store.CreateSandbox(
		ctx,
		"workspace provisioning persistence",
		"",
		driverpkg.RuntimeDriverBoxlite,
		"",
		"workspace-persistence",
		"",
		&SandboxWorkspace{
			ID:         "workspace-persistence",
			Name:       "Persistence Workspace",
			Type:       "file",
			ConfigJSON: `{"path":"template"}`,
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if sandbox.WorkspaceProvisioning == nil {
		t.Fatal("created workspace provisioning = nil, want pending state")
	}
	want := *sandbox.WorkspaceProvisioning
	if want.Version != domain.SandboxWorkspaceProvisioningVersion {
		t.Fatalf("created workspace provisioning version = %d, want %d", want.Version, domain.SandboxWorkspaceProvisioningVersion)
	}
	if want.Status != domain.SandboxWorkspaceProvisioningStatusPending {
		t.Fatalf("created workspace provisioning status = %q, want %q", want.Status, domain.SandboxWorkspaceProvisioningStatusPending)
	}
	if want.UpdatedAt.IsZero() {
		t.Fatal("created workspace provisioning updated_at is zero")
	}

	if err := store.SaveSandbox(sandbox); err != nil {
		t.Fatalf("SaveSandbox returned error: %v", err)
	}
	got, err := store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	assertWorkspaceProvisioningPersistenceEqual(t, got.WorkspaceProvisioning, want)

	rebuiltStore := newWorkspaceProvisioningPersistenceStore(t, sandboxRoot)
	reloaded, err := rebuiltStore.LoadSandbox(sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("LoadSandbox from rebuilt Store returned error: %v", err)
	}
	assertWorkspaceProvisioningPersistenceEqual(t, reloaded.WorkspaceProvisioning, want)
}

func TestWorkspaceProvisioningPersistenceLegacyLoadDoesNotRewriteMetadata(t *testing.T) {
	const sandboxID = "legacy-workspace-provisioning"

	ctx := context.Background()
	store := newWorkspaceProvisioningPersistenceStore(t, filepath.Join(t.TempDir(), "sandboxes"))
	sandboxDir := store.sandboxDir(sandboxID)
	if err := os.MkdirAll(sandboxDir, 0o755); err != nil {
		t.Fatalf("create legacy sandbox directory: %v", err)
	}
	metadataPath := filepath.Join(sandboxDir, "metadata.json")
	legacyMetadata := []byte(`{
  "summary": {
    "id": "legacy-workspace-provisioning",
    "title": "Legacy Workspace",
    "driver": "boxlite",
    "vm_status": "STOPPED"
  },
  "workspace_id": "legacy-workspace",
  "workspace": {
    "id": "legacy-workspace",
    "name": "Legacy Workspace",
    "type": "file",
    "config_json": "{}"
  }
}
`)
	if err := os.WriteFile(metadataPath, legacyMetadata, 0o644); err != nil {
		t.Fatalf("write legacy metadata: %v", err)
	}
	legacyModTime := time.Date(2024, time.January, 2, 3, 4, 5, 123_000_000, time.UTC)
	if err := os.Chtimes(metadataPath, legacyModTime, legacyModTime); err != nil {
		t.Fatalf("set legacy metadata timestamps: %v", err)
	}
	beforeInfo, err := os.Stat(metadataPath)
	if err != nil {
		t.Fatalf("stat legacy metadata before load: %v", err)
	}

	got, err := store.GetSandbox(ctx, sandboxID)
	if err != nil {
		t.Fatalf("GetSandbox legacy metadata returned error: %v", err)
	}
	if got.WorkspaceProvisioning != nil {
		t.Fatalf("GetSandbox legacy workspace provisioning = %#v, want nil", got.WorkspaceProvisioning)
	}
	loaded, err := store.LoadSandbox(sandboxID)
	if err != nil {
		t.Fatalf("LoadSandbox legacy metadata returned error: %v", err)
	}
	if loaded.WorkspaceProvisioning != nil {
		t.Fatalf("LoadSandbox legacy workspace provisioning = %#v, want nil", loaded.WorkspaceProvisioning)
	}

	afterData, err := os.ReadFile(metadataPath)
	if err != nil {
		t.Fatalf("read legacy metadata after load: %v", err)
	}
	if !bytes.Equal(afterData, legacyMetadata) {
		t.Fatalf("legacy metadata bytes changed after Get/Load:\n got: %s\nwant: %s", afterData, legacyMetadata)
	}
	afterInfo, err := os.Stat(metadataPath)
	if err != nil {
		t.Fatalf("stat legacy metadata after load: %v", err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("legacy metadata mtime changed after Get/Load: got %s, want %s", afterInfo.ModTime(), beforeInfo.ModTime())
	}
}

func TestWorkspaceProvisioningPersistenceRemoveSandboxDeletesStaging(t *testing.T) {
	ctx := context.Background()
	store := newWorkspaceProvisioningPersistenceStore(t, filepath.Join(t.TempDir(), "sandboxes"))
	sandbox, err := store.CreateSandbox(
		ctx,
		"workspace provisioning remove",
		"",
		driverpkg.RuntimeDriverBoxlite,
		"",
		"workspace-remove",
		"",
		&SandboxWorkspace{ID: "workspace-remove", Type: "file", ConfigJSON: "{}"},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	if sandbox.WorkspaceProvisioning == nil {
		t.Fatal("created workspace provisioning = nil, want pending state")
	}

	sandboxDir := store.SandboxDir(sandbox.Summary.ID)
	sentinelPath := filepath.Join(sandboxDir, "state", "workspace-provisioning", "attempt-test", "partial-workspace")
	if err := os.MkdirAll(filepath.Dir(sentinelPath), 0o755); err != nil {
		t.Fatalf("create provisioning staging directory: %v", err)
	}
	if err := os.WriteFile(sentinelPath, []byte("partial\n"), 0o644); err != nil {
		t.Fatalf("write provisioning staging sentinel: %v", err)
	}

	if err := store.RemoveSandbox(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("RemoveSandbox returned error: %v", err)
	}
	if _, err := os.Stat(sandboxDir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("sandbox directory stat after remove = %v, want not exist", err)
	}
}

func newWorkspaceProvisioningPersistenceStore(t *testing.T, sandboxRoot string) *Store {
	t.Helper()
	store, err := NewWithConfig(&appconfig.Config{
		SandboxRoot:          sandboxRoot,
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "workspace-provisioning-test:latest",
		BoxliteHome:          filepath.Join(filepath.Dir(sandboxRoot), "boxlite-home"),
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	})
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	return store
}

func assertWorkspaceProvisioningPersistenceEqual(t *testing.T, got *domain.SandboxWorkspaceProvisioning, want domain.SandboxWorkspaceProvisioning) {
	t.Helper()
	if got == nil {
		t.Fatal("workspace provisioning = nil, want persisted state")
	}
	if got.Version != want.Version {
		t.Errorf("workspace provisioning version = %d, want %d", got.Version, want.Version)
	}
	if got.Status != want.Status {
		t.Errorf("workspace provisioning status = %q, want %q", got.Status, want.Status)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("workspace provisioning updated_at = %s, want %s", got.UpdatedAt, want.UpdatedAt)
	}
}
