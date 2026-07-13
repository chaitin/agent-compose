package sessions_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"

	"github.com/samber/do/v2"
)

var errPersistenceWorkspaceConfigConsulted = errors.New("ready resume consulted workspace config")

func TestIntegrationStoreProvisionerReconstructionPersistencePreservesReadyWorkspace(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	seeded := seedPersistenceWorkspace(t, ctx, root)

	// Construct a fresh config value and every persistence/lifecycle component
	// from the same roots. The seeding helper has returned, so none of its Store,
	// Provisioner, Lifecycle, or driver instances are retained here.
	config := newPersistenceWorkspaceConfig(root)
	configDB := openPersistenceWorkspaceConfigStore(t, config)
	t.Cleanup(func() {
		if err := configDB.DB().Close(); err != nil {
			t.Errorf("close reconstructed config store: %v", err)
		}
	})
	if _, err := configDB.GetWorkspaceConfig(ctx, seeded.workspaceID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("reconstructed config store workspace lookup error = %v, want not found", err)
	}
	sandboxStore, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("reconstruct session store: %v", err)
	}

	reconstructed, err := sandboxStore.GetSandbox(ctx, seeded.sandboxID)
	if err != nil {
		t.Fatalf("load sandbox through reconstructed store: %v", err)
	}
	assertPersistenceSandboxIdentity(t, reconstructed, seeded)
	assertPersistenceWorkspaceReady(t, reconstructed, seeded.readyUpdatedAt, "before reconstructed resume")
	if reconstructed.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("reconstructed VM status = %q, want %q", reconstructed.Summary.VMStatus, domain.VMStatusStopped)
	}
	beforeResume := mustPersistenceWorkspaceManifest(t, reconstructed.Summary.WorkspacePath)
	if !reflect.DeepEqual(beforeResume, seeded.manifest) {
		t.Fatalf("persisted workspace manifest after reconstruction changed:\n got: %#v\nwant: %#v", beforeResume, seeded.manifest)
	}

	configGuard := &persistenceWorkspaceConfigGuard{store: configDB}
	provisioner := workspaces.NewProvisioner(config, configGuard, sandboxStore)
	driver := &persistenceReconstructedDriver{
		store:         sandboxStore,
		seeded:        seeded,
		wantManifest:  beforeResume,
		wantVMAtStart: domain.VMStatusStopped,
	}
	lifecycle := sessions.Lifecycle{
		Config:           config,
		Store:            sandboxStore,
		WorkspaceEnsurer: provisioner,
		Driver:           driver,
	}
	resumed, err := lifecycle.ResumeLoaded(ctx, reconstructed, nil)
	if err != nil {
		t.Fatalf("resume through reconstructed lifecycle: %v", err)
	}

	if configGuard.calls != 0 {
		t.Fatalf("workspace config lookups during ready resume = %d, want 0", configGuard.calls)
	}
	if driver.startCalls != 1 {
		t.Fatalf("reconstructed driver start calls = %d, want 1", driver.startCalls)
	}
	assertPersistenceSandboxIdentity(t, resumed, seeded)
	assertPersistenceWorkspaceReady(t, resumed, seeded.readyUpdatedAt, "returned after reconstructed resume")
	if resumed.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("returned VM status = %q, want %q", resumed.Summary.VMStatus, domain.VMStatusRunning)
	}

	persisted, err := sandboxStore.GetSandbox(ctx, seeded.sandboxID)
	if err != nil {
		t.Fatalf("reload sandbox after reconstructed resume: %v", err)
	}
	assertPersistenceSandboxIdentity(t, persisted, seeded)
	assertPersistenceWorkspaceReady(t, persisted, seeded.readyUpdatedAt, "persisted after reconstructed resume")
	if persisted.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("persisted VM status = %q, want %q", persisted.Summary.VMStatus, domain.VMStatusRunning)
	}
	afterResume := mustPersistenceWorkspaceManifest(t, persisted.Summary.WorkspacePath)
	if !reflect.DeepEqual(afterResume, beforeResume) {
		t.Fatalf("workspace manifest after reconstructed resume changed:\n got: %#v\nwant: %#v", afterResume, beforeResume)
	}

	sourceInfo, err := os.Lstat(seeded.sourceRoot)
	if err != nil {
		t.Fatalf("inspect poisoned source after ready resume: %v", err)
	}
	if !sourceInfo.Mode().IsRegular() {
		t.Fatalf("poisoned source mode after ready resume = %s, want regular file", sourceInfo.Mode())
	}
}

