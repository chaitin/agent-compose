package llms

import (
	"context"
	"testing"
)

func TestOpenAIEnvProviderUsesProtocolInsteadOfEndpointShape(t *testing.T) {
	tests := []struct {
		name         string
		protocol     string
		wantProvider bool
	}{
		{name: "responses path ending in messages remains OpenAI", protocol: APIProtocolResponses, wantProvider: true},
		{name: "messages protocol is not OpenAI", protocol: APIProtocolMessages, wantProvider: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newResolverCoverageStore()
			values := map[string]string{
				"LLM_API_ENDPOINT": "https://gateway.example/custom/messages",
				"LLM_API_PROTOCOL": tt.protocol,
				"LLM_API_KEY":      "generic-key",
				"LLM_MODEL":        "test-model",
			}
			id, err := EnsureOpenAIEnvProvider(context.Background(), store, mapLookup(values), "openai-env", "openai-env", ProviderScopeSessionEnv, "", false)
			if err != nil {
				t.Fatalf("EnsureOpenAIEnvProvider() error = %v", err)
			}
			if got := id != ""; got != tt.wantProvider {
				t.Fatalf("provider created = %t, want %t", got, tt.wantProvider)
			}
			if !tt.wantProvider {
				return
			}
			if len(store.providers) != 1 || store.providers[0].ProviderType != ProviderFamilyOpenAI || store.providers[0].BaseURL != values["LLM_API_ENDPOINT"] {
				t.Fatalf("OpenAI provider = %#v", store.providers)
			}
		})
	}
}

func TestAnthropicEnvProviderUsesGenericBaseURLForExplicitFamily(t *testing.T) {
	store := newResolverCoverageStore()
	values := map[string]string{
		"LLM_API_ENDPOINT": "https://gateway.example/api/anthropic",
		"LLM_API_KEY":      "generic-key",
		"LLM_MODEL":        "test-model",
	}
	id, err := EnsureAnthropicEnvProvider(context.Background(), store, mapLookup(values), "x-api-key", "", "anthropic-env", "anthropic-env", ProviderScopeSessionEnv, "", false)
	if err != nil {
		t.Fatalf("EnsureAnthropicEnvProvider() error = %v", err)
	}
	if id == "" || len(store.providers) != 1 {
		t.Fatalf("provider id = %q, providers = %#v", id, store.providers)
	}
	provider := store.providers[0]
	if provider.ProviderType != ProviderFamilyAnthropic || provider.DefaultWireAPI != APIProtocolMessages || provider.BaseURL != values["LLM_API_ENDPOINT"] || provider.APIKey != values["LLM_API_KEY"] {
		t.Fatalf("Anthropic provider = %#v", provider)
	}
	if got := EndpointForProvider(provider, APIProtocolMessages); got != "https://gateway.example/api/anthropic/messages" {
		t.Fatalf("Anthropic endpoint = %q", got)
	}
}

func TestDefaultAnthropicEnvProviderInputRequiresExplicitSignal(t *testing.T) {
	tests := []struct {
		name   string
		values map[string]string
		want   bool
	}{
		{
			name: "generic OpenAI configuration",
			values: map[string]string{
				"LLM_API_ENDPOINT": "https://gateway.example/openai",
				"LLM_API_PROTOCOL": APIProtocolResponses,
				"LLM_API_KEY":      "generic-key",
			},
		},
		{
			name: "generic messages configuration",
			values: map[string]string{
				"LLM_API_ENDPOINT": "https://gateway.example/anthropic",
				"LLM_API_PROTOCOL": APIProtocolMessages,
				"LLM_API_KEY":      "generic-key",
			},
			want: true,
		},
		{
			name: "Anthropic-specific configuration",
			values: map[string]string{
				"ANTHROPIC_BASE_URL": "https://gateway.example/anthropic",
				"ANTHROPIC_API_KEY":  "anthropic-key",
			},
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasDefaultAnthropicEnvProviderInput(mapLookup(tt.values)); got != tt.want {
				t.Fatalf("hasDefaultAnthropicEnvProviderInput() = %t, want %t", got, tt.want)
			}
		})
	}
}

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
