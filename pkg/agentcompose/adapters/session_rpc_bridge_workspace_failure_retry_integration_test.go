package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	testutil "agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sources"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/workspaces"
)

func TestIntegrationSandboxRPCBridgeFileWorkspaceProvisioningFailureRetry(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	recordingStore := installIntegrationRecordingProvisioner(bridge)

	const workspaceID = "file-provisioning-failure-retry"
	sourceRoot, err := workspaces.DefaultFileWorkspaceContentRoot(bridge.config, workspaceID)
	if err != nil {
		t.Fatalf("resolve file workspace source: %v", err)
	}
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("create file workspace source: %v", err)
	}
	brokenSource := filepath.Join(sourceRoot, "broken-source")
	if err := os.Symlink("missing-source-target", brokenSource); err != nil {
		t.Fatalf("create unsupported source symlink: %v", err)
	}
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "File Provisioning Failure Retry",
		Type:       "file",
		ConfigJSON: workspaces.DefaultFileConfigJSON(bridge.config, workspaceID),
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}

	_, err = bridge.createSandbox(ctx, sandboxRPCCreateRequest{
		Title:       "file provisioning failure retry",
		WorkspaceID: workspace.ID,
	}, domain.SandboxTypeManual)
	if err == nil || connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("CreateSession error = %v (code %v), want internal file materialization failure", err, connect.CodeOf(err))
	}
	failed := integrationOnlySandbox(t, ctx, bridge)
	assertIntegrationProvisioningState(t, failed, domain.SandboxWorkspaceProvisioningStatusFailed, domain.VMStatusFailed)
	if got := len(driver.startCalls); got != 0 {
		t.Fatalf("driver start count after file materialization failure = %d, want 0", got)
	}
	if got, want := recordingStore.statusHistory(failed.Summary.ID), []string{domain.SandboxWorkspaceProvisioningStatusFailed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provisioning persistence after file failure = %#v, want %#v", got, want)
	}

	if err := os.Remove(brokenSource); err != nil {
		t.Fatalf("remove unsupported source symlink: %v", err)
	}
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "README.md"), "file retry succeeded\n", 0o640)
	driver.onStart = integrationPersistedReadyStartCheck(t, ctx, bridge, failed.Summary.ID, nil)

	if _, err := bridge.ResumeSandbox(ctx, failed.Summary.ID); err != nil {
		t.Fatalf("ResumeSession after repairing file source returned error: %v", err)
	}
	ready, err := bridge.store.GetSandbox(ctx, failed.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox after file retry returned error: %v", err)
	}
	assertIntegrationProvisioningState(t, ready, domain.SandboxWorkspaceProvisioningStatusReady, domain.VMStatusRunning)
	if got := len(driver.startCalls); got != 1 {
		t.Fatalf("driver start count after successful file retry = %d, want 1", got)
	}
	if got, want := recordingStore.statusHistory(ready.Summary.ID), []string{
		domain.SandboxWorkspaceProvisioningStatusFailed,
		domain.SandboxWorkspaceProvisioningStatusPending,
		domain.SandboxWorkspaceProvisioningStatusReady,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("file retry provisioning persistence = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(filepath.Join(ready.Summary.WorkspacePath, "README.md"))
	if err != nil {
		t.Fatalf("read materialized file retry result: %v", err)
	}
	if got, want := string(data), "file retry succeeded\n"; got != want {
		t.Fatalf("materialized file retry content = %q, want %q", got, want)
	}
}

func TestIntegrationSandboxRPCBridgeGitWorkspaceProvisioningFailureRetry(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	recordingStore := installIntegrationRecordingProvisioner(bridge)

	const workspaceID = "git-provisioning-failure-retry"
	sourceRepo := filepath.Join(t.TempDir(), "source.git")
	configJSON, err := json.Marshal(workspaces.GitWorkspaceConfig{
		Source: sources.Source{Provider: sources.ProviderGit, URL: "file://" + filepath.ToSlash(sourceRepo), Ref: "main"},
	})
	if err != nil {
		t.Fatalf("marshal Git workspace config: %v", err)
	}
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "Git Provisioning Failure Retry",
		Type:       "git",
		ConfigJSON: string(configJSON),
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}

	_, err = bridge.createSandbox(ctx, sandboxRPCCreateRequest{
		Title:       "git provisioning failure retry",
		WorkspaceID: workspace.ID,
	}, domain.SandboxTypeManual)
	if err == nil || connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("CreateSession error = %v (code %v), want internal Git clone failure", err, connect.CodeOf(err))
	}
	failed := integrationOnlySandbox(t, ctx, bridge)
	assertIntegrationProvisioningState(t, failed, domain.SandboxWorkspaceProvisioningStatusFailed, domain.VMStatusFailed)
	if got := len(driver.startCalls); got != 0 {
		t.Fatalf("driver start count after Git clone failure = %d, want 0", got)
	}
	if got, want := recordingStore.statusHistory(failed.Summary.ID), []string{domain.SandboxWorkspaceProvisioningStatusFailed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("provisioning persistence after Git failure = %#v, want %#v", got, want)
	}

	integrationCreateLocalGitSource(t, sourceRepo)
	driver.onStart = integrationPersistedReadyStartCheck(t, ctx, bridge, failed.Summary.ID, nil)
	if _, err := bridge.ResumeSandbox(ctx, failed.Summary.ID); err != nil {
		t.Fatalf("ResumeSession after repairing Git source returned error: %v", err)
	}
	ready, err := bridge.store.GetSandbox(ctx, failed.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox after Git retry returned error: %v", err)
	}
	assertIntegrationProvisioningState(t, ready, domain.SandboxWorkspaceProvisioningStatusReady, domain.VMStatusRunning)
	if got := len(driver.startCalls); got != 1 {
		t.Fatalf("driver start count after successful Git retry = %d, want 1", got)
	}
	if got, want := recordingStore.statusHistory(ready.Summary.ID), []string{
		domain.SandboxWorkspaceProvisioningStatusFailed,
		domain.SandboxWorkspaceProvisioningStatusPending,
		domain.SandboxWorkspaceProvisioningStatusReady,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Git retry provisioning persistence = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(filepath.Join(ready.Summary.WorkspacePath, "README.md"))
	if err != nil {
		t.Fatalf("read cloned Git retry result: %v", err)
	}
	if got, want := string(data), "local Git retry fixture\n"; got != want {
		t.Fatalf("cloned Git retry content = %q, want %q", got, want)
	}
}

