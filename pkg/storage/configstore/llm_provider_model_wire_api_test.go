package configstore

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// A provider that publishes its own wire api stores it on the provider row
// (default_wire_api) and leaves the model binding's wire_api empty, because a
// binding only adds behavior on top of the provider. Reading that empty value as
// the default protocol turned "this binding declares nothing" into "this binding
// declares responses", which then won over the provider: the resolver reported
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
	db := store.DB()
	const providerID = "session-env:sandbox:openai"
	const modelID = "matrix-chat/deepseek-flash"
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider(
		id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, 'openai', 'chat_completions', 'https://gateway.example/v1', 'gateway-key', 'Authorization', 'Bearer', '{}', 10, 1, 'session_env', 0, 0)`,
		providerID, providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, '', 0, 1, 'session_env', 0, 0)`, modelID, modelID); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider_model(provider_id, model_id, wire_api, weight)
		VALUES(?, ?, '', 10)`, providerID, modelID); err != nil {
		t.Fatalf("insert model binding: %v", err)
	}

	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		RequestedModel: modelID,
		ProviderID:     providerID,
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv: %v", err)
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
	db := store.DB()
	const providerID = "session-env:bound:openai"
	const modelID = "matrix-responses/deepseek-flash"
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider(
		id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, 'openai', 'chat_completions', 'https://gateway.example/v1', 'gateway-key', 'Authorization', 'Bearer', '{}', 10, 1, 'session_env', 0, 0)`,
		providerID, providerID); err != nil {
		t.Fatalf("insert provider: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, '', 0, 1, 'session_env', 0, 0)`, modelID, modelID); err != nil {
		t.Fatalf("insert model: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider_model(provider_id, model_id, wire_api, weight)
		VALUES(?, ?, 'responses', 10)`, providerID, modelID); err != nil {
		t.Fatalf("insert model binding: %v", err)
	}

	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		RequestedModel: modelID,
		ProviderID:     providerID,
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv: %v", err)
	}
	if target.WireAPI != llms.APIProtocolResponses {
		t.Fatalf("resolved wire api = %q, want the binding's %q", target.WireAPI, llms.APIProtocolResponses)
	}
}
