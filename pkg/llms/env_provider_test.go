package llms

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
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

// A sandbox that publishes both model spellings — a gateway serving OpenAI and
// message protocols side by side — must have its model read through the family
// that owns the call. Reading the generic LLM_MODEL unconditionally made a
// message-protocol facade resolve the OpenAI model; reading the Anthropic names
// unconditionally did the reverse. Either way the verbatim comparison in
// sessionEnvModelForDeclaration missed and truncated a qualified model name.
func TestSessionEnvModelSelectsByProviderFamily(t *testing.T) {
	mixed := []domain.SandboxEnvVar{
		{Name: "LLM_MODEL", Value: "baizhi/deepseek-flash"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-sonnet"},
		{Name: "CLAUDE_MODEL", Value: "claude-legacy"},
	}
	tests := []struct {
		name   string
		family string
		want   string
	}{
		{name: "message protocol reads the Anthropic spelling", family: ProviderFamilyAnthropic, want: "claude-sonnet"},
		{name: "OpenAI family reads the generic name", family: ProviderFamilyOpenAI, want: "baizhi/deepseek-flash"},
		{name: "unknown family prefers the generic name", family: "", want: "baizhi/deepseek-flash"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SessionEnvModel(mixed, tt.family); got != tt.want {
				t.Fatalf("SessionEnvModel(%q) = %q, want %q", tt.family, got, tt.want)
			}
		})
	}

	anthropicOnly := []domain.SandboxEnvVar{{Name: "CLAUDE_MODEL", Value: "claude-legacy"}}
	if got := SessionEnvModel(anthropicOnly, ProviderFamilyAnthropic); got != "claude-legacy" {
		t.Fatalf("message protocol fallback = %q, want claude-legacy", got)
	}
	if got := SessionEnvModel(anthropicOnly, ProviderFamilyOpenAI); got != "claude-legacy" {
		t.Fatalf("generic fallback to the Anthropic spelling = %q, want claude-legacy", got)
	}
}

// The verbatim rule is family-scoped: only the model the resolved family
// publishes makes a declaration resolve as-is.
func TestSessionEnvModelForDeclarationIsFamilyScoped(t *testing.T) {
	envItems := []domain.SandboxEnvVar{
		{Name: "LLM_MODEL", Value: "baizhi/deepseek-flash"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-sonnet"},
	}
	if got := sessionEnvModelForDeclaration("baizhi", "deepseek-flash", envItems, ProviderFamilyOpenAI); got != "baizhi/deepseek-flash" {
		t.Fatalf("OpenAI verbatim model = %q, want the published qualified name", got)
	}
	if got := sessionEnvModelForDeclaration("baizhi", "deepseek-flash", envItems, ProviderFamilyAnthropic); got != "deepseek-flash" {
		t.Fatalf("message-protocol model = %q, want the declaration remainder", got)
	}
	if got := sessionEnvModelForDeclaration("", "claude-sonnet", envItems, ProviderFamilyAnthropic); got != "claude-sonnet" {
		t.Fatalf("message-protocol verbatim model = %q, want the published name", got)
	}
}
