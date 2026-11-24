package configstore

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
)

func TestIntegrationLoadApplyAndResolveModelCatalogProtocolOverrides(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), llms.ModelsCatalogFilename)
	data := `{"providers":{"gateway":{"baseUrl":"https://gateway.example/v1","protocol":"responses","apiKey":"catalog-key","models":[{"id":"chat-model","protocol":"chat_completions"}]}}}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}

	catalog, err := llms.LoadModelCatalog(path, nil)
	if err != nil {
		t.Fatalf("LoadModelCatalog: %v", err)
	}
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.ApplyModelCatalog(ctx, catalog); err != nil {
		t.Fatalf("ApplyModelCatalog: %v", err)
	}

	target, err := resolveCatalogModel(ctx, store, "chat-model")
	if err != nil {
		t.Fatalf("resolveCatalogModel: %v", err)
	}
	if target.Provider.ID != "gateway" || target.Model.ID != "chat-model" || target.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("resolved target = %#v", target)
	}
	if target.Endpoint != "https://gateway.example/v1/chat/completions" || target.Headers.Get("Authorization") != "Bearer catalog-key" {
		t.Fatalf("resolved transport = endpoint %q headers %#v", target.Endpoint, target.Headers)
	}

	invalidPath := filepath.Join(t.TempDir(), llms.ModelsCatalogFilename)
	invalidData := `{"providers":{"gateway":{"baseUrl":"https://gateway.example/v1","protocol":"responses","models":[{"id":"anthropic-model","protocol":"anthropic_messages"}]}}}`
	if err := os.WriteFile(invalidPath, []byte(invalidData), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := llms.LoadModelCatalog(invalidPath, nil); err == nil || !strings.Contains(err.Error(), "incompatible with provider family") {
		t.Fatalf("LoadModelCatalog cross-family error = %v", err)
	}
}

func TestIntegrationApplyModelCatalogResolvesLiteralModelsDefaultsAndBehavior(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	baseURL, protocol, apiKey, limit := "https://gateway.example/api/openai", llms.APIProtocolChatCompletions, "catalog-key", 99999
	window := 128_000
	catalog := llms.ModelCatalog{Default: llms.CatalogDefault("baizhi/deepseek-v4-flash"), Providers: map[string]llms.CatalogProvider{
		"baizhi": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey, Models: []llms.CatalogModel{{ID: "deepseek-v4-flash", MaxOutputTokens: &limit, ContextWindow: &window}}},
	}}
	if err := store.ApplyModelCatalog(ctx, catalog); err != nil {
		t.Fatalf("ApplyModelCatalog: %v", err)
	}

	providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx)
	if err != nil || !ok || providerID != "baizhi" || modelID != "deepseek-v4-flash" {
		t.Fatalf("default = %q/%q ok=%v err=%v", providerID, modelID, ok, err)
	}
	literal, err := resolveCatalogModel(ctx, store, "upstream-only-model")
	if err != nil {
		t.Fatalf("resolve literal model: %v", err)
	}
	if literal.Model.ID != "upstream-only-model" || literal.MaxOutputTokens != 0 || literal.ContextWindow != 0 {
		t.Fatalf("literal target = %#v", literal)
	}
	configured, err := resolveCatalogModel(ctx, store, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("resolve configured model: %v", err)
	}
	if configured.MaxOutputTokens != limit || configured.ContextWindow != window || configured.Provider.APIKey != apiKey {
		t.Fatalf("configured target = %#v", configured)
	}
	defaultTarget, err := resolveCatalogModel(ctx, store, "")
	if err != nil {
		t.Fatalf("resolve catalog default: %v", err)
	}
	if defaultTarget.Provider.ID != "baizhi" || defaultTarget.Model.ID != "deepseek-v4-flash" {
		t.Fatalf("catalog default target = %#v", defaultTarget)
	}
	window = 0
	if err := store.ApplyModelCatalog(ctx, catalog); err != nil {
		t.Fatal(err)
	}
	cleared, err := resolveCatalogModel(ctx, store, "deepseek-v4-flash")
	if err != nil || cleared.ContextWindow != 0 || cleared.MaxOutputTokens != limit {
		t.Fatalf("cleared catalog target = %#v, err = %v", cleared, err)
	}
}

// resolveCatalogModel resolves one opaque model id through the catalog snapshot
// the store feeds, mirroring the request path's SelectModel + Resolve pair.
func resolveCatalogModel(ctx context.Context, store *ConfigStore, requested string) (llms.ResolvedTarget, error) {
	snapshot, err := llms.LoadCatalog(ctx, store)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	model, err := snapshot.SelectModel(requested)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	return snapshot.Resolve("", model, llms.DefaultProtocolPreference())
}

func TestIntegrationApplyEmptyModelCatalogClearsCatalogOwnedStateButKeepsTheStoredDefault(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	legacyProvider := llms.Provider{
		ID: "legacy", Name: "legacy", ProviderType: llms.ProviderFamilyOpenAI,
		BaseURL: "https://legacy.example/v1", APIKey: "legacy-key", Scope: llms.ProviderScopeSystem,
	}
	legacyModel := llms.Model{ID: "legacy-model", Name: "legacy-model", DefaultModel: true, Scope: llms.ProviderScopeSystem}
	if err := store.UpsertDefaultLLMConfig(ctx, legacyProvider, legacyModel); err != nil {
		t.Fatalf("store legacy default: %v", err)
	}

	baseURL, protocol, apiKey := "https://catalog.example/v1", llms.APIProtocolResponses, "catalog-key"
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
		Default: llms.CatalogDefault("catalog/catalog-model"),
		Providers: map[string]llms.CatalogProvider{
			"catalog": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey},
		},
	}); err != nil {
		t.Fatalf("apply populated catalog: %v", err)
	}
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{}}); err != nil {
		t.Fatalf("apply empty catalog: %v", err)
	}

	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].ID != legacyProvider.ID || providers[0].Scope != llms.ProviderScopeSystem {
		t.Fatalf("enabled providers = %#v, want unchanged legacy provider", providers)
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != legacyModel.ID || !models[0].DefaultModel || models[0].Scope != llms.ProviderScopeSystem {
		t.Fatalf("enabled models = %#v, want unchanged legacy default", models)
	}
	// A models.json that declares no default leaves the stored reference alone:
	// removing a declaration must not silently wipe an operator's choice.
	if providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx); err != nil || !ok || providerID != "catalog" || modelID != "catalog-model" {
		t.Fatalf("catalog default = %q/%q ok=%v err=%v, want the stored reference preserved", providerID, modelID, ok, err)
	}
	// The preserved reference now names a connection the empty catalog removed,
	// so resolution ignores it and the environment default takes over.
	resolved, err := resolveCatalogModel(ctx, store, "")
	if err != nil {
		t.Fatalf("resolve after empty catalog: %v", err)
	}
	if resolved.Provider.ID != legacyProvider.ID || resolved.Model.ID != legacyModel.ID {
		t.Fatalf("resolved default = %#v, want the legacy environment default", resolved)
	}
}

// A connection the file no longer declares is released: its rows stay (an
// operator's stored default references the provider row with ON DELETE CASCADE,
// and a later file may restore the declaration) but they lose the catalog scope,
// so the id is no longer owned by a source that will never rewrite the row again.
func TestIntegrationModelCatalogReleasesWhatTheFileNoLongerDeclares(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	declared := catalogWithCredential("gateway", "gpt-4o")
	declared.Default = llms.CatalogDefault("gateway/gpt-4o")
	if err := store.ApplyModelCatalog(ctx, declared); err != nil {
		t.Fatalf("declare the connection: %v", err)
	}
	if count := countProviderModelBindings(t, ctx, store.DB(), "gateway"); count != 1 {
		t.Fatalf("bindings after declaring = %d, want 1", count)
	}

	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{}); err != nil {
		t.Fatalf("drop the connection: %v", err)
	}
	if providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx); err != nil || !ok || providerID != "gateway" || modelID != "gpt-4o" {
		t.Fatalf("catalog default = %q/%q ok=%v err=%v, want the stored reference preserved", providerID, modelID, ok, err)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 0 {
		t.Fatalf("enabled providers = %#v, want the released connection to serve nothing", providers)
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 0 {
		t.Fatalf("enabled models = %#v, want the released model to serve nothing", models)
	}
	assertReleased(t, ctx, store.DB(), "llm_provider", "gateway")
	assertReleased(t, ctx, store.DB(), "llm_model", "gpt-4o")

	// Declaring the connection again takes the released rows back, which is what
	// keeping them instead of deleting them promises.
	if err := store.ApplyModelCatalog(ctx, declared); err != nil {
		t.Fatalf("re-declare the connection: %v", err)
	}
	assertProviderOwnership(t, ctx, store.DB(), "gateway", llms.ProviderScopeCatalog, "https://catalog.example/v1")
	resolved, err := resolveCatalogModel(ctx, store, "")
	if err != nil {
		t.Fatalf("resolve the restored default: %v", err)
	}
	if resolved.Provider.ID != "gateway" || resolved.Model.ID != "gpt-4o" {
		t.Fatalf("resolved default = %#v, want the restored catalog connection", resolved)
	}
}

// assertReleased checks that a row the file projection stopped declaring kept its
// data but gave up the scope that made it an owner.
// A file that stops declaring a model must not leave an API-owned identity
// behind. The identity is what tells the environment bootstrap that an id already
// has an owner, so a row no binding and no default reference names any more would
// make a later LLM_MODEL yield its whole projection.
func TestIntegrationModelCatalogReleaseSweepsAnIdentityOnlyItsBindingKeptAlive(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)
	key := "api-key"

	// An API connection declares the model, and the file binds the same id to a
	// connection of its own.
	if _, err := store.CreateLLMProvider(ctx, llms.ProviderReplacement{
		ID: "api-gateway", BaseURL: "https://api.example/v1", Protocol: llms.APIProtocolResponses, APIKey: &key,
		Models: declaredModels(llms.ModelSpec{ID: "shared-model"}),
	}); err != nil {
		t.Fatalf("create the api connection: %v", err)
	}
	if err := store.ApplyModelCatalog(ctx, catalogWithCredential("file-gateway", "shared-model")); err != nil {
		t.Fatalf("declare the file connection: %v", err)
	}

	// The API connection goes away. Its model identity stays because the file still
	// binds the id, which is what keeps the environment bootstrap out of the way.
	if err := store.DeleteLLMProvider(ctx, "api-gateway"); err != nil {
		t.Fatalf("delete the api connection: %v", err)
	}
	if count := countModelIdentities(t, ctx, store.DB(), "shared-model"); count != 1 {
		t.Fatalf("model identities after the deletion = %d, want the file binding to keep the identity", count)
	}

	// The file drops the model too, so nothing references the identity any more.
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{}); err != nil {
		t.Fatalf("drop the file model: %v", err)
	}
	if count := countModelIdentities(t, ctx, store.DB(), "shared-model"); count != 0 {
		t.Fatalf("model identities after the release = %d, want the unreferenced api identity swept", count)
	}
}

func countModelIdentities(t *testing.T, ctx context.Context, db *sql.DB, modelID string) int {
	t.Helper()
	var count int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM llm_model WHERE id = ?`, modelID).Scan(&count); err != nil {
		t.Fatalf("count model identities for %q: %v", modelID, err)
	}
	return count
}

