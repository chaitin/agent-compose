package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type resourceLimitsResolverStub struct {
	limits *compose.NormalizedSandboxResourcesSpec
	err    error
}

func (s resourceLimitsResolverStub) ResolveResourceLimits(context.Context, *domain.Sandbox) (*compose.NormalizedSandboxResourcesSpec, error) {
	return s.limits, s.err
}

func TestDriverSandboxResourceLimitsResolve(t *testing.T) {
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1"}}
	declared := &compose.NormalizedSandboxResourcesSpec{CPUMillis: 2000, MemoryMiB: 4096}

	t.Run("zero value is inert", func(t *testing.T) {
		got, err := driverSandboxResourceLimits{}.resolve(context.Background(), session)
		if err != nil || got != nil {
			t.Fatalf("resolve = %+v, %v; want nil", got, err)
		}
	})

	t.Run("undeclared stays nil", func(t *testing.T) {
		gate := driverSandboxResourceLimits{declarations: resourceLimitsResolverStub{}}.forDriver(driverpkg.RuntimeDriverDocker)
		got, err := gate.resolve(context.Background(), session)
		if err != nil || got != nil {
			t.Fatalf("resolve = %+v, %v; want nil", got, err)
		}
	})

	t.Run("declared", func(t *testing.T) {
		gate := driverSandboxResourceLimits{declarations: resourceLimitsResolverStub{limits: declared}}.forDriver(driverpkg.RuntimeDriverDocker)
		got, err := gate.resolve(context.Background(), session)
		if err != nil || got == nil || got.CPUMillis != 2000 || got.MemoryMiB != 4096 {
			t.Fatalf("resolve = %+v, %v; want the declared limits", got, err)
		}
	})

	// The declaration wins over the microVM defaults in both directions.
	t.Run("declared above the microVM defaults", func(t *testing.T) {
		large := &compose.NormalizedSandboxResourcesSpec{CPUMillis: 16000, MemoryMiB: 65536}
		gate := driverSandboxResourceLimits{declarations: resourceLimitsResolverStub{limits: large}}.forDriver(driverpkg.RuntimeDriverMicrosandbox)
		got, err := gate.resolve(context.Background(), session)
		if err != nil || got == nil || got.CPUMillis != 16000 || got.MemoryMiB != 65536 {
			t.Fatalf("resolve = %+v, %v; want the declared limits", got, err)
		}
	})

	t.Run("driver that cannot honor the declaration", func(t *testing.T) {
		gate := driverSandboxResourceLimits{
			declarations: resourceLimitsResolverStub{limits: &compose.NormalizedSandboxResourcesSpec{CPUMillis: 500}},
		}.forDriver(driverpkg.RuntimeDriverMicrosandbox)
		_, err := gate.resolve(context.Background(), session)
		if !errors.Is(err, domain.ErrFailedPrecondition) || !errors.Is(err, driverpkg.ErrSandboxResourceLimitsUnavailable) {
			t.Fatalf("resolve error = %v, want a classified ErrSandboxResourceLimitsUnavailable", err)
		}
	})

	t.Run("resolver error propagates", func(t *testing.T) {
		gate := driverSandboxResourceLimits{declarations: resourceLimitsResolverStub{err: fmt.Errorf("store down")}}.forDriver(driverpkg.RuntimeDriverDocker)
		if _, err := gate.resolve(context.Background(), session); err == nil || !strings.Contains(err.Error(), "store down") {
			t.Fatalf("resolve error = %v, want the resolver failure", err)
		}
	})
}

func TestClassifyResourceLimitsError(t *testing.T) {
	driverErr := fmt.Errorf("create: %w", driverpkg.ErrSandboxResourceLimitsUnavailable)
	if err := classifyResourceLimitsError(driverErr); !errors.Is(err, domain.ErrFailedPrecondition) || !errors.Is(err, driverpkg.ErrSandboxResourceLimitsUnavailable) {
		t.Fatalf("classified = %v, want failed precondition wrapping the driver sentinel", err)
	}
	other := errors.New("docker unreachable")
	if err := classifyResourceLimitsError(other); err == nil || err.Error() != other.Error() || errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("unrelated error rewritten to %v", err)
	}
}

// TestEnsureSandboxCarriesDeclaredResourceLimits pins the seam: a declared
// limit reaches the driver, and a refused one never reaches it at all.
func TestEnsureSandboxCarriesDeclaredResourceLimits(t *testing.T) {
	runtime := &capturingNetworkRuntime{}
	adapter := driverRuntimeAdapter{
		runtime:    runtime,
		executions: newSandboxExecutions(),
		resourceLimits: driverSandboxResourceLimits{
			declarations: resourceLimitsResolverStub{limits: &compose.NormalizedSandboxResourcesSpec{CPUMillis: 1000, MemoryMiB: 1024}},
		}.forDriver(driverpkg.RuntimeDriverDocker),
	}
	if _, err := adapter.EnsureSandbox(context.Background(), networkPolicyTestSession(), domain.VMState{}, domain.ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox: %v", err)
	}
	if runtime.sandbox == nil || runtime.sandbox.ResourceLimits == nil || *runtime.sandbox.ResourceLimits != (driverpkg.SandboxResourceLimits{CPUMillis: 1000, MemoryMiB: 1024}) {
		t.Fatalf("driver received limits %+v, want the declaration", runtime.sandbox)
	}

	refused := &capturingNetworkRuntime{}
	adapter.runtime = refused
	adapter.resourceLimits = driverSandboxResourceLimits{
		declarations: resourceLimitsResolverStub{limits: &compose.NormalizedSandboxResourcesSpec{CPUMillis: 1500}},
	}.forDriver(driverpkg.RuntimeDriverMicrosandbox)
	if _, err := adapter.EnsureSandbox(context.Background(), networkPolicyTestSession(), domain.VMState{}, domain.ProxyState{}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("EnsureSandbox with fractional microVM cpus error = %v, want a failed precondition", err)
	}
	if refused.sandbox != nil {
		t.Fatal("a refused declaration still reached the driver")
	}
}
