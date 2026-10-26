package driver

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	config "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/egress"
)

func TestReportAgentNetworkPolicyUndeclaredIsNotDeny(t *testing.T) {
	report, err := ReportAgentNetworkPolicy("worker", RuntimeDriverDocker, nil, false, config.EgressUnenforceableWarn)
	if err != nil {
		t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
	}
	if report.Declared || report.Restricts || report.Enforced {
		t.Fatalf("report = %+v, want an undeclared policy that restricts nothing", report)
	}
	if report.Warning != "" {
		t.Fatalf("Warning = %q, want empty for an undeclared policy", report.Warning)
	}
	if report.Driver != RuntimeDriverDocker || report.Agent != "worker" {
		t.Fatalf("report identity = %+v, want agent worker on docker", report)
	}
}

func TestReportAgentNetworkPolicyAllowListWithoutDenyDoesNotRestrict(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Allow,
		Allow:   []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
	report, err := ReportAgentNetworkPolicy("worker", RuntimeDriverDocker, declaration, false, config.EgressUnenforceableWarn)
	if err != nil {
		t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
	}
	if !report.Declared || report.Restricts || report.Enforced {
		t.Fatalf("report = %+v, want a declared allow-all policy that restricts nothing", report)
	}
	if report.Warning != "" {
		t.Fatalf("Warning = %q, want empty: the declaration is delivered, it just does not restrict", report.Warning)
	}
	if len(report.Allow) != 1 || report.Allow[0].Endpoint != "api.example.com:443/https" {
		t.Fatalf("Allow = %+v, want the declared https allowance", report.Allow)
	}
}

func TestReportAgentNetworkPolicyDenyListIsReported(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Allow,
		Deny:    []egress.DenyEntry{{Host: "metadata.example.com", Port: 80, Protocol: egress.ProtocolTCP}},
	}
	report, err := ReportAgentNetworkPolicy("worker", RuntimeDriverDocker, declaration, true, config.EgressUnenforceableWarn)
	if err != nil {
		t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
	}
	if !report.Declared || !report.Restricts || !report.Enforced || !report.Mediated {
		t.Fatalf("report = %+v, want a mediated denylist policy that restricts and is enforced", report)
	}
	if report.Enforcement.Mechanism != mechanismForwardProxy || report.Enforcement.Strength != SandboxEgressStrengthDenyList {
		t.Fatalf("Enforcement = %+v, want the forward proxy at denylist strength", report.Enforcement)
	}
	if len(report.Deny) != 1 || report.Deny[0].Endpoint != "metadata.example.com:80/tcp" {
		t.Fatalf("Deny = %+v, want the declared denial reported", report.Deny)
	}
}

func TestReportAgentNetworkPolicyMediatedDenyIsEnforced(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow: []egress.AllowEntry{{
			Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS,
			AllowedIPs: []string{"203.0.113.0/24"},
		}},
	}
	report, err := ReportAgentNetworkPolicy("worker", RuntimeDriverDocker, declaration, true, config.EgressUnenforceableWarn)
	if err != nil {
		t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
	}
	if !report.Restricts || !report.Enforced || !report.Mediated {
		t.Fatalf("report = %+v, want an enforced, mediated default-deny policy", report)
	}
	if report.Enforcement.Mechanism != mechanismForwardProxy || report.Enforcement.Strength != SandboxEgressStrengthAllowList {
		t.Fatalf("Enforcement = %+v, want the forward proxy at allowlist strength", report.Enforcement)
	}
	if len(report.Unapplied) != 0 || report.Warning != "" {
		t.Fatalf("report = %+v, want nothing unapplied: the proxy resolves the destination itself", report)
	}
	if len(report.Allow) != 1 || len(report.Allow[0].AllowedIPs) != 1 || report.Allow[0].AllowedIPs[0] != "203.0.113.0/24" {
		t.Fatalf("Allow = %+v, want the declared address range reported", report.Allow)
	}
}

