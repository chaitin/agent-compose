package sessions_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
)

func TestIntegrationLegacyWorkspaceMigrationPreservesStateWithoutMaterialization(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		DbAddr:               filepath.Join(root, "data.db"),
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverDocker,
		DefaultImage:         "guest:latest",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}

	workspaceStore, sandboxStore, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	const workspaceID = "legacy-migration-workspace"
	workspaceConfig, err := workspaceStore.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "Legacy Migration Workspace",
		Type:       "file",
		ConfigJSON: workspaces.DefaultFileConfigJSON(config, workspaceID),
	})
	if err != nil {
		t.Fatalf("create workspace config: %v", err)
	}
	sourceRoot, err := workspaces.FileWorkspaceContentRoot(config, workspaceConfig)
	if err != nil {
		t.Fatalf("resolve workspace source root: %v", err)
	}
	writeLegacyMigrationFile(t, filepath.Join(sourceRoot, "editable.txt"), "source version\n", 0o644)
	writeLegacyMigrationFile(t, filepath.Join(sourceRoot, "deleted.txt"), "delete after seed\n", 0o640)
	writeLegacyMigrationFile(t, filepath.Join(sourceRoot, "nested", "kept.txt"), "keep after seed\n", 0o600)

	sandbox, err := sandboxStore.CreateSandbox(
		ctx,
		"legacy workspace migration",
		"",
		driverpkg.RuntimeDriverDocker,
		"guest:latest",
		workspaceConfig.ID,
		domain.SandboxTypeManual,
		&domain.SandboxWorkspace{
			ID:         workspaceConfig.ID,
			Name:       workspaceConfig.Name,
			Type:       workspaceConfig.Type,
			ConfigJSON: workspaceConfig.ConfigJSON,
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	initialProvisioner := workspaces.NewProvisioner(config, workspaceStore, sandboxStore)
	if err := initialProvisioner.Ensure(ctx, sandbox); err != nil {
		t.Fatalf("initial workspace provisioning: %v", err)
	}
	if sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("initial workspace provisioning = %#v, want ready", sandbox.WorkspaceProvisioning)
	}
	sandbox.Summary.VMStatus = domain.VMStatusStopped
	if err := sandboxStore.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("mark sandbox stopped: %v", err)
	}

	workspaceRoot := sandbox.Summary.WorkspacePath
	writeLegacyMigrationFile(t, filepath.Join(workspaceRoot, "editable.txt"), "user edit\n", 0o600)
	if err := os.Remove(filepath.Join(workspaceRoot, "deleted.txt")); err != nil {
		t.Fatalf("delete seeded workspace file: %v", err)
	}
	writeLegacyMigrationFile(t, filepath.Join(workspaceRoot, "generated", "result.txt"), "runtime output\n", 0o640)
	if err := os.Chmod(filepath.Join(workspaceRoot, "generated"), 0o750); err != nil {
		t.Fatalf("chmod generated workspace directory: %v", err)
	}
	if err := os.Symlink(filepath.Join("generated", "result.txt"), filepath.Join(workspaceRoot, "result-link")); err != nil {
		t.Fatalf("create workspace symlink: %v", err)
	}
	beforeResume, err := testutil.WorkspaceManifest(workspaceRoot)
	if err != nil {
		t.Fatalf("manifest legacy workspace before resume: %v", err)
	}
	assertLegacyMigrationManifestIsNontrivial(t, beforeResume)

	if err := workspaceStore.DeleteWorkspaceConfig(ctx, workspaceConfig.ID); err != nil {
		t.Fatalf("delete workspace config: %v", err)
	}
	if _, err := workspaceStore.GetWorkspaceConfig(ctx, workspaceConfig.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("workspace config lookup after delete error = %v, want not found", err)
	}
	if err := os.RemoveAll(sourceRoot); err != nil {
		t.Fatalf("delete workspace source: %v", err)
	}
	if _, err := os.Stat(sourceRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace source stat after delete = %v, want not exist", err)
	}

	metadataPath := filepath.Join(sandboxStore.SandboxDir(sandbox.Summary.ID), "metadata.json")
	rewriteLegacyMigrationMetadataWithoutProvisioning(t, metadataPath)

	// Reconstruct the file-backed store so the resume decision cannot depend on
	// the sandbox or provisioning objects used for initial materialization.
	resumedStore, err := sessionstore.NewWithDatabase(config, workspaceStore.DB())
	if err != nil {
		t.Fatalf("reconstruct session store: %v", err)
	}
	legacySandbox, err := resumedStore.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("load legacy sandbox: %v", err)
	}
	if legacySandbox.WorkspaceProvisioning != nil {
		t.Fatalf("legacy workspace provisioning before resume = %#v, want nil", legacySandbox.WorkspaceProvisioning)
	}
	if legacySandbox.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("legacy VM status before resume = %q, want %q", legacySandbox.Summary.VMStatus, domain.VMStatusStopped)
	}
	if legacySandbox.WorkspaceID != workspaceConfig.ID || legacySandbox.Summary.WorkspacePath != workspaceRoot || legacySandbox.Workspace == nil || legacySandbox.Workspace.ConfigJSON != workspaceConfig.ConfigJSON {
		t.Fatalf("legacy workspace identity = (%q, %q, %#v), want (%q, %q, saved snapshot)", legacySandbox.WorkspaceID, legacySandbox.Summary.WorkspacePath, legacySandbox.Workspace, workspaceConfig.ID, workspaceRoot)
	}

	provisioner := workspaces.NewProvisioner(config, workspaceStore, resumedStore)
	driver := &legacyMigrationDriver{
		store:         resumedStore,
		workspaceRoot: workspaceRoot,
		wantManifest:  beforeResume,
	}
	lifecycle := sessions.Lifecycle{
		Config:           config,
		Store:            resumedStore,
		WorkspaceEnsurer: provisioner,
		Driver:           driver,
	}
	resumed, err := lifecycle.ResumeLoaded(ctx, legacySandbox, nil)
	if err != nil {
		t.Fatalf("resume legacy sandbox: %v", err)
	}

	if got := driver.startCalls.Load(); got != 1 {
		t.Fatalf("driver start calls = %d, want 1", got)
	}
	if driver.readyUpdatedAt.IsZero() {
		t.Fatal("ready timestamp observed at driver start is zero")
	}
	afterResume, err := testutil.WorkspaceManifest(workspaceRoot)
	if err != nil {
		t.Fatalf("manifest legacy workspace after resume: %v", err)
	}
	if !reflect.DeepEqual(afterResume, beforeResume) {
		t.Fatalf("legacy workspace manifest changed across resume:\n got: %#v\nwant: %#v", afterResume, beforeResume)
	}
	if resumed.WorkspaceProvisioning == nil || resumed.WorkspaceProvisioning.Version != domain.SandboxWorkspaceProvisioningVersion || resumed.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("resumed workspace provisioning = %#v, want version-1 ready", resumed.WorkspaceProvisioning)
	}
	if !resumed.WorkspaceProvisioning.UpdatedAt.Equal(driver.readyUpdatedAt) {
		t.Fatalf("resumed ready timestamp = %s, want persisted driver-start timestamp %s", resumed.WorkspaceProvisioning.UpdatedAt, driver.readyUpdatedAt)
	}

	persisted, err := resumedStore.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("reload resumed legacy sandbox: %v", err)
	}
	if persisted.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("persisted VM status after resume = %q, want %q", persisted.Summary.VMStatus, domain.VMStatusRunning)
	}
	if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Version != domain.SandboxWorkspaceProvisioningVersion || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady || !persisted.WorkspaceProvisioning.UpdatedAt.Equal(driver.readyUpdatedAt) {
		t.Fatalf("persisted workspace provisioning after resume = %#v, want version-1 ready with timestamp %s", persisted.WorkspaceProvisioning, driver.readyUpdatedAt)
	}
	assertLegacyMigrationMetadataReady(t, metadataPath, driver.readyUpdatedAt)
	if _, err := os.Stat(sourceRoot); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("workspace source stat after resume = %v, want still absent", err)
	}
}

