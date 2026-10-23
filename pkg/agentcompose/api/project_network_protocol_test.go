package api

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// legacyEntryProtocol encodes the removed field the way a client built before its
// removal sends it: field 3 of the allow or deny entry, as a length-delimited
// string. The allow entry keeps a deprecated field for wire compatibility, so
// decoding preserves the value there; the deny entry reserves the number, so its
// value stays in unknown fields.
func legacyEntryProtocolBytes(protocol string) []byte {
	raw := protowire.AppendTag(nil, legacyNetworkProtocolField, protowire.BytesType)
	return protowire.AppendString(raw, protocol)
}

func legacyAllowEntry(t *testing.T, protocol string) *agentcomposev2.SandboxNetworkAllowSpec {
	t.Helper()
	entry := &agentcomposev2.SandboxNetworkAllowSpec{}
	if err := proto.Unmarshal(legacyEntryProtocolBytes(protocol), entry); err != nil {
		t.Fatalf("unmarshal legacy allow entry: %v", err)
	}
	return entry
}

func legacyDenyEntry(t *testing.T, protocol string) *agentcomposev2.SandboxNetworkDenySpec {
	t.Helper()
	entry := &agentcomposev2.SandboxNetworkDenySpec{}
	if err := proto.Unmarshal(legacyEntryProtocolBytes(protocol), entry); err != nil {
		t.Fatalf("unmarshal legacy deny entry: %v", err)
	}
	return entry
}

func legacyAgent(network *agentcomposev2.SandboxNetworkSpec) *agentcomposev2.AgentSpec {
	return &agentcomposev2.AgentSpec{
		Name:    "worker",
		Sandbox: &agentcomposev2.SandboxSpec{Network: network},
	}
}

func TestLegacyNetworkProtocolIssuesRejectsNamedProtocol(t *testing.T) {
	agents := []*agentcomposev2.AgentSpec{legacyAgent(&agentcomposev2.SandboxNetworkSpec{
		Default: "deny-all",
		Allow:   []*agentcomposev2.SandboxNetworkAllowSpec{legacyAllowEntry(t, "https")},
	})}
	issues := LegacyNetworkProtocolIssues(agents)
	if len(issues) != 1 {
		t.Fatalf("issues = %+v, want one issue for the legacy allow entry", issues)
	}
	if got := issues[0].GetPath(); got != "agents[0].policy.network.allow[0].protocol" {
		t.Fatalf("issue path = %q, want the policy.network path", got)
	}
	if message := issues[0].GetMessage(); !strings.Contains(message, `"https"`) || !strings.Contains(message, "removed network entry protocol") {
		t.Fatalf("issue message = %q, want the protocol and the migration hint", message)
	}
}

func TestLegacyNetworkProtocolIssuesRejectsDeniedProtocol(t *testing.T) {
	agents := []*agentcomposev2.AgentSpec{legacyAgent(&agentcomposev2.SandboxNetworkSpec{
		Default: "allow-all",
		Deny:    []*agentcomposev2.SandboxNetworkDenySpec{legacyDenyEntry(t, "tcp")},
	})}
	issues := LegacyNetworkProtocolIssues(agents)
	if len(issues) != 1 || issues[0].GetPath() != "agents[0].policy.network.deny[0].protocol" {
		t.Fatalf("issues = %+v, want one issue for the legacy deny entry", issues)
	}
}

// TestLegacyNetworkProtocolIssuesAcceptsDefault pins the value every older client
// sends whether or not it chose a protocol: rejecting it would make the upgrade
// break clients whose declarations never changed.
func TestLegacyNetworkProtocolIssuesAcceptsDefault(t *testing.T) {
	agents := []*agentcomposev2.AgentSpec{legacyAgent(&agentcomposev2.SandboxNetworkSpec{
		Default: "deny-all",
		Allow: []*agentcomposev2.SandboxNetworkAllowSpec{
			legacyAllowEntry(t, "any"),
			{Host: "api.example.com", Port: 443},
		},
	})}
	if issues := LegacyNetworkProtocolIssues(agents); len(issues) != 0 {
		t.Fatalf("issues = %+v, want the default and a current entry accepted", issues)
	}
}

func TestLegacyNetworkProtocolIssuesIgnoresUndeclaredNetwork(t *testing.T) {
	if issues := LegacyNetworkProtocolIssues([]*agentcomposev2.AgentSpec{{Name: "plain"}}); len(issues) != 0 {
		t.Fatalf("issues = %+v, want none for an agent without a network block", issues)
	}
}
