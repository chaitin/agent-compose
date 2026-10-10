package configstore

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// The daemon environment is a bootstrap fallback, so it must not take over an id
// models.json already declared. Before the ownership guard the projection
// rewrote the catalog row's scope to env_default and replaced its connection
// details, silently discarding the operator's models.json connection.
func TestIntegrationEnvProjectionKeepsCatalogOwnedProvider(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	insertCatalogProviderModel(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI, "catalog-model", "chat_completions", "")

	err := store.UpsertDefaultLLMConfig(ctx, envProjectionProvider(llms.ProviderIDDefaultOpenAI), envProjectionModel("env-model"))
	conflict, ok := llms.AsDefaultConfigConflict(err)
	if !ok {
		t.Fatalf("UpsertDefaultLLMConfig error = %v, want a default config conflict", err)
	}
	if conflict.Kind != "provider" || conflict.ID != llms.ProviderIDDefaultOpenAI || conflict.Scope != llms.ProviderScopeCatalog {
		t.Fatalf("conflict = %#v", conflict)
	}
	assertProviderOwnership(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI, llms.ProviderScopeCatalog, "https://gateway.example/v1")
	if count := countProviderModelBindings(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI); count != 1 {
		t.Fatalf("bindings = %d, want the catalog binding only", count)
	}
}

// A same-id model is also an ownership boundary: an environment run must not
// flip the default flag or the scope of a model models.json declared.
func TestIntegrationEnvProjectionKeepsCatalogOwnedModel(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	insertEnvScopedProvider(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI)
	insertCatalogOwnedModel(t, ctx, store.DB(), "shared-model")

	err := store.UpsertDefaultLLMConfig(ctx, envProjectionProvider(llms.ProviderIDDefaultOpenAI), envProjectionModel("shared-model"))
	conflict, ok := llms.AsDefaultConfigConflict(err)
	if !ok {
		t.Fatalf("UpsertDefaultLLMConfig error = %v, want a default config conflict", err)
	}
	if conflict.Kind != "model" || conflict.ID != "shared-model" || conflict.Scope != llms.ProviderScopeCatalog {
		t.Fatalf("conflict = %#v", conflict)
	}
	var scope string
	var defaultModel int
	if err := store.DB().QueryRowContext(ctx, `SELECT scope, default_model FROM llm_model WHERE id = ?`, "shared-model").Scan(&scope, &defaultModel); err != nil {
		t.Fatal(err)
	}
	if scope != llms.ProviderScopeCatalog || defaultModel != 0 {
		t.Fatalf("catalog model scope = %q default = %d, want it untouched", scope, defaultModel)
	}
}

// The projection still refreshes the rows it owns, so restarting the daemon with
// changed environment values keeps working.
func TestIntegrationEnvProjectionUpdatesItsOwnRows(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	provider := envProjectionProvider(llms.ProviderIDDefaultOpenAI)
	if err := store.UpsertDefaultLLMConfig(ctx, provider, envProjectionModel("env-model")); err != nil {
		t.Fatalf("first projection: %v", err)
	}
	provider.BaseURL = "https://rotated.example/v1"
	if err := store.UpsertDefaultLLMConfig(ctx, provider, envProjectionModel("env-model")); err != nil {
		t.Fatalf("second projection: %v", err)
	}
	assertProviderOwnership(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI, llms.ProviderScopeEnvDefault, "https://rotated.example/v1")
}

func newProjectionTestStore(t *testing.T) *ConfigStore {
	t.Helper()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(context.Background()); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	return store
}

func envProjectionProvider(id string) llms.Provider {
	return llms.Provider{
		ID:             id,
		Name:           "openai",
		ProviderType:   llms.ProviderFamilyOpenAI,
		DefaultWireAPI: llms.APIProtocolResponses,
		BaseURL:        "https://env.example/v1",
		APIKey:         "env-key",
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         10,
		Enabled:        true,
		Scope:          llms.ProviderScopeEnvDefault,
	}
}

func envProjectionModel(id string) llms.Model {
	return llms.Model{ID: id, Name: id, DefaultModel: true, Enabled: true, Scope: llms.ProviderScopeEnvDefault}
}

func insertEnvScopedProvider(t *testing.T, ctx context.Context, db *sql.DB, providerID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_provider(
		id, name, provider_type, default_wire_api, base_url, api_key, auth_header, auth_scheme, headers_json, weight, enabled, scope, created_at, updated_at)
		VALUES(?, ?, 'openai', 'responses', 'https://env.example/v1', 'env-key', 'Authorization', 'Bearer', '{}', 10, 1, 'env_default', 0, 0)`,
		providerID, providerID); err != nil {
		t.Fatalf("insert env provider: %v", err)
	}
}

func insertCatalogOwnedModel(t *testing.T, ctx context.Context, db *sql.DB, modelID string) {
	t.Helper()
	if _, err := db.ExecContext(ctx, `INSERT INTO llm_model(id, name, description, default_model, enabled, scope, created_at, updated_at)
		VALUES(?, ?, '', 0, 1, 'catalog', 0, 0)`, modelID, modelID); err != nil {
		t.Fatalf("insert catalog model: %v", err)
	}
}

func assertProviderOwnership(t *testing.T, ctx context.Context, db *sql.DB, providerID, wantScope, wantBaseURL string) {
	t.Helper()
	var scope, baseURL string
	if err := db.QueryRowContext(ctx, `SELECT scope, base_url FROM llm_provider WHERE id = ?`, providerID).Scan(&scope, &baseURL); err != nil {
		t.Fatal(err)
	}
	if scope != wantScope || baseURL != wantBaseURL {
		t.Fatalf("provider %q scope = %q base_url = %q, want %q/%q", providerID, scope, baseURL, wantScope, wantBaseURL)
	}
}

func countProviderModelBindings(t *testing.T, ctx context.Context, db *sql.DB, providerID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(1) FROM llm_provider_model WHERE provider_id = ?`, providerID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// Guard the sentinel contract the startup boundary relies on for errors.Is.
func TestDefaultConfigConflictIsClassifiable(t *testing.T) {
	wrapped := errors.Join(errors.New("outer"), &llms.DefaultConfigConflict{Kind: "provider", ID: "default", Scope: "catalog"})
	if !errors.Is(wrapped, llms.ErrDefaultConfigConflict) {
		t.Fatal("wrapped conflict does not match the sentinel")
	}
	if _, ok := llms.AsDefaultConfigConflict(wrapped); !ok {
		t.Fatal("AsDefaultConfigConflict did not extract the conflict")
	}
}
