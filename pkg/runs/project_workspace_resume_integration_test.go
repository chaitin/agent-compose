package runs_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/images"
	"agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestIntegrationProjectLocalWorkspaceExistingAndNewSandboxState(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	projectRoot := filepath.Join(root, "project")
	sourceRoot := filepath.Join(projectRoot, "workspace")
	writeProjectWorkspaceFile(t, filepath.Join(projectRoot, "agent-compose.yaml"), "name: project-local-resume\n", 0o644)
	writeProjectWorkspaceFile(t, filepath.Join(sourceRoot, "editable.txt"), "template v1\n", 0o644)
	writeProjectWorkspaceFile(t, filepath.Join(sourceRoot, "deleted.txt"), "delete from sandbox\n", 0o640)
	writeProjectWorkspaceFile(t, filepath.Join(sourceRoot, "v1-only.txt"), "source v1 only\n", 0o600)
	writeProjectWorkspaceFile(t, filepath.Join(sourceRoot, "nested", "kept.txt"), "keep across versions\n", 0o644)

	config := &appconfig.Config{
		DataRoot:      filepath.Join(root, "data"),
		DbAddr:        filepath.Join(root, "data", "data.db"),
		SandboxRoot:   filepath.Join(root, "sandboxes"),
		RuntimeDriver: driverpkg.RuntimeDriverDocker,
		DefaultImage:  "guest:latest",
	}
	di := do.New()
	do.ProvideValue(di, config)
	configDB, err := configstore.NewConfigStore(di)
	if err != nil {
		t.Fatalf("create config store: %v", err)
	}
	t.Cleanup(func() {
		if err := configDB.DB().Close(); err != nil {
			t.Errorf("close config store: %v", err)
		}
	})
	sandboxStore, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("create session store: %v", err)
	}

	const (
		projectID = "project-local-resume"
		agentID   = "project-local-resume-worker"
	)
	project, err := configDB.UpsertProject(ctx, domain.ProjectRecord{
		ID:         projectID,
		Name:       "Project Local Resume",
		SourcePath: filepath.Join(projectRoot, "agent-compose.yaml"),
	})
	if err != nil {
		t.Fatalf("create project: %v", err)
	}
	revisionV1 := saveProjectWorkspaceRevision(t, ctx, configDB, projectID, "v1")
	upsertProjectWorkspaceAgent(t, ctx, configDB, project, agentID, revisionV1.Revision)

	driver := &projectWorkspaceManifestDriver{store: sandboxStore}
	controller := runs.NewController(runs.ControllerDependencies{
		Config:           config,
		Store:            sandboxStore,
		ConfigDB:         configDB,
		WorkspaceEnsurer: workspaces.NewProvisioner(config, configDB, sandboxStore),
		Driver:           driver,
		Executor:         projectWorkspaceExecutor{},
		Images:           projectWorkspaceImages{},
	})

	runA, execErr, err := controller.RunProjectAgent(ctx, runs.RunAgentRequest{
		ProjectID:       projectID,
		AgentName:       "worker",
		Prompt:          "run a",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "project-local-run-a",
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION,
	}, nil)
	if err != nil || execErr != nil {
		t.Fatalf("run A err=%v execErr=%v run=%#v", err, execErr, runA)
	}
	if runA.Status != domain.ProjectRunStatusSucceeded || runA.ProjectRevision != revisionV1.Revision || runA.SandboxID == "" || runA.CleanupError != "" {
		t.Fatalf("run A = %#v", runA)
	}
	if len(driver.starts) != 1 || driver.starts[0].sandboxID != runA.SandboxID {
		t.Fatalf("driver starts after run A = %#v", driver.starts)
	}
	assertProjectWorkspaceManifestFile(t, driver.starts[0].manifest, "editable.txt", "template v1\n", 0o644)
	assertProjectWorkspaceManifestFile(t, driver.starts[0].manifest, "deleted.txt", "delete from sandbox\n", 0o640)
	assertProjectWorkspaceManifestFile(t, driver.starts[0].manifest, "v1-only.txt", "source v1 only\n", 0o600)

	sandboxA, err := sandboxStore.GetSandbox(ctx, runA.SandboxID)
	if err != nil {
		t.Fatalf("load run A sandbox: %v", err)
	}
	if sandboxA.Summary.VMStatus != domain.VMStatusStopped || sandboxA.Workspace == nil || sandboxA.Workspace.Type != "file" {
		t.Fatalf("run A sandbox = %#v", sandboxA)
	}
	if sandboxA.WorkspaceProvisioning == nil || sandboxA.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("run A provisioning = %#v, want ready", sandboxA.WorkspaceProvisioning)
	}
	workspaceA := *sandboxA.Workspace
	readyAtA := sandboxA.WorkspaceProvisioning.UpdatedAt
	if readyAtA.IsZero() || driver.starts[0].provisioningStatus != domain.SandboxWorkspaceProvisioningStatusReady || !driver.starts[0].readyAt.Equal(readyAtA) {
		t.Fatalf("run A driver provisioning = status %q timestamp %v, want ready timestamp %v", driver.starts[0].provisioningStatus, driver.starts[0].readyAt, readyAtA)
	}
	snapshotRootA := projectWorkspaceSnapshotRoot(t, config, &workspaceA)
	snapshotManifestA := mustProjectWorkspaceManifest(t, snapshotRootA)
	if !projectWorkspaceSnapshotContentsEqual(snapshotManifestA, driver.starts[0].manifest) {
		t.Fatalf("run A source snapshot differs from provisioned workspace:\n got: %#v\nwant: %#v", snapshotManifestA, driver.starts[0].manifest)
	}

	workspaceRootA := sandboxA.Summary.WorkspacePath
	writeProjectWorkspaceFile(t, filepath.Join(workspaceRootA, "editable.txt"), "user edit\n", 0o600)
	if err := os.Remove(filepath.Join(workspaceRootA, "deleted.txt")); err != nil {
		t.Fatalf("delete run A workspace file: %v", err)
	}
	writeProjectWorkspaceFile(t, filepath.Join(workspaceRootA, "generated", "result.txt"), "run A output\n", 0o640)
	if err := os.Symlink(filepath.Join("generated", "result.txt"), filepath.Join(workspaceRootA, "result-link")); err != nil {
		t.Fatalf("create run A workspace symlink: %v", err)
	}
	manifestBeforeReuse := mustProjectWorkspaceManifest(t, workspaceRootA)
	assertProjectWorkspaceManifestFile(t, manifestBeforeReuse, "editable.txt", "user edit\n", 0o600)
	assertProjectWorkspaceManifestFile(t, manifestBeforeReuse, "generated/result.txt", "run A output\n", 0o640)
	assertProjectWorkspaceManifestMissing(t, manifestBeforeReuse, "deleted.txt")
	assertProjectWorkspaceManifestSymlink(t, manifestBeforeReuse, "result-link", filepath.Join("generated", "result.txt"))

	writeProjectWorkspaceFile(t, filepath.Join(sourceRoot, "editable.txt"), "template v2\n", 0o644)
	writeProjectWorkspaceFile(t, filepath.Join(sourceRoot, "deleted.txt"), "source v2 replacement\n", 0o644)
	if err := os.Remove(filepath.Join(sourceRoot, "v1-only.txt")); err != nil {
		t.Fatalf("remove v1-only source file: %v", err)
	}
	writeProjectWorkspaceFile(t, filepath.Join(sourceRoot, "source-v2.txt"), "new source v2 file\n", 0o644)
	revisionV2 := saveProjectWorkspaceRevision(t, ctx, configDB, projectID, "v2")
	upsertProjectWorkspaceAgent(t, ctx, configDB, project, agentID, revisionV2.Revision)
	if got := mustProjectWorkspaceManifest(t, snapshotRootA); !reflect.DeepEqual(got, snapshotManifestA) {
		t.Fatalf("run A source snapshot changed after project source v2 update:\n got: %#v\nwant: %#v", got, snapshotManifestA)
	}

	reused, reusedExecErr, reusedErr := controller.RunProjectAgent(ctx, runs.RunAgentRequest{
		ProjectID:       projectID,
		AgentName:       "worker",
		Prompt:          "reuse run a sandbox",
		SandboxID:       runA.SandboxID,
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "project-local-reuse-a",
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION,
	}, nil)
	if reusedErr != nil || reusedExecErr != nil {
		t.Fatalf("reused run err=%v execErr=%v run=%#v", reusedErr, reusedExecErr, reused)
	}
	if reused.Status != domain.ProjectRunStatusSucceeded || reused.ProjectRevision != revisionV2.Revision || reused.SandboxID != runA.SandboxID || reused.CleanupError != "" {
		t.Fatalf("reused run = %#v, want sandbox %q and revision %d", reused, runA.SandboxID, revisionV2.Revision)
	}
	if len(driver.starts) != 2 || driver.starts[1].sandboxID != runA.SandboxID {
		t.Fatalf("driver starts after reused run = %#v", driver.starts)
	}
	if !reflect.DeepEqual(driver.starts[1].manifest, manifestBeforeReuse) {
		t.Fatalf("workspace changed before reused driver start:\n got: %#v\nwant: %#v", driver.starts[1].manifest, manifestBeforeReuse)
	}
	if driver.starts[1].workspace == nil || *driver.starts[1].workspace != workspaceA {
		t.Fatalf("reused driver workspace snapshot = %#v, want %#v", driver.starts[1].workspace, workspaceA)
	}
	if !driver.starts[1].readyAt.Equal(readyAtA) {
		t.Fatalf("reused driver ready timestamp = %v, want %v", driver.starts[1].readyAt, readyAtA)
	}
	afterReuse := mustProjectWorkspaceManifest(t, workspaceRootA)
	if !reflect.DeepEqual(afterReuse, manifestBeforeReuse) {
		t.Fatalf("workspace changed after reused run:\n got: %#v\nwant: %#v", afterReuse, manifestBeforeReuse)
	}
	persistedA, err := sandboxStore.GetSandbox(ctx, runA.SandboxID)
	if err != nil {
		t.Fatalf("REMOVE_ON_COMPLETION removed reused sandbox: %v", err)
	}
	if persistedA.Summary.VMStatus != domain.VMStatusStopped || persistedA.Workspace == nil || *persistedA.Workspace != workspaceA {
		t.Fatalf("persisted reused sandbox = %#v", persistedA)
	}
	if persistedA.WorkspaceProvisioning == nil || persistedA.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady || !persistedA.WorkspaceProvisioning.UpdatedAt.Equal(readyAtA) {
		t.Fatalf("persisted reused provisioning = %#v, want ready timestamp %v", persistedA.WorkspaceProvisioning, readyAtA)
	}
	if containsProjectWorkspaceSandboxID(driver.removed, runA.SandboxID) {
		t.Fatalf("REMOVE_ON_COMPLETION removed reused sandbox %q: removed=%#v", runA.SandboxID, driver.removed)
	}

	runB, runBExecErr, runBErr := controller.RunProjectAgent(ctx, runs.RunAgentRequest{
		ProjectID:       projectID,
		AgentName:       "worker",
		Prompt:          "run b",
		Source:          domain.ProjectRunSourceAPI,
		ClientRequestID: "project-local-run-b",
		CleanupPolicy:   agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION,
	}, nil)
	if runBErr != nil || runBExecErr != nil {
		t.Fatalf("run B err=%v execErr=%v run=%#v", runBErr, runBExecErr, runB)
	}
	if runB.Status != domain.ProjectRunStatusSucceeded || runB.ProjectRevision != revisionV2.Revision || runB.SandboxID == "" || runB.SandboxID == runA.SandboxID || runB.CleanupError != "" {
		t.Fatalf("run B = %#v, want distinct revision-v2 sandbox", runB)
	}
	if len(driver.starts) != 3 || driver.starts[0].sandboxID != runA.SandboxID || driver.starts[1].sandboxID != runA.SandboxID || driver.starts[2].sandboxID != runB.SandboxID {
		t.Fatalf("driver start order = %#v, want [%q %q %q]", driver.starts, runA.SandboxID, runA.SandboxID, runB.SandboxID)
	}
	startB := driver.starts[2]
	if startB.workspace == nil || startB.workspace.Type != "file" || startB.workspace.ID == workspaceA.ID {
		t.Fatalf("run B workspace snapshot = %#v, want distinct file snapshot", startB.workspace)
	}
	if startB.provisioningStatus != domain.SandboxWorkspaceProvisioningStatusReady || startB.readyAt.IsZero() {
		t.Fatalf("run B driver provisioning = status %q timestamp %v, want ready", startB.provisioningStatus, startB.readyAt)
	}
	assertProjectWorkspaceManifestFile(t, startB.manifest, "editable.txt", "template v2\n", 0o644)
	assertProjectWorkspaceManifestFile(t, startB.manifest, "deleted.txt", "source v2 replacement\n", 0o644)
	assertProjectWorkspaceManifestFile(t, startB.manifest, "source-v2.txt", "new source v2 file\n", 0o644)
	assertProjectWorkspaceManifestMissing(t, startB.manifest, "v1-only.txt")
	assertProjectWorkspaceManifestMissing(t, startB.manifest, "generated")
	assertProjectWorkspaceManifestMissing(t, startB.manifest, "generated/result.txt")
	assertProjectWorkspaceManifestMissing(t, startB.manifest, "result-link")
	snapshotManifestB := mustProjectWorkspaceManifest(t, projectWorkspaceSnapshotRoot(t, config, startB.workspace))
	if !projectWorkspaceSnapshotContentsEqual(snapshotManifestB, startB.manifest) {
		t.Fatalf("run B source snapshot differs from provisioned workspace:\n got: %#v\nwant: %#v", snapshotManifestB, startB.manifest)
	}
	if _, err := sandboxStore.GetSandbox(ctx, runB.SandboxID); err == nil {
		t.Fatalf("new run B sandbox %q still exists after REMOVE_ON_COMPLETION", runB.SandboxID)
	}
	if _, err := os.Stat(sandboxStore.SandboxDir(runB.SandboxID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run B sandbox directory stat error = %v, want not exist", err)
	}
	if !containsProjectWorkspaceSandboxID(driver.removed, runB.SandboxID) {
		t.Fatalf("run B sandbox %q was not removed: removed=%#v", runB.SandboxID, driver.removed)
	}
	finalA, err := sandboxStore.GetSandbox(ctx, runA.SandboxID)
	if err != nil {
		t.Fatalf("reload run A sandbox after run B cleanup: %v", err)
	}
	if finalA.Summary.VMStatus != domain.VMStatusStopped || finalA.Workspace == nil || *finalA.Workspace != workspaceA {
		t.Fatalf("run A sandbox changed after run B cleanup: %#v", finalA)
	}
	if finalA.WorkspaceProvisioning == nil || finalA.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady || !finalA.WorkspaceProvisioning.UpdatedAt.Equal(readyAtA) {
		t.Fatalf("run A provisioning changed after run B cleanup: %#v, want ready timestamp %v", finalA.WorkspaceProvisioning, readyAtA)
	}
	if finalManifestA := mustProjectWorkspaceManifest(t, finalA.Summary.WorkspacePath); !reflect.DeepEqual(finalManifestA, manifestBeforeReuse) {
		t.Fatalf("run A workspace changed after run B cleanup:\n got: %#v\nwant: %#v", finalManifestA, manifestBeforeReuse)
	}
	if len(driver.stopped) != 3 || driver.stopped[0] != runA.SandboxID || driver.stopped[1] != runA.SandboxID || driver.stopped[2] != runB.SandboxID {
		t.Fatalf("driver stop order = %#v, want [%q %q %q]", driver.stopped, runA.SandboxID, runA.SandboxID, runB.SandboxID)
	}
}

