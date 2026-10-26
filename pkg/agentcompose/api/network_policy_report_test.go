package api

import (
	"testing"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/egress"
)

// TestNetworkPolicyReportsToProtoCarriesTheDeclaredAccount pins that the
// transport does not lose the parts a consumer decides with: the address ranges
// a declaration constrained, the mechanism, and the warning that says the
// declaration will not be enforced as written.
func TestNetworkPolicyReportsToProtoCarriesTheDeclaredAccount(t *testing.T) {
	reports := []driverpkg.AgentNetworkPolicyReport{{
		Agent:     "worker",
		Driver:    "docker",
		Declared:  true,
		Restricts: true,
		Default:   egress.Deny,
		Allow: []egress.DeclaredAllowance{{
			Endpoint:   "api.github.com:443/*",
			AllowedIPs: []string{"203.0.113.0/24"},
			Sources:    []string{"daemon", "agent"},
		}},
		Deny: []egress.DeclaredDenial{{
			Endpoint: "metadata.example.com:80/*",
			Sources:  []string{"daemon"},
		}},
		Mediated:           true,
		Enforced:           true,
		Enforcement:        driverpkg.SandboxEgressForwardProxyEnforcement("docker"),
		Mechanisms:         []string{"forward_proxy"},
		Limitations:        []string{"traffic that bypasses the proxy is not decided"},
		DaemonPolicySource: "/policy.yaml",
		DaemonPolicySHA256: "abc123",
		Warning:            "",
	}}

	mapped := NetworkPolicyReportsToProto(reports)
	if len(mapped) != 1 {
		t.Fatalf("mapped = %+v, want one report", mapped)
	}
	report := mapped[0]
	if report.GetAgent() != "worker" || report.GetDriver() != "docker" || !report.GetRestricts() || !report.GetMediated() || !report.GetEnforced() {
		t.Fatalf("mapped report = %+v, want the declared, mediated default-deny account", report)
	}
	if report.GetDefault() != "deny-all" || report.GetMechanism() != "forward_proxy" || report.GetStrength() != "allowlist" {
		t.Fatalf("mapped report = %+v, want the deny-all mode and the proxy mechanism", report)
	}
	allow := report.GetAllow()
	if len(allow) != 1 || allow[0].GetEndpoint() != "api.github.com:443/*" || allow[0].GetAllowedIps()[0] != "203.0.113.0/24" {
		t.Fatalf("allow = %+v, want the declared allowance and its range", allow)
	}
	if got := allow[0].GetAllowedIps(); len(got) != 1 || got[0] != "203.0.113.0/24" {
		t.Fatalf("allowed_ips = %v, want the declared range", got)
	}
	if got := allow[0].GetSources(); len(got) != 2 || got[0] != "daemon" || got[1] != "agent" {
		t.Fatalf("allow sources = %v, want daemon and agent", got)
	}
	deny := report.GetDeny()
	if len(deny) != 1 || deny[0].GetEndpoint() != "metadata.example.com:80/*" || len(deny[0].GetSources()) != 1 || deny[0].GetSources()[0] != "daemon" {
		t.Fatalf("deny = %+v, want the declared denial and its daemon source", deny)
	}
	if len(report.GetMechanisms()) != 1 || report.GetMechanisms()[0] != "forward_proxy" {
		t.Fatalf("mechanisms = %v, want the proxy", report.GetMechanisms())
	}
	if len(report.GetLimitations()) != 1 || report.GetDaemonPolicySource() != "/policy.yaml" || report.GetDaemonPolicySha256() != "abc123" {
		t.Fatalf("report audit fields = %+v", report)
	}
}

// TestNetworkPolicyReportsToProtoKeepsTheGapVisible pins that a report with
// nothing to enforce still travels: an empty report list must not be confused
// with "the engine enforces nothing here".
func TestNetworkPolicyReportsToProtoKeepsTheGapVisible(t *testing.T) {
	report := driverpkg.AgentNetworkPolicyReport{
		Agent:     "worker",
		Driver:    "k8s",
		Declared:  true,
		Restricts: true,
		Default:   egress.Deny,
		Unapplied: []string{"the declared allowances"},
		Warning:   "the engine will not enforce this declaration on driver \"k8s\"",
	}
	mapped := NetworkPolicyReportsToProto([]driverpkg.AgentNetworkPolicyReport{report})
	if len(mapped) != 1 || mapped[0].GetEnforced() {
		t.Fatalf("mapped = %+v, want the unenforced account", mapped)
	}
	if len(mapped[0].GetUnapplied()) != 1 || mapped[0].GetWarning() == "" {
		t.Fatalf("mapped report = %+v, want the unapplied parts and the warning", mapped[0])
	}
	if got := NetworkPolicyReportsToProto(nil); got != nil {
		t.Fatalf("NetworkPolicyReportsToProto(nil) = %+v, want nil so an absent field stays absent", got)
	}
}
