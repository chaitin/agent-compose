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
    sandbox:
      network:
        default: deny
        allow:
          - host: "*.example.com"
            port: 8443
            protocol: tcp
          - host: api.github.com
            port: 443
            protocol: https
`)

	mapped := SandboxSpecToProto(spec.Agents[0].Sandbox)
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
	if network.GetDefault() != compose.SandboxNetworkDefaultDeny {
		t.Fatalf("default = %q, want %q", network.GetDefault(), compose.SandboxNetworkDefaultDeny)
	}
	allow := network.GetAllow()
	if len(allow) != 2 {
		t.Fatalf("allow = %+v, want two entries", allow)
	}
	if allow[0].GetHost() != "*.example.com" || allow[0].GetPort() != 8443 || allow[0].GetProtocol() != "tcp" {
		t.Fatalf("allow[0] = %+v, want *.example.com:8443/tcp", allow[0])
	}
	if allow[1].GetHost() != "api.github.com" || allow[1].GetPort() != 443 || allow[1].GetProtocol() != "https" {
		t.Fatalf("allow[1] = %+v, want api.github.com:443/https", allow[1])
	}
}

func TestSandboxSpecWithoutNetworkStaysAbsentInProto(t *testing.T) {
	spec := mustNormalizeNetworkCompose(t, `
name: network-proto
agents:
  worker:
    provider: codex
    sandbox:
      stopped_runtime_policy: remove
`)
	mapped := SandboxSpecToProto(spec.Agents[0].Sandbox)
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
