package api

import (
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// The spec's optional presence distinguishes "leave it alone" from "clear it", so
// the transport must carry absent, explicitly-unspecified, and each named
// presentation without collapsing them.
func TestProviderAuthMapping(t *testing.T) {
	cleared := llms.ProviderAuth("")
	xAPIKey, bearer := llms.ProviderAuthXAPIKey, llms.ProviderAuthBearer
	cases := []struct {
		name string
		in   *agentcomposev2.LLMProviderAuth
		want *llms.ProviderAuth
	}{
		{name: "absent keeps the stored override"},
		{name: "explicitly unspecified clears the override", in: authPtr(agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_UNSPECIFIED), want: &cleared},
		{name: "x-api-key", in: authPtr(agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY), want: &xAPIKey},
		{name: "bearer", in: authPtr(agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER), want: &bearer},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := providerAuthFromV2(tc.in)
			if err != nil {
				t.Fatal(err)
			}
			if !sameProviderAuthPtr(got, tc.want) {
				t.Fatalf("providerAuthFromV2 = %v, want %v", got, tc.want)
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
		if got == nil || *got != auth {
			t.Fatalf("round trip of %q = %v", auth, got)
		}
	}
}

// A response reports the stored override rather than the effective header, so a
// client that writes the object back cannot turn the protocol convention into an
// override.
func TestProviderToV2ReportsStoredAuthOverride(t *testing.T) {
	convention := providerToV2(llms.Provider{ID: "messages", DefaultWireAPI: llms.APIProtocolMessages, AuthHeader: "x-api-key"})
	if convention.GetAuth() != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_UNSPECIFIED {
		t.Fatalf("connection following the convention reported auth = %v", convention.GetAuth())
	}
	bearer := providerToV2(llms.Provider{ID: "bearer", DefaultWireAPI: llms.APIProtocolMessages, Auth: llms.ProviderAuthBearer})
	if bearer.GetAuth() != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER {
		t.Fatalf("bearer override reported auth = %v", bearer.GetAuth())
	}
	xAPIKey := providerToV2(llms.Provider{ID: "x-api-key", DefaultWireAPI: llms.APIProtocolResponses, Auth: llms.ProviderAuthXAPIKey})
	if xAPIKey.GetAuth() != agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY {
		t.Fatalf("x-api-key override reported auth = %v", xAPIKey.GetAuth())
	}
}

func sameProviderAuthPtr(a, b *llms.ProviderAuth) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func authPtr(auth agentcomposev2.LLMProviderAuth) *agentcomposev2.LLMProviderAuth {
	return &auth
}
