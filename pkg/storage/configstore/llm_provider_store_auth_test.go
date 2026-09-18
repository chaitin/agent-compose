package configstore

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
)

func providerAuthPtr(auth llms.ProviderAuth) *llms.ProviderAuth {
	return &auth
}

// An update that names the protocol must not silently reset a stored explicit
// credential presentation: the protocol only supplies a default, so an omitted
// auth preserves the operator's override.
func TestIntegrationManagedProviderUpdatePreservesExplicitAuth(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"

	// A bearer presentation on an Anthropic Messages connection is an override:
	// the protocol convention would have sent x-api-key.
	created, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example", Protocol: llms.APIProtocolMessages, APIKey: &key, Auth: providerAuthPtr(llms.ProviderAuthBearer),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.AuthHeader != "Authorization" || created.AuthScheme != "Bearer" || created.Auth != llms.ProviderAuthBearer {
		t.Fatalf("created = %#v, want a stored bearer override", created)
	}

	// Regression: re-sending the same protocol with auth omitted used to reset the
	// presentation to the protocol convention.
	repeated, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: llms.APIProtocolMessages})
	if err != nil {
		t.Fatal(err)
	}
	if repeated.AuthHeader != "Authorization" || repeated.AuthScheme != "Bearer" || repeated.Auth != llms.ProviderAuthBearer {
		t.Fatalf("protocol-only update reset the presentation: %#v", repeated)
	}
	if got := llms.ProviderAuthFromWire(repeated.AuthHeader, repeated.AuthScheme); got != llms.ProviderAuthBearer {
		t.Fatalf("effective presentation = %q, want bearer", got)
	}

	// A protocol change keeps the explicit override even where it coincides with
	// the new convention, so moving back restores the same header.
	changed, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: llms.APIProtocolResponses})
	if err != nil {
		t.Fatal(err)
	}
	if changed.DefaultWireAPI != llms.APIProtocolResponses || changed.AuthHeader != "Authorization" || changed.AuthScheme != "Bearer" || changed.Auth != llms.ProviderAuthBearer {
		t.Fatalf("protocol change dropped the explicit override: %#v", changed)
	}
	restored, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: llms.APIProtocolMessages})
	if err != nil {
		t.Fatal(err)
	}
	if restored.AuthHeader != "Authorization" || restored.AuthScheme != "Bearer" {
		t.Fatalf("override did not survive a protocol round trip: %#v", restored)
	}

	// ...while a connection that only ever used the convention refreshes with it.
	conventional, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "conventional", BaseURL: "https://conventional.example", Protocol: llms.APIProtocolResponses, APIKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}
	if conventional.Auth != "" || conventional.AuthHeader != "Authorization" || conventional.AuthScheme != "Bearer" {
		t.Fatalf("conventional create = %#v, want no stored override", conventional)
	}
	refreshed, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "conventional", Protocol: llms.APIProtocolMessages})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.AuthHeader != "x-api-key" || refreshed.AuthScheme != "" || refreshed.Auth != "" {
		t.Fatalf("protocol change did not refresh the default presentation: %#v", refreshed)
	}

	// Naming the protocol convention is not an override: the header changes but
	// nothing is stored, so a later protocol change still refreshes it.
	namedConvention, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Auth: providerAuthPtr(llms.ProviderAuthXAPIKey)})
	if err != nil {
		t.Fatal(err)
	}
	if namedConvention.Auth != "" || namedConvention.AuthHeader != "x-api-key" || namedConvention.AuthScheme != "" || namedConvention.DefaultWireAPI != llms.APIProtocolMessages {
		t.Fatalf("naming the convention stored an override: %#v", namedConvention)
	}

	// Naming a presentation the protocol does not use is an override, and the
	// update changes only the presentation, not the wire protocol.
	overridden, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Auth: providerAuthPtr(llms.ProviderAuthBearer)})
	if err != nil {
		t.Fatal(err)
	}
	if overridden.AuthHeader != "Authorization" || overridden.AuthScheme != "Bearer" || overridden.DefaultWireAPI != llms.APIProtocolMessages || overridden.Auth != llms.ProviderAuthBearer {
		t.Fatalf("explicit auth only update = %#v", overridden)
	}

	// An explicit empty auth clears the override so the connection follows the
	// protocol convention again.
	cleared, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Auth: providerAuthPtr("")})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Auth != "" || cleared.AuthHeader != "x-api-key" || cleared.AuthScheme != "" {
		t.Fatalf("clearing auth = %#v, want the anthropic_messages convention", cleared)
	}
	followed, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: llms.APIProtocolResponses})
	if err != nil {
		t.Fatal(err)
	}
	if followed.AuthHeader != "Authorization" || followed.AuthScheme != "Bearer" {
		t.Fatalf("cleared connection did not follow the protocol convention: %#v", followed)
	}
}

func TestE2EManagedProviderUpdatePreservesExplicitAuth(t *testing.T) {
	TestIntegrationManagedProviderUpdatePreservesExplicitAuth(t)
}

// Naming the protocol convention is not an override: every write path — the RPC
// create, the migration, and the environment bootstrap — records a presentation
// only when it differs from the protocol in effect. Naming the convention
// therefore leaves the connection following the protocol, while a presentation
// that does differ is stored and survives a later protocol change.
func TestIntegrationManagedProviderExplicitConventionAuthIsNotAnOverride(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"

	// x-api-key is the anthropic_messages convention, so naming it stores no
	// override and a later protocol change refreshes the header.
	conventional, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "conventional", BaseURL: "https://conventional.example", Protocol: llms.APIProtocolMessages, APIKey: &key, Auth: providerAuthPtr(llms.ProviderAuthXAPIKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	if conventional.Auth != "" || conventional.AuthHeader != "x-api-key" || conventional.AuthScheme != "" {
		t.Fatalf("created = %#v, want the convention left unstored", conventional)
	}
	refreshed, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "conventional", Protocol: llms.APIProtocolResponses})
	if err != nil {
		t.Fatal(err)
	}
	if refreshed.Auth != "" || refreshed.AuthHeader != "Authorization" || refreshed.AuthScheme != "Bearer" {
		t.Fatalf("conventional provider did not follow the new protocol: %#v", refreshed)
	}

	// bearer differs from the messages convention, so it is stored and kept
	// across the same protocol change.
	override, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "override", BaseURL: "https://override.example", Protocol: llms.APIProtocolMessages, APIKey: &key, Auth: providerAuthPtr(llms.ProviderAuthBearer),
	})
	if err != nil {
		t.Fatal(err)
	}
	if override.Auth != llms.ProviderAuthBearer || override.AuthHeader != "Authorization" || override.AuthScheme != "Bearer" {
		t.Fatalf("created = %#v, want a stored bearer override", override)
	}
	carried, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "override", Protocol: llms.APIProtocolResponses})
	if err != nil {
		t.Fatal(err)
	}
	if carried.Auth != llms.ProviderAuthBearer || carried.AuthHeader != "Authorization" || carried.AuthScheme != "Bearer" {
		t.Fatalf("stored override was dropped by the protocol change: %#v", carried)
	}
}

func TestE2EManagedProviderExplicitConventionAuthIsNotAnOverride(t *testing.T) {
	TestIntegrationManagedProviderExplicitConventionAuthIsNotAnOverride(t)
}