func assertReleased(t *testing.T, ctx context.Context, db *sql.DB, table, id string) {
	t.Helper()
	var scope string
	var enabled int
	if err := db.QueryRowContext(ctx, `SELECT scope, enabled FROM `+table+` WHERE id = ?`, id).Scan(&scope, &enabled); err != nil {
		t.Fatalf("read released %s %q: %v", table, id, err)
	}
	if scope != "" || enabled != 0 {
		t.Fatalf("%s %q scope = %q enabled = %d, want a released disabled row", table, id, scope, enabled)
	}
}

// An id the environment bootstrap materialized is not another source's to keep.
// The file is the daemon's own declaration and outranks the bootstrap fallback,
// so declaring the same Provider id takes the row over; the environment
// projection that follows yields instead of failing startup beside it.
func TestIntegrationModelCatalogTakesOverEnvironmentOwnedProvider(t *testing.T) {
	clearLLMTestEnvironment(t)
	for _, scope := range []string{llms.ProviderScopeSystem, llms.ProviderScopeEnvDefault} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			store := newProjectionTestStore(t)

			projected := envProjectionProvider(llms.ProviderIDDefaultOpenAI)
			projected.Scope = scope
			projected.Name = scope + " provider"
			if err := store.UpsertDefaultLLMConfig(ctx, projected, envProjectionModel("env-model")); err != nil {
				t.Fatalf("project the environment: %v", err)
			}

			if err := store.ApplyModelCatalog(ctx, catalogWithCredential(llms.ProviderIDDefaultOpenAI, "file-model")); err != nil {
				t.Fatalf("the file declared an id the environment materialized: %v", err)
			}
			assertProviderOwnership(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI, llms.ProviderScopeCatalog, "https://catalog.example/v1")

			// The environment's model kept its default flag but lost the connection
			// that served it, so the catalog must report no default rather than a
			// model no run can receive.
			loaded, err := llms.LoadCatalog(ctx, store)
			if err != nil {
				t.Fatalf("load catalog: %v", err)
			}
			if got := loaded.DefaultModel(); got != "" {
				t.Fatalf("DefaultModel() = %q, want no default after the file took the connection over", got)
			}

			err = store.UpsertDefaultLLMConfig(ctx, envProjectionProvider(llms.ProviderIDDefaultOpenAI), envProjectionModel("env-model"))
			if _, ok := llms.AsDefaultConfigConflict(err); !ok {
				t.Fatalf("environment projection error = %v, want it to yield to the file that owns the id", err)
			}
		})
	}
}