type persistenceWorkspaceSeed struct {
	sandboxID      string
	workspaceID    string
	workspaceRoot  string
	sourceRoot     string
	snapshot       domain.SandboxWorkspace
	readyUpdatedAt time.Time
	manifest       []testutil.WorkspaceManifestEntry
}

func seedPersistenceWorkspace(t *testing.T, ctx context.Context, root string) persistenceWorkspaceSeed {
	t.Helper()
	config := newPersistenceWorkspaceConfig(root)
	configDB := openPersistenceWorkspaceConfigStore(t, config)
	defer func() {
		if err := configDB.DB().Close(); err != nil {
			t.Errorf("close initial config store before reconstruction: %v", err)
		}
	}()
	sandboxStore, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("create initial session store: %v", err)
	}

	const workspaceID = "store-provisioner-persistence"
	workspaceConfig, err := configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "Store Provisioner Persistence",
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
	writePersistenceWorkspaceFile(t, filepath.Join(sourceRoot, "editable.txt"), "template v1\n", 0o644)
	writePersistenceWorkspaceFile(t, filepath.Join(sourceRoot, "deleted.txt"), "delete from sandbox\n", 0o640)
	writePersistenceWorkspaceFile(t, filepath.Join(sourceRoot, "nested", "seed.txt"), "seeded\n", 0o600)

	snapshot := domain.SandboxWorkspace{
		ID:         workspaceConfig.ID,
		Name:       workspaceConfig.Name,
		Type:       workspaceConfig.Type,
		ConfigJSON: workspaceConfig.ConfigJSON,
	}
	sandbox, err := sandboxStore.CreateSandboxWithOptions(
		ctx,
		"Store reconstruction persistence",
		"",
		driverpkg.RuntimeDriverDocker,
		"guest:latest",
		workspaceConfig.ID,
		domain.SandboxTypeManual,
		&snapshot,
		nil,
		nil,
		sessionstore.CreateSandboxOptions{},
	)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}

	initialDriver := &persistenceInitialDriver{store: sandboxStore}
	lifecycle := sessions.Lifecycle{
		Config:           config,
		Store:            sandboxStore,
		WorkspaceEnsurer: workspaces.NewProvisioner(config, configDB, sandboxStore),
		Driver:           initialDriver,
	}
	running, err := lifecycle.ResumeLoaded(ctx, sandbox, nil)
	if err != nil {
		t.Fatalf("provision and start initial sandbox: %v", err)
	}
	if initialDriver.startCalls != 1 {
		t.Fatalf("initial driver start calls = %d, want 1", initialDriver.startCalls)
	}
	if running.WorkspaceProvisioning == nil {
		t.Fatal("initial workspace provisioning is nil, want ready")
	}
	readyUpdatedAt := running.WorkspaceProvisioning.UpdatedAt
	assertPersistenceWorkspaceReady(t, running, readyUpdatedAt, "after initial provisioning")

	workspaceRoot := running.Summary.WorkspacePath
	writePersistenceWorkspaceFile(t, filepath.Join(workspaceRoot, "editable.txt"), "user edit\n", 0o600)
	if err := os.Remove(filepath.Join(workspaceRoot, "deleted.txt")); err != nil {
		t.Fatalf("delete seeded workspace file: %v", err)
	}
	writePersistenceWorkspaceFile(t, filepath.Join(workspaceRoot, "generated", "result.txt"), "runtime output\n", 0o640)
	if err := os.Symlink(filepath.Join("generated", "result.txt"), filepath.Join(workspaceRoot, "result-link")); err != nil {
		t.Fatalf("create workspace symlink: %v", err)
	}

	stopped, didStop, err := lifecycle.StopLoaded(ctx, running)
	if err != nil {
		t.Fatalf("stop initial sandbox: %v", err)
	}
	if !didStop || initialDriver.stopCalls != 1 {
		t.Fatalf("initial stop result = %t with %d driver calls, want true with 1 call", didStop, initialDriver.stopCalls)
	}
	assertPersistenceWorkspaceReady(t, stopped, readyUpdatedAt, "after initial stop")
	if stopped.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("stopped VM status = %q, want %q", stopped.Summary.VMStatus, domain.VMStatusStopped)
	}
	manifest := mustPersistenceWorkspaceManifest(t, workspaceRoot)

	if err := configDB.DeleteWorkspaceConfig(ctx, workspaceConfig.ID); err != nil {
		t.Fatalf("delete workspace config before reconstruction: %v", err)
	}
	if _, err := configDB.GetWorkspaceConfig(ctx, workspaceConfig.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("workspace config lookup after delete error = %v, want not found", err)
	}
	if err := os.RemoveAll(sourceRoot); err != nil {
		t.Fatalf("remove workspace source before reconstruction: %v", err)
	}
	if err := os.WriteFile(sourceRoot, []byte("must not be consulted\n"), 0o600); err != nil {
		t.Fatalf("replace workspace source with poison file: %v", err)
	}

	persisted, err := sandboxStore.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("reload stopped sandbox before releasing stores: %v", err)
	}
	seed := persistenceWorkspaceSeed{
		sandboxID:      persisted.Summary.ID,
		workspaceID:    persisted.WorkspaceID,
		workspaceRoot:  persisted.Summary.WorkspacePath,
		sourceRoot:     sourceRoot,
		readyUpdatedAt: readyUpdatedAt,
		manifest:       manifest,
	}
	if persisted.Workspace == nil {
		t.Fatal("persisted workspace snapshot is nil")
	}
	seed.snapshot = *persisted.Workspace

	return seed
}