func TestIntegrationSandboxRPCBridgeRuntimeStartFailureRetryPreservesReadyWorkspace(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	bridge.config.RuntimeBaseURL = "http://agent-compose.test:7410"
	bridge.config.LLMAPIEndpoint = "https://llm.example.test/v1"
	bridge.config.LLMAPIKey = "provider-key"
	bridge.config.LLMModel = "gpt-retry"
	bridge.config.LLMAPIProtocol = "responses"
	recordingStore := installIntegrationRecordingProvisioner(bridge)

	const workspaceID = "runtime-failure-ready-retry"
	sourceRoot, err := workspaces.DefaultFileWorkspaceContentRoot(bridge.config, workspaceID)
	if err != nil {
		t.Fatalf("resolve file workspace source: %v", err)
	}
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		t.Fatalf("create file workspace source: %v", err)
	}
	writeIntegrationWorkspaceFile(t, filepath.Join(sourceRoot, "README.md"), "materialized exactly once\n", 0o640)
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         workspaceID,
		Name:       "Runtime Failure Ready Retry",
		Type:       "file",
		ConfigJSON: workspaces.DefaultFileConfigJSON(bridge.config, workspaceID),
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}

	runtimeErr := errors.New("injected runtime start failure")
	driver.startErr = runtimeErr
	var firstStartSandboxID string
	var firstStartToken string
	driver.onStart = func(sandbox *domain.Sandbox) {
		firstStartSandboxID = sandbox.Summary.ID
		firstStartToken = domain.SandboxEnvMap(sandbox.RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]
		integrationAssertPersistedReady(t, ctx, bridge, sandbox.Summary.ID, nil)
	}
	_, err = bridge.createSandbox(ctx, sandboxRPCCreateRequest{
		Title:       "runtime start failure retry",
		WorkspaceID: workspace.ID,
	}, domain.SandboxTypeManual)
	if err == nil || connect.CodeOf(err) != connect.CodeInternal || !errors.Is(err, runtimeErr) {
		t.Fatalf("CreateSession error = %v (code %v), want injected internal runtime failure", err, connect.CodeOf(err))
	}
	if firstStartSandboxID == "" {
		t.Fatal("runtime driver was not called after workspace became ready")
	}
	if firstStartToken == "" {
		t.Fatal("first runtime start did not receive an agent token")
	}
	failed, err := bridge.store.GetSandbox(ctx, firstStartSandboxID)
	if err != nil {
		t.Fatalf("GetSandbox after runtime start failure returned error: %v", err)
	}
	assertIntegrationProvisioningState(t, failed, domain.SandboxWorkspaceProvisioningStatusReady, domain.VMStatusFailed)
	if got := len(driver.startCalls); got != 1 {
		t.Fatalf("driver start count after injected runtime failure = %d, want 1", got)
	}
	if got, want := recordingStore.statusHistory(failed.Summary.ID), []string{domain.SandboxWorkspaceProvisioningStatusReady}; !reflect.DeepEqual(got, want) {
		t.Fatalf("runtime failure provisioning persistence = %#v, want %#v", got, want)
	}
	readyUpdatedAt := failed.WorkspaceProvisioning.UpdatedAt
	workspaceSnapshot := *failed.Workspace
	beforeRetry, err := testutil.WorkspaceManifest(failed.Summary.WorkspacePath)
	if err != nil {
		t.Fatalf("manifest ready workspace after runtime failure: %v", err)
	}

	if err := bridge.configDB.DeleteWorkspaceConfig(ctx, workspace.ID); err != nil {
		t.Fatalf("DeleteWorkspaceConfig returned error: %v", err)
	}
	if err := os.RemoveAll(sourceRoot); err != nil {
		t.Fatalf("remove file workspace source after ready: %v", err)
	}
	if err := os.WriteFile(sourceRoot, []byte("hostile non-directory source\n"), 0o600); err != nil {
		t.Fatalf("replace file workspace source with hostile file: %v", err)
	}
	driver.startErr = nil
	persistedReadyCheck := integrationPersistedReadyStartCheck(t, ctx, bridge, failed.Summary.ID, func(persisted *domain.Sandbox) {
		if !persisted.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
			t.Errorf("ready timestamp at runtime retry start = %s, want %s", persisted.WorkspaceProvisioning.UpdatedAt, readyUpdatedAt)
		}
		if persisted.Workspace == nil || !reflect.DeepEqual(*persisted.Workspace, workspaceSnapshot) {
			t.Errorf("workspace snapshot at runtime retry start = %#v, want %#v", persisted.Workspace, workspaceSnapshot)
		}
		atStart, manifestErr := testutil.WorkspaceManifest(persisted.Summary.WorkspacePath)
		if manifestErr != nil {
			t.Errorf("manifest workspace at runtime retry start: %v", manifestErr)
			return
		}
		if !reflect.DeepEqual(atStart, beforeRetry) {
			t.Errorf("ready workspace changed before runtime retry start:\n got: %#v\nwant: %#v", atStart, beforeRetry)
		}
	})
	var retryToken string
	driver.onStart = func(sandbox *domain.Sandbox) {
		persistedReadyCheck(sandbox)
		retryToken = domain.SandboxEnvMap(sandbox.RuntimeEnvItems)["AGENT_COMPOSE_SANDBOX_TOKEN"]
	}

	if _, err := bridge.ResumeSandbox(ctx, failed.Summary.ID); err != nil {
		t.Fatalf("ResumeSession after runtime start failure returned error: %v", err)
	}
	resumed, err := bridge.store.GetSandbox(ctx, failed.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox after runtime retry returned error: %v", err)
	}
	assertIntegrationProvisioningState(t, resumed, domain.SandboxWorkspaceProvisioningStatusReady, domain.VMStatusRunning)
	if !resumed.WorkspaceProvisioning.UpdatedAt.Equal(readyUpdatedAt) {
		t.Fatalf("ready timestamp after runtime retry = %s, want %s", resumed.WorkspaceProvisioning.UpdatedAt, readyUpdatedAt)
	}
	if resumed.Workspace == nil || !reflect.DeepEqual(*resumed.Workspace, workspaceSnapshot) {
		t.Fatalf("workspace snapshot after runtime retry = %#v, want %#v", resumed.Workspace, workspaceSnapshot)
	}
	afterRetry, err := testutil.WorkspaceManifest(resumed.Summary.WorkspacePath)
	if err != nil {
		t.Fatalf("manifest workspace after runtime retry: %v", err)
	}
	if !reflect.DeepEqual(afterRetry, beforeRetry) {
		t.Fatalf("ready workspace changed across runtime-only retry:\n got: %#v\nwant: %#v", afterRetry, beforeRetry)
	}
	if got := len(driver.startCalls); got != 2 {
		t.Fatalf("driver start count across runtime failure/retry = %d, want 2 attempts", got)
	}
	if retryToken == "" || retryToken == firstStartToken {
		t.Fatalf("runtime retry token = %q, want a newly prepared token distinct from first start", retryToken)
	}
	if got, want := recordingStore.statusHistory(resumed.Summary.ID), []string{domain.SandboxWorkspaceProvisioningStatusReady}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Provisioner writes across runtime-only retry = %#v, want unchanged %#v", got, want)
	}
}