// The model id is the other half of the same ownership rule: an identity the
// environment bootstrap materialized is reclaimed by the file that declares it,
// exactly as a Provider id is. While the bootstrap kept owning the identity, the
// environment projection could not see the file's declaration, materialized a
// second connection for the same model, and connection selection picked between
// the two peers at random.
func TestIntegrationModelCatalogReclaimsTheModelIdentityTheEnvironmentMaterialized(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	if err := store.UpsertDefaultLLMConfig(ctx, envProjectionProvider(llms.ProviderIDDefaultOpenAI), envProjectionModel("shared-model")); err != nil {
		t.Fatalf("project the environment: %v", err)
	}
	// The file declares the same model id under a connection of its own.
	if err := store.ApplyModelCatalog(ctx, catalogWithCredential("gateway", "shared-model")); err != nil {
		t.Fatalf("the file declared a model id the environment materialized: %v", err)
	}

	var scope string
	if err := store.DB().QueryRowContext(ctx, `SELECT scope FROM llm_model WHERE id = ?`, "shared-model").Scan(&scope); err != nil {
		t.Fatal(err)
	}
	if scope != llms.ProviderScopeCatalog {
		t.Fatalf("model identity scope = %q, want the file to own the id it declares", scope)
	}

	err := store.UpsertDefaultLLMConfig(ctx, envProjectionProvider(llms.ProviderIDDefaultOpenAI), envProjectionModel("shared-model"))
	conflict, ok := llms.AsDefaultConfigConflict(err)
	if !ok {
		t.Fatalf("environment projection error = %v, want it to yield to the file that now owns the model id", err)
	}
	if conflict.Kind != "model" || conflict.ID != "shared-model" || conflict.Scope != llms.ProviderScopeCatalog {
		t.Fatalf("conflict = %#v", conflict)
	}
}

