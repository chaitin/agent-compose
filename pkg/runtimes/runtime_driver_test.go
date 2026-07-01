package runtimes

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/model"
	"context"
	"strings"
	"testing"
)

func TestRuntimeProviderSelectsConfiguredRuntime(t *testing.T) {
	testRuntimeProviderSelectsConfiguredRuntime(t)
}

func testRuntimeProviderSelectsConfiguredRuntime(t *testing.T) {
	t.Helper()
	boxliteRuntime := &fakeRuntime{}
	dockerRuntime := &fakeRuntime{}
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

	got, err = provider.ForSession(&Session{Summary: model.SessionSummary{Driver: ""}})
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

type fakeRuntime struct{}

func (f *fakeRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (SessionVMInfo, error) {
	return SessionVMInfo{}, nil
}

func (f *fakeRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return false, nil
}

func (f *fakeRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, nil
}

func (f *fakeRuntime) ExecStream(context.Context, *Session, VMState, ExecSpec, ExecStreamWriter) (ExecResult, error) {
	return ExecResult{}, nil
}
