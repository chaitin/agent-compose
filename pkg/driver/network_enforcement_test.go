package driver

import (
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

func TestSandboxNetworkEnforcementFor(t *testing.T) {
	tests := []struct {
		driver             string
		wantStrength       SandboxEgressStrength
		wantMechanism      string
		wantAppliesAllow   bool
		wantAppliesEngine  bool
		wantAppliesDomains bool
		wantCNI            bool
	}{
		{
			driver: RuntimeDriverMicrosandbox, wantStrength: SandboxEgressStrengthAllowList,
			wantMechanism: mechanismSDKNetworkPolicy, wantAppliesAllow: true, wantAppliesEngine: true, wantAppliesDomains: true,
		},
		{
			// k8s generates a per-sandbox egress NetworkPolicy, which is an
			// outer deny: it cannot express the allowance list, so it must not
			// claim one.
			driver: RuntimeDriverK8s, wantStrength: SandboxEgressStrengthOuterDeny,
			wantMechanism: mechanismNetworkPolicyEgress, wantCNI: true,
		},
		{
			driver: RuntimeDriverDocker, wantStrength: SandboxEgressStrengthOuterDeny,
			wantMechanism: mechanismDockerNetworkModeNone, wantAppliesAllow: false, wantAppliesEngine: false, wantAppliesDomains: false,
		},
		{
			driver: RuntimeDriverBoxlite, wantStrength: SandboxEgressStrengthNone,
			wantMechanism: reasonNotConfigured,
		},
		{
			driver: "msb", wantStrength: SandboxEgressStrengthAllowList, wantMechanism: mechanismSDKNetworkPolicy,
			wantAppliesAllow: true, wantAppliesEngine: true, wantAppliesDomains: true,
		},
	}
	for _, test := range tests {
		t.Run(test.driver, func(t *testing.T) {
			enforcement := SandboxNetworkEnforcementFor(test.driver)
			if enforcement.Strength != test.wantStrength {
				t.Fatalf("Strength = %q, want %q", enforcement.Strength, test.wantStrength)
			}
			if enforcement.Mechanism != test.wantMechanism {
				t.Fatalf("Mechanism = %q, want %q", enforcement.Mechanism, test.wantMechanism)
			}
			if enforcement.AppliesAllowEntries != test.wantAppliesAllow {
				t.Fatalf("AppliesAllowEntries = %v, want %v", enforcement.AppliesAllowEntries, test.wantAppliesAllow)
			}
			if enforcement.AppliesEngineEndpoints != test.wantAppliesEngine {
				t.Fatalf("AppliesEngineEndpoints = %v, want %v", enforcement.AppliesEngineEndpoints, test.wantAppliesEngine)
			}
			if enforcement.AppliesDenyDomains != test.wantAppliesDomains {
				t.Fatalf("AppliesDenyDomains = %v, want %v", enforcement.AppliesDenyDomains, test.wantAppliesDomains)
			}
			if enforcement.RequiresCNI != test.wantCNI {
				t.Fatalf("RequiresCNI = %v, want %v", enforcement.RequiresCNI, test.wantCNI)
			}
			if strings.TrimSpace(enforcement.Notes) == "" {
				t.Fatal("Notes is empty; the strength report must state what is not applied")
			}
		})
	}
}

func denyPolicyForTest(t *testing.T) SandboxNetworkPolicy {
	t.Helper()
	return SandboxNetworkPolicy{
		Default:         egress.Deny,
		Allow:           []egress.Endpoint{mustEndpoint(t, "api.example.com", 443, egress.ProtocolHTTPS)},
		EngineEndpoints: []egress.EngineEndpoint{mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)},
	}
}

// TestRequireSandboxNetworkEnforcementFailsClosed is the failure path: a
// default-deny policy on a driver with no enforcement surface must abort sandbox
// creation instead of starting it with open egress.
func TestRequireSandboxNetworkEnforcementFailsClosed(t *testing.T) {
	deny := denyPolicyForTest(t)
	tests := []struct {
		name    string
		driver  string
		policy  *SandboxNetworkPolicy
		wantErr bool
	}{
		{name: "boxlite denies and fails closed", driver: RuntimeDriverBoxlite, policy: &deny, wantErr: true},
		{name: "unknown driver fails closed", driver: "mystery", policy: &deny, wantErr: true},
		{name: "docker enforces an outer deny", driver: RuntimeDriverDocker, policy: &deny},
		{name: "k8s enforces an outer deny", driver: RuntimeDriverK8s, policy: &deny},
		{name: "microsandbox enforces an allow list", driver: RuntimeDriverMicrosandbox, policy: &deny},
		{name: "undeclared policy is unchanged", driver: RuntimeDriverBoxlite, policy: nil},
		{
			name:   "permissive policy is unchanged on an unenforcing driver",
			driver: RuntimeDriverBoxlite,
			policy: &SandboxNetworkPolicy{Default: egress.Allow},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := RequireSandboxNetworkEnforcement(test.driver, test.policy)
			if test.wantErr {
				if err == nil {
					t.Fatal("RequireSandboxNetworkEnforcement() error = nil, want a fail-closed error")
				}
				if !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
					t.Fatalf("error = %v, want ErrSandboxNetworkEnforcementUnavailable", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("RequireSandboxNetworkEnforcement() error = %v, want nil", err)
			}
		})
	}
}

