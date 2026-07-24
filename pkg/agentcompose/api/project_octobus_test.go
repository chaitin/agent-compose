package api

import (
	"testing"

	"agent-compose/pkg/compose"
)

func TestProjectSpecOctoBusServersRoundTripAndRedaction(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "octobus",
		OctoBusServers: map[string]compose.NormalizedOctoBusServerSpec{
			"public":   {URL: "https://public.example"},
			"internal": {URL: "https://internal.example", Token: "secret-token"},
		},
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

	redacted := ProjectSpecToProtoWithRedactedOctoBusTokens(spec)
	if got := redacted.GetOctobusServers()[0].GetToken(); got == "secret-token" || got == "" {
		t.Fatalf("redacted token = %q", got)
	}
	if got := wire.GetOctobusServers()[0].GetToken(); got != "secret-token" {
		t.Fatalf("redaction mutated source token = %q", got)
	}
}
