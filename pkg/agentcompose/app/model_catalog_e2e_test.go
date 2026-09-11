package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/llms/runtimefacade"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestE2EModelCatalogConfiguresOpenCodeFacadeTarget(t *testing.T) {
	ctx := context.Background()
	// Keep the catalog resolution deterministic when the developer/CI environment
	// provides global LLM credentials. The test exercises models.json as the
	// source of truth and must not inherit ambient provider configuration.
	for _, key := range []string{"LLM_API_KEY", "LLM_API_HEADERS", "LLM_BASE_URL", "LLM_MODEL", "OPENAI_API_KEY", "OPENAI_BASE_URL", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT", "ANTHROPIC_MODEL", "CLAUDE_MODEL"} {
		t.Setenv(key, "")
	}
	root := t.TempDir()
	t.Setenv("MODEL_CATALOG_E2E_KEY", "catalog-e2e-key")
	catalog := `{
  "default": "baizhi/deepseek-v4-flash",
  "providers": {
    "baizhi": {
      "baseUrl": "https://gateway.example.test/api/openai",
      "protocol": "chat_completions",
      "apiKey": "${MODEL_CATALOG_E2E_KEY}",
      "headers": {"X-Provider": "provider-header"},
      "models": [{
        "id": "deepseek-v4-flash",
        "headers": {"X-Model": "model-header"},
        "maxOutputTokens": 99999
      }]
    }
  }
}`
	if err := os.WriteFile(filepath.Join(root, llms.ModelsCatalogFilename), []byte(catalog), 0o600); err != nil {
		t.Fatalf("write models.json: %v", err)
	}

	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		SandboxRoot:    filepath.Join(root, "sandboxes"),
		RuntimeBaseURL: "http://agent-compose.example.test:7410",
		GuestHomePath:  "/root",
	}
	store, _, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	if err := loadModelCatalog(ctx, config, store); err != nil {
		t.Fatalf("load models.json: %v", err)
	}

	defaultTarget, err := llms.ResolveRuntimeLLMTarget(ctx, config, store, "", "")
	if err != nil {
		t.Fatalf("resolve models.json default: %v", err)
	}
	if defaultTarget.Provider.ID != "baizhi" || defaultTarget.Model.ID != "deepseek-v4-flash" {
		t.Fatalf("default target = %#v", defaultTarget)
	}

	sandboxID := "catalog-e2e-sandbox"
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            sandboxID,
		WorkspacePath: filepath.Join(config.SandboxRoot, sandboxID, "workspace"),
	}}
	runtimeConfig, err := runtimefacade.EnsureSessionAgentRuntimeConfig(
		ctx,
		runtimefacade.SessionFacadeConfigRequest{
			Config:  config,
			Store:   store,
			Session: sandbox,
			Agent:   "opencode",
			Model:   "baizhi/deepseek-v4-flash",
			Source:  runtimefacade.TokenSourceAgent,
			RunID:   "catalog-e2e-run",
		},
	)
	if err != nil {
		t.Fatalf("configure OpenCode facade: %v", err)
	}
	if runtimeConfig.Model != "agent-compose/deepseek-v4-flash" || runtimeConfig.Env["LLM_API_PROTOCOL"] != llms.APIProtocolChatCompletions {
		t.Fatalf("OpenCode runtime config = %#v", runtimeConfig)
	}

	rawToken := runtimeConfig.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]
	token, err := store.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("load facade token: %v", err)
	}
	if token.ProviderID != "baizhi" || token.Model != "deepseek-v4-flash" || token.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("facade token = %#v", token)
	}
	target, err := llms.ResolveRuntimeLLMTarget(ctx, config, store, token.Model, token.ProviderID)
	if err != nil {
		t.Fatalf("resolve facade upstream target: %v", err)
	}
	if target.Endpoint != "https://gateway.example.test/api/openai/v1/chat/completions" || target.MaxOutputTokens != 99999 {
		t.Fatalf("upstream target = %#v", target)
	}
	if target.Headers.Get("Authorization") != "Bearer catalog-e2e-key" ||
		target.Headers.Get("X-Provider") != "provider-header" ||
		target.Headers.Get("X-Model") != "model-header" {
		t.Fatalf("upstream headers = %#v", target.Headers)
	}

	guestConfigPath := filepath.Join(config.SandboxRoot, sandboxID, "home", ".config", "opencode", "opencode.json")
	guestConfig, err := os.ReadFile(guestConfigPath)
	if err != nil {
		t.Fatalf("read OpenCode config: %v", err)
	}
	if strings.Contains(string(guestConfig), "catalog-e2e-key") || strings.Contains(string(guestConfig), "gateway.example.test") {
		t.Fatalf("OpenCode config leaked upstream configuration: %s", guestConfig)
	}
}

func TestE2ELoadMissingModelCatalogPreservesExistingDefault(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:    root,
		DbAddr:      filepath.Join(root, "data.db"),
		SandboxRoot: filepath.Join(root, "sandboxes"),
	}
	store, _, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}
	provider := llms.Provider{
		ID: "existing", Name: "existing", ProviderType: llms.ProviderFamilyOpenAI,
		BaseURL: "https://existing.example.test/v1", APIKey: "existing-key", Scope: llms.ProviderScopeSystem,
	}
	model := llms.Model{ID: "existing-model", Name: "existing-model", DefaultModel: true, Scope: llms.ProviderScopeSystem}
	if err := store.UpsertDefaultLLMConfig(ctx, provider, model); err != nil {
		t.Fatalf("store existing default: %v", err)
	}

	if err := loadModelCatalog(ctx, config, store); err != nil {
		t.Fatalf("load missing models.json: %v", err)
	}

	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(providers) != 1 || providers[0].ID != provider.ID || providers[0].BaseURL != provider.BaseURL || providers[0].APIKey != provider.APIKey {
		t.Fatalf("providers after startup = %#v", providers)
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].ID != model.ID || !models[0].DefaultModel || models[0].Scope != llms.ProviderScopeSystem {
		t.Fatalf("models after startup = %#v", models)
	}
}
