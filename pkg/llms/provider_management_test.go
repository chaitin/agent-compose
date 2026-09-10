package llms

import (
	"errors"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"strings"
	"testing"
)

func TestNormalizeProviderReplacement(t *testing.T) {
	key := " key "
	valid := ProviderReplacement{ID: "gateway-1", BaseURL: " https://example.com/v1 ", Protocol: APIProtocolResponses, APIKey: &key, Enabled: true}
	result, err := NormalizeProviderReplacement(valid)
	if err != nil || result.Name != valid.ID || result.BaseURL != "https://example.com/v1" || *result.APIKey != "key" || key != " key " {
		t.Fatalf("normalization or input ownership failed: %v", err)
	}
	for _, protocol := range []string{APIProtocolResponses, APIProtocolChatCompletions, APIProtocolMessages} {
		input := valid
		input.Protocol = protocol
		if _, err := NormalizeProviderReplacement(input); err != nil {
			t.Fatalf("valid protocol %s: %v", protocol, err)
		}
	}
	for _, tc := range []struct {
		name   string
		modify func(*ProviderReplacement)
	}{
		{"reserved", func(p *ProviderReplacement) { p.ID = "default" }},
		{"anthropic reserved", func(p *ProviderReplacement) { p.ID = "anthropic" }},
		{"session reserved", func(p *ProviderReplacement) { p.ID = "session-env:abc:openai" }},
		{"slash", func(p *ProviderReplacement) { p.ID = "gateway/model" }},
		{"empty id", func(p *ProviderReplacement) { p.ID = "" }},
		{"long id", func(p *ProviderReplacement) { p.ID = strings.Repeat("a", 129) }},
		{"protocol", func(p *ProviderReplacement) { p.Protocol = "unknown" }},
		{"relative url", func(p *ProviderReplacement) { p.BaseURL = "/v1" }},
		{"url user", func(p *ProviderReplacement) { p.BaseURL = "https://secret@example.com" }},
		{"url query", func(p *ProviderReplacement) { p.BaseURL = "https://example.com?key=secret" }},
		{"url fragment", func(p *ProviderReplacement) { p.BaseURL = "https://example.com/#secret" }},
		{"url scheme", func(p *ProviderReplacement) { p.BaseURL = "file:///secret" }},
		{"empty key", func(p *ProviderReplacement) { v := " "; p.APIKey = &v }},
		{"header injection", func(p *ProviderReplacement) { v := "secret\r\nX-Key: injected"; p.APIKey = &v }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := valid
			tc.modify(&input)
			_, err := NormalizeProviderReplacement(input)
			if !errors.Is(err, domain.ErrInvalidArgument) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected redacted validation error, got %v", err)
			}
		})
	}
}