// Releasing a row is not a claim the file can never take back: once the
// environment bootstrap has taken the id over, declaring it in models.json again
// takes the row back instead of failing every later startup.
func TestIntegrationModelCatalogTakesItsReleasedIDBackFromTheEnvironment(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	declared := catalogWithCredential(llms.ProviderIDDefaultOpenAI, "file-model")
	if err := store.ApplyModelCatalog(ctx, declared); err != nil {
		t.Fatalf("declare the connection: %v", err)
	}
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{}); err != nil {
		t.Fatalf("drop the connection: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, envProjectionProvider(llms.ProviderIDDefaultOpenAI), envProjectionModel("env-model")); err != nil {
		t.Fatalf("the environment takes the released id over: %v", err)
	}

	if err := store.ApplyModelCatalog(ctx, declared); err != nil {
		t.Fatalf("the file restores the declaration: %v", err)
	}
	assertProviderOwnership(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI, llms.ProviderScopeCatalog, "https://catalog.example/v1")
}

// A released connection gives its model bindings up with its id. A binding is what
// makes a connection serve a model and it carries the file's own endpoint and
// header overrides, so a binding left behind on a released row is inherited by
// whoever takes the id over — the environment bootstrap reclaims the
// openai/anthropic ids — and that connection then serves models the file declared
// under endpoints and headers neither source declared for it.
func TestIntegrationModelCatalogReleaseDropsTheReleasedBindings(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	declared := catalogWithCredential(llms.ProviderIDDefaultOpenAI, "file-model")
	provider := declared.Providers[llms.ProviderIDDefaultOpenAI]
	provider.Models = append(provider.Models, llms.CatalogModel{ID: "second-file-model"})
	declared.Providers[llms.ProviderIDDefaultOpenAI] = provider
	if err := store.ApplyModelCatalog(ctx, declared); err != nil {
		t.Fatalf("declare the connection: %v", err)
	}
	if count := countProviderModelBindings(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI); count != 2 {
		t.Fatalf("bindings after declaring = %d, want both declared models", count)
	}

	// The operator removes the connection from models.json.
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{}); err != nil {
		t.Fatalf("drop the connection: %v", err)
	}
	if count := countProviderModelBindings(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI); count != 0 {
		t.Fatalf("bindings after the release = %d, want the released connection to give them up", count)
	}

	// The environment bootstrap reclaims the released id and declares its own model.
	if err := store.UpsertDefaultLLMConfig(ctx, envProjectionProvider(llms.ProviderIDDefaultOpenAI), envProjectionModel("env-model")); err != nil {
		t.Fatalf("the environment takes the released id over: %v", err)
	}
	if count := countProviderModelBindings(t, ctx, store.DB(), llms.ProviderIDDefaultOpenAI); count != 1 {
		t.Fatalf("bindings after the environment reclaimed the id = %d, want only the model the environment declares", count)
	}
	var boundModel string
	if err := store.DB().QueryRowContext(ctx, `SELECT model_id FROM llm_provider_model WHERE provider_id = ?`, llms.ProviderIDDefaultOpenAI).Scan(&boundModel); err != nil {
		t.Fatal(err)
	}
	if boundModel != "env-model" {
		t.Fatalf("reclaimed connection serves %q, want the environment model only", boundModel)
	}
}