type legacyMigrationDriver struct {
	store          *sessionstore.Store
	workspaceRoot  string
	wantManifest   []testutil.WorkspaceManifestEntry
	readyUpdatedAt time.Time
	startCalls     atomic.Int32
}

func (d *legacyMigrationDriver) StartSandboxVM(ctx context.Context, sandbox *domain.Sandbox) error {
	d.startCalls.Add(1)
	persisted, err := d.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return fmt.Errorf("reload sandbox at driver start: %w", err)
	}
	if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Version != domain.SandboxWorkspaceProvisioningVersion || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		return fmt.Errorf("persisted provisioning at driver start = %#v, want version-1 ready", persisted.WorkspaceProvisioning)
	}
	if persisted.WorkspaceProvisioning.UpdatedAt.IsZero() {
		return errors.New("persisted ready timestamp at driver start is zero")
	}
	if persisted.Summary.VMStatus != domain.VMStatusStopped {
		return fmt.Errorf("persisted VM status at driver start = %q, want %q", persisted.Summary.VMStatus, domain.VMStatusStopped)
	}
	if sandbox.WorkspaceProvisioning == nil || !sandbox.WorkspaceProvisioning.UpdatedAt.Equal(persisted.WorkspaceProvisioning.UpdatedAt) {
		return fmt.Errorf("caller provisioning at driver start = %#v, want persisted %#v", sandbox.WorkspaceProvisioning, persisted.WorkspaceProvisioning)
	}
	if sandbox.Summary.WorkspacePath != d.workspaceRoot {
		return fmt.Errorf("workspace path at driver start = %q, want %q", sandbox.Summary.WorkspacePath, d.workspaceRoot)
	}
	manifest, err := testutil.WorkspaceManifest(sandbox.Summary.WorkspacePath)
	if err != nil {
		return fmt.Errorf("manifest workspace at driver start: %w", err)
	}
	if !reflect.DeepEqual(manifest, d.wantManifest) {
		return fmt.Errorf("workspace manifest changed before driver start: got %#v, want %#v", manifest, d.wantManifest)
	}
	d.readyUpdatedAt = persisted.WorkspaceProvisioning.UpdatedAt
	return nil
}

