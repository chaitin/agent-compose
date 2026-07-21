package llms

import (
	"context"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func TestResolverBootstrapAndRuntimeTargetWorkflows(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	store := newResolverCoverageStore()
	config := &appconfig.Config{
		LLMAPIEndpoint: "https://api.openai.test",
		LLMAPIProtocol: "chat",
		LLMAPIKey:      "openai-key",
		LLMModel:       "gpt-default",
	}
	if err := BootstrapDefaultLLMConfig(ctx, config, store, ""); err != nil {
		t.Fatalf("BootstrapDefaultLLMConfig returned error: %v", err)
	}
	if len(store.providers) != 1 || store.providers[0].ID != ProviderIDDefaultOpenAI || store.models[0].ID != "gpt-default" {
		t.Fatalf("default bootstrap providers=%#v models=%#v", store.providers, store.models)
	}
	if err := BootstrapDefaultLLMConfig(ctx, config, store, "ignored"); err != nil {
		t.Fatalf("BootstrapDefaultLLMConfig configured returned error: %v", err)
	}
	if len(store.providers) != 1 {
		t.Fatalf("default bootstrap did not skip configured provider: %#v", store.providers)
	}
	lookup, err := DefaultLLMEnvProviderLookup(ctx, config, store)
	if err != nil {
		t.Fatalf("DefaultLLMEnvProviderLookup returned error: %v", err)
	}
	if value := lookup("LLM_API_KEY"); value != "openai-key" {
		t.Fatalf("default lookup config value = %q", value)
	}

	target, err := ResolveLLMTarget(ctx, config, store, "")
	if err != nil {
		t.Fatalf("ResolveLLMTarget returned error: %v", err)
	}
	if target.Model.ID != "gpt-default" || target.Provider.ID != ProviderIDDefaultOpenAI || !strings.Contains(target.Endpoint, "chat/completions") || target.Headers.Get("Authorization") == "" {
		t.Fatalf("default target = %#v", target)
	}
	providerless, err := ResolveRuntimeLLMTarget(ctx, config, store, "gpt-default", "")
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTarget providerless returned error: %v", err)
	}
	if providerless.Model.ID != "gpt-default" || providerless.Provider.ID != ProviderIDDefaultOpenAI {
		t.Fatalf("providerless target = %#v", providerless)
	}

	store.global = []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "anthropic-token"},
		{Name: "ANTHROPIC_MODEL", Value: "claude-default"},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://api.anthropic.test"},
	}
	if err := BootstrapAnthropicLLMConfig(ctx, &appconfig.Config{}, store, ""); err != nil {
		t.Fatalf("BootstrapAnthropicLLMConfig returned error: %v", err)
	}
	anthropic, err := ResolveLLMTargetForProviderFamily(ctx, &appconfig.Config{}, store, ProviderFamilyAnthropic, "")
	if err != nil {
		t.Fatalf("ResolveLLMTargetForProviderFamily anthropic returned error: %v", err)
	}
	if anthropic.Provider.ProviderType != ProviderFamilyAnthropic || anthropic.Model.ID != "claude-default" || anthropic.Headers.Get("Authorization") == "" {
		t.Fatalf("anthropic target = %#v", anthropic)
	}

	sessionStore := newResolverCoverageStore()
	runtimeTarget, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, sessionStore, "session-1", ProviderFamilyOpenAI, "", "", []domain.SandboxEnvVar{
		{Name: "OPENAI_API_KEY", Value: "session-key"},
		{Name: "LLM_MODEL", Value: "gpt-session"},
		{Name: "LLM_API_PROTOCOL", Value: "responses"},
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if runtimeTarget.Provider.Scope != ProviderScopeSessionEnv || runtimeTarget.Model.ID != "gpt-session" || runtimeTarget.WireAPI != APIProtocolResponses {
		t.Fatalf("session runtime target = %#v", runtimeTarget)
	}
	pinned, err := ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, sessionStore, "session-1", ProviderFamilyOpenAI, "gpt-session", runtimeTarget.Provider.ID, nil)
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv pinned returned error: %v", err)
	}
	if pinned.Provider.ID != runtimeTarget.Provider.ID {
		t.Fatalf("pinned target = %#v want provider %s", pinned, runtimeTarget.Provider.ID)
	}

	emptyStore := newResolverCoverageStore()
	if _, err := ResolveRuntimeLLMTarget(ctx, &appconfig.Config{}, emptyStore, "", ""); err == nil {
		t.Fatal("ResolveRuntimeLLMTarget returned nil error without model/provider")
	}
}

func isolateLLMEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"LLM_API_ENDPOINT",
		"LLM_API_PROTOCOL",
		"LLM_API_KEY",
		"OPENAI_API_KEY",
		"LLM_MODEL",
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_API_ENDPOINT",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_MODEL",
		"CLAUDE_MODEL",
	} {
		t.Setenv(key, "")
	}
}

type resolverCoverageStore struct {
	providers []Provider
	models    []Model
	wire      map[string]string
	global    []domain.SandboxEnvVar
	globalErr error
}

func newResolverCoverageStore() *resolverCoverageStore {
	return &resolverCoverageStore{wire: map[string]string{}}
}

func (s *resolverCoverageStore) UpsertDefaultLLMConfig(_ context.Context, provider Provider, model Model) error {
	provider.Enabled = true
	model.Enabled = true
	replacedProvider := false
	for i := range s.providers {
		if s.providers[i].ID == provider.ID {
			s.providers[i] = provider
			replacedProvider = true
		}
	}
	if !replacedProvider {
		s.providers = append(s.providers, provider)
	}
	replacedModel := false
	for i := range s.models {
		if s.models[i].ID == model.ID {
			if model.Scope != ProviderScopeSessionEnv || s.models[i].Scope == ProviderScopeSessionEnv {
				s.models[i] = model
			}
			replacedModel = true
		}
	}
	if !replacedModel {
		s.models = append(s.models, model)
	}
	s.wire[provider.ID+"\x00"+model.ID] = firstNonEmpty(NormalizeWireAPI(provider.DefaultWireAPI), APIProtocolResponses)
	return nil
}

func (s *resolverCoverageStore) ListEnabledLLMProviders(context.Context) ([]Provider, error) {
	var out []Provider
	for _, provider := range s.providers {
		if provider.Enabled {
			out = append(out, provider)
		}
	}
	return out, nil
}

func (s *resolverCoverageStore) ListEnabledLLMModels(context.Context) ([]Model, error) {
	var out []Model
	for _, model := range s.models {
		if model.Enabled {
			out = append(out, model)
		}
	}
	return out, nil
}

func (s *resolverCoverageStore) LLMProviderModelWireAPI(_ context.Context, providerID, modelID string) (string, bool, error) {
	wire, ok := s.wire[providerID+"\x00"+modelID]
	return wire, ok, nil
}

func (s *resolverCoverageStore) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return s.global, s.globalErr
}
