package configstore

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
)

func providerAuthPtr(auth llms.ProviderAuth) *llms.ProviderAuth {
	return &auth
}

func TestIntegrationManagedProviderUpdatePreservesExplicitAuth(t *testing.T) {
	clearLLMTestEnvironment(t)
	for _, tc := range []struct {
		name, protocol, nextProtocol string
		auth                         llms.ProviderAuth
	}{
		{"same family", llms.APIProtocolChatCompletions, llms.APIProtocolResponses, llms.ProviderAuthXAPIKey},
		{"override becomes convention", llms.APIProtocolMessages, llms.APIProtocolResponses, llms.ProviderAuthBearer},
		{"explicit convention", llms.APIProtocolMessages, llms.APIProtocolResponses, llms.ProviderAuthXAPIKey},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := FromDB(newMemoryDB(t))
			if err := store.InitSchema(ctx); err != nil {
				t.Fatal(err)
			}
			key := "gateway-key"
			created, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
				ID: "gateway", BaseURL: "https://gateway.example", Protocol: tc.protocol,
				APIKey: &key, Auth: providerAuthPtr(tc.auth),
			})
			if err != nil {
				t.Fatal(err)
			}
			assertProviderAuth(t, created, tc.auth, tc.auth)
			// Repeated writes and reads must preserve the same explicit choice,
			// even when it matches an intermediate protocol's default.
			for _, protocol := range []string{tc.protocol, tc.nextProtocol, tc.protocol} {
				updated, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: protocol})
				if err != nil {
					t.Fatal(err)
				}
				assertProviderAuth(t, updated, tc.auth, tc.auth)
				if updated.DefaultWireAPI != protocol || updated.APIKey != key {
					t.Fatal("update did not change the protocol or preserve the key")
				}
				stored, err := store.GetManagedLLMProvider(ctx, "gateway")
				if err != nil {
					t.Fatal(err)
				}
				assertProviderAuth(t, stored, tc.auth, tc.auth)
			}
		})
	}
}

func TestIntegrationManagedProviderAuthDefaultAndExplicitUpdates(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"
	created, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example", Protocol: llms.APIProtocolResponses, APIKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}
	assertProviderAuth(t, created, "", llms.ProviderAuthBearer)
	steps := []struct {
		name      string
		input     llms.ProviderReplacement
		intent    llms.ProviderAuth
		effective llms.ProviderAuth
	}{
		{"implicit follows protocol", llms.ProviderReplacement{Protocol: llms.APIProtocolMessages}, "", llms.ProviderAuthXAPIKey},
		{"pin current convention", llms.ProviderReplacement{Auth: providerAuthPtr(llms.ProviderAuthXAPIKey)}, llms.ProviderAuthXAPIKey, llms.ProviderAuthXAPIKey},
		{"change protocol with pinned auth", llms.ProviderReplacement{Protocol: llms.APIProtocolResponses}, llms.ProviderAuthXAPIKey, llms.ProviderAuthXAPIKey},
		{"clear override", llms.ProviderReplacement{Auth: providerAuthPtr("")}, "", llms.ProviderAuthBearer},
		{"cleared follows protocol", llms.ProviderReplacement{Protocol: llms.APIProtocolMessages}, "", llms.ProviderAuthXAPIKey},
		{"replace auth and protocol", llms.ProviderReplacement{Protocol: llms.APIProtocolResponses, Auth: providerAuthPtr(llms.ProviderAuthBearer)}, llms.ProviderAuthBearer, llms.ProviderAuthBearer},
		{"clear and change protocol", llms.ProviderReplacement{Protocol: llms.APIProtocolMessages, Auth: providerAuthPtr("")}, "", llms.ProviderAuthXAPIKey},
	}
	for _, step := range steps {
		step.input.ID = "gateway"
		updated, err := store.UpdateLLMProvider(ctx, step.input)
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		assertProviderAuth(t, updated, step.intent, step.effective)
	}
}

func assertProviderAuth(t *testing.T, provider llms.Provider, intent, effective llms.ProviderAuth) {
	t.Helper()
	if provider.Auth != intent {
		t.Fatalf("stored auth = %q, want %q", provider.Auth, intent)
	}
	if got := llms.ProviderAuthFromWire(provider.AuthHeader, provider.AuthScheme); got != effective {
		t.Fatalf("effective auth = %q, want %q", got, effective)
	}
}

func TestE2EManagedProviderUpdatePreservesExplicitAuth(t *testing.T) {
	TestIntegrationManagedProviderUpdatePreservesExplicitAuth(t)
}
