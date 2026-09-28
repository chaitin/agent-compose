package adapters

import (
	"context"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestSchedulerSandboxRunnerRebindsRunningSandboxToRunTrustedHeaders(t *testing.T) {
	ctx := context.Background()
	bridge, driver := newTestSandboxRPCBridge(t)
	resolver := NewCapabilitySandboxResolver(bridge.store)
	resolver.initialized = true
	runner := NewSchedulerSandboxRunner(SchedulerSandboxRunnerDeps{
		Config:           bridge.config,
		Store:            bridge.store,
		ConfigDB:         bridge.configDB,
		WorkspaceEnsurer: bridge.workspaceEnsurer,
		Driver:           driver,
		Streams:          bridge.streams,
		CapTokens:        resolver,
		AgentExecutor:    bridge.agentExecutor,
	})
	sandbox, err := bridge.store.CreateSandbox(ctx, "sticky", "", driverpkg.RuntimeDriverDocker, "", "", "scheduler", nil,
		[]domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: "sticky-token", Secret: true}},
		[]domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "dev"}},
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusRunning
	if err := bridge.store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}

	runCtx := domain.NewContextWithTrustedHeaders(ctx, []domain.TrustedHeader{{Name: "x-mpi-user-id", Value: "user-1"}})
	if _, _, err := runner.LoadOrResume(runCtx, sandbox.Summary.ID); err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	binding, err := resolver.ResolveCapabilitySandbox(ctx, "sticky-token")
	if err != nil {
		t.Fatalf("ResolveCapabilitySandbox returned error: %v", err)
	}
	want := []domain.TrustedHeader{{Name: "x-octobus-ext-user-id", Value: "user-1"}}
	if !reflect.DeepEqual(binding.TrustedHeaders, want) {
		t.Fatalf("trusted headers = %#v, want %#v", binding.TrustedHeaders, want)
	}

	if _, _, err := runner.LoadOrResume(ctx, sandbox.Summary.ID); err != nil {
		t.Fatalf("LoadOrResume returned error: %v", err)
	}
	binding, err = resolver.ResolveCapabilitySandbox(ctx, "sticky-token")
	if err != nil {
		t.Fatalf("ResolveCapabilitySandbox returned error: %v", err)
	}
	if len(binding.TrustedHeaders) != 0 {
		t.Fatalf("run without trusted headers kept the previous binding: %#v", binding.TrustedHeaders)
	}
}
