package llms

import (
	"testing"
)

func TestNewResolvedTargetDropsForbiddenPerModelHeaders(t *testing.T) {
	config := ProviderModelConfig{
		HeadersJSON: `{"Authorization":"Bearer stolen","X-Model-Header":"allowed"}`,
	}
	provider := Provider{
		ID:             "provider-1",
		BaseURL:        "https://provider.test",
		DefaultWireAPI: APIProtocolChatCompletions,
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		APIKey:         "provider-secret",
	}
	model := Model{ID: "model-1", Name: "model-1"}

	target, err := NewResolvedTarget(provider, model, config)
	if err != nil {
		t.Fatalf("NewResolvedTarget returned error: %v", err)
	}
	if got := target.Headers.Get("Authorization"); got != "Bearer provider-secret" {
		t.Fatalf("Authorization header = %q, want provider credential to survive unclobbered", got)
	}
	if got := target.Headers.Get("X-Model-Header"); got != "allowed" {
		t.Fatalf("X-Model-Header = %q, want per-model header to pass through", got)
	}
}

// ForbiddenProviderHeader lower-cases and trims its "name" argument before
// comparing, so passing the raw (untrimmed) per-model key into it and only
// trimming for the later headers.Set call cannot disagree: trimming is
// idempotent, so canonical(rawKey) == canonical(trim(rawKey)) always holds.
// This test pins that guarantee against case and whitespace variants of the
// auth header name.
func TestNewResolvedTargetDropsForbiddenPerModelHeaderCaseAndWhitespaceVariants(t *testing.T) {
	for _, key := range []string{"authorization", " Authorization", "AUTHORIZATION", " authorization "} {
		t.Run(key, func(t *testing.T) {
			config := ProviderModelConfig{HeadersJSON: `{"` + key + `":"Bearer stolen"}`}
			provider := Provider{
				ID:             "provider-1",
				BaseURL:        "https://provider.test",
				DefaultWireAPI: APIProtocolChatCompletions,
				AuthHeader:     "Authorization",
				AuthScheme:     "Bearer",
				APIKey:         "provider-secret",
			}
			model := Model{ID: "model-1", Name: "model-1"}

			target, err := NewResolvedTarget(provider, model, config)
			if err != nil {
				t.Fatalf("NewResolvedTarget returned error: %v", err)
			}
			if got := target.Headers.Get("Authorization"); got != "Bearer provider-secret" {
				t.Fatalf("Authorization header = %q for per-model key %q, want provider credential to survive unclobbered", got, key)
			}
		})
	}
}
