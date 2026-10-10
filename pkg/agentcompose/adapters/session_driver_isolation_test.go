package adapters

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/capmatrix"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

func testEngineCapabilitySnapshot(t *testing.T) capmatrix.Snapshot {
	t.Helper()
	facts, err := driverpkg.CompiledRuntimeCapabilities()
	if err != nil {
		t.Fatalf("CompiledRuntimeCapabilities() error = %v", err)
	}
	snapshot, err := capmatrix.BuildSnapshot(facts, time.Now().UTC())
	if err != nil {
		t.Fatalf("BuildSnapshot() error = %v", err)
	}
	return snapshot
}

// isolationTestDriver builds a SandboxDriver whose capability snapshot is the
// real frozen matrix, so the fail-closed decision uses the same declarations
// production does.
func isolationTestDriver(t *testing.T, runtime SandboxRuntime) (*SandboxDriver, *sandboxstore.Store, *domain.Sandbox) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		BoxliteHome:          filepath.Join(root, "boxlite"),
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "isolation session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	driver := NewSandboxDriver(config, store, nil, fakeRuntimeProvider{runtime: runtime}, testEngineCapabilitySnapshot(t))
	return driver, store, session
}

// TestStartSandboxVMDefaultPathIsUnchangedByTheIsolationGate is the D3
// regression test: with no declared isolation requirement the structural gate
// runs, is satisfied, and the runtime is created exactly as before.
func TestStartSandboxVMDefaultPathIsUnchangedByTheIsolationGate(t *testing.T) {
	ensureCalled := false
	runtime := fakeSessionRuntime{
		info:       domain.SandboxVMInfo{BoxID: "container-1"},
		ensureHook: func(*domain.Sandbox) { ensureCalled = true },
	}
	driver, store, session := isolationTestDriver(t, runtime)
	if len(session.IsolationRequirements) != 0 {
		t.Fatalf("a freshly created sandbox already declares isolation requirements: %v", session.IsolationRequirements)
	}

	if err := driver.StartSandboxVM(context.Background(), session); err != nil {
		t.Fatalf("StartSandboxVM returned error: %v", err)
	}
	if !ensureCalled {
		t.Fatal("EnsureSandbox was not called on the default path")
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.BoxID != "container-1" {
		t.Fatalf("vmState.BoxID = %q, want the runtime to have started", vmState.BoxID)
	}
}

// TestStartSandboxVMFailsClosedWhenDefaultDenyEgressIsDeclared is the
// acceptance test: declaring default-deny egress on Docker fails the start
// with a determinable reason, before any runtime state is created.
func TestStartSandboxVMFailsClosedWhenDefaultDenyEgressIsDeclared(t *testing.T) {
	ensureCalled := false
	runtime := fakeSessionRuntime{
		info:       domain.SandboxVMInfo{BoxID: "container-1"},
		ensureHook: func(*domain.Sandbox) { ensureCalled = true },
	}
	driver, store, session := isolationTestDriver(t, runtime)
	session.Summary.Driver = driverpkg.RuntimeDriverDocker
	session.IsolationRequirements = []string{"egress_policy"}

	err := driver.StartSandboxVM(context.Background(), session)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("StartSandboxVM error = %v, want ErrFailedPrecondition", err)
	}
	if !strings.Contains(err.Error(), "egress_policy=not_enforced") {
		t.Fatalf("StartSandboxVM error = %v, want the machine-consumable reason", err)
	}
	if ensureCalled {
		t.Fatal("the runtime was created even though the isolation pre-flight failed")
	}
	vmState, stateErr := store.GetVMState(session.Summary.ID)
	if stateErr != nil {
		t.Fatalf("GetVMState returned error: %v", stateErr)
	}
	if !vmState.StartAttemptedAt.IsZero() {
		t.Fatalf("vmState.StartAttemptedAt = %v, want the pre-flight to run before the start-attempt fence", vmState.StartAttemptedAt)
	}
}

func TestStartSandboxVMRejectsAnUnknownIsolationRequirement(t *testing.T) {
	runtime := fakeSessionRuntime{info: domain.SandboxVMInfo{BoxID: "container-1"}}
	driver, _, session := isolationTestDriver(t, runtime)
	session.IsolationRequirements = []string{"not_a_dimension"}

	err := driver.StartSandboxVM(context.Background(), session)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("StartSandboxVM error = %v, want ErrFailedPrecondition", err)
	}
	if !strings.Contains(err.Error(), "not_a_dimension") {
		t.Fatalf("StartSandboxVM error = %v, want the offending dimension named", err)
	}
}

func TestStartSandboxVMAllowsABestEffortIsolationRequirement(t *testing.T) {
	ensureCalled := false
	runtime := fakeSessionRuntime{
		info:       domain.SandboxVMInfo{BoxID: "container-1"},
		ensureHook: func(*domain.Sandbox) { ensureCalled = true },
	}
	driver, _, session := isolationTestDriver(t, runtime)
	session.IsolationRequirements = []string{"best_effort:egress_policy"}

	if err := driver.StartSandboxVM(context.Background(), session); err != nil {
		t.Fatalf("StartSandboxVM returned error for a best-effort requirement: %v", err)
	}
	if !ensureCalled {
		t.Fatal("EnsureSandbox was not called for a best-effort requirement")
	}
}