const projectWorkspaceRevisionSpec = `{
  "name": "Project Local Resume",
  "variables": [{"name": "SOURCE_VERSION", "value": %q}],
  "workspaces": [
    {"name": "local-source", "workspace": {"provider": "file", "path": "workspace"}}
  ],
  "agents": [
    {"name": "worker", "provider": "codex", "workspace": {"name": "local-source"}}
  ]
}`

func saveProjectWorkspaceRevision(t *testing.T, ctx context.Context, store *configstore.ConfigStore, projectID, version string) domain.ProjectRevisionRecord {
	t.Helper()
	revision, created, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{
		ProjectID: projectID,
		SpecHash:  "project-local-" + version,
		SpecJSON:  fmt.Sprintf(projectWorkspaceRevisionSpec, version),
	})
	if err != nil || !created {
		t.Fatalf("save project revision %q: revision=%#v created=%v err=%v", version, revision, created, err)
	}
	return revision
}

func upsertProjectWorkspaceAgent(t *testing.T, ctx context.Context, store *configstore.ConfigStore, project domain.ProjectRecord, agentID string, revision int64) {
	t.Helper()
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{
		ID:             agentID,
		Name:           "worker",
		ProjectID:      project.ID,
		AgentName:      "worker",
		ManagedAgentID: agentID,
		Revision:       revision,
		Provider:       "codex",
		Image:          "guest:latest",
		Driver:         driverpkg.RuntimeDriverDocker,
		SpecJSON:       `{"name":"worker"}`,
	}); err != nil {
		t.Fatalf("upsert project agent revision %d: %v", revision, err)
	}
	if _, err := store.UpsertManagedAgentDefinition(ctx, domain.AgentDefinition{
		ID:                     agentID,
		Name:                   "worker",
		Enabled:                true,
		Provider:               "codex",
		Driver:                 driverpkg.RuntimeDriverDocker,
		GuestImage:             "guest:latest",
		ConfigJSON:             "{}",
		ManagedProjectID:       project.ID,
		ManagedProjectRevision: revision,
		ManagedAgentName:       "worker",
	}); err != nil {
		t.Fatalf("upsert managed agent revision %d: %v", revision, err)
	}
}

