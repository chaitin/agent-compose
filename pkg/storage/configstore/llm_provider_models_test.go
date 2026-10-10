package configstore

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func declaredModels(specs ...llms.ModelSpec) *[]llms.ModelSpec {
	return &specs
}

func bindingsFor(t *testing.T, ctx context.Context, store *ConfigStore, providerID string) []llms.ProviderModelBinding {
	t.Helper()
	bindings, err := store.ListLLMProviderModelConfigs(ctx)
	if err != nil {
		t.Fatalf("list provider model bindings: %v", err)
	}
	var owned []llms.ProviderModelBinding
	for _, binding := range bindings {
		if binding.ProviderID == providerID {
			owned = append(owned, binding)
		}
	}
	return owned
}

// A connection may declare the model names an operator types, so the UI can
// configure a provider without depending on what the endpoint advertises.
func TestIntegrationProviderDeclaredModelsRoundTrip(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"
	if _, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key,
		Models: declaredModels(
			llms.ModelSpec{ID: "gpt-4o", Name: "GPT-4o", Headers: map[string]string{"x-tier": "batch"}},
			llms.ModelSpec{ID: "gpt-4o-mini", Protocol: llms.APIProtocolChatCompletions, MaxOutputTokens: 4096},
		),
	}); err != nil {
		t.Fatalf("create provider with models: %v", err)
	}

	bindings := bindingsFor(t, ctx, store, "gateway")
	if len(bindings) != 2 {
		t.Fatalf("bindings = %#v, want two declared models", bindings)
	}
	inherited, override := bindings[0], bindings[1]
	if inherited.ModelID != "gpt-4o" || inherited.Config.DisplayName != "GPT-4o" || inherited.Config.HeadersJSON != `{"x-tier":"batch"}` {
		t.Fatalf("inherited binding = %#v", inherited)
	}
	// An unset model protocol stays unset, so the target builder inherits the
	// connection protocol rather than fabricating responses.
	if inherited.Config.WireAPI != "" {
		t.Fatalf("inherited wire api = %q, want unset", inherited.Config.WireAPI)
	}
	if override.ModelID != "gpt-4o-mini" || override.Config.WireAPI != llms.APIProtocolChatCompletions || override.Config.MaxOutputTokens != 4096 {
		t.Fatalf("override binding = %#v", override)
	}

	// The declared names are routable immediately, with the per-model protocol
	// and cap taking effect.
	target, err := resolveProviderTarget(ctx, store, "gateway", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("resolve declared model: %v", err)
	}
	if target.WireAPI != llms.APIProtocolChatCompletions || target.MaxOutputTokens != 4096 {
		t.Fatalf("declared model target = %#v", target)
	}
	inheritedTarget, err := resolveProviderTarget(ctx, store, "gateway", "gpt-4o")
	if err != nil {
		t.Fatalf("resolve inherited model: %v", err)
	}
	if inheritedTarget.WireAPI != llms.APIProtocolResponses || inheritedTarget.Headers.Get("x-tier") != "batch" {
		t.Fatalf("inherited model target = %#v", inheritedTarget)
	}
}

// The spec's model set follows the same presence rule as its optional scalars:
// absent preserves, present empty clears.
func TestIntegrationProviderModelSetPresence(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"
	create := llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key,
		Models: declaredModels(llms.ModelSpec{ID: "gpt-4o"}),
	}
	if _, err := store.CreateLLMProvider(ctx, create); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Name: ""}); err != nil {
		t.Fatalf("update without models: %v", err)
	}
	if got := bindingsFor(t, ctx, store, "gateway"); len(got) != 1 || got[0].ModelID != "gpt-4o" {
		t.Fatalf("bindings after update without models = %#v, want the declared set preserved", got)
	}
	if _, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Models: declaredModels()}); err != nil {
		t.Fatalf("update with an empty model set: %v", err)
	}
	if got := bindingsFor(t, ctx, store, "gateway"); len(got) != 0 {
		t.Fatalf("bindings after clearing = %#v, want none", got)
	}
}

// A model the operator declares must stay in the connection's protocol family,
// because one connection speaks one family's dialect.
func TestIntegrationProviderDeclaredModelFamilyIsEnforced(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"
	_, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key,
		Models: declaredModels(llms.ModelSpec{ID: "claude-3-5-sonnet", Protocol: llms.APIProtocolMessages}),
	})
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("create with a cross-family model error = %v, want invalid argument", err)
	}
	if got := bindingsFor(t, ctx, store, "gateway"); len(got) != 0 {
		t.Fatalf("bindings after a rejected create = %#v, want none", got)
	}
}