func (*legacyMigrationDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	return nil
}

func rewriteLegacyMigrationMetadataWithoutProvisioning(t *testing.T, path string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sandbox metadata before legacy rewrite: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat sandbox metadata before legacy rewrite: %v", err)
	}
	var metadata map[string]json.RawMessage
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("decode sandbox metadata before legacy rewrite: %v", err)
	}
	if _, ok := metadata["workspace_provisioning"]; !ok {
		t.Fatal("sandbox metadata has no workspace_provisioning before legacy rewrite")
	}
	delete(metadata, "workspace_provisioning")
	legacyData, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("encode legacy sandbox metadata: %v", err)
	}
	legacyData = append(legacyData, '\n')
	if err := os.WriteFile(path, legacyData, info.Mode().Perm()); err != nil {
		t.Fatalf("write legacy sandbox metadata: %v", err)
	}
	var persisted map[string]json.RawMessage
	if err := json.Unmarshal(legacyData, &persisted); err != nil {
		t.Fatalf("decode rewritten legacy sandbox metadata: %v", err)
	}
	if _, ok := persisted["workspace_provisioning"]; ok {
		t.Fatal("rewritten legacy sandbox metadata still has workspace_provisioning")
	}
}

func assertLegacyMigrationMetadataReady(t *testing.T, path string, wantUpdatedAt time.Time) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read sandbox metadata after legacy resume: %v", err)
	}
	var metadata struct {
		WorkspaceProvisioning *domain.SandboxWorkspaceProvisioning `json:"workspace_provisioning"`
	}
	if err := json.Unmarshal(data, &metadata); err != nil {
		t.Fatalf("decode sandbox metadata after legacy resume: %v", err)
	}
	got := metadata.WorkspaceProvisioning
	if got == nil || got.Version != domain.SandboxWorkspaceProvisioningVersion || got.Status != domain.SandboxWorkspaceProvisioningStatusReady || !got.UpdatedAt.Equal(wantUpdatedAt) {
		t.Fatalf("authoritative metadata workspace provisioning = %#v, want version-1 ready with timestamp %s", got, wantUpdatedAt)
	}
}

func assertLegacyMigrationManifestIsNontrivial(t *testing.T, manifest []testutil.WorkspaceManifestEntry) {
	t.Helper()
	entries := make(map[string]testutil.WorkspaceManifestEntry, len(manifest))
	for _, entry := range manifest {
		entries[entry.Path] = entry
	}
	if entry := entries["editable.txt"]; entry.Type != testutil.WorkspaceManifestEntryTypeFile || entry.Mode != 0o600 {
		t.Fatalf("edited manifest entry = %#v, want regular file mode 0600", entry)
	}
	if _, ok := entries["deleted.txt"]; ok {
		t.Fatal("deleted workspace file is still present in pre-resume manifest")
	}
	if entry := entries["generated/result.txt"]; entry.Type != testutil.WorkspaceManifestEntryTypeFile || entry.Mode != 0o640 {
		t.Fatalf("generated manifest entry = %#v, want regular file mode 0640", entry)
	}
	if entry := entries["result-link"]; entry.Type != testutil.WorkspaceManifestEntryTypeSymlink || entry.SymlinkTarget != filepath.Join("generated", "result.txt") {
		t.Fatalf("symlink manifest entry = %#v, want generated/result.txt symlink", entry)
	}
}

func writeLegacyMigrationFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}