type integrationProvisioningRecordingStore struct {
	store *sessionstore.Store

	mu       sync.Mutex
	statuses map[string][]string
}

var _ workspaces.SandboxStore = (*integrationProvisioningRecordingStore)(nil)

func (s *integrationProvisioningRecordingStore) GetSandbox(ctx context.Context, id string) (*domain.Sandbox, error) {
	return s.store.GetSandbox(ctx, id)
}

func (s *integrationProvisioningRecordingStore) UpdateSandbox(ctx context.Context, sandbox *domain.Sandbox) error {
	if err := s.store.UpdateSandbox(ctx, sandbox); err != nil {
		return err
	}
	status := ""
	if sandbox.WorkspaceProvisioning != nil {
		status = sandbox.WorkspaceProvisioning.Status
	}
	s.mu.Lock()
	s.statuses[sandbox.Summary.ID] = append(s.statuses[sandbox.Summary.ID], status)
	s.mu.Unlock()
	return nil
}

func (s *integrationProvisioningRecordingStore) SandboxDir(id string) string {
	return s.store.SandboxDir(id)
}

func (s *integrationProvisioningRecordingStore) statusHistory(id string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.statuses[id]...)
}

func installIntegrationRecordingProvisioner(bridge *SandboxRPCBridge) *integrationProvisioningRecordingStore {
	recordingStore := &integrationProvisioningRecordingStore{
		store:    bridge.store,
		statuses: make(map[string][]string),
	}
	bridge.workspaceEnsurer = workspaces.NewProvisioner(bridge.config, bridge.configDB, recordingStore)
	return recordingStore
}

