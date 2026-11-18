package adapters

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
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
		gate := driverSandboxResourceLimits{config: &appconfig.Config{}, declarations: resourceLimitsResolverStub{}}.forDriver(driverpkg.RuntimeDriverDocker)
		got, err := gate.resolve(context.Background(), session)
		if err != nil || got != nil {
			t.Fatalf("resolve = %+v, %v; want nil", got, err)
		}
	})

	t.Run("declared within the ceiling", func(t *testing.T) {
		gate := driverSandboxResourceLimits{
			config:       &appconfig.Config{SandboxMaxCPUMillis: 4000, SandboxMaxMemoryMiB: 8192},
			declarations: resourceLimitsResolverStub{limits: declared},
		}.forDriver(driverpkg.RuntimeDriverDocker)
		got, err := gate.resolve(context.Background(), session)
		if err != nil || got == nil || got.CPUMillis != 2000 || got.MemoryMiB != 4096 {
			t.Fatalf("resolve = %+v, %v; want the declared limits", got, err)
		}
	})

	for name, config := range map[string]*appconfig.Config{
		"SANDBOX_MAX_CPUS":       {SandboxMaxCPUMillis: 1500},
		"SANDBOX_MAX_MEMORY_MIB": {SandboxMaxMemoryMiB: 2048},
	} {
		t.Run("above "+name, func(t *testing.T) {
			gate := driverSandboxResourceLimits{config: config, declarations: resourceLimitsResolverStub{limits: declared}}.forDriver(driverpkg.RuntimeDriverDocker)
			_, err := gate.resolve(context.Background(), session)
			if !errors.Is(err, domain.ErrFailedPrecondition) || !strings.Contains(err.Error(), name) {
				t.Fatalf("resolve error = %v, want a failed precondition naming %s", err, name)
			}
		})
	}

	t.Run("driver that cannot honor the declaration", func(t *testing.T) {
		gate := driverSandboxResourceLimits{
			config:       &appconfig.Config{},
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
			config:       networkPolicyTestConfig(),
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
	config := networkPolicyTestConfig()
	config.SandboxMaxMemoryMiB = 512
	adapter.runtime = refused
	adapter.resourceLimits.config = config
	if _, err := adapter.EnsureSandbox(context.Background(), networkPolicyTestSession(), domain.VMState{}, domain.ProxyState{}); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("EnsureSandbox above the ceiling error = %v, want a failed precondition", err)
	}
	if refused.sandbox != nil {
		t.Fatal("a refused declaration still reached the driver")
	}
}
