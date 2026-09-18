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

	// An explicit auth changes only the presentation, not the wire protocol.
	flipped, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Auth: providerAuthPtr(llms.ProviderAuthXAPIKey)})
	if err != nil {
		t.Fatal(err)
	}
	if flipped.AuthHeader != "x-api-key" || flipped.AuthScheme != "" || flipped.DefaultWireAPI != llms.APIProtocolMessages || flipped.Auth != llms.ProviderAuthXAPIKey {
		t.Fatalf("explicit auth only update = %#v", flipped)
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

// The RPC path records the presentation the operator named, so naming the
// protocol convention explicitly is still a stored override. That is deliberate:
// the migration and the environment bootstrap can only infer an override from a
// stored header, and normalizing a named value away would silently drop it the
// next time the protocol changed to one whose convention happens to match.
func TestIntegrationManagedProviderExplicitConventionAuthIsAnOverride(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"

	// x-api-key is the anthropic_messages convention, but the operator named it.
	created, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example", Protocol: llms.APIProtocolMessages, APIKey: &key, Auth: providerAuthPtr(llms.ProviderAuthXAPIKey),
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Auth != llms.ProviderAuthXAPIKey {
		t.Fatalf("created = %#v, want the named presentation recorded", created)
	}

	// Changing the protocol keeps it, so a gateway configured for x-api-key does
	// not silently lose that choice on the way to another protocol.
	changed, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: llms.APIProtocolResponses})
	if err != nil {
		t.Fatal(err)
	}
	if changed.AuthHeader != "x-api-key" || changed.AuthScheme != "" || changed.Auth != llms.ProviderAuthXAPIKey {
		t.Fatalf("named presentation was dropped by the protocol change: %#v", changed)
	}

	// Clearing it returns the connection to the protocol convention.
	cleared, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Auth: providerAuthPtr("")})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.Auth != "" || cleared.AuthHeader != "Authorization" || cleared.AuthScheme != "Bearer" {
		t.Fatalf("cleared provider = %#v, want the responses convention", cleared)
	}
}

func TestE2EManagedProviderExplicitConventionAuthIsAnOverride(t *testing.T) {
	TestIntegrationManagedProviderExplicitConventionAuthIsAnOverride(t)
}
