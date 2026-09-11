package llms

import (
	"context"
	"strings"
)

type EnvProviderLookup func(keys ...string) string

type DefaultConfigStore interface {
	UpsertDefaultLLMConfig(ctx context.Context, provider Provider, model Model) error
}

type ProviderListStore interface {
	ListEnabledLLMProviders(ctx context.Context) ([]Provider, error)
}

func hasDefaultAnthropicEnvProviderInput(lookup EnvProviderLookup) bool {
	if strings.TrimSpace(firstNonEmpty(
		lookup("ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT"),
		lookup("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"),
		lookup("ANTHROPIC_MODEL", "CLAUDE_MODEL"),
	)) != "" {
		return true
	}
	if NormalizeWireAPI(lookup("LLM_API_PROTOCOL")) != APIProtocolMessages {
		return false
	}
	return strings.TrimSpace(firstNonEmpty(
		lookup("LLM_API_ENDPOINT"),
		lookup("LLM_API_KEY"),
		lookup("LLM_MODEL"),
	)) != ""
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

// EnvProviderRegistration groups the identity/model fields shared by the
// Ensure*EnvProvider family: which provider/model to register, under which
// scope, and whether it becomes the default.
type EnvProviderRegistration struct {
	ProviderID     string
	Name           string
	Scope          string
	RequestedModel string
	DefaultModel   bool
}

func EnsureOpenAIEnvProvider(ctx context.Context, store DefaultConfigStore, lookup EnvProviderLookup, reg EnvProviderRegistration) (string, error) {
	providerID, name, scope, requestedModel, defaultModel := reg.ProviderID, reg.Name, reg.Scope, reg.RequestedModel, reg.DefaultModel
	endpoint := firstNonEmpty(lookup("LLM_API_ENDPOINT"), "https://api.openai.com")
	protocol := NormalizeWireAPI(lookup("LLM_API_PROTOCOL"))
	if protocol == APIProtocolMessages {
		return "", nil
	}
	apiKey := lookup("LLM_API_KEY", "OPENAI_API_KEY")
	model := strings.TrimSpace(firstNonEmpty(requestedModel, lookup("LLM_MODEL")))
	if providerID == "" || model == "" {
		return "", nil
	}
	headersJSON, err := envProviderHeadersJSON(lookup, nil)
	if err != nil {
		return "", err
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, Provider{
		ID:             providerID,
		Name:           name,
		ProviderType:   ProviderFamilyOpenAI,
		DefaultWireAPI: protocol,
		BaseURL:        endpoint,
		APIKey:         apiKey,
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    headersJSON,
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, Model{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}

// anthropicEnvProviderInput groups ensureAnthropicEnvProvider's credential
// and registration inputs.
type anthropicEnvProviderInput struct {
	Credential anthropicCredential
	EnvProviderRegistration
}

func ensureAnthropicEnvProvider(ctx context.Context, store DefaultConfigStore, lookup EnvProviderLookup, in anthropicEnvProviderInput) (string, error) {
	credential, reg := in.Credential, in.EnvProviderRegistration
	providerID, name, scope, requestedModel, defaultModel := reg.ProviderID, reg.Name, reg.Scope, reg.RequestedModel, reg.DefaultModel
	anthropicEndpoint := lookup("ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT")
	genericEndpoint := lookup("LLM_API_ENDPOINT")
	anthropicModel := lookup("ANTHROPIC_MODEL", "CLAUDE_MODEL")
	genericModel := lookup("LLM_MODEL")
	if anthropicEndpoint == "" && strings.TrimSpace(credential.apiKey) == "" && strings.TrimSpace(anthropicModel) == "" && genericEndpoint == "" && strings.TrimSpace(genericModel) == "" {
		return "", nil
	}
	anthropicEndpoint = firstNonEmpty(anthropicEndpoint, genericEndpoint)
	anthropicModel = firstNonEmpty(anthropicModel, genericModel)
	endpoint := firstNonEmpty(anthropicEndpoint, "https://api.anthropic.com")
	model := strings.TrimSpace(firstNonEmpty(requestedModel, anthropicModel))
	if providerID == "" || model == "" {
		return "", nil
	}
	headersJSON, err := envProviderHeadersJSON(lookup, map[string]string{
		"anthropic-version": "2023-06-01",
	})
	if err != nil {
		return "", err
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, Provider{
		ID:             providerID,
		Name:           name,
		ProviderType:   ProviderFamilyAnthropic,
		DefaultWireAPI: APIProtocolMessages,
		BaseURL:        endpoint,
		APIKey:         credential.apiKey,
		AuthHeader:     credential.authHeader,
		AuthScheme:     credential.authScheme,
		HeadersJSON:    headersJSON,
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, Model{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}

// AnthropicEnvProviderRequest groups EnsureAnthropicEnvProvider's inputs:
// caller-selected authentication semantics plus the shared registration
// fields (which provider/model to register, under which scope).
type AnthropicEnvProviderRequest struct {
	AuthHeader string
	AuthScheme string
	EnvProviderRegistration
}

// EnsureAnthropicEnvProvider persists an environment-backed Anthropic
// provider using caller-selected authentication semantics. Resolver paths use
// layeredAnthropicCredential so the key and semantics share a source layer.
func EnsureAnthropicEnvProvider(ctx context.Context, store DefaultConfigStore, lookup EnvProviderLookup, req AnthropicEnvProviderRequest) (string, error) {
	credential := anthropicCredential{
		apiKey: firstNonEmpty(
			lookup("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"),
			lookup("LLM_API_KEY"),
		),
		authHeader: req.AuthHeader,
		authScheme: req.AuthScheme,
	}
	return ensureAnthropicEnvProvider(ctx, store, lookup, anthropicEnvProviderInput{Credential: credential, EnvProviderRegistration: req.EnvProviderRegistration})
}
