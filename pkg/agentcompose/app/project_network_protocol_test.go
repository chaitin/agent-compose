package app

import (
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TestParseProjectRequestRejectsLegacyNetworkProtocol pins the end-to-end wire
// behavior: a client built before the entry protocol was removed still sends the
// field, the generated type drops it, and without the check the daemon would
// apply an entry wider than the client declared.
func TestParseProjectRequestRejectsLegacyNetworkProtocol(t *testing.T) {
	raw := protowire.AppendTag(nil, 3, protowire.BytesType)
	raw = protowire.AppendString(raw, "https")
	entry := &agentcomposev2.SandboxNetworkAllowSpec{}
	if err := proto.Unmarshal(raw, entry); err != nil {
		t.Fatalf("decode legacy entry: %v", err)
	}
	spec := &agentcomposev2.ProjectSpec{
		Name: "legacy-client",
		Agents: []*agentcomposev2.AgentSpec{{
			Name: "worker",
			Sandbox: &agentcomposev2.SandboxSpec{Network: &agentcomposev2.SandboxNetworkSpec{
				Default: "deny-all",
				Allow:   []*agentcomposev2.SandboxNetworkAllowSpec{entry},
			}},
		}},
	}
	parsed, issues, err := parseProjectRequest(spec)
	if err != nil {
		t.Fatalf("parseProjectRequest() error = %v", err)
	}
	if parsed != nil {
		t.Fatalf("parseProjectRequest() returned a spec, want the legacy field refused")
	}
	if len(issues) != 1 || issues[0].Path != "agents[0].policy.network.allow[0].protocol" {
		t.Fatalf("issues = %+v, want the legacy protocol path", issues)
	}
}