type projectWorkspaceExecutor struct{}

type projectWorkspaceImages struct{}

func (projectWorkspaceImages) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{}, nil
}

func (projectWorkspaceImages) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, nil
}

func (projectWorkspaceImages) InspectImage(context.Context, images.InspectRequest) (images.InspectResult, error) {
	return images.InspectResult{}, nil
}

func (projectWorkspaceImages) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}

func (projectWorkspaceExecutor) ExecuteAgentRequest(_ context.Context, _ *domain.Sandbox, _ execution.ExecuteAgentRequest) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	return domain.NotebookCell{ID: "cell", Type: execution.CellTypeAgent, Output: "done", Success: true}, domain.SandboxEvent{}, domain.SandboxEvent{}, nil
}

type projectWorkspaceDriverStart struct {
	sandboxID          string
	manifest           []testutil.WorkspaceManifestEntry
	workspace          *domain.SandboxWorkspace
	provisioningStatus string
	readyAt            time.Time
}

type projectWorkspaceManifestDriver struct {
	store   *sessionstore.Store
	starts  []projectWorkspaceDriverStart
	stopped []string
	removed []string
}

func (d *projectWorkspaceManifestDriver) StartSandboxVM(_ context.Context, sandbox *domain.Sandbox) error {
	manifest, err := testutil.WorkspaceManifest(sandbox.Summary.WorkspacePath)
	if err != nil {
		return fmt.Errorf("capture workspace manifest at driver start: %w", err)
	}
	var workspace *domain.SandboxWorkspace
	if sandbox.Workspace != nil {
		copy := *sandbox.Workspace
		workspace = &copy
	}
	var readyAt time.Time
	var provisioningStatus string
	if sandbox.WorkspaceProvisioning != nil {
		provisioningStatus = sandbox.WorkspaceProvisioning.Status
		readyAt = sandbox.WorkspaceProvisioning.UpdatedAt
	}
	d.starts = append(d.starts, projectWorkspaceDriverStart{
		sandboxID:          sandbox.Summary.ID,
		manifest:           manifest,
		workspace:          workspace,
		provisioningStatus: provisioningStatus,
		readyAt:            readyAt,
	})
	return d.store.SaveVMState(sandbox.Summary.ID, domain.VMState{
		Driver: sandbox.Summary.Driver,
		BoxID:  fmt.Sprintf("box-%d", len(d.starts)),
	})
}

