package configstore

import (
	"context"
	"database/sql"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// A provider that publishes its own wire api stores it on the provider row
// (default_wire_api) and leaves the model binding's wire_api empty, because a
// binding only adds behavior on top of the provider. Reading that empty value as
// the default protocol turned "this binding declares nothing" into "this binding
// declares responses", which then won over the provider: the catalog reported
// responses for a provider that published chat_completions, and the runtime
// proxy posted a chat-completions model to /v1/responses — a pair the gateway
// rejects because the logical model only serves chat completions.
func TestIntegrationUnsetModelBindingWireAPIPreservesProviderProtocol(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	const providerID = "gateway-unset-binding"
	const modelID = "matrix-chat/deepseek-flash"
	insertCatalogProviderModel(t, ctx, store.DB(), providerID, modelID, "chat_completions", "")

	target, err := resolveProviderTarget(ctx, store, providerID, modelID)
	if err != nil {
		t.Fatalf("resolveProviderTarget: %v", err)
	}
	if target.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("resolved wire api = %q, want the provider's published %q", target.WireAPI, llms.APIProtocolChatCompletions)
	}
	if target.Endpoint != "https://gateway.example/v1/chat/completions" {
		t.Fatalf("resolved endpoint = %q", target.Endpoint)
	}
}

// A binding that does declare a wire api still overrides the provider default.
func TestIntegrationModelBindingWireAPIOverridesProviderProtocol(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	const providerID = "gateway-bound-binding"
	const modelID = "matrix-responses/deepseek-flash"
	insertCatalogProviderModel(t, ctx, store.DB(), providerID, modelID, "chat_completions", "responses")

	target, err := resolveProviderTarget(ctx, store, providerID, modelID)
	if err != nil {
		t.Fatalf("resolveProviderTarget: %v", err)
	}
	if target.WireAPI != llms.APIProtocolResponses {
		t.Fatalf("resolved wire api = %q, want the binding's %q", target.WireAPI, llms.APIProtocolResponses)
	}
}

// insertCatalogProviderModel writes one catalog-owned connection, its logical
// model, and the binding between them. bindingWireAPI is empty when the binding
// declares no protocol of its own.
func insertCatalogProviderModel(t *testing.T, ctx context.Context, db *sql.DB, providerID, modelID, providerWireAPI, bindingWireAPI string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider(
		id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, 'openai', ?, 'https://gateway.example/v1', 'gateway-key', 'Authorization', 'Bearer', '{}', 10, 1, 'catalog', 0, 0)`,
		providerID, providerID, providerWireAPI); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, '', 0, 1, 'catalog', 0, 0)`, modelID, modelID); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider_model(provider_id, model_id, wire_api, weight)
		VALUES(?, ?, ?, 10)`, providerID, modelID, bindingWireAPI); err != nil {
		t.Fatalf("insert model binding: %v", err)
	}
}
