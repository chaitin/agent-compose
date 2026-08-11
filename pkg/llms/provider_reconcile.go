package llms

import (
	domain "agent-compose/pkg/model"
)

// EnvDefaultProviderCredentials contains credentials derived from global
// environment values for the automatically managed default providers.
type EnvDefaultProviderCredentials struct {
	OpenAIAPIKey        string
	AnthropicAPIKey     string
	AnthropicAuthHeader string
	AnthropicAuthScheme string
}

// ResolveEnvDefaultProviderCredentials applies the same credential precedence
// used by environment-provider bootstrap.
func ResolveEnvDefaultProviderCredentials(items []domain.SandboxEnvVar) EnvDefaultProviderCredentials {
	return ResolveEnvDefaultProviderCredentialsFromLayers(items)
}

// ResolveEnvDefaultProviderCredentialsFromLayers resolves credentials from
// layers in precedence order. Each layer is exhausted before the next layer is
// considered, preserving the bootstrap source-major precedence.
func ResolveEnvDefaultProviderCredentialsFromLayers(layers ...[]domain.SandboxEnvVar) EnvDefaultProviderCredentials {
	openAIKey := resolveFamilyAPIKey(layers, "LLM_API_KEY", "OPENAI_API_KEY")
	anthropic := resolveFamilyAnthropicCredential(layers)
	return EnvDefaultProviderCredentials{OpenAIAPIKey: openAIKey, AnthropicAPIKey: anthropic.apiKey, AnthropicAuthHeader: anthropic.authHeader, AnthropicAuthScheme: anthropic.authScheme}
}

func resolveFamilyAPIKey(layers [][]domain.SandboxEnvVar, keys ...string) string {
	for _, layer := range layers {
		if value := firstNonEmptyTrimmed(envValues(layer, keys...)...); value != "" {
			return value
		}
	}
	return ""
}

func resolveFamilyAnthropicCredential(layers [][]domain.SandboxEnvVar) anthropicCredential {
	for _, layer := range layers {
		if credential, ok := anthropicCredentialFromItems(layer); ok {
			return credential
		}
	}
	return anthropicCredential{authHeader: "x-api-key"}
}

func envValues(items []domain.SandboxEnvVar, keys ...string) []string {
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, EnvItemValue(items, key))
	}
	return values
}
