package llms

import (
	"testing"

	domain "agent-compose/pkg/model"
)

func TestResolveEnvDefaultProviderCredentialsFromLayersResolvesFamiliesIndependently(t *testing.T) {
	credentials := ResolveEnvDefaultProviderCredentialsFromLayers(
		[]domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "global-openai"}},
		[]domain.SandboxEnvVar{{Name: "ANTHROPIC_AUTH_TOKEN", Value: "process-anthropic"}, {Name: "LLM_API_KEY", Value: "process-generic"}},
	)
	if credentials.OpenAIAPIKey != "global-openai" {
		t.Fatalf("OpenAI key = %q, want global-openai", credentials.OpenAIAPIKey)
	}
	if credentials.AnthropicAPIKey != "process-anthropic" || credentials.AnthropicAuthHeader != "Authorization" || credentials.AnthropicAuthScheme != "Bearer" {
		t.Fatalf("Anthropic credentials = %#v", credentials)
	}
}
