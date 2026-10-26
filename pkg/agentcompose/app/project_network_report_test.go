package app

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/egress"
)

type staticMediation struct {
	drivers map[string]bool
}

func (m staticMediation) MediatesDriver(driver string) bool {
	return m.drivers[driver]
}

func normalizedAgentWithNetwork(name, driver string, network *compose.NormalizedSandboxNetworkSpec) compose.NormalizedAgentSpec {
	agent := compose.NormalizedAgentSpec{Name: name}
	if driver != "" {
		agent.Driver = &compose.NormalizedDriverSpec{Name: driver}
	}
	if network != nil {
		agent.Policy = &compose.NormalizedSandboxSpec{Network: network}
	}
	return agent
}

// TestAgentNetworkPolicyReportsNameTheMechanismAndTheGap pins the admission
// account: a declaration is reported with its addresses, the mechanism that will
// apply it is named, and a declaration the mechanism cannot deliver says so
// rather than being presented as a control.
func TestAgentNetworkPolicyReportsNameTheMechanismAndTheGap(t *testing.T) {
	deny := &compose.NormalizedSandboxNetworkSpec{
		Default: compose.SandboxNetworkDefaultDenyAll,
		Allow: []compose.NormalizedSandboxNetworkAllowSpec{{
			Host: "api.github.com", Port: 443,
			AllowedIPs: []string{"203.0.113.0/24"},
		}},
	}
	allowAll := &compose.NormalizedSandboxNetworkSpec{Default: compose.SandboxNetworkDefaultAllowAll}
	spec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		normalizedAgentWithNetwork("worker", "", deny),
		normalizedAgentWithNetwork("builder", "microsandbox", deny),
		normalizedAgentWithNetwork("helper", "docker", allowAll),
		normalizedAgentWithNetwork("plain", "docker", nil),
	}}

	reports, err := agentNetworkPolicyReports(spec, staticMediation{drivers: map[string]bool{"docker": true}}, &appconfig.Config{})
	if err != nil {
		t.Fatalf("agentNetworkPolicyReports() error = %v", err)
	}
	if len(reports) != 3 {
		t.Fatalf("reports = %+v, want one per agent that declared a policy", reports)
	}
	// Declared allowances are reported in a stable, agent-sorted order.
	if got := []string{reports[0].Agent, reports[1].Agent, reports[2].Agent}; !equalStrings(got, []string{"builder", "helper", "worker"}) {
		t.Fatalf("reported agents = %v, want builder, helper, worker", got)
	}

	worker := reports[2]
	if worker.Driver != "docker" || !worker.Restricts || !worker.Enforced || !worker.Mediated {
		t.Fatalf("worker report = %+v, want an enforced mediated default-deny policy on the default driver", worker)
	}
	if worker.Enforcement.Mechanism != "forward_proxy" {
		t.Fatalf("worker mechanism = %q, want forward_proxy", worker.Enforcement.Mechanism)
	}
	if len(worker.Allow) != 1 || !equalStrings(worker.Allow[0].AllowedIPs, []string{"203.0.113.0/24"}) {
		t.Fatalf("worker allow = %+v, want the declared address range", worker.Allow)
	}
	if len(worker.Mechanisms) != 1 || worker.Mechanisms[0] != "forward_proxy" {
		t.Fatalf("worker mechanisms = %v, want the forward proxy", worker.Mechanisms)
	}
	if len(worker.Limitations) == 0 || !strings.Contains(strings.Join(worker.Limitations, " "), "bypasses") {
		t.Fatalf("worker limitations = %v, want the proxy-bypass limit", worker.Limitations)
	}

	// The engine cannot reach this agent's sandbox through the proxy, and the
	// driver applies destinations but not addresses: the report names the part
	// it leaves, and the creation gate reports the same gap.
	builder := reports[0]
	if !builder.Enforced || builder.Mediated {
		t.Fatalf("builder report = %+v, want the driver's own mechanism", builder)
	}
	if len(builder.Mechanisms) != 1 || builder.Mechanisms[0] != "sdk_network_policy" {
		t.Fatalf("builder mechanisms = %v, want the driver's own mechanism", builder.Mechanisms)
	}
	if len(builder.Unapplied) != 1 || !strings.Contains(builder.Unapplied[0], "addresses") {
		t.Fatalf("builder unapplied = %v, want the address constraint", builder.Unapplied)
	}
	if !strings.Contains(builder.Warning, "does not constrain which addresses") {
		t.Fatalf("builder warning = %q, want the address limit", builder.Warning)
	}

	helper := reports[1]
	if helper.Restricts || helper.Enforced {
		t.Fatalf("helper report = %+v, want a declared allow-all policy that restricts nothing", helper)
	}
}

