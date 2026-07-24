package api

import (
	"testing"

	"agent-compose/pkg/compose"
)

func TestProjectSpecOctoBusServersRoundTripAndRedaction(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "octobus",
		Variables: map[string]compose.EnvVarSpec{
			"A_PROJECT_SECRET": {Value: "project-secret", Secret: true},
			"Z_PROJECT_PUBLIC": {Value: "project-public"},
		},
		MCPServers: map[string]compose.NormalizedMCPServerSpec{
			"project-mcp": {
				Env:     map[string]compose.EnvVarSpec{"TOKEN": {Value: "project-mcp-env", Secret: true}},
				Headers: map[string]compose.EnvVarSpec{"Authorization": {Value: "project-mcp-header", Secret: true}},
			},
		},
		OctoBusServers: map[string]compose.NormalizedOctoBusServerSpec{
			"public":   {URL: "https://public.example"},
			"internal": {URL: "https://internal.example", Token: "secret-token"},
		},
		Agents: []compose.NormalizedAgentSpec{{
			Name: "worker",
			Env:  map[string]compose.EnvVarSpec{"TOKEN": {Value: "agent-secret", Secret: true}},
			MCPServers: map[string]compose.NormalizedMCPServerSpec{
				"agent-mcp": {
					Env:     map[string]compose.EnvVarSpec{"TOKEN": {Value: "agent-mcp-env", Secret: true}},
					Headers: map[string]compose.EnvVarSpec{"Authorization": {Value: "agent-mcp-header", Secret: true}},
				},
			},
		}},
	}

	wire := ProjectSpecToProto(spec)
	if len(wire.GetOctobusServers()) != 2 || wire.GetOctobusServers()[0].GetName() != "internal" || wire.GetOctobusServers()[0].GetToken() != "secret-token" {
		t.Fatalf("octobus proto = %#v", wire.GetOctobusServers())
	}
	shape, issues := ProjectSpecYAMLShape(wire)
	if len(issues) != 0 {
		t.Fatalf("ProjectSpecYAMLShape issues = %#v", issues)
	}
	servers, ok := shape["octobus_servers"].(map[string]any)
	if !ok || len(servers) != 2 {
		t.Fatalf("octobus yaml shape = %#v", shape["octobus_servers"])
	}
	internal, ok := servers["internal"].(map[string]any)
	if !ok || internal["url"] != "https://internal.example" || internal["token"] != "secret-token" {
		t.Fatalf("internal yaml shape = %#v", servers["internal"])
	}

	redacted := ProjectSpecToProtoRedacted(spec)
	if got := redacted.GetOctobusServers()[0].GetToken(); got == "secret-token" || got == "" {
		t.Fatalf("redacted token = %q", got)
	}
	if got := redacted.GetVariables()[0].GetValue(); got != secretRedactedValue {
		t.Fatalf("redacted project variable = %q", got)
	}
	if got := redacted.GetVariables()[1].GetValue(); got != "project-public" {
		t.Fatalf("non-secret project variable = %q", got)
	}
	if got := redacted.GetMcpServers()[0].GetEnv()[0].GetValue(); got != secretRedactedValue {
		t.Fatalf("redacted project MCP env = %q", got)
	}
	if got := redacted.GetMcpServers()[0].GetHeaders()[0].GetValue(); got != secretRedactedValue {
		t.Fatalf("redacted project MCP header = %q", got)
	}
	if got := redacted.GetAgents()[0].GetEnv()[0].GetValue(); got != secretRedactedValue {
		t.Fatalf("redacted agent env = %q", got)
	}
	if got := redacted.GetAgents()[0].GetMcpServers()[0].GetEnv()[0].GetValue(); got != secretRedactedValue {
		t.Fatalf("redacted agent MCP env = %q", got)
	}
	if got := redacted.GetAgents()[0].GetMcpServers()[0].GetHeaders()[0].GetValue(); got != secretRedactedValue {
		t.Fatalf("redacted agent MCP header = %q", got)
	}
	if got := wire.GetOctobusServers()[0].GetToken(); got != "secret-token" {
		t.Fatalf("redaction mutated source token = %q", got)
	}
	if got := wire.GetVariables()[0].GetValue(); got != "project-secret" {
		t.Fatalf("redaction mutated source project variable = %q", got)
	}
	if got := wire.GetMcpServers()[0].GetHeaders()[0].GetValue(); got != "project-mcp-header" {
		t.Fatalf("redaction mutated source MCP header = %q", got)
	}
	if got := wire.GetAgents()[0].GetMcpServers()[0].GetEnv()[0].GetValue(); got != "agent-mcp-env" {
		t.Fatalf("redaction mutated source agent MCP env = %q", got)
	}
}