func newPersistenceWorkspaceConfig(root string) *appconfig.Config {
	return &appconfig.Config{
		DataRoot:            root,
		DbAddr:              filepath.Join(root, "data.db"),
		SandboxRoot:         filepath.Join(root, "sandboxes"),
		RuntimeDriver:       driverpkg.RuntimeDriverDocker,
		DefaultImage:        "guest:latest",
		SandboxStartTimeout: 2 * time.Second,
	}
}

func openPersistenceWorkspaceConfigStore(t *testing.T, config *appconfig.Config) *configstore.ConfigStore {
	t.Helper()
	di := do.New()
	do.ProvideValue(di, config)
	store, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("open config store: %v", err)
	}
	return store
}

type persistenceWorkspaceConfigGuard struct {
	store *configstore.ConfigStore
	calls int
}

func (g *persistenceWorkspaceConfigGuard) GetWorkspaceConfig(ctx context.Context, id string) (domain.WorkspaceConfig, error) {
	g.calls++
	_, lookupErr := g.store.GetWorkspaceConfig(ctx, id)
	return domain.WorkspaceConfig{}, errors.Join(errPersistenceWorkspaceConfigConsulted, lookupErr)
}

type persistenceInitialDriver struct {
	store      *sessionstore.Store
	startCalls int
	stopCalls  int
}

func (d *persistenceInitialDriver) StartSandboxVM(ctx context.Context, sandbox *domain.Sandbox) error {
	d.startCalls++
	persisted, err := d.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return fmt.Errorf("load sandbox at initial driver start: %w", err)
	}
	if persisted.WorkspaceProvisioning == nil ||
		persisted.WorkspaceProvisioning.Version != domain.SandboxWorkspaceProvisioningVersion ||
		persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady ||
		sandbox.WorkspaceProvisioning == nil ||
		!persisted.WorkspaceProvisioning.UpdatedAt.Equal(sandbox.WorkspaceProvisioning.UpdatedAt) {
		return fmt.Errorf("persisted provisioning at initial driver start = %#v, want caller ready state %#v", persisted.WorkspaceProvisioning, sandbox.WorkspaceProvisioning)
	}
	return nil
}

func (d *persistenceInitialDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	d.stopCalls++
	return nil
}

type persistenceReconstructedDriver struct {
	store         *sessionstore.Store
	seeded        persistenceWorkspaceSeed
	wantManifest  []testutil.WorkspaceManifestEntry
	wantVMAtStart string
	startCalls    int
}

