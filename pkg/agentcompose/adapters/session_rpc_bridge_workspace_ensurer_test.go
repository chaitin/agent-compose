package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

type recordingBridgeWorkspaceEnsurer struct {
	calls             []*domain.Sandbox
	initialIDs        []string
	initialWorkspaces []*domain.SandboxWorkspace
	initialStatuses   []string
	err               error
	ensure            func(context.Context, *domain.Sandbox) error
}

var _ workspaces.WorkspaceEnsurer = (*recordingBridgeWorkspaceEnsurer)(nil)

func (e *recordingBridgeWorkspaceEnsurer) Ensure(ctx context.Context, sandbox *domain.Sandbox) error {
	e.calls = append(e.calls, sandbox)
	e.initialIDs = append(e.initialIDs, sandbox.Summary.ID)
	if sandbox.Workspace == nil {
		e.initialWorkspaces = append(e.initialWorkspaces, nil)
	} else {
		workspace := *sandbox.Workspace
		e.initialWorkspaces = append(e.initialWorkspaces, &workspace)
	}
	if sandbox.WorkspaceProvisioning == nil {
		e.initialStatuses = append(e.initialStatuses, "")
	} else {
		e.initialStatuses = append(e.initialStatuses, sandbox.WorkspaceProvisioning.Status)
	}
	if e.ensure != nil {
		if err := e.ensure(ctx, sandbox); err != nil {
			return err
		}
	}
	return e.err
}

