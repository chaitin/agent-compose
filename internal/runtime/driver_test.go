package runtime

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"strings"
	"testing"
)

type fakeLoaderAgentRuntime struct{}

func (fakeLoaderAgentRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error) {
	return SessionVMInfo{}, nil
}

func (fakeLoaderAgentRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return true, nil
}

func (fakeLoaderAgentRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, nil
}

func (fakeLoaderAgentRuntime) ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error) {
	return ExecResult{}, nil
}

func TestRuntimeProviderSelectsConfiguredRuntime(t *testing.T) {
	testRuntimeProviderSelectsConfiguredRuntime(t)
}

func testRuntimeProviderSelectsConfiguredRuntime(t *testing.T) {
	t.Helper()
	boxliteRuntime := &fakeLoaderAgentRuntime{}
	dockerRuntime := &fakeLoaderAgentRuntime{}
	provider := &runtimeProvider{
		config: &appconfig.Config{RuntimeDriver: driverpkg.RuntimeDriverDocker},
		runtimes: map[string]BoxRuntime{
			driverpkg.RuntimeDriverBoxlite: boxliteRuntime,
			driverpkg.RuntimeDriverDocker:  dockerRuntime,
		},
	}

	got, err := provider.ForDriver("docker-engine")
	if err != nil {
		t.Fatalf("ForDriver returned error: %v", err)
	}
	if got != dockerRuntime {
		t.Fatalf("ForDriver returned %p, want docker runtime %p", got, dockerRuntime)
	}

	got, err = provider.ForSession(&Session{Summary: SessionSummary{Driver: ""}})
	if err != nil {
		t.Fatalf("ForSession returned error: %v", err)
	}
	if got != dockerRuntime {
		t.Fatalf("ForSession fallback returned %p, want docker runtime %p", got, dockerRuntime)
	}

	if _, err := provider.ForDriver(driverpkg.RuntimeDriverMicrosandbox); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("ForDriver(missing runtime) error = %v, want not configured", err)
	}
	if _, err := provider.ForSession(nil); err == nil || !strings.Contains(err.Error(), "session is required") {
		t.Fatalf("ForSession(nil) error = %v, want session required", err)
	}
}
