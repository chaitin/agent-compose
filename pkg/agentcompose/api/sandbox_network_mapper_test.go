package api

import (
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestSandboxSpecNetworkRoundTripsThroughProto(t *testing.T) {
	spec := mustNormalizeNetworkCompose(t, `
name: network-proto
agents:
  worker:
    provider: codex
    policy:
      network:
        default: deny-all
        allow:
          - host: "*.example.com"
            port: 8443
          - host: api.github.com
            port: 443
`)

	mapped := SandboxSpecToProto(spec.Agents[0].PolicyBlock())
	if mapped.GetNetwork() == nil {
		t.Fatal("declared network policy was dropped by the proto mapping")
	}
	encoded, err := proto.Marshal(mapped)
	if err != nil {
		t.Fatalf("proto.Marshal returned error: %v", err)
	}
	decoded := &agentcomposev2.SandboxSpec{}
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("proto.Unmarshal returned error: %v", err)
	}

	network := decoded.GetNetwork()
	if network.GetDefault() != compose.SandboxNetworkDefaultDenyAll {
		t.Fatalf("default = %q, want %q", network.GetDefault(), compose.SandboxNetworkDefaultDenyAll)
	}
	allow := network.GetAllow()
	if len(allow) != 2 {
		t.Fatalf("allow = %+v, want two entries", allow)
	}
	if allow[0].GetHost() != "*.example.com" || allow[0].GetPort() != 8443 {
		t.Fatalf("allow[0] = %+v, want *.example.com:8443", allow[0])
	}
	if allow[1].GetHost() != "api.github.com" || allow[1].GetPort() != 443 {
		t.Fatalf("allow[1] = %+v, want api.github.com:443", allow[1])
	}
}

func TestSandboxSpecWithoutNetworkStaysAbsentInProto(t *testing.T) {
	spec := mustNormalizeNetworkCompose(t, `
name: network-proto
agents:
  worker:
    provider: codex
    policy:
      stopped_runtime_policy: remove
`)
	mapped := SandboxSpecToProto(spec.Agents[0].PolicyBlock())
	if mapped.GetNetwork() != nil {
		t.Fatalf("undeclared network policy appeared in proto: %+v", mapped.GetNetwork())
	}
	encoded, err := proto.Marshal(mapped)
	if err != nil {
		t.Fatalf("proto.Marshal returned error: %v", err)
	}
	decoded := &agentcomposev2.SandboxSpec{}
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("proto.Unmarshal returned error: %v", err)
	}
	if decoded.GetNetwork() != nil {
		t.Fatalf("undeclared network policy survived the wire round trip: %+v", decoded.GetNetwork())
	}
}

func mustNormalizeNetworkCompose(t *testing.T, raw string) *compose.NormalizedProjectSpec {
	t.Helper()
	parsed, err := compose.Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	return normalized
}

func TestSandboxSpecDenialsRoundTripThroughProto(t *testing.T) {
	spec := mustNormalizeNetworkCompose(t, `
name: network-proto
agents:
  worker:
    provider: codex
    policy:
      network:
        deny:
          - host: 10.2.16.21
            port: 443
          - host: "*.blocked.example"
            port: 80
`)
	mapped := SandboxSpecToProto(spec.Agents[0].PolicyBlock())
	encoded, err := proto.Marshal(mapped)
	if err != nil {
		t.Fatalf("proto.Marshal returned error: %v", err)
	}
	decoded := &agentcomposev2.SandboxSpec{}
	if err := proto.Unmarshal(encoded, decoded); err != nil {
		t.Fatalf("proto.Unmarshal returned error: %v", err)
	}
	deny := decoded.GetNetwork().GetDeny()
	if len(deny) != 2 {
		t.Fatalf("deny = %+v, want two entries", deny)
	}
	if deny[0].GetHost() != "*.blocked.example" || deny[0].GetPort() != 80 {
		t.Fatalf("deny[0] = %+v, want the sorted *.blocked.example:80 entry", deny[0])
	}
	if deny[1].GetHost() != "10.2.16.21" || deny[1].GetPort() != 443 {
		t.Fatalf("deny[1] = %+v, want 10.2.16.21:443", deny[1])
	}

	shape := policyYAMLShape(decoded)
	network, ok := shape["network"].(map[string]any)
	if !ok {
		t.Fatalf("network shape = %#v, want a mapping", shape["network"])
	}
	rendered, ok := network["deny"].([]map[string]any)
	if !ok || len(rendered) != 2 {
		t.Fatalf("deny shape = %#v, want two entries", network["deny"])
	}
}