func TestSandboxRPCBridgeCreateSessionUsesWorkspaceEnsurer(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	order := []string{}
	deletedSource := filepath.Join(t.TempDir(), "deleted-source")
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "bridge-workspace",
		Name:       "Bridge Workspace",
		Type:       "file",
		ConfigJSON: `{"root":"` + filepath.ToSlash(deletedSource) + `"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	ensurer := &recordingBridgeWorkspaceEnsurer{}
	ensurer.ensure = func(ctx context.Context, sandbox *domain.Sandbox) error {
		order = append(order, "ensure")
		persisted, err := bridge.store.GetSandbox(ctx, sandbox.Summary.ID)
		if err != nil {
			return err
		}
		providerEnv := domain.SandboxEnvMap(persisted.ProviderEnvItems)
		if providerEnv["LLM_API_ENDPOINT"] != "https://sandbox.example/v1" || providerEnv["LLM_API_PROTOCOL"] != "chat_completions" {
			t.Fatalf("provider env before workspace Ensure = %#v", providerEnv)
		}
		if err := domain.TransitionSandboxWorkspaceProvisioning(sandbox, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
			return err
		}
		return bridge.store.UpdateSandbox(ctx, sandbox)
	}
	bridge.workspaceEnsurer = ensurer
	bridge.cap = testCapabilityProvider{guide: func(context.Context, string) ([]byte, error) {
		order = append(order, "guide")
		return []byte("# capability guide"), nil
	}}
	driver.onStart = func(*domain.Sandbox) {
		order = append(order, "driver.start")
	}

	created, err := bridge.createSandbox(ctx, sandboxRPCCreateRequest{
		Title:       "workspace ensurer",
		WorkspaceID: workspace.ID,
		CapsetIDs:   []string{"dev"},
		EnvItems: []domain.SandboxEnvVar{
			{Name: "LLM_API_ENDPOINT", Value: "https://sandbox.example/v1"},
			{Name: "LLM_API_PROTOCOL", Value: "chat_completions"},
		},
	}, domain.SandboxTypeManual)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	if len(ensurer.calls) != 1 {
		t.Fatalf("workspace Ensure call count = %d, want 1", len(ensurer.calls))
	}
	sessionID := created.Summary.ID
	if ensurer.initialIDs[0] != sessionID {
		t.Fatalf("workspace Ensure sandbox id = %q, want %q", ensurer.initialIDs[0], sessionID)
	}
	if got := ensurer.initialStatuses[0]; got != domain.SandboxWorkspaceProvisioningStatusPending {
		t.Fatalf("workspace Ensure initial provisioning status = %q, want %q", got, domain.SandboxWorkspaceProvisioningStatusPending)
	}
	snapshot := ensurer.initialWorkspaces[0]
	if snapshot == nil || snapshot.ID != workspace.ID || snapshot.Type != workspace.Type || snapshot.ConfigJSON != workspace.ConfigJSON {
		t.Fatalf("workspace Ensure snapshot = %#v, want identity of %#v", snapshot, workspace)
	}
	if len(driver.startCalls) != 1 || len(driver.startSessions) != 1 {
		t.Fatalf("driver starts = %#v sessions=%d, want one", driver.startCalls, len(driver.startSessions))
	}
	if ensurer.calls[0] != driver.startSessions[0] {
		t.Fatalf("workspace Ensure sandbox pointer = %p, driver sandbox pointer = %p", ensurer.calls[0], driver.startSessions[0])
	}
	if got := strings.Join(order, ","); got != "ensure,guide,driver.start" {
		t.Fatalf("create call order = %q, want %q", got, "ensure,guide,driver.start")
	}
	persisted, err := bridge.store.GetSandbox(ctx, sessionID)
	if err != nil {
		t.Fatalf("GetSandbox after create returned error: %v", err)
	}
	if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("created provisioning state = %#v, want ready", persisted.WorkspaceProvisioning)
	}
	if _, err := os.Stat(deletedSource); !os.IsNotExist(err) {
		t.Fatalf("deleted workspace source was touched, stat error = %v", err)
	}
}

func TestSandboxRPCBridgeCreateSessionWorkspaceEnsurerErrorShortCircuitsDriver(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	ensureErr := errors.New("workspace provisioning failed")
	ensurer := &recordingBridgeWorkspaceEnsurer{err: ensureErr}
	bridge.workspaceEnsurer = ensurer

	_, err := bridge.createSandbox(ctx, sandboxRPCCreateRequest{Title: "workspace failure"}, domain.SandboxTypeManual)
	if err == nil {
		t.Fatal("CreateSession error = nil, want workspace provisioning failure")
	}
	if connect.CodeOf(err) != connect.CodeInternal || !errors.Is(err, ensureErr) {
		t.Fatalf("CreateSession error = %v (code %v), want wrapped internal workspace error", err, connect.CodeOf(err))
	}
	if len(ensurer.calls) != 1 {
		t.Fatalf("workspace Ensure call count = %d, want 1", len(ensurer.calls))
	}
	if len(driver.startCalls) != 0 || len(driver.startSessions) != 0 {
		t.Fatalf("driver starts after workspace error = %#v sessions=%d, want zero", driver.startCalls, len(driver.startSessions))
	}
	persisted, loadErr := bridge.store.GetSandbox(ctx, ensurer.initialIDs[0])
	if loadErr != nil {
		t.Fatalf("GetSandbox after workspace error returned error: %v", loadErr)
	}
	if persisted.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("persisted VM status = %q, want %q", persisted.Summary.VMStatus, domain.VMStatusFailed)
	}
	events, listErr := bridge.store.ListEvents(ctx, persisted.Summary.ID)
	if listErr != nil {
		t.Fatalf("ListEvents after workspace error returned error: %v", listErr)
	}
	if len(events) != 0 {
		t.Fatalf("events after workspace error = %#v, want none", events)
	}
}

func TestSandboxRPCBridgeCreateSessionRuntimeFailurePreservesReadyProvisioning(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	workspace, err := bridge.configDB.CreateWorkspaceConfig(ctx, domain.WorkspaceConfig{
		ID:         "bridge-runtime-failure-workspace",
		Name:       "Bridge Runtime Failure Workspace",
		Type:       "file",
		ConfigJSON: `{"root":"unused-by-fake-ensurer"}`,
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}
	ensurer := &recordingBridgeWorkspaceEnsurer{ensure: func(ctx context.Context, sandbox *domain.Sandbox) error {
		if err := domain.TransitionSandboxWorkspaceProvisioning(sandbox, domain.SandboxWorkspaceProvisioningStatusReady); err != nil {
			return err
		}
		return bridge.store.UpdateSandbox(ctx, sandbox)
	}}
	bridge.workspaceEnsurer = ensurer
	startErr := errors.New("runtime start failed")
	driver.startErr = startErr

	_, err = bridge.createSandbox(ctx, sandboxRPCCreateRequest{
		Title:       "runtime failure",
		WorkspaceID: workspace.ID,
	}, domain.SandboxTypeManual)
	if err == nil || connect.CodeOf(err) != connect.CodeInternal || !errors.Is(err, startErr) {
		t.Fatalf("CreateSession error = %v (code %v), want wrapped internal runtime error", err, connect.CodeOf(err))
	}
	if len(ensurer.calls) != 1 || len(driver.startCalls) != 1 {
		t.Fatalf("workspace Ensure/driver start calls = %d/%d, want 1/1", len(ensurer.calls), len(driver.startCalls))
	}
	persisted, loadErr := bridge.store.GetSandbox(ctx, ensurer.initialIDs[0])
	if loadErr != nil {
		t.Fatalf("GetSandbox after runtime error returned error: %v", loadErr)
	}
	if persisted.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("persisted VM status = %q, want %q", persisted.Summary.VMStatus, domain.VMStatusFailed)
	}
	if persisted.WorkspaceProvisioning == nil || persisted.WorkspaceProvisioning.Status != domain.SandboxWorkspaceProvisioningStatusReady {
		t.Fatalf("persisted workspace provisioning = %#v, want ready", persisted.WorkspaceProvisioning)
	}
}
