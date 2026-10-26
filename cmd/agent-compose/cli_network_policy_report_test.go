package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestWriteComposeNetworkPolicyReportSeparatesDeclaredFromEnforced pins the
// admission report a user reads: what the project declared, and — adjacently —
// the mechanism that will apply it, so the difference is impossible to miss.
func TestWriteComposeNetworkPolicyReportSeparatesDeclaredFromEnforced(t *testing.T) {
	policies := composeNetworkPoliciesFromProto([]*agentcomposev2.AgentNetworkPolicyReport{
		{
			Agent: "worker", Driver: "docker", Declared: true, Restricts: true, Default: "deny-all",
			Allow: []*agentcomposev2.DeclaredNetworkAllowance{{
				Endpoint: "api.github.com:443/https", AllowedIps: []string{"203.0.113.0/24"},
			}},
			Mediated: true, Enforced: true, Strength: "allowlist", Mechanism: "forward_proxy",
		},
		{
			Agent: "builder", Driver: "k8s", Declared: true, Restricts: true, Default: "deny-all",
			Allow:    []*agentcomposev2.DeclaredNetworkAllowance{{Endpoint: "api.github.com:443/https"}},
			Enforced: false, Strength: "none", Mechanism: "not_configured",
			Unapplied: []string{"the declared allowances"},
			Warning:   "the engine will not enforce this declaration on driver \"k8s\": no egress filter is installed by this driver",
		},
		{
			Agent: "helper", Driver: "docker", Declared: true, Default: "allow-all",
			Allow: []*agentcomposev2.DeclaredNetworkAllowance{{
				Endpoint: "telemetry.example.com:4317/tcp",
			}},
		},
		{
			Agent: "guard", Driver: "docker", Declared: true, Restricts: true, Default: "allow-all",
			Deny: []*agentcomposev2.DeclaredNetworkDenial{{
				Endpoint: "10.2.16.21:443/https",
			}},
			Mediated: true, Enforced: true, Strength: "denylist", Mechanism: "forward_proxy",
		},
	})

	var out bytes.Buffer
	if err := writeComposeNetworkPolicyReport(&out, policies); err != nil {
		t.Fatalf("writeComposeNetworkPolicyReport() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"Network policy",
		"worker (driver docker)",
		"declared  default deny-all, 1 allowance(s)",
		"api.github.com:443/https (addresses 203.0.113.0/24)",
		"enforced  forward_proxy (allowlist), the engine's forward proxy",
		"builder (driver k8s)",
		"enforced  no: the engine will not enforce this declaration",
		"partial   the declared allowances",
		"helper (driver docker)",
		"egress is not restricted, because an allow list needs default: deny-all and no denial matches",
		"telemetry.example.com:4317/tcp",
		"guard (driver docker)",
		"declared  default allow-all, 0 allowance(s), 1 denial(s)",
		"deny  10.2.16.21:443/https",
		"enforced  forward_proxy (denylist), the engine's forward proxy",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text =\n%s\nwant it to contain %q", text, want)
		}
	}
}

// TestWriteComposeDeclaredNetworkPolicyNotesIgnoresAgentsWithoutAPolicy pins the
// common case: an agent that declared no policy block must not be reported, and
// must not make the command fail.
func TestWriteComposeDeclaredNetworkPolicyNotesIgnoresAgentsWithoutAPolicy(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{
		{Name: "plain"},
		{Name: "declared", Policy: &compose.NormalizedSandboxSpec{Network: &compose.NormalizedSandboxNetworkSpec{
			Default: "deny-all",
			Allow: []compose.NormalizedSandboxNetworkAllowSpec{{
				Host: "api.github.com", Port: 443, AllowedIPs: []string{"203.0.113.0/24"},
			}},
		}}},
	}}
	var out bytes.Buffer
	if err := writeComposeDeclaredNetworkPolicyNotes(&out, spec); err != nil {
		t.Fatalf("writeComposeDeclaredNetworkPolicyNotes() error = %v", err)
	}
	text := out.String()
	if strings.Contains(text, "plain") {
		t.Fatalf("notes = %q, want the undeclared agent omitted", text)
	}
	for _, want := range []string{"declared", "default deny-all, 1 allowance(s)", "addresses 203.0.113.0/24"} {
		if !strings.Contains(text, want) {
			t.Fatalf("notes = %q, want it to contain %q", text, want)
		}
	}
	// A project that declares nothing is silent, and a nil spec is safe.
	out.Reset()
	if err := writeComposeDeclaredNetworkPolicyNotes(&out, &compose.NormalizedProjectSpec{Agents: []compose.NormalizedAgentSpec{{Name: "plain"}}}); err != nil {
		t.Fatalf("writeComposeDeclaredNetworkPolicyNotes() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("notes = %q, want no output for a project that declares nothing", out.String())
	}
	if err := writeComposeDeclaredNetworkPolicyNotes(&out, nil); err != nil {
		t.Fatalf("writeComposeDeclaredNetworkPolicyNotes(nil) error = %v", err)
	}
}

