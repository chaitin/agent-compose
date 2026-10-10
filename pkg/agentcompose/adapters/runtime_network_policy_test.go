package adapters

import (
	"context"
	"errors"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// capturingNetworkRuntime records the driver-boundary Sandbox the adapter hands
// the runtime, which is the observable seam the enforcement gate reads.
type capturingNetworkRuntime struct {
	sandbox *driverpkg.Sandbox
	calls   int
	err     error
}

func (r *capturingNetworkRuntime) EnsureSandbox(_ context.Context, sandbox *driverpkg.Sandbox, _ driverpkg.VMState, _ driverpkg.ProxyState) (driverpkg.SandboxVMInfo, error) {
	r.calls++
	r.sandbox = sandbox
	if r.err != nil {
		return driverpkg.SandboxVMInfo{}, r.err
	}
	return driverpkg.SandboxVMInfo{}, nil
}

func (*capturingNetworkRuntime) StopSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) (bool, error) {
	return false, nil
}

func (*capturingNetworkRuntime) RemoveSandbox(context.Context, *driverpkg.Sandbox, driverpkg.VMState) error {
	return nil
}

func (*capturingNetworkRuntime) Exec(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

func (*capturingNetworkRuntime) ExecStream(context.Context, *driverpkg.Sandbox, driverpkg.VMState, driverpkg.ExecSpec, driverpkg.ExecStreamWriter) (driverpkg.ExecResult, error) {
	return driverpkg.ExecResult{}, nil
}

// stubNetworkDeclarationResolver stands in for the project store: it returns
// the declaration the sandbox's canonical agent spec holds.
type stubNetworkDeclarationResolver struct {
	declaration *egress.NetworkDeclaration
	err         error
}

func (s stubNetworkDeclarationResolver) ResolveNetworkDeclaration(context.Context, *domain.Sandbox) (*egress.NetworkDeclaration, error) {
	return s.declaration, s.err
}

func networkPolicyTestConfig() *appconfig.Config {
	return &appconfig.Config{
		RuntimeBaseURL: "http://127.0.0.1:7410",
		AgentTelemetry: appconfig.AgentTelemetryConfig{
			Endpoint: "http://127.0.0.1:4318",
		},
	}
}

func networkPolicyTestSession() *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", Driver: driverpkg.RuntimeDriverDocker}}
}

// TestEnsureSandboxCarriesDeclaredNetworkPolicyToDriverBoundary is the
// end-to-end regression: the policy a project declares on its agent must reach
// driverpkg.Sandbox.NetworkPolicy, which is the only value the per-driver
// enforcement reads. Before the wiring it was always nil, so every declared
// policy was inert.
func TestEnsureSandboxCarriesDeclaredNetworkPolicyToDriverBoundary(t *testing.T) {
	denyDeclaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "api.github.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
	permissiveDeclaration := &egress.NetworkDeclaration{Default: egress.Allow}

	tests := []struct {
		name           string
		resolver       SandboxNetworkDeclarationResolver
		wantPolicy     bool
		wantDenyByDef  bool
		wantEndpointOK bool
	}{
		{
			name:           "declared default deny reaches the driver as deny-by-default",
			resolver:       stubNetworkDeclarationResolver{declaration: denyDeclaration},
			wantPolicy:     true,
			wantDenyByDef:  true,
			wantEndpointOK: true,
		},
		{
			name:       "undeclared project leaves the driver policy nil",
			resolver:   stubNetworkDeclarationResolver{declaration: nil},
			wantPolicy: false,
		},
		{
			name:          "declared permissive policy stays permissive",
			resolver:      stubNetworkDeclarationResolver{declaration: permissiveDeclaration},
			wantPolicy:    true,
			wantDenyByDef: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtime := &capturingNetworkRuntime{}
			adapter := driverRuntimeAdapter{
				runtime:    runtime,
				executions: newSandboxExecutions(),
				networkPolicies: driverSandboxNetworkPolicy{
					config:       networkPolicyTestConfig(),
					declarations: tt.resolver,
				},
			}

			if _, err := adapter.EnsureSandbox(context.Background(), networkPolicyTestSession(), domain.VMState{}, domain.ProxyState{}); err != nil {
				t.Fatalf("EnsureSandbox returned error: %v", err)
			}
			if runtime.sandbox == nil {
				t.Fatal("runtime did not receive a driver-boundary sandbox")
			}

			policy := runtime.sandbox.NetworkPolicy
			if !tt.wantPolicy {
				if policy != nil {
					t.Fatalf("NetworkPolicy = %+v, want nil for an undeclared policy (D3)", policy)
				}
				return
			}
			if policy == nil {
				t.Fatal("NetworkPolicy = nil, want the declared policy at the driver boundary")
			}
			if got := policy.DenyByDefault(); got != tt.wantDenyByDef {
				t.Fatalf("DenyByDefault() = %v, want %v (policy %+v)", got, tt.wantDenyByDef, policy)
			}
			if tt.wantEndpointOK && len(policy.EngineEndpoints) != 2 {
				t.Fatalf("EngineEndpoints = %+v, want the sandbox's LLM facade and telemetry endpoints", policy.EngineEndpoints)
			}
		})
	}
}

