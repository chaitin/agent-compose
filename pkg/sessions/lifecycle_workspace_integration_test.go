package sessions_test

import (
	"context"
	"fmt"
	"net"
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
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
)

func TestIntegrationJupyterWorkspaceProxyResumePreservesState(t *testing.T) {
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
		SandboxStartTimeout:  2 * time.Second,
	}

	workspaceStore, sandboxStore, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("create storage: %v", err)
	}

	workspaceConfig, err := workspaceStore.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "jupyter-resume-workspace",
		Name:       "Jupyter Resume Workspace",
		Type:       "file",
		ConfigJSON: workspaces.DefaultFileConfigJSON(config, "jupyter-resume-workspace"),
	})
	if err != nil {
		t.Fatalf("create workspace config: %v", err)
	}
	sourceRoot, err := workspaces.FileWorkspaceContentRoot(config, workspaceConfig)
	if err != nil {
		t.Fatalf("resolve workspace source root: %v", err)
	}
	writeJupyterWorkspaceFile(t, filepath.Join(sourceRoot, "editable.txt"), "template v1\n", 0o644)
	writeJupyterWorkspaceFile(t, filepath.Join(sourceRoot, "deleted.txt"), "delete me\n", 0o640)
	writeJupyterWorkspaceFile(t, filepath.Join(sourceRoot, "nested", "kept.txt"), "keep me\n", 0o600)

	sandbox, err := sandboxStore.CreateSandboxWithOptions(
		ctx,
		"Jupyter workspace resume",
		"",
		driverpkg.RuntimeDriverDocker,
		"guest:latest",
		workspaceConfig.ID,
		domain.SandboxTypeManual,
		nil,
		nil,
		nil,
		sessionstore.CreateSandboxOptions{JupyterEnabled: true},
	)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	provisioner := workspaces.NewProvisioner(config, workspaceStore, sandboxStore)
	if err := provisioner.Ensure(ctx, sandbox); err != nil {
		t.Fatalf("initial workspace Ensure: %v", err)
	}
	if sandbox.WorkspaceProvisioning == nil || sandbox.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("initial provisioning = %#v, want ready", sandbox.WorkspaceProvisioning)
	}
	readyUpdatedAt := sandbox.WorkspaceProvisioning.UpdatedAt

	sandbox.Summary.VMStatus = domain.VMStatusStopped
	if err := sandboxStore.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("mark sandbox stopped: %v", err)
	}

	workspaceRoot := sandbox.Summary.WorkspacePath
	writeJupyterWorkspaceFile(t, filepath.Join(workspaceRoot, "editable.txt"), "user edit\n", 0o600)
	if err := os.Remove(filepath.Join(workspaceRoot, "deleted.txt")); err != nil {
		t.Fatalf("delete seeded workspace file: %v", err)
	}
	writeJupyterWorkspaceFile(t, filepath.Join(workspaceRoot, "generated", "result.txt"), "runtime output\n", 0o640)
	if err := os.Symlink(filepath.Join("generated", "result.txt"), filepath.Join(workspaceRoot, "result-link")); err != nil {
		t.Fatalf("create workspace symlink: %v", err)
	}

	writeJupyterWorkspaceFile(t, filepath.Join(sourceRoot, "editable.txt"), "template v2\n", 0o644)
	writeJupyterWorkspaceFile(t, filepath.Join(sourceRoot, "deleted.txt"), "revived by template v2\n", 0o644)
	writeJupyterWorkspaceFile(t, filepath.Join(sourceRoot, "source-v2.txt"), "new template file\n", 0o644)

	before, err := testutil.WorkspaceManifest(workspaceRoot)
	if err != nil {
		t.Fatalf("manifest before automatic resume: %v", err)
	}
	proxyBefore, err := sandboxStore.GetProxyState(sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("load proxy state: %v", err)
	}
	proxyBefore.HostPort = unusedJupyterWorkspacePort(t)
	if err := sandboxStore.SaveProxyState(sandbox.Summary.ID, proxyBefore); err != nil {
		t.Fatalf("persist unreachable proxy target: %v", err)
	}

	driver := &jupyterWorkspaceManifestDriver{
		workspaceRoot: workspaceRoot,
		wantAtStart:   before,
	}
	lifecycle := sessions.Lifecycle{
		Config:           config,
		Store:            sandboxStore,
		WorkspaceEnsurer: provisioner,
		Driver:           driver,
	}
	loaded, proxyAfter, err := lifecycle.EnsureProxyReady(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("EnsureProxyReady: %v", err)
	}
	if driver.startCalls != 1 {
		t.Fatalf("driver start calls = %d, want 1", driver.startCalls)
	}
	if !reflect.DeepEqual(driver.manifestAtStart, before) {
		t.Fatalf("workspace manifest at driver start changed:\n got: %#v\nwant: %#v", driver.manifestAtStart, before)
	}
	after, err := testutil.WorkspaceManifest(workspaceRoot)
	if err != nil {
		t.Fatalf("manifest after automatic resume: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("workspace manifest after automatic resume changed:\n got: %#v\nwant: %#v", after, before)
	}

	if loaded.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("loaded VM status = %q, want %q", loaded.Summary.VMStatus, domain.VMStatusRunning)
	}
	if loaded.WorkspaceProvisioning == nil || loaded.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("loaded provisioning = %#v, want ready", loaded.WorkspaceProvisioning)
	}
	if !loaded.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
		t.Fatalf("loaded provisioning timestamp = %s, want preserved %s", loaded.WorkspaceProvisioning.UpdatedAt, readyUpdatedAt)
	}
	if !reflect.DeepEqual(proxyAfter, proxyBefore) {
		t.Fatalf("returned proxy state = %#v, want preserved %#v", proxyAfter, proxyBefore)
	}
	persisted, err := sandboxStore.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("reload sandbox after automatic resume: %v", err)
	}
	if persisted.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("persisted VM status = %q, want %q", persisted.Summary.VMStatus, domain.VMStatusRunning)
	}
	if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady || !persisted.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
		t.Fatalf("persisted provisioning = %#v, want ready with timestamp %s", persisted.WorkspaceProvisioning, readyUpdatedAt)
	}
	persistedProxy, err := sandboxStore.GetProxyState(sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("reload proxy state after automatic resume: %v", err)
	}
	if !reflect.DeepEqual(persistedProxy, proxyBefore) {
		t.Fatalf("persisted proxy state = %#v, want preserved %#v", persistedProxy, proxyBefore)
	}
}

