package compose

import (
	"bytes"
	"strings"
	"testing"
)

func TestParseCanonicalJSONRoundTrip(t *testing.T) {
	normalized := mustNormalizeCompose(t, `
name: canonical
variables:
  PROJECT_TOKEN:
    value: secret
    secret: true
workspaces:
  source:
    provider: git
    url: https://example.test/repo.git
mcp_servers:
  tools:
    type: local
    command: tool-server
    env:
      MODE: test
volumes:
  cache:
    driver: local
agents:
  worker:
    provider: codex
    env:
      AGENT_VALUE: agent
    volumes:
      - source: cache
        target: /cache
    workspace:
      provider: git
      url: https://example.test/repo.git
`, nil)

	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	parsed, err := ParseCanonicalJSON(data)
	if err != nil {
		t.Fatalf("ParseCanonicalJSON returned error: %v", err)
	}
	roundTrip, err := parsed.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("round-trip MarshalCanonicalJSON returned error: %v", err)
	}
	if !bytes.Equal(roundTrip, data) {
		t.Fatalf("round-trip canonical JSON differs:\n got: %s\nwant: %s", roundTrip, data)
	}
	if parsed.Variables["PROJECT_TOKEN"].Value != "secret" || parsed.Agents[0].Env["AGENT_VALUE"].Value != "agent" {
		t.Fatalf("parsed environment maps = %#v / %#v", parsed.Variables, parsed.Agents[0].Env)
	}
	if parsed.Workspaces["source"].Provider != "git" || parsed.MCPServers["tools"].Command != "tool-server" || parsed.Volumes["cache"].Driver != "local" {
		t.Fatalf("parsed project maps = %#v / %#v / %#v", parsed.Workspaces, parsed.MCPServers, parsed.Volumes)
	}
}

func TestParseCanonicalJSONDefaultsHistoricalAgentEnabled(t *testing.T) {
	parsed, err := ParseCanonicalJSON([]byte(`{"name":"historical","agents":[{"name":"missing"},{"name":"disabled","enabled":false}]}`))
	if err != nil {
		t.Fatalf("ParseCanonicalJSON returned error: %v", err)
	}
	if !parsed.Agents[0].Enabled {
		t.Fatal("historical agent without enabled field is disabled")
	}
	if parsed.Agents[1].Enabled {
		t.Fatal("explicitly disabled agent was enabled")
	}
}

func TestParseCanonicalJSONDefaultsHistoricalSchedulerPolicies(t *testing.T) {
	parsed, err := ParseCanonicalJSON([]byte(`{"name":"historical","agents":[{"name":"missing","scheduler":{}},{"name":"empty","scheduler":{"sandbox_policy":"","concurrency_policy":""}}]}`))
	if err != nil {
		t.Fatalf("ParseCanonicalJSON returned error: %v", err)
	}
	for _, agent := range parsed.Agents {
		if agent.Scheduler == nil || agent.Scheduler.SandboxPolicy != "new" || agent.Scheduler.ConcurrencyPolicy != "skip" {
			t.Fatalf("historical scheduler default for %s = %#v", agent.Name, agent.Scheduler)
		}
	}
}

func TestParseCanonicalJSONRejectsMalformedJSON(t *testing.T) {
	if _, err := ParseCanonicalJSON([]byte(`{"agents":`)); err == nil {
		t.Fatal("ParseCanonicalJSON accepted malformed JSON")
	}
}

func TestParseCanonicalJSONRejectsRemovedNetworkProtocol(t *testing.T) {
	body := []byte(`{"name":"legacy","agents":[{"name":"worker","policy":{"network":{"default":"deny-all","allow":[{"host":"api.example.com","port":443,"protocol":"https"}]}}}]}`)
	_, err := ParseCanonicalJSON(body)
	if err == nil || !strings.Contains(err.Error(), `agent "worker" policy.network.allow[0]`) || !strings.Contains(err.Error(), "removed protocol field") {
		t.Fatalf("ParseCanonicalJSON() error = %v, want the actionable protocol migration error", err)
	}
}

func TestParseCanonicalJSONLegacyDenyDefaultIsActionable(t *testing.T) {
	parsed, err := ParseCanonicalJSON([]byte(`{"name":"legacy","agents":[{"name":"worker","policy":{"network":{"default":"deny"}}}]}`))
	if err != nil {
		t.Fatalf("ParseCanonicalJSON() error = %v", err)
	}
	if len(parsed.Agents) != 1 || parsed.Agents[0].Policy == nil || parsed.Agents[0].Policy.Network == nil {
		t.Fatalf("parsed policy = %+v, want the legacy network block", parsed.Agents)
	}
	_, err = parsed.Agents[0].Policy.Network.EgressDeclaration()
	if err == nil || !strings.Contains(err.Error(), `use "deny-all"`) {
		t.Fatalf("EgressDeclaration() error = %v, want the deny-all migration error", err)
	}
}

// TestParseCanonicalJSONAcceptsLegacyDefaultProtocol covers the value every
// spec stored by an older daemon carries: normalization always wrote the
// field's default, so rejecting it would make existing projects unloadable
// without any change in meaning. A spec that named a protocol stays rejected.
func TestParseCanonicalJSONAcceptsLegacyDefaultProtocol(t *testing.T) {
	body := []byte(`{"name":"legacy","agents":[{"name":"worker","policy":{"network":{"default":"deny-all","allow":[{"host":"api.example.com","port":443,"protocol":"any","allowed_ips":["203.0.113.0/24"]}]}},"sandbox":{"network":{"default":"deny-all","allow":[{"host":"10.0.0.0/8","port":22,"protocol":"ANY"}]}}}]}`)
	parsed, err := ParseCanonicalJSON(body)
	if err != nil {
		t.Fatalf("ParseCanonicalJSON() error = %v, want the legacy default protocol to load", err)
	}
	if len(parsed.Agents) != 1 || parsed.Agents[0].Policy == nil || parsed.Agents[0].Policy.Network == nil {
		t.Fatalf("parsed policy = %+v, want the network block", parsed.Agents)
	}
	allow := parsed.Agents[0].Policy.Network.Allow
	if len(allow) != 1 || allow[0].Host != "api.example.com" || allow[0].Port != 443 || len(allow[0].AllowedIPs) != 1 {
		t.Fatalf("parsed allow = %+v, want the declaration without the removed field", allow)
	}
	// The parsed spec is the current representation, so re-encoding it must not
	// emit the removed field again.
	canonical, err := parsed.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON() error = %v", err)
	}
	if bytes.Contains(canonical, []byte("protocol")) {
		t.Fatalf("re-encoded canonical spec still carries the removed field:\n%s", canonical)
	}
}
