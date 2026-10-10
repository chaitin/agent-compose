package driver

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/credentials"
)

// TestCredentialEnvNameEnumeratesEveryCredentialForm pins the explicit
// credential surface. Before this table the engine recognized only LLM provider
// keys; git, MCP, registry, and auth-header credentials were invisible to every
// credential decision. Each entry states both the handle kind it maps to and
// whether the daemon already holds the value alone.
func TestCredentialEnvNameEnumeratesEveryCredentialForm(t *testing.T) {
	tests := []struct {
		name       string
		wantKind   credentials.Kind
		absorbable bool
	}{
		{name: "OPENAI_API_KEY", wantKind: credentials.KindLLMFacade, absorbable: true},
		{name: "anthropic_auth_token", wantKind: credentials.KindLLMFacade, absorbable: true},
		{name: "GITHUB_TOKEN", wantKind: credentials.KindGit},
		{name: "GITLAB_TOKEN", wantKind: credentials.KindGit},
		{name: "MCP_TOKEN", wantKind: credentials.KindMCP},
		{name: "MCP_api_key", wantKind: credentials.KindMCP},
		{name: "REGISTRY_TOKEN", wantKind: credentials.KindRegistry},
		{name: "DOCKER_AUTH_CONFIG", wantKind: credentials.KindRegistry},
		{name: "CARGO_REGISTRY_TOKEN", wantKind: credentials.KindRegistry},
		{name: "Authorization", wantKind: credentials.KindGeneric},
		{name: "X-API-Key", wantKind: credentials.KindGeneric},
		{name: "CAP_TOKEN", wantKind: credentials.KindGeneric},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			surface, ok := CredentialEnvName(test.name)
			if !ok {
				t.Fatalf("CredentialEnvName(%q) = false, want a recognized credential", test.name)
			}
			if surface.Kind != test.wantKind {
				t.Fatalf("CredentialEnvName(%q).Kind = %q, want %q", test.name, surface.Kind, test.wantKind)
			}
			if surface.Absorbable != test.absorbable {
				t.Fatalf("CredentialEnvName(%q).Absorbable = %v, want %v", test.name, surface.Absorbable, test.absorbable)
			}
		})
	}
}

func TestCredentialEnvNameRejectsUnrecognizedNames(t *testing.T) {
	for _, name := range []string{"", "MODE", "MYCORP_API_KEY", "MYCORP_AUTH_TOKEN", "OPENAI_BASE_URL", "LLM_API_ENDPOINT", "DATABASE_PASSWORD"} {
		if surface, ok := CredentialEnvName(name); ok {
			t.Errorf("CredentialEnvName(%q) = %#v, want an unrecognized name to stay out of the surface", name, surface)
		}
		if IsHeldCredentialName(name) {
			t.Errorf("IsHeldCredentialName(%q) = true, want false for a name the daemon does not hold", name)
		}
	}
}

// TestIsHeldCredentialNameOnlyClaimsAbsorbedCredentials pins the honesty rule
// the redaction layer depends on: a view may hide a value only when the daemon
// holds it alone, because hiding an exposed value would describe it as
// protected without changing the exposure.
func TestIsHeldCredentialNameOnlyClaimsAbsorbedCredentials(t *testing.T) {
	for _, name := range []string{"OPENAI_API_KEY", "LLM_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		if !IsHeldCredentialName(name) {
			t.Errorf("IsHeldCredentialName(%q) = false, want true for an LLM credential the facade absorbs", name)
		}
	}
	for _, name := range []string{"GITHUB_TOKEN", "MCP_TOKEN", "REGISTRY_TOKEN", "Authorization", "CAP_TOKEN"} {
		if IsHeldCredentialName(name) {
			t.Errorf("IsHeldCredentialName(%q) = true, want false while the value still reaches the guest", name)
		}
	}
}
