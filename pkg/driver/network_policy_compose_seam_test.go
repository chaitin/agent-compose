package driver

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/egress"
)

// TestSandboxNetworkPolicyFromComposeDeclaration feeds a real compose
// declaration through the schema's normalizer and the compose-to-egress seam
// into the driver boundary. It is the regression for a vocabulary mismatch that
// the hand-written egress constants in the other tests could not catch: the
// schema normalizes an omitted (or explicit) permissive default to "allow-all",
// which is not an egress.Action, so a declaration that did not spell out
// default: deny used to fail compilation and refuse to start the sandbox.
func TestSandboxNetworkPolicyFromComposeDeclaration(t *testing.T) {
	tests := []struct {
		name            string
		declaration     *compose.SandboxNetworkSpec
		wantDefault     egress.Action
		wantDenyDefault bool
	}{
		{
			name: "omitted default with an allowance",
			declaration: &compose.SandboxNetworkSpec{
				Allow: []compose.SandboxNetworkAllowSpec{{Host: "api.github.com", Port: 443, Protocol: "https"}},
			},
			wantDefault: egress.Allow,
		},
		{
			name:        "empty declared block",
			declaration: &compose.SandboxNetworkSpec{},
			wantDefault: egress.Allow,
		},
		{
			name:        "explicit allow-all",
			declaration: &compose.SandboxNetworkSpec{Default: "allow-all"},
			wantDefault: egress.Allow,
		},
		{
			name: "declared deny",
			declaration: &compose.SandboxNetworkSpec{
				Default: "deny",
				Allow:   []compose.SandboxNetworkAllowSpec{{Host: "api.github.com", Port: 443, Protocol: "https"}},
			},
			wantDefault:     egress.Deny,
			wantDenyDefault: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := compose.NormalizeSandboxNetworkSpec("agents.worker.sandbox.network", test.declaration)
			if err != nil {
				t.Fatalf("NormalizeSandboxNetworkSpec() error = %v", err)
			}
			declaration, err := normalized.EgressDeclaration()
			if err != nil {
				t.Fatalf("EgressDeclaration() error = %v", err)
			}
			engine := []egress.EngineEndpoint{mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)}
			policy, err := SandboxNetworkPolicyFromDeclaration(&declaration, engine, nil)
			if err != nil {
				t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
			}
			if policy.Default != test.wantDefault {
				t.Fatalf("policy.Default = %q, want %q", policy.Default, test.wantDefault)
			}
			if got := policy.DenyByDefault(); got != test.wantDenyDefault {
				t.Fatalf("DenyByDefault() = %v, want %v", got, test.wantDenyDefault)
			}
			if want := len(declaration.Allow); len(policy.Allow) != want {
				t.Fatalf("policy.Allow = %+v, want %d allowances", policy.Allow, want)
			}
			// A compiled policy must be usable by the decision path, which is
			// what a driver actually consults.
			compiled, err := SandboxNetworkEgressPolicy(policy)
			if err != nil {
				t.Fatalf("SandboxNetworkEgressPolicy() error = %v", err)
			}
			if compiled.Default() != test.wantDefault {
				t.Fatalf("compiled policy default = %q, want %q", compiled.Default(), test.wantDefault)
			}
		})
	}
}

// TestSandboxNetworkPolicyFromUndeclaredComposeBlock pins D3 end to end: a
// project that writes no network block keeps unrestricted egress. An undeclared
// block normalizes to nil, which the driver boundary models as a nil
// declaration rather than as a declared allow-all policy.
func TestSandboxNetworkPolicyFromUndeclaredComposeBlock(t *testing.T) {
	normalized, err := compose.NormalizeSandboxNetworkSpec("agents.worker.sandbox.network", nil)
	if err != nil {
		t.Fatalf("NormalizeSandboxNetworkSpec(nil) error = %v", err)
	}
	if normalized != nil {
		t.Fatalf("NormalizeSandboxNetworkSpec(nil) = %+v, want nil", normalized)
	}
	policy, err := SandboxNetworkPolicyFromDeclaration(nil, nil, nil)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration(nil) error = %v", err)
	}
	if policy.DenyByDefault() {
		t.Fatalf("undeclared block produced a deny policy: %+v", policy)
	}
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverDocker, &policy); err != nil {
		t.Fatalf("undeclared block was refused on docker: %v", err)
	}
}