func integrationOnlySandbox(t *testing.T, ctx context.Context, bridge *SandboxRPCBridge) *domain.Sandbox {
	t.Helper()
	listed, err := bridge.store.ListSandboxes(ctx, domain.SandboxListOptions{})
	if err != nil {
		t.Fatalf("ListSandboxes returned error: %v", err)
	}
	if listed.TotalCount != 1 || len(listed.Sandboxes) != 1 {
		t.Fatalf("sandboxes = %#v total=%d, want exactly one", listed.Sandboxes, listed.TotalCount)
	}
	return listed.Sandboxes[0]
}

func assertIntegrationProvisioningState(t *testing.T, sandbox *domain.Sandbox, wantProvisioning, wantVM string) {
	t.Helper()
	if sandbox == nil || sandbox.WorkspaceProvisioning == nil {
		t.Fatalf("sandbox provisioning = %#v, want %q", sandbox, wantProvisioning)
	}
	if got := sandbox.WorkspaceProvisioning.Status; got != wantProvisioning {
		t.Fatalf("workspace provisioning status = %q, want %q", got, wantProvisioning)
	}
	if got := sandbox.Summary.VMStatus; got != wantVM {
		t.Fatalf("sandbox VM status = %q, want %q", got, wantVM)
	}
}

func integrationPersistedReadyStartCheck(t *testing.T, ctx context.Context, bridge *SandboxRPCBridge, sandboxID string, check func(*domain.Sandbox)) func(*domain.Sandbox) {
	t.Helper()
	return func(started *domain.Sandbox) {
		if started.Summary.ID != sandboxID {
			t.Errorf("driver start sandbox ID = %q, want %q", started.Summary.ID, sandboxID)
		}
		assertIntegrationWorkspaceReady(t, started, "in driver input")
		integrationAssertPersistedReady(t, ctx, bridge, sandboxID, check)
	}
}

func integrationAssertPersistedReady(t *testing.T, ctx context.Context, bridge *SandboxRPCBridge, sandboxID string, check func(*domain.Sandbox)) {
	t.Helper()
	persisted, err := bridge.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		t.Fatalf("GetSandbox at driver start returned error: %v", err)
	}
	assertIntegrationWorkspaceReady(t, persisted, "persisted before driver start")
	if check != nil {
		check(persisted)
	}
}

func integrationCreateLocalGitSource(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("create local Git fixture parent: %v", err)
	}
	integrationRunGit(t, "", "init", "-b", "main", path)
	integrationRunGit(t, path, "config", "user.email", "agent-compose@example.test")
	integrationRunGit(t, path, "config", "user.name", "Agent Compose")
	writeIntegrationWorkspaceFile(t, filepath.Join(path, "README.md"), "local Git retry fixture\n", 0o644)
	integrationRunGit(t, path, "add", "README.md")
	integrationRunGit(t, path, "commit", "-m", "deterministic fixture")
}

func integrationRunGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s failed: %v\n%s", strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output)
}