func TestIntegrationApplyModelCatalogRejectsConnectionsAnotherSourceOwns(t *testing.T) {
	clearLLMTestEnvironment(t)
	for _, scope := range []string{llms.ProviderScopeAPI, llms.ProviderScopeDeclared} {
		t.Run(scope, func(t *testing.T) {
			ctx := context.Background()
			store := FromDB(newMemoryDB(t))
			if err := store.InitSchema(ctx); err != nil {
				t.Fatal(err)
			}
			original := llms.Provider{
				ID: "shared", Name: scope + " provider", ProviderType: llms.ProviderFamilyOpenAI,
				DefaultWireAPI: llms.APIProtocolChatCompletions, BaseURL: "https://system.example/v1", APIKey: "system-key",
				AuthHeader: "X-System-Key", HeadersJSON: `{"X-System":"preserved"}`,
				UseGenericResponsesTextParts: true, Weight: 7, Enabled: true, Scope: scope,
			}
			model := llms.Model{ID: "system-model", Name: "system-model", DefaultModel: true, Scope: scope}
			if err := store.UpsertDefaultLLMConfig(ctx, original, model); err != nil {
				t.Fatal(err)
			}

			baseURL, protocol, apiKey := "https://catalog.example/v1", llms.APIProtocolResponses, "catalog-key"
			err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{
				"shared": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey},
			}})
			if err == nil || !strings.Contains(err.Error(), "conflicts with an existing non-catalog provider") {
				t.Fatalf("ApplyModelCatalog error = %v", err)
			}

			providers, listErr := store.ListEnabledLLMProviders(ctx)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(providers) != 1 || providers[0].ID != original.ID || providers[0].Name != original.Name ||
				providers[0].ProviderType != original.ProviderType || providers[0].DefaultWireAPI != original.DefaultWireAPI ||
				providers[0].BaseURL != original.BaseURL || providers[0].APIKey != original.APIKey ||
				providers[0].AuthHeader != original.AuthHeader || providers[0].HeadersJSON != original.HeadersJSON ||
				providers[0].UseGenericResponsesTextParts != original.UseGenericResponsesTextParts ||
				providers[0].Weight != original.Weight || !providers[0].Enabled || providers[0].Scope != original.Scope {
				t.Fatalf("providers after rejected collision = %#v", providers)
			}
			models, listErr := store.ListEnabledLLMModels(ctx)
			if listErr != nil {
				t.Fatal(listErr)
			}
			if len(models) != 1 || models[0].ID != model.ID || !models[0].DefaultModel || models[0].Scope != scope {
				t.Fatalf("models after rejected collision = %#v", models)
			}
			if _, ok, bindingErr := store.LLMProviderModelWireAPI(ctx, original.ID, model.ID); bindingErr != nil || !ok {
				t.Fatalf("original model binding ok=%v err=%v", ok, bindingErr)
			}
		})
	}
}