func TestRequireSandboxNetworkEnforcementRejectsMalformedPolicy(t *testing.T) {
	policy := SandboxNetworkPolicy{
		Default: egress.Deny,
		Allow:   []egress.Endpoint{{Host: "bad host", Port: 443}},
	}
	err := RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, &policy)
	if err == nil {
		t.Fatal("RequireSandboxNetworkEnforcement() error = nil, want a validation error")
	}
	if errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("error = %v, want a validation error rather than the enforcement-unavailable sentinel", err)
	}
}

// TestDecideSandboxNetworkEgressUsesTheEgressModel pins that engine-owned
// endpoints stay reachable under default deny and that an undeclared
// destination is refused, decided by pkg/egress rather than by driver-local
// logic.
func TestDecideSandboxNetworkEgressUsesTheEgressModel(t *testing.T) {
	policy, err := SandboxNetworkPolicyFromDeclaration(
		&egress.NetworkDeclaration{
			Default: egress.Deny,
			Allow: []egress.AllowEntry{
				{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS},
				{Host: "*.trusted.example.com", Port: 443, Protocol: egress.ProtocolHTTPS},
			},
		},
		[]egress.EngineEndpoint{
			mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS),
			mustEngineEndpoint(t, egress.PurposeTelemetry, "telemetry.internal", 4318, egress.ProtocolHTTP),
		},
		nil,
	)
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}

	tests := []struct {
		name  string
		entry egress.Endpoint
		want  egress.Action
	}{
		{name: "engine LLM facade is allowed", entry: mustEndpoint(t, "facade.internal", 7410, egress.ProtocolHTTPS), want: egress.Allow},
		{name: "engine telemetry endpoint is allowed", entry: mustEndpoint(t, "telemetry.internal", 4318, egress.ProtocolHTTP), want: egress.Allow},
		{name: "declared allowance is allowed", entry: mustEndpoint(t, "api.example.com", 443, egress.ProtocolHTTPS), want: egress.Allow},
		{name: "declared wildcard pattern matches one label", entry: mustEndpoint(t, "svc.trusted.example.com", 443, egress.ProtocolHTTPS), want: egress.Allow},
		{name: "wildcard does not match a deeper name", entry: mustEndpoint(t, "a.b.trusted.example.com", 443, egress.ProtocolHTTPS), want: egress.Deny},
		{name: "undeclared host is denied", entry: mustEndpoint(t, "exfil.example.com", 443, egress.ProtocolHTTPS), want: egress.Deny},
		{name: "undeclared port on an allowed host is denied", entry: mustEndpoint(t, "api.example.com", 8080, egress.ProtocolHTTPS), want: egress.Deny},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := DecideSandboxNetworkEgress(policy, test.entry)
			if err != nil {
				t.Fatalf("DecideSandboxNetworkEgress() error = %v", err)
			}
			if result.Action != test.want {
				t.Fatalf("Action = %q, want %q (result %+v)", result.Action, test.want, result)
			}
		})
	}
}

// TestSandboxNetworkEgressPolicyPermissiveIsAllowAll pins D3: a policy whose
// default is allow resolves every destination to allow, so the driver gate
// cannot accidentally introduce default deny for an undeclared workload.
func TestSandboxNetworkEgressPolicyPermissiveIsAllowAll(t *testing.T) {
	policy := SandboxNetworkPolicy{Default: egress.Allow}
	result, err := DecideSandboxNetworkEgress(policy, mustEndpoint(t, "anywhere.example.com", 1234, egress.ProtocolTCP))
	if err != nil {
		t.Fatalf("DecideSandboxNetworkEgress() error = %v", err)
	}
	if result.Action != egress.Allow {
		t.Fatalf("Action = %q, want %q", result.Action, egress.Allow)
	}
}