// TestAgentNetworkPolicyReportsStateTheOutcomeOfAnUnenforceableDeclaration pins
// that admission answers "and then what": the report names the deployment's own
// EGRESS_UNENFORCEABLE_POLICY rather than leaving it to be looked up.
func TestAgentNetworkPolicyReportsStateTheOutcomeOfAnUnenforceableDeclaration(t *testing.T) {
	deny := &compose.NormalizedSandboxNetworkSpec{Default: compose.SandboxNetworkDefaultDenyAll}
	spec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		normalizedAgentWithNetwork("worker", "k8s", deny),
	}}

	for _, test := range []struct {
		mode appconfig.EgressUnenforceablePolicy
		want string
	}{
		{mode: appconfig.EgressUnenforceableWarn, want: "EGRESS_UNENFORCEABLE_POLICY=warn"},
		{mode: appconfig.EgressUnenforceableRefuse, want: "EGRESS_UNENFORCEABLE_POLICY=refuse"},
	} {
		reports, err := agentNetworkPolicyReports(spec, nil, &appconfig.Config{EgressUnenforceablePolicy: test.mode})
		if err != nil {
			t.Fatalf("agentNetworkPolicyReports(%q) error = %v", test.mode, err)
		}
		if len(reports) != 1 || reports[0].Enforced {
			t.Fatalf("reports = %+v, want one unenforced declaration", reports)
		}
		if !strings.Contains(reports[0].Warning, test.want) {
			t.Fatalf("warning = %q, want it to name %q", reports[0].Warning, test.want)
		}
	}
}

// TestAgentNetworkPolicyReportsRejectADeclarationTheEngineCannotRead pins that
// admission fails instead of describing a project the engine does not have.
func TestAgentNetworkPolicyReportsRejectADeclarationTheEngineCannotRead(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		normalizedAgentWithNetwork("worker", "docker", &compose.NormalizedSandboxNetworkSpec{Default: "sometimes"}),
	}}
	if _, err := agentNetworkPolicyReports(spec, nil, nil); err == nil {
		t.Fatal("agentNetworkPolicyReports() error = nil, want a rejection of the unknown default")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}

// TestAgentNetworkPolicyReportsIncludeTheDaemonGlobalPolicy pins that admission
// accounts for the daemon-wide bound even when an agent declared no policy of
// its own; otherwise the report would describe a project wider than the one the
// daemon will actually run.
func TestAgentNetworkPolicyReportsIncludeTheDaemonGlobalPolicy(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		normalizedAgentWithNetwork("plain", "docker", nil),
	}}
	global := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow:   []egress.AllowEntry{{Host: "api.github.com", Port: 443, Protocol: egress.ProtocolAny}},
	}
	reports, err := agentNetworkPolicyReports(spec, staticMediation{drivers: map[string]bool{"docker": true}}, &appconfig.Config{GlobalNetworkPolicy: global})
	if err != nil {
		t.Fatalf("agentNetworkPolicyReports() error = %v", err)
	}
	if len(reports) != 1 || reports[0].Agent != "plain" || !reports[0].Declared || !reports[0].Restricts {
		t.Fatalf("reports = %+v, want the daemon policy reported for the undeclared agent", reports)
	}
	if reports[0].Default != egress.Deny || len(reports[0].Allow) != 1 {
		t.Fatalf("report = %+v, want the daemon allowlist", reports[0])
	}
	if !equalStrings(reports[0].Allow[0].Sources, []string{"daemon"}) {
		t.Fatalf("allow sources = %v, want daemon", reports[0].Allow[0].Sources)
	}
}

