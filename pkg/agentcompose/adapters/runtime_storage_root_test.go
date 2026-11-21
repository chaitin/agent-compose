package adapters

import (
	"context"
	"errors"
	"fmt"
	"testing"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// storageRootRefusingRuntime refuses every operation the way the Microsandbox
// driver does while its storage root is unsafe.
type storageRootRefusingRuntime struct{}

func storageRootRefusal() error {
	return fmt.Errorf("%w: ancestor /data of /data/microsandbox has mode 0775", driverpkg.ErrSandboxStorageRootUnsafe)
}

func (storageRootRefusingRuntime) EnsureSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ProxyState) (driverpkg.SandboxVMInfo, error) {
	return driverpkg.SandboxVMInfo{}, storageRootRefusal()
}

func (storageRootRefusingRuntime) StopSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error) {
	return false, storageRootRefusal()
}

func (storageRootRefusingRuntime) RemoveSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) error {
	return storageRootRefusal()
}

func (storageRootRefusingRuntime) Exec(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, storageRootRefusal()
}

func (storageRootRefusingRuntime) ExecStream(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec, driverpkg.ExecStreamWriter) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, storageRootRefusal()
}

func TestClassifyStorageRootError(t *testing.T) {
	if err := classifyStorageRootError(storageRootRefusal()); !errors.Is(err, domain.ErrFailedPrecondition) || !errors.Is(err, driverpkg.ErrSandboxStorageRootUnsafe) {
		t.Fatalf("classified = %v, want failed precondition wrapping the driver sentinel", err)
	}
	other := errors.New("io error: disk full")
	if err := classifyStorageRootError(other); err == nil || err.Error() != other.Error() || errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("unrelated error rewritten to %v", err)
	}
	if err := classifyStorageRootError(nil); err != nil {
		t.Fatalf("classifyStorageRootError(nil) = %v", err)
	}
}

// Every runtime operation must report an unsafe storage root as a failed
// precondition. On devboard the refusal first surfaced from a stop issued by
// run cleanup, not from a create, so covering only EnsureSandbox would leave
// the cleanup path reporting an opaque internal error.
func TestRuntimeAdapterClassifiesStorageRootRefusalOnEveryOperation(t *testing.T) {
	ctx := context.Background()
	session := networkPolicyTestSession()
	// Each operation gets its own adapter: a stop blocks new executions on the
	// sandbox, which would otherwise mask the Exec classification.
	operations := map[string]func(adapter driverRuntimeAdapter) error{
		"EnsureSandbox": func(adapter driverRuntimeAdapter) error {
			_, err := adapter.EnsureSandbox(ctx, session, domain.VMState{}, domain.ProxyState{})
			return err
		},
		"StopSandbox": func(adapter driverRuntimeAdapter) error {
			_, err := adapter.StopSandbox(ctx, session, domain.VMState{})
			return err
		},
		"RemoveSandbox": func(adapter driverRuntimeAdapter) error {
			return adapter.RemoveSandbox(ctx, session, domain.VMState{})
		},
		"Exec": func(adapter driverRuntimeAdapter) error {
			_, err := adapter.Exec(ctx, session, domain.VMState{}, domain.ExecSpec{Command: "true"})
			return err
		},
		"ExecStream": func(adapter driverRuntimeAdapter) error {
			_, err := adapter.ExecStream(ctx, session, domain.VMState{}, domain.ExecSpec{Command: "true"}, nil)
			return err
		},
	}
	for name, operation := range operations {
		err := operation(driverRuntimeAdapter{runtime: storageRootRefusingRuntime{}, executions: newSandboxExecutions()})
		if !errors.Is(err, domain.ErrFailedPrecondition) || !errors.Is(err, driverpkg.ErrSandboxStorageRootUnsafe) {
			t.Errorf("%s error = %v, want failed precondition wrapping ErrSandboxStorageRootUnsafe", name, err)
		}
	}
}