type jupyterWorkspaceManifestDriver struct {
	workspaceRoot   string
	wantAtStart     []testutil.WorkspaceManifestEntry
	manifestAtStart []testutil.WorkspaceManifestEntry
	startCalls      int
}

func (d *jupyterWorkspaceManifestDriver) StartSandboxVM(_ context.Context, sandbox *domain.Sandbox) error {
	d.startCalls++
	if sandbox.Summary.WorkspacePath != d.workspaceRoot {
		return fmt.Errorf("driver workspace path = %q, want %q", sandbox.Summary.WorkspacePath, d.workspaceRoot)
	}
	manifest, err := testutil.WorkspaceManifest(sandbox.Summary.WorkspacePath)
	if err != nil {
		return fmt.Errorf("manifest workspace at driver start: %w", err)
	}
	d.manifestAtStart = manifest
	if !reflect.DeepEqual(manifest, d.wantAtStart) {
		return fmt.Errorf("workspace changed before driver start: got %#v, want %#v", manifest, d.wantAtStart)
	}
	return nil
}

func (*jupyterWorkspaceManifestDriver) StopSandboxVM(context.Context, *domain.Sandbox) error {
	return nil
}

func writeJupyterWorkspaceFile(t *testing.T, path, content string, mode os.FileMode) {
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

func unusedJupyterWorkspacePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for unused Jupyter port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close unused Jupyter port listener: %v", err)
	}
	return port
}
