package llms

import (
	"errors"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// A connection's protocol names the request body it speaks, not the header that
// carries its credential: a gateway can serve Anthropic Messages while
// authenticating with a bearer token.
func TestResolveProviderAuthPrefersExplicitPresentation(t *testing.T) {
	cases := []struct {
		name     string
		protocol string
		auth     ProviderAuth
		header   string
		scheme   string
	}{
		{name: "openai protocol keeps bearer", protocol: APIProtocolResponses, header: "Authorization", scheme: "Bearer"},
		{name: "chat completions keeps bearer", protocol: APIProtocolChatCompletions, header: "Authorization", scheme: "Bearer"},
		{name: "anthropic protocol keeps x-api-key", protocol: APIProtocolMessages, header: "x-api-key"},
		{name: "anthropic protocol overridden to bearer", protocol: APIProtocolMessages, auth: ProviderAuthBearer, header: "Authorization", scheme: "Bearer"},
		{name: "openai protocol overridden to x-api-key", protocol: APIProtocolChatCompletions, auth: ProviderAuthXAPIKey, header: "x-api-key"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			header, scheme := ResolveProviderAuth(tc.protocol, tc.auth)
			if header != tc.header || scheme != tc.scheme {
				t.Fatalf("ResolveProviderAuth(%q, %q) = (%q, %q), want (%q, %q)", tc.protocol, tc.auth, header, scheme, tc.header, tc.scheme)
			}
		})
	}
}

func TestProviderAuthFromWireRoundTripsKnownPresentations(t *testing.T) {
	for _, auth := range []ProviderAuth{ProviderAuthXAPIKey, ProviderAuthBearer} {
		header, scheme := ProviderAuthWire(auth)
		if got := ProviderAuthFromWire(header, scheme); got != auth {
			t.Fatalf("ProviderAuthFromWire(%q, %q) = %q, want %q", header, scheme, got, auth)
		}
	}
	if got := ProviderAuthFromWire("x-custom", "Token"); got != "" {
		t.Fatalf("unknown presentation = %q, want the empty presentation", got)
	}
}

func TestNormalizeProviderReplacementValidatesAuth(t *testing.T) {
	key := "gateway-key"
	xAPIKey, bearer, empty := ProviderAuthXAPIKey, ProviderAuthBearer, ProviderAuth("")
	for _, auth := range []*ProviderAuth{nil, &empty, &xAPIKey, &bearer} {
		input := ProviderReplacement{ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: APIProtocolMessages, APIKey: &key, Auth: auth}
		normalized, err := NormalizeProviderReplacement(input)
		if err != nil || !sameProviderAuth(normalized.Auth, auth) {
			t.Fatalf("auth %v: got %v, err %v", auth, normalized.Auth, err)
		}
	}
	// A misspelled presentation must not silently keep the protocol convention.
	spaced := ProviderAuth("Bearer ")
	invalid := ProviderReplacement{ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: APIProtocolMessages, APIKey: &key, Auth: &spaced}
	normalized, err := NormalizeProviderReplacement(invalid)
	if err != nil || normalized.Auth == nil || *normalized.Auth != ProviderAuthBearer {
		t.Fatalf("surrounding whitespace was not trimmed: %v %v", normalized.Auth, err)
	}
	boat := ProviderAuth("boat")
	invalid.Auth = &boat
	if _, err := NormalizeProviderReplacement(invalid); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown auth err = %v, want an invalid-argument error", err)
	}
	if _, err := NormalizeProviderUpdate(invalid); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("update unknown auth err = %v, want an invalid-argument error", err)
	}
}

func sameProviderAuth(a, b *ProviderAuth) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Legacy headers carry no explicit intent; only a difference from the protocol
// default is evidence of an override. RPC input does not use this inference.
func TestInferLegacyProviderAuthRecordsOnlyOverrides(t *testing.T) {
	cases := []struct {
		protocol, header, scheme string
		want                     ProviderAuth
	}{
		{protocol: APIProtocolResponses, header: "Authorization", scheme: "Bearer"},
		{protocol: APIProtocolMessages, header: "x-api-key"},
		{protocol: APIProtocolResponses, header: "x-api-key", want: ProviderAuthXAPIKey},
		{protocol: APIProtocolMessages, header: "Authorization", scheme: "Bearer", want: ProviderAuthBearer},
		{protocol: APIProtocolResponses, header: "X-Custom", scheme: "Token"},
	}
	for _, tc := range cases {
		if got := InferLegacyProviderAuth(tc.protocol, tc.header, tc.scheme); got != tc.want {
			t.Fatalf("InferLegacyProviderAuth(%q, %q, %q) = %q, want %q", tc.protocol, tc.header, tc.scheme, got, tc.want)
		}
	}
}