func TestReportAgentNetworkPolicyNamesWhatTheMechanismLeavesUnapplied(t *testing.T) {
	tests := []struct {
		name         string
		driver       string
		mediated     bool
		mode         config.EgressUnenforceablePolicy
		declaration  *egress.NetworkDeclaration
		wantEnforced bool
		wantWarning  []string
	}{
		{
			name:   "a driver without an egress surface enforces nothing",
			driver: RuntimeDriverDocker,
			mode:   config.EgressUnenforceableWarn,
			wantWarning: []string{
				"will not enforce this declaration on driver \"docker\"",
				"no egress filter is installed by this driver",
				"EGRESS_UNENFORCEABLE_POLICY=warn",
			},
		},
		{
			name:   "refuse mode states the refusal it would produce",
			driver: RuntimeDriverK8s,
			mode:   config.EgressUnenforceableRefuse,
			wantWarning: []string{
				"EGRESS_UNENFORCEABLE_POLICY=refuse",
			},
		},
		{
			name:         "a driver that applies destinations but not addresses says so",
			driver:       RuntimeDriverMicrosandbox,
			mode:         config.EgressUnenforceableWarn,
			wantEnforced: true,
			wantWarning: []string{
				"does not constrain which addresses",
			},
		},
		{
			// The creation path refuses exactly this gap in refuse mode, so the
			// report must not answer Enforced for a sandbox that never starts.
			name:         "refuse mode does not present a refused declaration as enforced",
			driver:       RuntimeDriverMicrosandbox,
			mode:         config.EgressUnenforceableRefuse,
			wantEnforced: false,
			wantWarning: []string{
				"EGRESS_UNENFORCEABLE_POLICY=refuse",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			declaration := &egress.NetworkDeclaration{
				Default: egress.Deny,
				Allow: []egress.AllowEntry{{
					Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS,
					AllowedIPs: []string{"203.0.113.0/24"},
				}},
			}
			report, err := ReportAgentNetworkPolicy("worker", test.driver, declaration, test.mediated, test.mode)
			if err != nil {
				t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
			}
			if report.Enforced != test.wantEnforced {
				t.Fatalf("Enforced = %v, want %v (%s)", report.Enforced, test.wantEnforced, report.Warning)
			}
			if len(report.Unapplied) == 0 {
				t.Fatalf("Unapplied is empty, want the parts the mechanism leaves: %+v", report)
			}
			for _, want := range test.wantWarning {
				if !strings.Contains(report.Warning, want) {
					t.Fatalf("Warning = %q, want it to contain %q", report.Warning, want)
				}
			}
		})
	}
}

// TestReportAgentNetworkPolicyRefusesAnInvalidDeclaration pins that the report
// cannot describe a policy the decision model would reject.
func TestReportAgentNetworkPolicyRefusesAnInvalidDeclaration(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "bad host", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
	if _, err := ReportAgentNetworkPolicy("worker", RuntimeDriverDocker, declaration, true, config.EgressUnenforceableWarn); err == nil {
		t.Fatal("ReportAgentNetworkPolicy() error = nil, want a validation error")
	}
}

// TestAdmissionReportAgreesWithTheCreationGate pins the invariant both entry
// points are supposed to satisfy: in refuse mode, a report that answers Enforced
// and a gate that lets the sandbox start must return the same answer for the
// same declaration. The two share the declaration and the enforcement
// descriptors, so a gap either one learns to see has to reach the other.
func TestAdmissionReportAgreesWithTheCreationGate(t *testing.T) {
	engineEndpoints := []egress.EngineEndpoint{
		mustEngineEndpoint(t, egress.PurposeLLMFacade, "facade.internal", 7410, egress.ProtocolHTTPS),
	}
	declarations := map[string]egress.NetworkDeclaration{
		"a name allowance": {
			Default: egress.Deny,
			Allow:   []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
		},
		"an address-constrained allowance": {
			Default: egress.Deny,
			Allow: []egress.AllowEntry{{
				Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS,
				AllowedIPs: []string{"203.0.113.0/24"},
			}},
		},
	}
	drivers := []string{RuntimeDriverDocker, RuntimeDriverBoxlite, RuntimeDriverMicrosandbox, RuntimeDriverK8s, "mystery"}
	for name, declaration := range declarations {
		for _, driver := range drivers {
			for _, mediated := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/mediated=%t", name, driver, mediated), func(t *testing.T) {
					policy, err := SandboxNetworkPolicyFromDeclaration(&declaration, engineEndpoints, nil)
					if err != nil {
						t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
					}
					policy.MediatedByForwardProxy = mediated
					gateErr := RequireSandboxNetworkEnforcement(driver, "sandbox-1", &policy, config.EgressUnenforceableRefuse)
					report, err := ReportAgentNetworkPolicy("worker", driver, &declaration, mediated, config.EgressUnenforceableRefuse)
					if err != nil {
						t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
					}
					if report.Enforced == (gateErr != nil) {
						t.Fatalf("report.Enforced = %v while the creation gate returned %v; the two entries disagree about whether this declaration is applied",
							report.Enforced, gateErr)
					}
				})
			}
		}
	}
}

