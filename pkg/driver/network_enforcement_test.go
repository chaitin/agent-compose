package driver

import (
	"errors"
	"strings"
	"testing"

	config "github.com/chaitin/agent-compose/pkg/config"
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
	}{
		{
			driver: RuntimeDriverMicrosandbox, wantStrength: SandboxEgressStrengthAllowList,
			wantMechanism: mechanismSDKNetworkPolicy, wantAppliesAllow: true, wantAppliesEngine: true, wantAppliesDomains: true,
		},
		{
			// k8s refuses a declared default-deny policy, because a
			// NetworkPolicy deny would also cut the engine's own endpoints and
			// the CNI cannot be assumed to enforce it.
			driver: RuntimeDriverK8s, wantStrength: SandboxEgressStrengthNone,
			wantMechanism: reasonNotConfigured,
		},
		{
			// Docker refuses a declared default-deny policy, because
			// NetworkMode=none would also cut the engine's own endpoints.
			driver: RuntimeDriverDocker, wantStrength: SandboxEgressStrengthNone,
			wantMechanism: reasonNotConfigured,
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
		Allow:           []egress.AllowEntry{mustAllow(t, "api.example.com", 443, egress.ProtocolHTTPS)},
		EngineEndpoints: []egress.EngineEndpoint{mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS)},
	}
}

// TestRequireSandboxNetworkEnforcementRefuseModeAborts is the opt-in failure
// path: a deployment that requires the declaration to be real aborts sandbox
// creation instead of starting it with open egress.
func TestRequireSandboxNetworkEnforcementRefuseModeAborts(t *testing.T) {
	deny := denyPolicyForTest(t)
	tests := []struct {
		name    string
		driver  string
		policy  *SandboxNetworkPolicy
		wantErr bool
	}{
		{name: "boxlite denies and is refused", driver: RuntimeDriverBoxlite, policy: &deny, wantErr: true},
		{name: "unknown driver is refused", driver: "mystery", policy: &deny, wantErr: true},
		{name: "docker is refused because it cannot keep the engine endpoints reachable", driver: RuntimeDriverDocker, policy: &deny, wantErr: true},
		{name: "k8s is refused because it cannot keep the engine endpoints reachable", driver: RuntimeDriverK8s, policy: &deny, wantErr: true},
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
			err := RequireSandboxNetworkEnforcement(test.driver, "sandbox-1", test.policy, config.EgressUnenforceableRefuse)
			if test.wantErr {
				if err == nil {
					t.Fatal("RequireSandboxNetworkEnforcement() error = nil, want a refusal error")
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
		Allow:   []egress.AllowEntry{{Host: "bad host", Port: 443}},
	}
	err := RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, "sandbox-1", &policy, config.EgressUnenforceableRefuse)
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

// TestRequireSandboxNetworkEnforcementWarnsByDefault is the default path: a
// restriction the driver cannot deliver must not make the agent unrunnable, so
// the sandbox starts and the unenforced declaration is reported instead.
func TestRequireSandboxNetworkEnforcementWarnsByDefault(t *testing.T) {
	deny := denyPolicyForTest(t)
	for _, driver := range []string{RuntimeDriverDocker, RuntimeDriverK8s, RuntimeDriverBoxlite, "mystery"} {
		t.Run(driver, func(t *testing.T) {
			if err := RequireSandboxNetworkEnforcement(driver, "sandbox-1", &deny, config.EgressUnenforceableWarn); err != nil {
				t.Fatalf("RequireSandboxNetworkEnforcement(warn) error = %v, want nil: an unenforceable declaration must not make the agent unrunnable", err)
			}
		})
	}
	// An unset mode is what a deployment that never heard of the setting gets,
	// and it must behave like warn rather than refusing.
	t.Run("empty mode behaves as warn", func(t *testing.T) {
		if err := RequireSandboxNetworkEnforcement(RuntimeDriverDocker, "sandbox-1", &deny, ""); err != nil {
			t.Fatalf("RequireSandboxNetworkEnforcement(empty mode) error = %v, want nil", err)
		}
	})
	t.Run("warn mode still rejects a malformed policy", func(t *testing.T) {
		malformed := SandboxNetworkPolicy{Default: egress.Deny, Allow: []egress.AllowEntry{{Host: "bad host", Port: 443}}}
		err := RequireSandboxNetworkEnforcement(RuntimeDriverDocker, "sandbox-1", &malformed, config.EgressUnenforceableWarn)
		if err == nil {
			t.Fatal("RequireSandboxNetworkEnforcement(warn) error = nil, want a validation error")
		}
		if errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
			t.Fatalf("error = %v, want a validation error, not the enforcement-unavailable sentinel", err)
		}
	})
}

// TestRequireSandboxNetworkEnforcementReportsAnUnappliedAddressConstraint pins
// the seam rule: a mechanism that applies a declaration's destinations but not
// the addresses they may resolve to is applying less than the project
// declared, and a sandbox must not run as if the constraint were in place.
func TestRequireSandboxNetworkEnforcementReportsAnUnappliedAddressConstraint(t *testing.T) {
	constrained := SandboxNetworkPolicy{
		Default: egress.Deny,
		Allow: []egress.AllowEntry{{
			Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS,
			AllowedIPs: []string{"203.0.113.0/24"},
		}},
	}

	// The SDK allowlist matches the name the sandbox asked for; it cannot
	// constrain the address behind it.
	if enforcement := SandboxNetworkEnforcementFor(RuntimeDriverMicrosandbox); enforcement.AppliesAllowedAddresses {
		t.Fatalf("Microsandbox claims to apply allowed addresses: %+v", enforcement)
	}
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, "sandbox-1", &constrained, config.EgressUnenforceableWarn); err != nil {
		t.Fatalf("warn mode refused instead of reporting: %v", err)
	}
	err := RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, "sandbox-1", &constrained, config.EgressUnenforceableRefuse)
	if !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("refuse mode error = %v, want the enforcement-unavailable sentinel", err)
	}
	if err == nil || !strings.Contains(err.Error(), "constrain the addresses") {
		t.Fatalf("refusal = %v, want it to name the unapplied address constraint", err)
	}
	if strings.Contains(err.Error(), "unrestricted egress") {
		t.Fatalf("refusal = %v, want it to state the partial gap rather than claiming unrestricted egress", err)
	}

	// The engine's proxy resolves the destination itself, so it does apply them.
	mediated := constrained
	mediated.MediatedByForwardProxy = true
	if enforcement := SandboxEgressForwardProxyEnforcement(RuntimeDriverDocker); !enforcement.AppliesAllowedAddresses {
		t.Fatalf("the forward proxy does not claim to apply allowed addresses: %+v", enforcement)
	}
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverDocker, "sandbox-1", &mediated, config.EgressUnenforceableRefuse); err != nil {
		t.Fatalf("a mediated sandbox with an address-constrained allowance was refused: %v", err)
	}

	// Microsandbox also receives the proxy, but its SDK filter applies the
	// destination without allowed_ips. Traffic that bypasses the proxy therefore
	// sees a weaker rule than the declaration, which refuse mode must not accept.
	mediatedDriver := constrained
	mediatedDriver.MediatedByForwardProxy = true
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, "sandbox-1", &mediatedDriver, config.EgressUnenforceableWarn); err != nil {
		t.Fatalf("warn mode refused instead of reporting: %v", err)
	}
	err = RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, "sandbox-1", &mediatedDriver, config.EgressUnenforceableRefuse)
	if !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("refuse mode error = %v, want the enforcement-unavailable sentinel", err)
	}
	if !strings.Contains(err.Error(), "bypasses the proxy") || !strings.Contains(err.Error(), "allowed_ips") {
		t.Fatalf("refusal = %v, want the bypass-only address gap named", err)
	}

	// An allowance with no address constraint is unaffected.
	plain := SandboxNetworkPolicy{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, "sandbox-1", &plain, config.EgressUnenforceableRefuse); err != nil {
		t.Fatalf("an unconstrained allowance was refused: %v", err)
	}
}