// A model's explicit protocol outranks the connection protocol, so an update
// that changes families while keeping such a binding would resolve requests to
// an endpoint and protocol that disagree. The retained set is revalidated, and
// the operator restates it to state the new intent.
func TestIntegrationProtocolChangeRevalidatesRetainedModels(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	key := "gateway-key"
	if _, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key,
		Models: declaredModels(
			llms.ModelSpec{ID: "gpt-4o", Protocol: llms.APIProtocolChatCompletions},
			llms.ModelSpec{ID: "gpt-4o-mini"},
		),
	}); err != nil {
		t.Fatalf("create: %v", err)
	}

	// A same-family move keeps every retained binding valid, including the
	// model-level override.
	if _, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: llms.APIProtocolChatCompletions}); err != nil {
		t.Fatalf("same-family update: %v", err)
	}
	override, err := resolveProviderTarget(ctx, store, "gateway", "gpt-4o")
	if err != nil {
		t.Fatalf("resolve a retained override: %v", err)
	}
	if override.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("retained override protocol = %q", override.WireAPI)
	}
	inherited, err := resolveProviderTarget(ctx, store, "gateway", "gpt-4o-mini")
	if err != nil {
		t.Fatalf("resolve a retained inherited model: %v", err)
	}
	if inherited.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("a retained model did not inherit the new connection protocol: %q", inherited.WireAPI)
	}

	// Moving to the other family while keeping the chat-completions override is
	// rejected, and the rejected update leaves the stored state alone.
	if _, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{ID: "gateway", Protocol: llms.APIProtocolMessages}); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("cross-family update keeping a model error = %v, want invalid argument", err)
	}
	stored, err := store.GetManagedLLMProvider(ctx, "gateway")
	if err != nil {
		t.Fatalf("read the provider after a rejected update: %v", err)
	}
	if stored.DefaultWireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("a rejected update changed the protocol to %q", stored.DefaultWireAPI)
	}
	if got := bindingsFor(t, ctx, store, "gateway"); len(got) != 2 {
		t.Fatalf("bindings after a rejected update = %#v, want both retained", got)
	}

	// Restating the set states the new intent, and the resolved target is
	// coherent under the new protocol.
	if _, err := store.UpdateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", Protocol: llms.APIProtocolMessages,
		Models: declaredModels(llms.ModelSpec{ID: "claude-3-5-sonnet"}),
	}); err != nil {
		t.Fatalf("cross-family update restating models: %v", err)
	}
	restated, err := resolveProviderTarget(ctx, store, "gateway", "claude-3-5-sonnet")
	if err != nil {
		t.Fatalf("resolve a restated model: %v", err)
	}
	if restated.WireAPI != llms.APIProtocolMessages || !strings.HasSuffix(restated.Endpoint, "/v1/messages") {
		t.Fatalf("restated target = %#v, want the anthropic protocol and endpoint", restated)
	}
}

// Two connections may declare the same model ID. The identity row belongs to
// whoever created it first, so a later declaration adds a binding without
// rewriting another source's model.
func TestIntegrationDeclaredModelKeepsAnotherOwnersIdentity(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	baseURL, protocol, apiKey := "https://catalog.example/v1", llms.APIProtocolResponses, "catalog-key"
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{
		"catalog": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey, Models: []llms.CatalogModel{{ID: "shared-model"}}},
	}}); err != nil {
		t.Fatalf("apply model catalog: %v", err)
	}
	key := "gateway-key"
	if _, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "gateway", BaseURL: "https://gateway.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key,
		Models: declaredModels(llms.ModelSpec{ID: "shared-model"}),
	}); err != nil {
		t.Fatalf("create provider declaring a shared model: %v", err)
	}

	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scopes := map[string]string{}
	for _, model := range models {
		scopes[model.ID] = model.Scope
	}
	if scopes["shared-model"] != llms.ProviderScopeCatalog {
		t.Fatalf("shared model scope = %q, want the catalog to keep its identity", scopes["shared-model"])
	}
	// Both connections serve it, so the catalog sees two candidates rather than
	// one connection losing its binding.
	bindings := bindingsFor(t, ctx, store, "gateway")
	if len(bindings) != 1 || bindings[0].ModelID != "shared-model" {
		t.Fatalf("gateway bindings = %#v", bindings)
	}
	if got := bindingsFor(t, ctx, store, "catalog"); len(got) != 1 {
		t.Fatalf("catalog bindings = %#v", got)
	}
}