func (d *projectWorkspaceManifestDriver) StopSandboxVM(_ context.Context, sandbox *domain.Sandbox) error {
	d.stopped = append(d.stopped, sandbox.Summary.ID)
	return nil
}

func (d *projectWorkspaceManifestDriver) RemoveSandboxVM(_ context.Context, sandbox *domain.Sandbox) error {
	d.removed = append(d.removed, sandbox.Summary.ID)
	return nil
}

func writeProjectWorkspaceFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create directory for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

func mustProjectWorkspaceManifest(t *testing.T, root string) []testutil.WorkspaceManifestEntry {
	t.Helper()
	manifest, err := testutil.WorkspaceManifest(root)
	if err != nil {
		t.Fatalf("workspace manifest %s: %v", root, err)
	}
	return manifest
}

func projectWorkspaceSnapshotRoot(t *testing.T, config *appconfig.Config, workspace *domain.SandboxWorkspace) string {
	t.Helper()
	if workspace == nil {
		t.Fatal("workspace snapshot is nil")
	}
	root, err := workspaces.FileWorkspaceContentRoot(config, domain.WorkspaceConfig{
		ID:         workspace.ID,
		Name:       workspace.Name,
		Type:       workspace.Type,
		ConfigJSON: workspace.ConfigJSON,
	})
	if err != nil {
		t.Fatalf("resolve workspace snapshot root: %v", err)
	}
	return root
}