// TestAdmissionReportCannotSeeUnappliedDenyDomains characterizes the one input
// the two entries cannot share: the creation gate reads the driver-boundary
// policy's deny domains and refuses a mediated sandbox whose driver does not
// deliver them, while the report reads only the declaration, which has no field
// for them. No project can reach this state — the compose schema declares no
// deny domains and every production path builds the policy with none — so this
// pins today's answer rather than a behavior an operator can hit. Making deny
// domains declarable has to extend ReportAgentNetworkPolicy, and this test fails
// when that happens so the two entries are changed together.
func TestAdmissionReportCannotSeeUnappliedDenyDomains(t *testing.T) {
	declaration := egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS}},
	}
	policy, err := SandboxNetworkPolicyFromDeclaration(&declaration, nil, []string{"metadata.example"})
	if err != nil {
		t.Fatalf("SandboxNetworkPolicyFromDeclaration() error = %v", err)
	}
	policy.MediatedByForwardProxy = true
	if err := RequireSandboxNetworkEnforcement(RuntimeDriverDocker, "sandbox-1", &policy, config.EgressUnenforceableRefuse); !errors.Is(err, ErrSandboxNetworkEnforcementUnavailable) {
		t.Fatalf("creation gate error = %v, want the unapplied deny domains refused", err)
	}

	report, err := ReportAgentNetworkPolicy("worker", RuntimeDriverDocker, &declaration, true, config.EgressUnenforceableRefuse)
	if err != nil {
		t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
	}
	if !report.Enforced || len(report.Unapplied) != 0 || report.Warning != "" {
		t.Fatalf("report = %+v: the report now accounts for deny domains, so this characterization test must be replaced by an assertion that it agrees with the creation gate", report)
	}
}

// TestReportAgentNetworkPolicyNamesEveryMechanism pins that a mediated
// Microsandbox sandbox does not hide the driver's own filter behind the proxy
// label: both mechanisms apply part of the declaration, so both are named.
func TestReportAgentNetworkPolicyNamesEveryMechanism(t *testing.T) {
	declaration := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow: []egress.AllowEntry{{
			Host: "api.example.com", Port: 443, Protocol: egress.ProtocolHTTPS,
			AllowedIPs: []string{"203.0.113.0/24"},
		}},
	}
	report, err := ReportAgentNetworkPolicy("worker", RuntimeDriverMicrosandbox, declaration, true, config.EgressUnenforceableWarn)
	if err != nil {
		t.Fatalf("ReportAgentNetworkPolicy() error = %v", err)
	}
	if got, want := report.Mechanisms, []string{mechanismSDKNetworkPolicy, mechanismForwardProxy}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Mechanisms = %v, want %v", got, want)
	}
	if len(report.Limitations) < 2 {
		t.Fatalf("Limitations = %v, want the proxy-bypass and allowed_ips limits", report.Limitations)
	}
	if !strings.Contains(strings.Join(report.Limitations, " "), "allowed_ips") {
		t.Fatalf("Limitations = %v, want the driver's allowed_ips gap", report.Limitations)
	}
	if len(report.Unapplied) == 0 || !strings.Contains(strings.Join(report.Unapplied, " "), "bypasses the forward proxy") {
		t.Fatalf("Unapplied = %v, want the bypass-only address gap", report.Unapplied)
	}
}