// TestEnsureSandboxDeclaredDenyFiresTheEnforcementGate pins that the policy the
// adapter builds is strong enough for the existing fail-closed gate: Docker and
// BoxLite and k8s cannot apply it and must refuse, while microsandbox engages.
func TestEnsureSandboxDeclaredDenyFiresTheEnforcementGate(t *testing.T) {
	declaration := &egress.NetworkDeclaration{Default: egress.Deny}
	runtime := &capturingNetworkRuntime{}
	adapter := driverRuntimeAdapter{
		runtime:    runtime,
		executions: newSandboxExecutions(),
		networkPolicies: driverSandboxNetworkPolicy{
			config:       networkPolicyTestConfig(),
			declarations: stubNetworkDeclarationResolver{declaration: declaration},
		},
	}
	if _, err := adapter.EnsureSandbox(context.Background(), networkPolicyTestSession(), domain.VMState{}, domain.ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox returned error: %v", err)
	}
	if runtime.sandbox == nil || runtime.sandbox.NetworkPolicy == nil {
		t.Fatal("driver-boundary policy is nil, so the enforcement gate can never fire")
	}
	policy := runtime.sandbox.NetworkPolicy

	for _, driver := range []string{driverpkg.RuntimeDriverDocker, driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverK8s} {
		if err := driverpkg.RequireSandboxNetworkEnforcement(driver, policy); err == nil {
			t.Fatalf("RequireSandboxNetworkEnforcement(%q) = nil, want a fail-closed refusal", driver)
		} else if !errors.Is(err, driverpkg.ErrSandboxNetworkEnforcementUnavailable) {
			t.Fatalf("RequireSandboxNetworkEnforcement(%q) error = %v, want ErrSandboxNetworkEnforcementUnavailable", driver, err)
		}
	}
	for _, driver := range []string{driverpkg.RuntimeDriverMicrosandbox} {
		if err := driverpkg.RequireSandboxNetworkEnforcement(driver, policy); err != nil {
			t.Fatalf("RequireSandboxNetworkEnforcement(%q) error = %v, want nil so the driver engages", driver, err)
		}
	}
}

// TestEnsureSandboxUndeclaredLeavesGateInert pins D3 at the gate: an undeclared
// project must produce a nil policy and change nothing about today's behavior.
func TestEnsureSandboxUndeclaredLeavesGateInert(t *testing.T) {
	runtime := &capturingNetworkRuntime{}
	adapter := driverRuntimeAdapter{
		runtime:    runtime,
		executions: newSandboxExecutions(),
		networkPolicies: driverSandboxNetworkPolicy{
			config:       networkPolicyTestConfig(),
			declarations: stubNetworkDeclarationResolver{declaration: nil},
		},
	}
	if _, err := adapter.EnsureSandbox(context.Background(), networkPolicyTestSession(), domain.VMState{}, domain.ProxyState{}); err != nil {
		t.Fatalf("EnsureSandbox returned error: %v", err)
	}
	if runtime.sandbox.NetworkPolicy != nil {
		t.Fatalf("NetworkPolicy = %+v, want nil for an undeclared policy", runtime.sandbox.NetworkPolicy)
	}
	for _, driver := range []string{driverpkg.RuntimeDriverDocker, driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverK8s, driverpkg.RuntimeDriverMicrosandbox} {
		if err := driverpkg.RequireSandboxNetworkEnforcement(driver, runtime.sandbox.NetworkPolicy); err != nil {
			t.Fatalf("RequireSandboxNetworkEnforcement(%q, nil) = %v, want nil", driver, err)
		}
	}
}

// TestEnsureSandboxNetworkDeclarationFailureFailsClosed asserts that an
// unresolvable declaration stops the start instead of silently running the
// sandbox unrestricted, which is what a declared default-deny must never do.
func TestEnsureSandboxNetworkDeclarationFailureFailsClosed(t *testing.T) {
	runtime := &capturingNetworkRuntime{}
	adapter := driverRuntimeAdapter{
		runtime:    runtime,
		executions: newSandboxExecutions(),
		networkPolicies: driverSandboxNetworkPolicy{
			config:       networkPolicyTestConfig(),
			declarations: stubNetworkDeclarationResolver{err: errors.New("store unavailable")},
		},
	}
	if _, err := adapter.EnsureSandbox(context.Background(), networkPolicyTestSession(), domain.VMState{}, domain.ProxyState{}); err == nil {
		t.Fatal("EnsureSandbox returned nil, want the declaration resolution failure")
	}
	if runtime.calls != 0 {
		t.Fatalf("runtime EnsureSandbox calls = %d, want 0 before the policy is known", runtime.calls)
	}
}