// TestRequireSandboxNetworkEnforcementReportsAnUnappliedDenyDomain covers the
// deny-domain half of the same gap: a declared deny domain is a restriction the
// declaration asked for, so a mechanism that cannot deliver it must not present
// the sandbox as constrained.
func TestRequireSandboxNetworkEnforcementReportsAnUnappliedDenyDomain(t *testing.T) {
	declared := SandboxNetworkPolicy{
		Default:     egress.Deny,
		Allow:       []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
		DenyDomains: []string{"metadata.example"},
	}
	mediated := declared
	mediated.MediatedByForwardProxy = true

	// Docker's own mechanism applies nothing and the forward proxy does not
	// consult deny domains, so a mediated docker sandbox leaves them unapplied.
	err := RequireSandboxNetworkEnforcement(RuntimeDriverDocker, "sandbox-1", &mediated, config.EgressUnenforceableRefuse)
	if !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("refuse mode error = %v, want the enforcement-unavailable sentinel", err)
	}
	if !strings.Contains(err.Error(), "deny domain") {
		t.Fatalf("refusal = %v, want it to name the unapplied deny domains", err)
	}
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverDocker, "sandbox-1", &mediated, config.EgressUnenforceableWarn); err != nil {
		t.Fatalf("warn mode refused instead of reporting: %v", err)
	}

	// Microsandbox applies deny domains through its own in-guest filter, so the
	// same mediated declaration is delivered even though the proxy ignores them.
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverMicrosandbox, "sandbox-1", &mediated, config.EgressUnenforceableRefuse); err != nil {
		t.Fatalf("a driver that applies deny domains was refused: %v", err)
	}
}