func TestIntegrationCatalogModelPresentationIsProviderSpecific(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	baseURL, protocol, apiKey := "https://catalog.example/v1", llms.APIProtocolResponses, "catalog-key"
	firstName, secondName := "First deployment", "Second deployment"
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{
		"first":  {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey, Models: []llms.CatalogModel{{ID: "shared-model", Name: &firstName}}},
		"second": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey, Models: []llms.CatalogModel{{ID: "shared-model", Name: &secondName}}},
	}}); err != nil {
		t.Fatal(err)
	}

	first, ok, err := store.LLMProviderModelConfig(ctx, "first", "shared-model")
	if err != nil || !ok || first.DisplayName != firstName {
		t.Fatalf("first provider model config = %#v ok=%v err=%v", first, ok, err)
	}
	second, ok, err := store.LLMProviderModelConfig(ctx, "second", "shared-model")
	if err != nil || !ok || second.DisplayName != secondName {
		t.Fatalf("second provider model config = %#v ok=%v err=%v", second, ok, err)
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != "shared-model" || models[0].Name != "shared-model" {
		t.Fatalf("global model identities = %#v", models)
	}
}

func clearLLMTestEnvironment(t *testing.T) {
	t.Helper()
	for _, key := range []string{"LLM_API_ENDPOINT", "LLM_API_PROTOCOL", "LLM_API_KEY", "LLM_API_HEADERS", "LLM_MODEL", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT", "ANTHROPIC_MODEL", "CLAUDE_MODEL"} {
		t.Setenv(key, "")
	}
}

// A local endpoint needs no credential. Declaring "enabled": true must make the
// file's connection usable and its default resolvable instead of failing the
// daemon start because the row was persisted disabled.
func TestIntegrationCatalogProviderServesWithoutACredential(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := newProjectionTestStore(t)

	baseURL, protocol := "http://127.0.0.1:11434/v1", llms.APIProtocolChatCompletions
	enabled := true
	catalog := llms.ModelCatalog{
		Default: llms.CatalogDefault("local/local-model"),
		Providers: map[string]llms.CatalogProvider{
			"local": {BaseURL: &baseURL, Protocol: &protocol, Enabled: &enabled, Models: []llms.CatalogModel{{ID: "local-model"}}},
		},
	}
	if err := store.ApplyModelCatalog(ctx, catalog); err != nil {
		t.Fatalf("ApplyModelCatalog: %v", err)
	}

	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders: %v", err)
	}
	if len(providers) != 1 || providers[0].ID != "local" {
		t.Fatalf("enabled connections = %#v, want the credential-less local connection", providers)
	}
	if providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx); err != nil || !ok || providerID != "local" || modelID != "local-model" {
		t.Fatalf("default reference = %q/%q ok=%v err=%v, want local/local-model", providerID, modelID, ok, err)
	}

	loaded, err := llms.LoadCatalog(ctx, store)
	if err != nil {
		t.Fatalf("LoadCatalog: %v", err)
	}
	selected, err := loaded.SelectModel("")
	if err != nil {
		t.Fatalf("SelectModel: %v", err)
	}
	target, err := loaded.Resolve("", selected, nil)
	if err != nil {
		t.Fatalf("Resolve(%q): %v", selected, err)
	}
	if target.Provider.ID != "local" || target.Model.ID != "local-model" {
		t.Fatalf("resolved %s/%s, want local/local-model", target.Provider.ID, target.Model.ID)
	}
}