func (d *persistenceReconstructedDriver) StartSandboxVM(ctx context.Context, sandbox *domain.Sandbox) error {
	d.startCalls++
	persisted, err := d.store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		return fmt.Errorf("load sandbox at reconstructed driver start: %w", err)
	}
	if err := persistenceSandboxIdentityError(persisted, d.seeded); err != nil {
		return err
	}
	if persisted.WorkspaceProvisioning == nil ||
		persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady ||
		!persisted.WorkspaceProvisioning.UpdatedAt.Equal(d.seeded.readyUpdatedAt) {
		return fmt.Errorf("persisted provisioning at reconstructed driver start = %#v, want ready at %s", persisted.WorkspaceProvisioning, d.seeded.readyUpdatedAt)
	}
	if persisted.Summary.VMStatus != d.wantVMAtStart {
		return fmt.Errorf("persisted VM status at reconstructed driver start = %q, want %q", persisted.Summary.VMStatus, d.wantVMAtStart)
	}
	manifest, err := testutil.WorkspaceManifest(persisted.Summary.WorkspacePath)
	if err != nil {
		return fmt.Errorf("manifest workspace at reconstructed driver start: %w", err)
	}
	if !reflect.DeepEqual(manifest, d.wantManifest) {
		return fmt.Errorf("workspace changed before reconstructed driver start: got %#v, want %#v", manifest, d.wantManifest)
	}
	return nil
}

func (*persistenceReconstructedDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	return nil
}

func assertPersistenceSandboxIdentity(t *testing.T, sandbox *domain.Sandbox, seeded persistenceWorkspaceSeed) {
	t.Helper()
	if err := persistenceSandboxIdentityError(sandbox, seeded); err != nil {
		t.Fatal(err)
	}
}

func persistenceSandboxIdentityError(sandbox *domain.Sandbox, seeded persistenceWorkspaceSeed) error {
	if sandbox == nil {
		return errors.New("sandbox is nil")
	}
	if sandbox.Summary.ID != seeded.sandboxID {
		return fmt.Errorf("sandbox ID = %q, want %q", sandbox.Summary.ID, seeded.sandboxID)
	}
	if sandbox.Summary.WorkspacePath != seeded.workspaceRoot {
		return fmt.Errorf("workspace path = %q, want %q", sandbox.Summary.WorkspacePath, seeded.workspaceRoot)
	}
	if sandbox.WorkspaceID != seeded.workspaceID {
		return fmt.Errorf("workspace ID = %q, want %q", sandbox.WorkspaceID, seeded.workspaceID)
	}
	if sandbox.Workspace == nil || !reflect.DeepEqual(*sandbox.Workspace, seeded.snapshot) {
		return fmt.Errorf("workspace snapshot = %#v, want %#v", sandbox.Workspace, seeded.snapshot)
	}
	return nil
}

func assertPersistenceWorkspaceReady(t *testing.T, sandbox *domain.Sandbox, wantUpdatedAt time.Time, stage string) {
	t.Helper()
	if sandbox == nil || sandbox.WorkspaceProvisioning == nil {
		t.Fatalf("workspace provisioning %s = %#v, want ready", stage, sandbox)
	}
	if sandbox.WorkspaceProvisioning.Version != domain.SandboxWorkspaceProvisioningVersion ||
		sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady ||
		!sandbox.WorkspaceProvisioning.UpdatedAt.Equal(wantUpdatedAt) {
		t.Fatalf("workspace provisioning %s = %#v, want version %d ready at %s", stage, sandbox.WorkspaceProvisioning, domain.SandboxWorkspaceProvisioningVersion, wantUpdatedAt)
	}
}

func mustPersistenceWorkspaceManifest(t *testing.T, root string) []testutil.WorkspaceManifestEntry {
	t.Helper()
	manifest, err := testutil.WorkspaceManifest(root)
	if err != nil {
		t.Fatalf("build workspace manifest for %s: %v", root, err)
	}
	return manifest
}

func writePersistenceWorkspaceFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create workspace parent for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write workspace file %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod workspace file %s: %v", path, err)
	}
}
