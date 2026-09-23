package llms

import "testing"

// isolateLLMEnv clears every process-environment name the LLM configuration
// loader reads, so a test that configures its own connection cannot inherit the
// developer machine's. It backs the projection and facade tests in this
// package.
func isolateLLMEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LLM_API_ENDPOINT",
		"LLM_API_PROTOCOL",
		"LLM_API_KEY",
		"LLM_API_HEADERS",
		"OPENAI_API_KEY",
		"LLM_MODEL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_ENDPOINT",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"CLAUDE_MODEL",
	} {
		t.Setenv(key, "")
	}
}

// mapLookup builds an EnvProviderLookup over a literal map for the projection
// tests.
func mapLookup(values map[string]string) EnvProviderLookup {
	return func(keys ...string) string {
		for _, key := range keys {
			if value := values[key]; value != "" {
				return value
			}
		}
		return ""
	}
}
