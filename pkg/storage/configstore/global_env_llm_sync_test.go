package configstore

import (
	"context"
	"testing"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestReplaceGlobalEnvSyncsEnvironmentDefaultProviders(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}

	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "old-openai", Secret: true},
		{Name: "ANTHROPIC_API_KEY", Value: "old-anthropic", Secret: true},
	}); err != nil {
		t.Fatalf("seed global env: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "default", Name: "default", ProviderType: llms.ProviderFamilyOpenAI,
		APIKey: "old-openai", Scope: llms.ProviderScopeEnvDefault, Enabled: true,
	}, llms.Model{ID: "openai-model", Name: "openai-model", Scope: llms.ProviderScopeEnvDefault, Enabled: true}); err != nil {
		t.Fatalf("seed OpenAI provider: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "custom-openai", Name: "custom-openai", ProviderType: llms.ProviderFamilyOpenAI,
		APIKey: "old-openai", Scope: llms.ProviderScopeEnvDefault, Enabled: true,
	}, llms.Model{ID: "custom-openai-model", Name: "custom-openai-model", Scope: llms.ProviderScopeEnvDefault, Enabled: true}); err != nil {
		t.Fatalf("seed custom OpenAI provider: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "anthropic", Name: "anthropic", ProviderType: llms.ProviderFamilyAnthropic,
		APIKey: "old-anthropic", AuthHeader: "x-api-key", Scope: llms.ProviderScopeEnvDefault, Enabled: true,
	}, llms.Model{ID: "anthropic-model", Name: "anthropic-model", Scope: llms.ProviderScopeEnvDefault, Enabled: true}); err != nil {
		t.Fatalf("seed Anthropic provider: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "system-provider", Name: "system-provider", ProviderType: llms.ProviderFamilyAnthropic,
		APIKey: "system-key", AuthHeader: "x-api-key", Scope: llms.ProviderScopeSystem, Enabled: true,
	}, llms.Model{ID: "system-model", Name: "system-model", Scope: llms.ProviderScopeSystem, Enabled: true}); err != nil {
		t.Fatalf("seed system provider: %v", err)
	}

	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{
		{Name: "LLM_API_KEY", Value: "new-openai", Secret: true},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "new-anthropic", Secret: true},
	}); err != nil {
		t.Fatalf("update global env: %v", err)
	}

	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	byID := make(map[string]llms.Provider, len(providers))
	for _, provider := range providers {
		byID[provider.ID] = provider
	}
	if got := byID["default"].APIKey; got != "new-openai" {
		t.Errorf("default provider key = %q, want new-openai", got)
	}
	if got := byID["custom-openai"].APIKey; got != "new-openai" {
		t.Errorf("custom OpenAI provider key = %q, want new-openai", got)
	}
	anthropic := byID["anthropic"]
	if anthropic.APIKey != "new-anthropic" || anthropic.AuthHeader != "Authorization" || anthropic.AuthScheme != "Bearer" {
		t.Errorf("anthropic credentials = %#v", anthropic)
	}
	if got := byID["system-provider"].APIKey; got != "system-key" {
		t.Errorf("system provider key = %q, want system-key", got)
	}
}

func TestReplaceGlobalEnvClearsRemovedEnvironmentDefaultCredential(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{{Name: "ANTHROPIC_API_KEY", Value: "old", Secret: true}}); err != nil {
		t.Fatalf("seed global env: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "anthropic", Name: "anthropic", ProviderType: llms.ProviderFamilyAnthropic,
		APIKey: "old", AuthHeader: "x-api-key", Scope: llms.ProviderScopeEnvDefault, Enabled: true,
	}, llms.Model{ID: "model", Name: "model", Scope: llms.ProviderScopeEnvDefault, Enabled: true}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := store.ReplaceGlobalEnv(ctx, nil); err != nil {
		t.Fatalf("clear global env: %v", err)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 1 || providers[0].APIKey != "" {
		t.Fatalf("providers after clear = %#v", providers)
	}
}

func TestReplaceGlobalEnvWithProviderCredentialsUsesEffectiveFallback(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "global-key", Secret: true}}); err != nil {
		t.Fatalf("seed global env: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "default", Name: "default", ProviderType: llms.ProviderFamilyOpenAI,
		APIKey: "global-key", Scope: llms.ProviderScopeEnvDefault, Enabled: true,
	}, llms.Model{ID: "model", Name: "model", Scope: llms.ProviderScopeEnvDefault, Enabled: true}); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if _, err := store.ReplaceGlobalEnvWithProviderCredentials(ctx, nil, llms.EnvDefaultProviderCredentials{OpenAIAPIKey: "process-key"}); err != nil {
		t.Fatalf("replace global env with fallback credentials: %v", err)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 1 || providers[0].APIKey != "process-key" {
		t.Fatalf("providers after fallback = %#v", providers)
	}
}

func TestReplaceGlobalEnvRepairsStaleProviderWithUnchangedGlobalCredential(t *testing.T) {
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	items := []domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "current-key", Secret: true}}
	if _, err := store.ReplaceGlobalEnv(ctx, items); err != nil {
		t.Fatalf("seed global env: %v", err)
	}
	if err := store.UpsertDefaultLLMConfig(ctx, llms.Provider{
		ID: "default", Name: "default", ProviderType: llms.ProviderFamilyOpenAI,
		APIKey: "stale-key", Scope: llms.ProviderScopeEnvDefault, Enabled: true,
	}, llms.Model{ID: "model", Name: "model", Scope: llms.ProviderScopeEnvDefault, Enabled: true}); err != nil {
		t.Fatalf("seed stale provider: %v", err)
	}
	if _, err := store.DB().ExecContext(ctx, `UPDATE llm_provider SET updated_at = 1 WHERE id = 'default'`); err != nil {
		t.Fatalf("set stale provider timestamp: %v", err)
	}
	if _, err := store.ReplaceGlobalEnv(ctx, items); err != nil {
		t.Fatalf("replace unchanged global env: %v", err)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("list providers: %v", err)
	}
	if len(providers) != 1 || providers[0].APIKey != "current-key" || providers[0].UpdatedAt.Unix() <= 1 {
		t.Fatalf("repaired provider = %#v", providers)
	}
}