// TestAgentNetworkPolicyReportsCarryPolicyProvenance pins that a merged entry
// names the layer that contributed it. Without this, an operator cannot tell a
// daemon boundary from an agent rule once the effective declaration has been
// compiled.
func TestAgentNetworkPolicyReportsCarryPolicyProvenance(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		normalizedAgentWithNetwork("worker", "docker", &compose.NormalizedSandboxNetworkSpec{
			Default: compose.SandboxNetworkDefaultDenyAll,
			Allow: []compose.NormalizedSandboxNetworkAllowSpec{{
				Host: "10.0.0.0/8", Port: 443,
			}},
			Deny: []compose.NormalizedSandboxNetworkDenySpec{{
				Host: "metadata.example.com", Port: 80,
			}},
		}),
	}}
	global := &egress.NetworkDeclaration{
		Default: egress.Deny,
		Allow: []egress.AllowEntry{{
			Host: "10.0.0.0/8", Port: 443, Protocol: egress.ProtocolAny,
		}},
		Deny: []egress.DenyEntry{{
			Host: "metadata.example.com", Port: 80, Protocol: egress.ProtocolAny,
		}},
	}
	config := &appconfig.Config{
		GlobalNetworkPolicy: global,
		GlobalNetworkPolicyInfo: appconfig.GlobalNetworkPolicyInfo{
			Path:   "/etc/agent-compose/network-policy.yaml",
			SHA256: "0123456789abcdef",
		},
	}
	reports, err := agentNetworkPolicyReports(spec, staticMediation{drivers: map[string]bool{"docker": true}}, config)
	if err != nil {
		t.Fatalf("agentNetworkPolicyReports() error = %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v, want one", reports)
	}
	report := reports[0]
	if !equalStrings(report.Allow[0].Sources, []string{"daemon", "agent"}) {
		t.Fatalf("allow sources = %v, want daemon and agent", report.Allow[0].Sources)
	}
	if !equalStrings(report.Deny[0].Sources, []string{"daemon", "agent"}) {
		t.Fatalf("deny sources = %v, want daemon and agent", report.Deny[0].Sources)
	}
	if report.DaemonPolicySource != config.GlobalNetworkPolicyInfo.Path || report.DaemonPolicySHA256 != config.GlobalNetworkPolicyInfo.SHA256 {
		t.Fatalf("daemon policy identity = %q %q, want %q %q", report.DaemonPolicySource, report.DaemonPolicySHA256, config.GlobalNetworkPolicyInfo.Path, config.GlobalNetworkPolicyInfo.SHA256)
	}
}

// TestNetworkPolicyConnectErrorClassifiesTheDeclaration pins that a report the
// daemon cannot derive is answered as a declaration problem. The project has
// already been applied when the report is built, so CodeInternal would both
// misclassify the cause and suggest a retry.
func TestNetworkPolicyConnectErrorClassifiesTheDeclaration(t *testing.T) {
	err := networkPolicyConnectError(errors.New(`agent "worker": allowance api.example.com:443 is outside the daemon-wide policy`))
	if code := connect.CodeOf(err); code != connect.CodeInvalidArgument {
		t.Fatalf("connect code = %v, want %v", code, connect.CodeInvalidArgument)
	}
	if !strings.Contains(err.Error(), "outside the daemon-wide policy") {
		t.Fatalf("error = %v, want the declaration failure preserved", err)
	}
}
