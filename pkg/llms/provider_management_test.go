package llms

import (
	"errors"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestNormalizeProviderReplacement(t *testing.T) {
	key := " key "
	valid := ProviderReplacement{ID: "gateway-1", BaseURL: " https://example.com/v1 ", Protocol: APIProtocolResponses, APIKey: &key}
	result, err := NormalizeProviderReplacement(valid)
	if err != nil || result.Name != valid.ID || result.BaseURL != "https://example.com/v1" || *result.APIKey != "key" || key != " key " || result.Enabled == nil || !*result.Enabled {
		t.Fatalf("normalization or input ownership failed: %v %#v", err, result)
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
		{"missing key", func(p *ProviderReplacement) { p.APIKey = nil }},
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

func TestNormalizeProviderUpdatePreservesOmittedFields(t *testing.T) {
	result, err := NormalizeProviderUpdate(ProviderReplacement{ID: "gateway-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "" || result.BaseURL != "" || result.Protocol != "" || result.APIKey != nil || result.Enabled != nil {
		t.Fatalf("omitted update fields were defaulted: %#v", result)
	}
	disabled := false
	key := " rotated "
	result, err = NormalizeProviderUpdate(ProviderReplacement{ID: "gateway-1", Name: " renamed ", BaseURL: " https://second.example/v1 ", Protocol: APIProtocolMessages, APIKey: &key, Enabled: &disabled})
	if err != nil || result.Name != "renamed" || result.BaseURL != "https://second.example/v1" || result.Protocol != APIProtocolMessages || *result.APIKey != "rotated" || result.Enabled == nil || *result.Enabled || key != " rotated " {
		t.Fatalf("explicit update fields: %v %#v", err, result)
	}
	empty := " "
	_, err = NormalizeProviderUpdate(ProviderReplacement{ID: "gateway-1", APIKey: &empty})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("empty key: %v", err)
	}
}

func TestManagedProviderHeadersJSON(t *testing.T) {
	if got := ManagedProviderHeadersJSON(APIProtocolMessages); got != AnthropicVersionHeadersJSON {
		t.Fatalf("anthropic headers = %q", got)
	}
	if got := ManagedProviderHeadersJSON(APIProtocolResponses); got != "{}" {
		t.Fatalf("openai headers = %q", got)
	}
}

func TestNormalizeProviderUpdateRejectsInvalidValues(t *testing.T) {
	injected := "secret\r\nX-Key: injected"
	for _, tc := range []struct {
		name  string
		input ProviderReplacement
	}{
		{name: "reserved id", input: ProviderReplacement{ID: "default"}},
		{name: "invalid protocol", input: ProviderReplacement{ID: "gateway-1", Protocol: "unknown"}},
		{name: "relative url", input: ProviderReplacement{ID: "gateway-1", BaseURL: "/v1"}},
		{name: "header injection key", input: ProviderReplacement{ID: "gateway-1", APIKey: &injected}},
		{name: "url newline", input: ProviderReplacement{ID: "gateway-1", BaseURL: "https://exam\nple.com"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeProviderUpdate(tc.input)
			if !errors.Is(err, domain.ErrInvalidArgument) || strings.Contains(err.Error(), "secret") {
				t.Fatalf("expected redacted validation error, got %v", err)
			}
		})
	}
}

func TestE2ENormalizeProviderReplacement(t *testing.T) {
	TestNormalizeProviderReplacement(t)
}

func TestE2ENormalizeProviderUpdatePreservesOmittedFields(t *testing.T) {
	TestNormalizeProviderUpdatePreservesOmittedFields(t)
}

func TestE2EManagedProviderHeadersJSON(t *testing.T) {
	TestManagedProviderHeadersJSON(t)
}

func TestE2ENormalizeProviderUpdateRejectsInvalidValues(t *testing.T) {
	TestNormalizeProviderUpdateRejectsInvalidValues(t)
}
