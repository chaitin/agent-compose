package llms

import (
	"context"
	"strings"
)

type EnvProviderLookup func(keys ...string) string

type DefaultConfigStore interface {
	// UpsertDefaultLLMConfig persists a provider/model binding. A session-scoped
	// model must not replace metadata owned by an existing non-session model.
	UpsertDefaultLLMConfig(ctx context.Context, provider Provider, model Model) error
}

type ProviderListStore interface {
	ListEnabledLLMProviders(ctx context.Context) ([]Provider, error)
}

func HasEnabledProviderID(ctx context.Context, store ProviderListStore, providerID string) bool {
	providerID = strings.TrimSpace(providerID)
	if store == nil || providerID == "" {
		return false
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return false
	}
	for _, provider := range providers {
		if provider.ID == providerID {
			return true
		}
	}
	return false
}

func HasConfiguredProviderForFamily(ctx context.Context, store ProviderListStore, providerFamily string) bool {
	if store == nil {
		return false
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return false
	}
	for _, provider := range providers {
		if NormalizeProviderType(provider.ProviderType) != NormalizeProviderType(providerFamily) {
			continue
		}
		if ProviderScopeIsConfigured(provider.Scope) {
			return true
		}
	}
	return false
}

func EnsureOpenAIEnvProvider(ctx context.Context, store DefaultConfigStore, lookup EnvProviderLookup, providerID, name, scope, requestedModel string, defaultModel bool) (string, error) {
	environment := providerEnvironment{
		endpoint: lookup("LLM_API_ENDPOINT"),
		protocol: lookup("LLM_API_PROTOCOL"),
		apiKey:   lookup("LLM_API_KEY", "OPENAI_API_KEY"),
		model:    lookup("LLM_MODEL"),
	}
	return ensureOpenAIProviderEnvironment(ctx, store, environment, providerID, name, scope, requestedModel, defaultModel)
}

func ensureOpenAIProviderEnvironment(ctx context.Context, store DefaultConfigStore, environment providerEnvironment, providerID, name, scope, requestedModel string, defaultModel bool) (string, error) {
	endpoint := firstNonEmpty(environment.endpoint, "https://api.openai.com")
	protocol := NormalizeWireAPI(environment.protocol)
	model := strings.TrimSpace(firstNonEmpty(requestedModel, environment.model))
	if providerID == "" || model == "" {
		return "", nil
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, Provider{
		ID:             providerID,
		Name:           name,
		ProviderType:   ProviderFamilyOpenAI,
		DefaultWireAPI: protocol,
		BaseURL:        endpoint,
		APIKey:         environment.apiKey,
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    "{}",
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, Model{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}

func ensureAnthropicProviderEnvironment(ctx context.Context, store DefaultConfigStore, environment providerEnvironment, providerID, name, scope, requestedModel string, defaultModel bool) (string, error) {
	if !environment.configured {
		return "", nil
	}
	endpoint := firstNonEmpty(environment.endpoint, "https://api.anthropic.com")
	model := strings.TrimSpace(firstNonEmpty(requestedModel, environment.model))
	if providerID == "" || model == "" {
		return "", nil
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, Provider{
		ID:             providerID,
		Name:           name,
		ProviderType:   ProviderFamilyAnthropic,
		DefaultWireAPI: APIProtocolMessages,
		BaseURL:        endpoint,
		APIKey:         environment.apiKey,
		AuthHeader:     environment.authHeader,
		AuthScheme:     environment.authScheme,
		HeadersJSON:    `{"anthropic-version":"2023-06-01"}`,
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, Model{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}