// TestComposeNetworkPolicyWarningsNameTheAgent pins that a declaration the engine
// will not enforce is visible on stderr, where a redirected report cannot hide it.
func TestComposeNetworkPolicyWarningsNameTheAgent(t *testing.T) {
	policies := composeNetworkPoliciesFromProto([]*agentcomposev2.AgentNetworkPolicyReport{
		{Agent: "worker", Driver: "docker", Declared: true, Restricts: true, Enforced: true},
		{Agent: "builder", Driver: "k8s", Declared: true, Restricts: true, Warning: "not enforced"},
		{Agent: "helper", Driver: "k8s", Declared: true, Restricts: true, Warning: "not enforced"},
	})
	warnings := composeNetworkPolicyWarnings(policies)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one warning for the two agents sharing a gap", warnings)
	}
	if !strings.Contains(warnings[0], "agents builder, helper") || !strings.Contains(warnings[0], "not enforced") {
		t.Fatalf("warning = %q, want it to name both agents and the gap", warnings[0])
	}
	if strings.Contains(warnings[0], "worker") {
		t.Fatalf("warning = %q, want the enforced agent excluded", warnings[0])
	}
}

// TestWriteComposeNetworkPolicyReportIsSilentWithoutDeclarations pins that a
// project that declares nothing prints nothing.
func TestWriteComposeNetworkPolicyReportIsSilentWithoutDeclarations(t *testing.T) {
	var out bytes.Buffer
	if err := writeComposeNetworkPolicyReport(&out, nil); err != nil {
		t.Fatalf("writeComposeNetworkPolicyReport() error = %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("report = %q, want no output for an undeclared project", out.String())
	}
}

// TestWriteComposeNetworkPolicyReportShowsMechanismsSourcesAndLimits pins the
// audit details an operator needs when more than one mechanism is active or a
// rule came from the daemon: the report names both mechanisms, attributes each
// entry, identifies the daemon policy bytes, and states material limits.
func TestWriteComposeNetworkPolicyReportShowsMechanismsSourcesAndLimits(t *testing.T) {
	policies := composeNetworkPoliciesFromProto([]*agentcomposev2.AgentNetworkPolicyReport{{
		Agent: "worker", Driver: "microsandbox", Declared: true, Restricts: true, Default: "deny-all",
		Allow: []*agentcomposev2.DeclaredNetworkAllowance{{
			Endpoint: "api.example.com:443/*", AllowedIps: []string{"203.0.113.0/24"},
			Sources: []string{"daemon", "agent"},
		}},
		Deny:     []*agentcomposev2.DeclaredNetworkDenial{{Endpoint: "metadata.example.com:80/*", Sources: []string{"daemon"}}},
		Mediated: true, Enforced: true, Strength: "allowlist", Mechanism: "forward_proxy",
		Mechanisms:         []string{"sdk_network_policy", "forward_proxy"},
		Limitations:        []string{"a process that bypasses the proxy is not decided by the proxy", "the driver does not apply allowed_ips to bypass traffic"},
		DaemonPolicySource: "/etc/agent-compose/network-policy.yaml",
		DaemonPolicySha256: "abc123",
	}})

	var out bytes.Buffer
	if err := writeComposeNetworkPolicyReport(&out, policies); err != nil {
		t.Fatalf("writeComposeNetworkPolicyReport() error = %v", err)
	}
	text := out.String()
	for _, want := range []string{
		"daemon policy  /etc/agent-compose/network-policy.yaml (sha256:abc123)",
		"allow api.example.com:443/* (addresses 203.0.113.0/24) [daemon+agent]",
		"deny  metadata.example.com:80/* [daemon]",
		"mechanisms: sdk_network_policy, forward_proxy",
		"limit     a process that bypasses the proxy",
		"limit     the driver does not apply allowed_ips",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("report text =\n%s\nwant it to contain %q", text, want)
		}
	}
}

// TestComposeNetworkPolicyJSONCarriesReportAuditFields pins that the machine
// output exposes the same audit facts as the text report.
func TestComposeNetworkPolicyJSONCarriesReportAuditFields(t *testing.T) {
	policies := composeNetworkPoliciesFromProto([]*agentcomposev2.AgentNetworkPolicyReport{{
		Agent: "worker", Driver: "microsandbox", Declared: true, Restricts: true, Default: "deny-all",
		Allow:              []*agentcomposev2.DeclaredNetworkAllowance{{Endpoint: "api.example.com:443/*", Sources: []string{"agent"}}},
		Mediated:           true,
		Enforced:           true,
		Strength:           "allowlist",
		Mechanism:          "forward_proxy",
		Mechanisms:         []string{"sdk_network_policy", "forward_proxy"},
		Limitations:        []string{"bypass limit"},
		DaemonPolicySource: "/policy.yaml",
		DaemonPolicySha256: "abc123",
	}})
	data, err := json.Marshal(policies)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	for _, want := range []string{`"mechanisms":["sdk_network_policy","forward_proxy"]`, `"limitations":["bypass limit"]`, `"daemon_policy_source":"/policy.yaml"`, `"sources":["agent"]`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("JSON = %s, want %q", data, want)
		}
	}
}
