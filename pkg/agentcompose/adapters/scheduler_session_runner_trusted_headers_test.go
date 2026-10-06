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

var schedulerUserTestHeaders = []domain.TrustedHeader{{Name: "x-mpi-user", Value: "bob"}}

func schedulerUserTestContext() context.Context {
	return domain.NewContextWithTrustedHeaders(context.Background(), schedulerUserTestHeaders)
}

func assertBoundUser(t *testing.T, resolver *CapabilitySandboxResolver, token, want string) {
	t.Helper()
	binding, ok := resolver.tokens[token]
	if !ok {
		t.Fatal("sandbox token was not indexed")
	}
	if want == "" {
		if len(binding.TrustedHeaders) != 0 {
			t.Fatalf("binding headers = %#v, want none", binding.TrustedHeaders)
		}
		return
	}
	if len(binding.TrustedHeaders) != 1 || binding.TrustedHeaders[0] != (domain.TrustedHeader{Name: "x-octobus-ext-user", Value: want}) {
		t.Fatalf("binding headers = %#v, want x-octobus-ext-user=%s", binding.TrustedHeaders, want)
	}
}

// A sandbox resumed through the scheduler runner is bound to the identity of
// the execution resuming it: the caller's for a run a user started, none for
// an unattended run.
func TestSchedulerSandboxRunnerResumeBindsExecutionIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"user":       {ctx: schedulerUserTestContext(), want: "bob"},
		"unattended": {ctx: context.Background(), want: ""},
	} {
		t.Run(name, func(t *testing.T) {
			runner, resolver, sandbox, token := newTrustedHeaderSchedulerRunner(t, domain.VMStatusStopped)
			if _, eventType, err := runner.LoadOrResume(tc.ctx, sandbox.Summary.ID); err != nil || eventType != "scheduler.sandbox.resumed" {
				t.Fatalf("LoadOrResume event=%q err=%v", eventType, err)
			}
			assertBoundUser(t, resolver, token, tc.want)
		})
	}
}

// Entering a sandbox that is already running replaces the binding its previous
// user left behind, so an unattended run never calls capsets as that user and a
// user never inherits someone else's identity.
func TestSchedulerSandboxRunnerRunningSandboxRebindsToExecutionIdentity(t *testing.T) {
	for name, tc := range map[string]struct {
		ctx  context.Context
		want string
	}{
		"unattended clears a left-over user": {ctx: context.Background(), want: ""},
		"user replaces a left-over user":     {ctx: schedulerUserTestContext(), want: "bob"},
	} {
		t.Run(name, func(t *testing.T) {
			runner, resolver, sandbox, token := newTrustedHeaderSchedulerRunner(t, domain.VMStatusRunning)
			resolver.IndexSandbox(sandbox, []domain.TrustedHeader{{Name: "x-mpi-user", Value: "alice"}})
			assertBoundUser(t, resolver, token, "alice")

			if _, eventType, err := runner.LoadOrResume(tc.ctx, sandbox.Summary.ID); err != nil || eventType != "" {
				t.Fatalf("LoadOrResume event=%q err=%v, want early return", eventType, err)
			}
			assertBoundUser(t, resolver, token, tc.want)
		})
	}
}

func newTrustedHeaderRPCBridge(t *testing.T, token string) (*SandboxRPCBridge, *CapabilitySandboxResolver, *domain.Sandbox) {
	t.Helper()
	ctx := context.Background()
	bridge, _ := newTestSandboxRPCBridge(t)
	resolver := NewCapabilitySandboxResolver(bridge.store)
	resolver.initialized = true
	bridge.capTokens = resolver
	sandbox, err := bridge.store.CreateSandbox(ctx, "identity", "", driverpkg.RuntimeDriverBoxlite, "", "", domain.SandboxTypeManual, nil,
		[]domain.SandboxEnvVar{{Name: capabilities.SandboxTokenEnvName, Value: token, Secret: true}},
		[]domain.SandboxTag{{Name: capabilities.CapsetTagName, Value: "dev"}},
	)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusStopped
	if err := bridge.store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	if err := bridge.store.SaveVMState(sandbox.Summary.ID, domain.VMState{
		Driver:    driverpkg.RuntimeDriverBoxlite,
		StartedAt: time.Now().Add(-time.Minute).UTC(),
	}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	return bridge, resolver, sandbox
}

// A scheduler script's sandbox RPC acts as the execution it belongs to, so a
// sandbox it resumes is bound to that execution's identity, like the script's
// agent and command calls.
func TestSandboxRPCBridgeScriptResumeBindsExecutionIdentity(t *testing.T) {
	const token = "rpc-script-token"
	bridge, resolver, sandbox := newTrustedHeaderRPCBridge(t, token)

	request := `{"sandboxId":"` + sandbox.Summary.ID + `"}`
	if _, err := bridge.CallJSONWithSource(schedulerUserTestContext(), "ResumeSandbox", request, domain.SandboxTypeScript+":scheduler-1"); err != nil {
		t.Fatalf("script ResumeSandbox returned error: %v", err)
	}
	assertBoundUser(t, resolver, token, "bob")
}

// The SandboxService ResumeSandbox RPC is a lifecycle operation: it binds the
// resumed sandbox without an identity even when the request carries one.
func TestSandboxRPCBridgeExternalResumeBindsNoIdentity(t *testing.T) {
	const token = "rpc-external-token"
	bridge, resolver, sandbox := newTrustedHeaderRPCBridge(t, token)

	if _, err := bridge.ResumeSandbox(schedulerUserTestContext(), sandbox.Summary.ID); err != nil {
		t.Fatalf("ResumeSandbox returned error: %v", err)
	}
	assertBoundUser(t, resolver, token, "")
}
