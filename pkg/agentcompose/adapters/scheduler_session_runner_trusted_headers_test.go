package adapters

import (
	"context"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/capabilities"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func newTrustedHeaderSchedulerRunner(t *testing.T, status string) (*SchedulerSandboxRunner, *CapabilitySandboxResolver, *domain.Sandbox, string) {
	t.Helper()
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

	const token = "capability-token"
	sandbox, err := bridge.store.CreateSandbox(ctx, "headers", "", driverpkg.RuntimeDriverBoxlite, "", "", "scheduler", nil,
		[]domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: token, Secret: true}},
		[]domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "dev"}},
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = status
	if err := bridge.store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	if err := bridge.store.SaveVMState(sandbox.Summary.ID, domain.VMState{
		Driver:    driverpkg.RuntimeDriverBoxlite,
		StartedAt: time.Now().Add(-time.Minute).UTC(),
	}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	return runner, resolver, sandbox, token
}

// Characterizes that resuming a sandbox through the scheduler runner indexes
// its capability token with no trusted headers, even when the request context
// carries some.
func TestSchedulerSandboxRunnerResumeIgnoresRequestTrustedHeaders(t *testing.T) {
	runner, resolver, sandbox, token := newTrustedHeaderSchedulerRunner(t, domain.VMStatusStopped)
	ctx := domain.NewContextWithTrustedHeaders(context.Background(), []domain.TrustedHeader{{Name: "x-mpi-user", Value: "bob"}})

	if _, eventType, err := runner.LoadOrResume(ctx, sandbox.Summary.ID); err != nil || eventType != "scheduler.sandbox.resumed" {
		t.Fatalf("LoadOrResume event=%q err=%v", eventType, err)
	}
	binding, ok := resolver.tokens[token]
	if !ok {
		t.Fatal("resumed sandbox token was not indexed")
	}
	if len(binding.TrustedHeaders) != 0 {
		t.Fatalf("resumed binding headers = %#v, want none", binding.TrustedHeaders)
	}
}

// Characterizes that LoadOrResume on an already running sandbox returns early
// and leaves whatever binding a previous caller indexed untouched.
func TestSchedulerSandboxRunnerRunningSandboxKeepsExistingTrustedHeaders(t *testing.T) {
	runner, resolver, sandbox, token := newTrustedHeaderSchedulerRunner(t, domain.VMStatusRunning)
	resolver.IndexSandbox(sandbox, []domain.TrustedHeader{{Name: "x-mpi-user", Value: "bob"}})
	before := resolver.tokens[token].TrustedHeaders
	if len(before) != 1 {
		t.Fatalf("setup binding headers = %#v, want one", before)
	}

	if _, eventType, err := runner.LoadOrResume(context.Background(), sandbox.Summary.ID); err != nil || eventType != "" {
		t.Fatalf("LoadOrResume event=%q err=%v, want early return", eventType, err)
	}
	after := resolver.tokens[token].TrustedHeaders
	if len(after) != 1 || after[0] != before[0] {
		t.Fatalf("binding headers changed: before=%#v after=%#v", before, after)
	}
}