func assertProjectWorkspaceManifestFile(t *testing.T, manifest []testutil.WorkspaceManifestEntry, path, content string, mode os.FileMode) {
	t.Helper()
	for _, entry := range manifest {
		if entry.Path != path {
			continue
		}
		if entry.Type != testutil.WorkspaceManifestEntryTypeFile || entry.Mode != mode || entry.ContentSHA256 != projectWorkspaceFileSHA256(content) {
			t.Fatalf("manifest entry %q = %#v, want regular file mode=%v content=%q", path, entry, mode, content)
		}
		return
	}
	t.Fatalf("manifest entry %q is missing: %#v", path, manifest)
}

func assertProjectWorkspaceManifestMissing(t *testing.T, manifest []testutil.WorkspaceManifestEntry, path string) {
	t.Helper()
	for _, entry := range manifest {
		if entry.Path == path {
			t.Fatalf("manifest unexpectedly contains %q: %#v", path, entry)
		}
	}
}

func assertProjectWorkspaceManifestSymlink(t *testing.T, manifest []testutil.WorkspaceManifestEntry, path, target string) {
	t.Helper()
	for _, entry := range manifest {
		if entry.Path != path {
			continue
		}
		if entry.Type != testutil.WorkspaceManifestEntryTypeSymlink || entry.SymlinkTarget != target {
			t.Fatalf("manifest entry %q = %#v, want symlink to %q", path, entry, target)
		}
		return
	}
	t.Fatalf("manifest symlink %q is missing: %#v", path, manifest)
}

func projectWorkspaceFileSHA256(content string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
}

func containsProjectWorkspaceSandboxID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func projectWorkspaceSnapshotContentsEqual(left, right []testutil.WorkspaceManifestEntry) bool {
	left = append([]testutil.WorkspaceManifestEntry(nil), left...)
	right = append([]testutil.WorkspaceManifestEntry(nil), right...)
	for i := range left {
		if left[i].Path == "." {
			left[i].Mode = 0
		}
	}
	for i := range right {
		if right[i].Path == "." {
			right[i].Mode = 0
		}
	}
	return reflect.DeepEqual(left, right)
}
