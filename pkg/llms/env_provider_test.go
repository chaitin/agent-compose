package llms

import (
	"testing"

	domain "agent-compose/pkg/model"
)

func TestEnvProviderFamilySelectionUsesExplicitSignals(t *testing.T) {
	tests := []struct {
		name          string
		items         []domain.SandboxEnvVar
		wantOpenAI    bool
		wantAnthropic bool
	}{
		{
			name: "responses protocol remains OpenAI when path ends in messages",
			items: []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/custom/messages"},
				{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
				{Name: "LLM_API_KEY", Value: "generic-key"},
			},
			wantOpenAI: true,
		},
		{
			name: "messages protocol selects Anthropic for a base URL",
			items: []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/api/anthropic"},
				{Name: "LLM_API_PROTOCOL", Value: APIProtocolMessages},
				{Name: "LLM_API_KEY", Value: "generic-key"},
			},
			wantAnthropic: true,
		},
		{
			name: "Anthropic variables select Anthropic regardless of generic protocol",
			items: []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/base"},
				{Name: "LLM_API_PROTOCOL", Value: APIProtocolResponses},
				{Name: "ANTHROPIC_API_KEY", Value: "anthropic-key"},
			},
			wantAnthropic: true,
		},
		{
			name: "OpenAI variables select OpenAI regardless of generic protocol",
			items: []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.example/base"},
				{Name: "LLM_API_PROTOCOL", Value: APIProtocolMessages},
				{Name: "OPENAI_API_KEY", Value: "openai-key"},
			},
			wantOpenAI: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HasOpenAIEnvProviderInput(tt.items); got != tt.wantOpenAI {
				t.Fatalf("HasOpenAIEnvProviderInput() = %t, want %t", got, tt.wantOpenAI)
			}
			if got := HasAnthropicEnvProviderInput(tt.items); got != tt.wantAnthropic {
				t.Fatalf("HasAnthropicEnvProviderInput() = %t, want %t", got, tt.wantAnthropic)
			}
		})
	}
}
