package api

import (
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// The wire presentation is not implied by the protocol, so the transport must
// carry it both ways and reject a value it cannot name.
func TestProviderAuthMapping(t *testing.T) {
	cases := []struct {
		name string
		in   *agentcomposev2.LLMProviderAuth
		want llms.ProviderAuth
	}{
		{name: "absent keeps the protocol convention"},
		{name: "explicitly unspecified keeps the protocol convention", in: authPtr(agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_UNSPECIFIED)},
		{name: "x-api-key", in: authPtr(agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY), want: llms.ProviderAuthXAPIKey},
		{name: "bearer", in: authPtr(agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER), want: llms.ProviderAuthBearer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := providerAuthFromV2(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if got != tc.want {
				t.Fatalf("providerAuthFromV2 = %q, want %q", got, tc.want)
			}
		})
	}
	unknown := agentcomposev2.LLMProviderAuth(99)
	if _, err := providerAuthFromV2(&unknown); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("unknown enum err = %v, want an invalid-argument error", err)
	}
	for _, auth := range []llms.ProviderAuth{llms.ProviderAuthXAPIKey, llms.ProviderAuthBearer} {
		got, err := providerAuthFromV2(authPtr(providerAuthToV2(auth)))
		if err != nil {
			t.Fatal(err)
		}
		if got != auth {
			t.Fatalf("round trip of %q = %q", auth, got)
		}
	}
}

// A provider reports the presentation it will actually use, including the
// protocol convention, so an operator can see why an upstream rejects a key.
func TestProviderToV2ReportsEffectiveAuth(t *testing.T) {
	messages := providerToV2(llms.Provider{ID: "messages", DefaultWireAPI: llms.APIProtocolMessages, AuthHeader: "x-api-key"})
	if messages.GetAuth() != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY {
		t.Fatalf("messages auth = %v", messages.GetAuth())
	}
	bearer := providerToV2(llms.Provider{ID: "bearer", DefaultWireAPI: llms.APIProtocolMessages, AuthHeader: "Authorization", AuthScheme: "Bearer"})
	if bearer.GetAuth() != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER {
		t.Fatalf("bearer auth = %v", bearer.GetAuth())
	}
	unknown := providerToV2(llms.Provider{ID: "custom", AuthHeader: "x-custom", AuthScheme: "Token"})
	if unknown.GetAuth() != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_UNSPECIFIED {
		t.Fatalf("unknown presentation = %v", unknown.GetAuth())
	}
}

func authPtr(auth agentcomposev2.LLMProviderAuth) *agentcomposev2.LLMProviderAuth {
	return &auth
}