// guestFileNetworkRuntime records the driver-boundary sandbox every guest-file
// entry point receives and applies the same fail-closed gate the k8s driver
// applies from inside its own EnsureSandbox.
type guestFileNetworkRuntime struct {
	capturingNetworkRuntime
	sandboxes []*driverpkg.Sandbox
}

func (r *guestFileNetworkRuntime) gate(sandbox *driverpkg.Sandbox) error {
	r.sandboxes = append(r.sandboxes, sandbox)
	return driverpkg.RequireSandboxNetworkEnforcement(driverpkg.RuntimeDriverK8s, sandbox.NetworkPolicy)
}

func (r *guestFileNetworkRuntime) ReadGuestFile(_ context.Context, sandbox *driverpkg.Sandbox, _ driverpkg.VMState, _ string) ([]byte, error) {
	return nil, r.gate(sandbox)
}

func (r *guestFileNetworkRuntime) ReadGuestDir(_ context.Context, sandbox *driverpkg.Sandbox, _ driverpkg.VMState, _, _ string) error {
	return r.gate(sandbox)
}

func (r *guestFileNetworkRuntime) WriteGuestFile(_ context.Context, sandbox *driverpkg.Sandbox, _ driverpkg.VMState, _ string, _ []byte) error {
	return r.gate(sandbox)
}

func (r *guestFileNetworkRuntime) WriteGuestDir(_ context.Context, sandbox *driverpkg.Sandbox, _ driverpkg.VMState, _, _ string) error {
	return r.gate(sandbox)
}

func (r *guestFileNetworkRuntime) PublishGuestDirectory(_ context.Context, sandbox *driverpkg.Sandbox, _ driverpkg.VMState, _ driverpkg.GuestDirectoryPublication) error {
	return r.gate(sandbox)
}

func (r *guestFileNetworkRuntime) EnsureGuestSymlink(_ context.Context, sandbox *driverpkg.Sandbox, _ driverpkg.VMState, _ driverpkg.GuestSymlinkProjection) error {
	return r.gate(sandbox)
}

// TestGuestFilePathsCarryDeclaredNetworkPolicy is the regression for a
// fail-closed ordering hole: the guest-file methods reach the driver's own
// EnsureSandbox and can create a k8s Pod, so a sandbox handed to the driver
// without the declared policy would create the runtime before the enforcement
// gate ever saw the declaration.
func TestGuestFilePathsCarryDeclaredNetworkPolicy(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "api.github.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
	runtime := &guestFileNetworkRuntime{}
	adapter := guestFileRuntimeAdapter{driverRuntimeAdapter{
		runtime:    runtime,
		executions: newSandboxExecutions(),
		networkPolicies: driverSandboxNetworkPolicy{
			config:       networkPolicyTestConfig(),
			declarations: stubNetworkDeclarationResolver{declaration: declaration},
		},
	}}
	session := networkPolicyTestSession()
	ctx := context.Background()
	var vmState domain.VMState

	calls := []struct {
		name string
		call func() error
	}{
		{name: "ReadGuestFile", call: func() error {
			_, err := adapter.ReadGuestFile(ctx, session, vmState, "/tmp/x")
			return err
		}},
		{name: "ReadGuestDir", call: func() error {
			return adapter.ReadGuestDir(ctx, session, vmState, "/tmp/x", t.TempDir())
		}},
		{name: "WriteGuestFile", call: func() error {
			return adapter.WriteGuestFile(ctx, session, vmState, "/tmp/x", []byte("x"))
		}},
		{name: "WriteGuestDir", call: func() error {
			return adapter.WriteGuestDir(ctx, session, vmState, t.TempDir(), "/tmp/x")
		}},
		{name: "PublishGuestDirectory", call: func() error {
			return adapter.PublishGuestDirectory(ctx, session, vmState, driverpkg.GuestDirectoryPublication{})
		}},
		{name: "EnsureGuestSymlink", call: func() error {
			return adapter.EnsureGuestSymlink(ctx, session, vmState, driverpkg.GuestSymlinkProjection{})
		}},
	}
	for _, call := range calls {
		t.Run(call.name, func(t *testing.T) {
			runtime.sandboxes = nil
			err := call.call()
			if err == nil {
				t.Fatal("guest-file call returned nil, want the fail-closed gate to refuse a declared default-deny")
			}
			if !errors.Is(err, driverpkg.ErrSandboxNetworkEnforcementUnavailable) {
				t.Fatalf("error = %v, want ErrSandboxNetworkEnforcementUnavailable", err)
			}
			if len(runtime.sandboxes) != 1 || runtime.sandboxes[0] == nil || runtime.sandboxes[0].NetworkPolicy == nil {
				t.Fatalf("driver received %+v, want the declared policy attached", runtime.sandboxes)
			}
		})
	}
}
