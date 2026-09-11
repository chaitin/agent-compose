package configstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
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

	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: nil, SessionID: "", PreferredProviderFamily: "", RequestedModel: "gateway/chat-model", ProviderID: "", EnvItems: nil,
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv: %v", err)
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
	catalog := llms.ModelCatalog{Default: "baizhi/deepseek-v4-flash", Providers: map[string]llms.CatalogProvider{
		"baizhi": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey, Models: []llms.CatalogModel{{ID: "deepseek-v4-flash", MaxOutputTokens: &limit}}},
	}}
	if err := store.ApplyModelCatalog(ctx, catalog); err != nil {
		t.Fatalf("ApplyModelCatalog: %v", err)
	}

	providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx)
	if err != nil || !ok || providerID != "baizhi" || modelID != "deepseek-v4-flash" {
		t.Fatalf("default = %q/%q ok=%v err=%v", providerID, modelID, ok, err)
	}
	literal, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: nil, SessionID: "", PreferredProviderFamily: "", RequestedModel: "baizhi/upstream-only-model", ProviderID: "", EnvItems: nil,
	})
	if err != nil {
		t.Fatalf("resolve literal model: %v", err)
	}
	if literal.Model.ID != "upstream-only-model" || literal.MaxOutputTokens != 0 {
		t.Fatalf("literal target = %#v", literal)
	}
	configured, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: nil, SessionID: "", PreferredProviderFamily: "", RequestedModel: "baizhi/deepseek-v4-flash", ProviderID: "", EnvItems: nil,
	})
	if err != nil {
		t.Fatalf("resolve configured model: %v", err)
	}
	if configured.MaxOutputTokens != limit || configured.Provider.APIKey != apiKey {
		t.Fatalf("configured target = %#v", configured)
	}
	defaultTarget, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: nil, SessionID: "", PreferredProviderFamily: "", RequestedModel: "", ProviderID: "", EnvItems: nil,
	})
	if err != nil {
		t.Fatalf("resolve catalog default: %v", err)
	}
	if defaultTarget.Provider.ID != "baizhi" || defaultTarget.Model.ID != "deepseek-v4-flash" {
		t.Fatalf("catalog default target = %#v", defaultTarget)
	}
}

func TestIntegrationDaemonEnvironmentDefaultWinsButExplicitCatalogProviderRemainsPinned(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	baseURL, protocol, apiKey := "https://gateway.example/v1", llms.APIProtocolResponses, "catalog-key"
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Default: "baizhi/catalog-model", Providers: map[string]llms.CatalogProvider{
		"baizhi": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey},
	}}); err != nil {
		t.Fatal(err)
	}
	config := &appconfig.Config{LLMAPIEndpoint: "https://legacy.example/v1", LLMAPIProtocol: llms.APIProtocolResponses, LLMAPIKey: "legacy-key", LLMModel: "feature/gpt-5.6-sol"}
	legacy, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: config, SessionID: "", PreferredProviderFamily: "", RequestedModel: "", ProviderID: "", EnvItems: nil,
	})
	if err != nil {
		t.Fatalf("resolve daemon environment default: %v", err)
	}
	if legacy.Provider.ID != llms.ProviderIDDefaultOpenAI || legacy.Model.ID != "feature/gpt-5.6-sol" || legacy.Provider.APIKey != "legacy-key" {
		t.Fatalf("daemon environment target = %#v", legacy)
	}
	explicit, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: config, SessionID: "", PreferredProviderFamily: "", RequestedModel: "baizhi/literal-model", ProviderID: "", EnvItems: nil,
	})
	if err != nil {
		t.Fatalf("resolve explicit catalog provider: %v", err)
	}
	if explicit.Provider.ID != "baizhi" || explicit.Model.ID != "literal-model" || explicit.Provider.APIKey != apiKey {
		t.Fatalf("explicit catalog target = %#v", explicit)
	}
}

func TestIntegrationExplicitUnavailableCatalogProviderDoesNotFallBack(t *testing.T) {
	clearLLMTestEnvironment(t)
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.InitSchema(ctx); err != nil {
		t.Fatal(err)
	}
	baseURL, protocol := "https://unavailable.example/v1", llms.APIProtocolResponses
	if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{Providers: map[string]llms.CatalogProvider{"custom": {BaseURL: &baseURL, Protocol: &protocol}}}); err != nil {
		t.Fatal(err)
	}
	_, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: &appconfig.Config{LLMAPIEndpoint: "https://daemon.example/v1", LLMAPIKey: "daemon-key", LLMModel: "daemon-model"}, SessionID: "", PreferredProviderFamily: "", RequestedModel: "custom/literal-model", ProviderID: "", EnvItems: nil,
	})
	if err == nil || !strings.Contains(err.Error(), `provider "custom"`) {
		t.Fatalf("explicit unavailable provider error = %v", err)
	}
}

func TestIntegrationApplyEmptyModelCatalogOnlyClearsCatalogOwnedState(t *testing.T) {
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
		Default: "catalog/catalog-model",
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
	if providerID, modelID, ok, err := store.DefaultLLMModelReference(ctx); err != nil || ok {
		t.Fatalf("catalog default = %q/%q ok=%v err=%v, want cleared", providerID, modelID, ok, err)
	}
}

func TestIntegrationApplyModelCatalogRejectsNonCatalogProviderCollision(t *testing.T) {
	clearLLMTestEnvironment(t)
	for _, scope := range []string{llms.ProviderScopeSystem, llms.ProviderScopeEnvDefault, llms.ProviderScopeSessionEnv} {
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
