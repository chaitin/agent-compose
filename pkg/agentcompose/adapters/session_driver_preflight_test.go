package adapters

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

// refusingPreflightRuntime refuses the preflight and counts the calls that
// reached EnsureSandbox. Runtime state must not be created after the runtime
// itself said it cannot start, so the count is the observable fact this test
// asserts on rather than the fake's return value.
type refusingPreflightRuntime struct {
	preflightErr error
	ensureCalls  *int
}

func (r refusingPreflightRuntime) PreflightSandboxRuntime(context.Context, *domain.Sandbox, domain.VMState) error {
	return r.preflightErr
}

func (r refusingPreflightRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	*r.ensureCalls++
	return domain.SandboxVMInfo{}, nil
}

func (r refusingPreflightRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r refusingPreflightRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r refusingPreflightRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

func (r refusingPreflightRuntime) ExecStream(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error) {
	return domain.ExecResult{}, nil
}

type refusingPreflightRuntimeProvider struct {
	runtime SandboxRuntime
}

func (p refusingPreflightRuntimeProvider) ForDriver(string) (SandboxRuntime, error) {
	return p.runtime, nil
}

func (p refusingPreflightRuntimeProvider) ForSession(*domain.Sandbox) (SandboxRuntime, error) {
	return p.runtime, nil
}

// newPreflightSandboxDriverFixture builds the smallest driver a preflight needs:
// a real store with one sandbox record, and the supplied runtime provider.
func newPreflightSandboxDriverFixture(t *testing.T, provider RuntimeProvider) (*SandboxDriver, *domain.Sandbox) {
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
	session, err := store.CreateSandbox(ctx, "k8s preflight", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	return newTestSandboxDriver(config, store, nil, provider), session
}

// TestSandboxDriverPreflightFailureStopsTheStartBeforeRuntimeState pins the
// order StartSandboxVM promises: a runtime that reports it is unusable fails the
// sandbox with a classified reason and no runtime is created. Reversing the
// order, or dropping the preflight entirely, leaves an unstartable sandbox with
// partially created state for cleanup to chase - the behavior the test round
// observed when no cluster was configured.
func TestSandboxDriverPreflightFailureStopsTheStartBeforeRuntimeState(t *testing.T) {
	ctx := context.Background()
	ensureCalls := 0
	provider := refusingPreflightRuntimeProvider{runtime: refusingPreflightRuntime{
		preflightErr: domain.ClassifyError(domain.ErrFailedPrecondition, "kubernetes runtime is not usable", errors.New("no kubeconfig")),
		ensureCalls:  &ensureCalls,
	}}
	driver, session := newPreflightSandboxDriverFixture(t, provider)
	store := driver.Store

	err := driver.StartSandboxVM(ctx, session)
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("StartSandboxVM error = %v, want a failed precondition", err)
	}
	if ensureCalls != 0 {
		t.Fatalf("EnsureSandbox called %d times after the preflight refused, want 0", ensureCalls)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.LastError == "" {
		t.Fatal("GetVMState LastError is empty, want the preflight failure recorded for the sandbox")
	}
}

// TestSandboxDriverPreflightAsksTheRuntimeForTheSandboxsDriver pins what the run
// path relies on when it asks for the check before creating a sandbox record:
// the runtime is resolved from the state the caller describes, and the answer is
// the runtime's own.
func TestSandboxDriverPreflightAsksTheRuntimeForTheSandboxsDriver(t *testing.T) {
	ctx := context.Background()
	ensureCalls := 0
	preflightErr := errors.New("cluster unreachable")
	provider := refusingPreflightRuntimeProvider{runtime: refusingPreflightRuntime{
		preflightErr: preflightErr,
		ensureCalls:  &ensureCalls,
	}}
	driver, session := newPreflightSandboxDriverFixture(t, provider)
	session.Summary.Driver = ""

	err := driver.PreflightSandboxRuntime(ctx, session, domain.VMState{Driver: driverpkg.RuntimeDriverBoxlite, K8sContext: "cluster-a"})
	if !errors.Is(err, preflightErr) {
		t.Fatalf("PreflightSandboxRuntime error = %v, want the runtime's own refusal", err)
	}
}

// TestSandboxDriverPreflightAcceptsARuntimeWithoutTheCapability keeps the
// capability optional: most drivers cannot judge their own usability, and the
// run path must not refuse them for that.
func TestSandboxDriverPreflightAcceptsARuntimeWithoutTheCapability(t *testing.T) {
	driver, session := newPreflightSandboxDriverFixture(t, &sequenceRuntimeProvider{})

	if err := driver.PreflightSandboxRuntime(context.Background(), session, domain.VMState{}); err != nil {
		t.Fatalf("PreflightSandboxRuntime = %v, want nil for a runtime without the capability", err)
	}
}
