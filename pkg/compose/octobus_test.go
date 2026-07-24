package compose

import (
	"strings"
	"testing"
)

func TestNormalizeOctoBusServersAndQualifiedCapsets(t *testing.T) {
	spec := mustParseCompose(t, `
name: octobus-demo
octobus_servers:
  public:
    url: https://public.example/${PATH}
  internal:
    url: https://internal.example
    token: ${OCTOBUS_TOKEN}
agents:
  coder:
    capset_ids: [legacy-capset, internal/dev, public/web-search, internal/a/b]
`)
	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{
		"PATH":          "octobus",
		"OCTOBUS_TOKEN": "secret-token",
	}})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if got := normalized.OctoBusServers["public"].URL; got != "https://public.example/octobus" {
		t.Fatalf("public url = %q", got)
	}
	if got := normalized.OctoBusServers["internal"].Token; got != "secret-token" {
		t.Fatalf("internal token = %q", got)
	}
	wantCapsets := []string{"legacy-capset", "internal/dev", "public/web-search", "internal/a/b"}
	if got := normalized.Agents[0].CapsetIDs; strings.Join(got, ",") != strings.Join(wantCapsets, ",") {
		t.Fatalf("capset ids = %#v, want %#v", got, wantCapsets)
	}
}

func TestNormalizeRejectsInvalidOctoBusConfiguration(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string
	}{
		{name: "invalid name", raw: "octobus_servers:\n  BadName:\n    url: https://example.test\n", wantField: "octobus_servers.BadName"},
		{name: "missing url", raw: "octobus_servers:\n  internal:\n    token: token\n", wantField: "octobus_servers.internal.url"},
		{name: "relative url", raw: "octobus_servers:\n  internal:\n    url: /octobus\n", wantField: "octobus_servers.internal.url"},
		{name: "unsupported scheme", raw: "octobus_servers:\n  internal:\n    url: grpc://example.test\n", wantField: "octobus_servers.internal.url"},
		{name: "userinfo", raw: "octobus_servers:\n  internal:\n    url: https://user:pass@example.test\n", wantField: "octobus_servers.internal.url"},
		{name: "undefined server", raw: "agents:\n  coder:\n    capset_ids: [missing/dev]\n", wantField: "agents.coder.capset_ids[0]"},
		{name: "invalid qualified server", raw: "agents:\n  coder:\n    capset_ids: [BadName/dev]\n", wantField: "agents.coder.capset_ids[0]"},
		{name: "empty server", raw: "agents:\n  coder:\n    capset_ids: [/dev]\n", wantField: "agents.coder.capset_ids[0]"},
		{name: "empty capset", raw: "octobus_servers:\n  internal:\n    url: https://example.test\nagents:\n  coder:\n    capset_ids: [internal/]\n", wantField: "agents.coder.capset_ids[0]"},
		{name: "missing token environment", raw: "octobus_servers:\n  internal:\n    url: https://example.test\n    token: ${MISSING_TOKEN}\n", wantField: "octobus_servers.internal.token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustParseCompose(t, "name: octobus-test\n"+tt.raw)
			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil || !strings.Contains(err.Error(), tt.wantField) {
				t.Fatalf("Normalize error = %v, want field %s", err, tt.wantField)
			}
		})
	}
}

func TestOctoBusCanonicalOutputOrderingAndRedaction(t *testing.T) {
	spec := mustParseCompose(t, `
name: octobus-output
octobus_servers:
  zeta:
    url: https://zeta.example
    token: zeta-secret
  alpha:
    url: https://alpha.example
    token: alpha-secret
agents:
  coder:
    capset_ids: [zeta/dev, alpha/search]
`)
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	redacted, err := normalized.MarshalCanonicalYAML(true)
	if err != nil {
		t.Fatalf("MarshalCanonicalYAML returned error: %v", err)
	}
	text := string(redacted)
	if strings.Contains(text, "alpha-secret") || strings.Contains(text, "zeta-secret") {
		t.Fatalf("redacted output leaked token: %s", text)
	}
	if strings.Count(text, redactedOctoBusToken) != 2 || strings.Index(text, "name: alpha") > strings.Index(text, "name: zeta") {
		t.Fatalf("canonical output is not sorted/redacted: %s", text)
	}
	unredacted, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if !strings.Contains(string(unredacted), "alpha-secret") || !strings.Contains(string(unredacted), `"octobus_servers"`) {
		t.Fatalf("unredacted canonical json = %s", unredacted)
	}
}
