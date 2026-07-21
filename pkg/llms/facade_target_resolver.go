package llms

import (
	"context"
	"fmt"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

// FacadeEnvironmentStore resolves token-owned Provider Env layers. The layer is
// stored independently from selectable providers so concurrent executions can
// never overwrite one another.
type FacadeEnvironmentStore interface {
	LLMResolverStore
	GetLLMFacadeEnvironment(ctx context.Context, providerID string) (FacadeEnvironment, bool, error)
}

// ResolveFacadeTargetWithEnv applies provider selection before environment
// resolution: an explicit configured provider, then a configured system
// provider, then env bootstrap. Environment fields are resolved independently.
func ResolveFacadeTargetWithEnv(ctx context.Context, config *appconfig.Config, store LLMResolverStore, providerFamily, requestedModel, explicitProviderID string, envItems []domain.SandboxEnvVar) (FacadeTarget, error) {
	var err error
	providerFamily, err = normalizeFacadeProviderFamily(providerFamily)
	if err != nil {
		return FacadeTarget{}, err
	}
	requestedModel = strings.TrimSpace(requestedModel)
	explicitProviderID = strings.TrimSpace(explicitProviderID)

	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return FacadeTarget{}, fmt.Errorf("list llm providers for facade target: %w", err)
	}
	if explicitProviderID != "" {
		if provider, ok := configuredProviderByID(providers, explicitProviderID); ok {
			target, err := resolveExplicitFacadeProviderTarget(ctx, store, provider, requestedModel)
			return FacadeTarget{Target: target}, err
		}
	}

	configured := configuredProvidersForFamily(providers, providerFamily)
	if len(configured) > 0 {
		target, err := resolveConfiguredFacadeProviderTarget(ctx, store, configured, providerFamily, requestedModel)
		return FacadeTarget{Target: target}, err
	}

	sources, normalizedItems, err := layeredProviderEnvSources(ctx, config, store, envItems)
	if err != nil {
		return FacadeTarget{}, err
	}
	target, err := resolveFacadeEnvironmentTarget(providerFamily, firstNonEmptyTrimmed(explicitProviderID, facadeEnvironmentDefaultProviderID(providerFamily)), requestedModel, sources)
	if err != nil {
		return FacadeTarget{}, err
	}
	return FacadeTarget{
		Target:      target,
		Environment: facadeEnvironmentOverrides(providerFamily, normalizedItems),
	}, nil
}

// ResolveFacadeRuntimeTarget resolves a provider-bound request without reading
// mutable sandbox configuration. A token-owned env layer is combined with the
// current Global Env and daemon layer; configured providers are resolved by ID.
func ResolveFacadeRuntimeTarget(ctx context.Context, config *appconfig.Config, store FacadeEnvironmentStore, requestedModel, providerID string) (ResolvedTarget, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	providerID = strings.TrimSpace(providerID)
	if IsFacadeEnvironmentProviderID(providerID) {
		environment, ok, err := store.GetLLMFacadeEnvironment(ctx, providerID)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if !ok {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, "llm facade environment is unavailable", nil)
		}
		environment.ProviderType, err = normalizeFacadeProviderFamily(environment.ProviderType)
		if err != nil {
			return ResolvedTarget{}, err
		}
		lowerSources, err := defaultProviderEnvSources(ctx, config, store)
		if err != nil {
			return ResolvedTarget{}, err
		}
		sources := append(providerEnvSources{facadeEnvironmentSource(environment)}, lowerSources...)
		return resolveFacadeEnvironmentTarget(environment.ProviderType, providerID, requestedModel, sources)
	}
	if providerID != "" {
		target, ok, err := resolveRuntimeLLMProviderTarget(ctx, store, requestedModel, providerID)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if !ok {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm provider %q is unavailable", providerID), nil)
		}
		return target, nil
	}
	return resolveRuntimeLLMTarget(ctx, config, store, requestedModel, "")
}

// NewFacadeGrant creates a token and binds env-bootstrap selections to a unique
// token-owned provider identity. The raw token is never stored in the grant.
func NewFacadeGrant(sandboxID string, target FacadeTarget, ingressWireAPI, source, runID string) (string, FacadeGrant, error) {
	rawToken, token, err := NewFacadeToken(sandboxID, target.Target.Model.Name, target.Target.Provider.ID, ingressWireAPI, source, runID)
	if err != nil {
		return "", FacadeGrant{}, err
	}
	grant := FacadeGrant{Token: token}
	if target.Environment == nil {
		return rawToken, grant, nil
	}
	environment := *target.Environment
	environment.ProviderType, err = normalizeFacadeProviderFamily(environment.ProviderType)
	if err != nil {
		return "", FacadeGrant{}, err
	}
	environment.ProviderID = FacadeEnvironmentProviderID(token.TokenHash, environment.ProviderType)
	grant.Token.ProviderID = environment.ProviderID
	grant.Environment = &environment
	return rawToken, grant, nil
}

// FacadeEnvironmentProviderID returns the private provider identity owned by a
// single facade token.
func FacadeEnvironmentProviderID(tokenHash, providerFamily string) string {
	tokenHash = strings.TrimSpace(tokenHash)
	providerFamily = NormalizeOptionalProviderType(providerFamily)
	if tokenHash == "" || providerFamily == "" {
		return ""
	}
	return "facade-env:" + tokenHash + ":" + providerFamily
}

// IsFacadeEnvironmentProviderID reports whether an ID belongs to a token-owned
// facade environment grant.
func IsFacadeEnvironmentProviderID(providerID string) bool {
	return strings.HasPrefix(strings.TrimSpace(providerID), "facade-env:")
}

func configuredProviderByID(providers []Provider, providerID string) (Provider, bool) {
	for _, provider := range providers {
		if provider.ID == providerID && ProviderScopeIsConfigured(provider.Scope) {
			return provider, true
		}
	}
	return Provider{}, false
}

func configuredProvidersForFamily(providers []Provider, providerFamily string) []Provider {
	configured := make([]Provider, 0, len(providers))
	for _, provider := range providers {
		if NormalizeProviderType(provider.ProviderType) != providerFamily || !ProviderScopeIsConfigured(provider.Scope) {
			continue
		}
		configured = append(configured, provider)
	}
	return configured
}

func resolveExplicitFacadeProviderTarget(ctx context.Context, store LLMResolverStore, provider Provider, requestedModel string) (ResolvedTarget, error) {
	if requestedModel != "" {
		target, ok, err := resolveRuntimeLLMProviderTarget(ctx, store, requestedModel, provider.ID)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if ok {
			return target, nil
		}
	}
	return resolveConfiguredFacadeProviderTarget(ctx, store, []Provider{provider}, NormalizeProviderType(provider.ProviderType), requestedModel)
}

func resolveConfiguredFacadeProviderTarget(ctx context.Context, store LLMResolverStore, providers []Provider, providerFamily, requestedModel string) (ResolvedTarget, error) {
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return ResolvedTarget{}, fmt.Errorf("list llm models for facade target: %w", err)
	}
	if len(models) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrRequired, "llm model is required", nil)
	}
	model, provider, wireAPI, ok, err := SelectModelAndProvider(ctx, store, models, providers, requestedModel, providerFamily, "")
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !ok {
		if requestedModel != "" {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured for provider family %q", requestedModel, providerFamily), nil)
		}
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm provider is not configured for provider family %q", providerFamily), nil)
	}
	return resolvedTarget(provider, model, wireAPI)
}

func resolveFacadeEnvironmentTarget(providerFamily, providerID, requestedModel string, sources providerEnvSources) (ResolvedTarget, error) {
	var err error
	providerFamily, err = normalizeFacadeProviderFamily(providerFamily)
	if err != nil {
		return ResolvedTarget{}, err
	}
	providerID = strings.TrimSpace(providerID)
	var environment providerEnvironment
	var provider Provider
	switch providerFamily {
	case ProviderFamilyAnthropic:
		environment = sources.anthropicEnvironment()
		if !environment.configured {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, "anthropic environment is not configured", nil)
		}
		provider = applyAnthropicProviderEnvironment(Provider{ID: providerID, Name: providerID, Scope: ProviderScopeFacadeEnv}, environment)
	case ProviderFamilyOpenAI:
		environment = sources.openAIEnvironment()
		provider = applyOpenAIProviderEnvironment(Provider{ID: providerID, Name: providerID, Scope: ProviderScopeFacadeEnv}, environment)
	}
	modelName := firstNonEmptyTrimmed(requestedModel, environment.model)
	if modelName == "" {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrRequired, "llm model is required", nil)
	}
	provider = NormalizeProviderConfig(provider)
	return resolvedTarget(provider, Model{ID: modelName, Name: modelName, Enabled: true, Scope: ProviderScopeFacadeEnv}, provider.DefaultWireAPI)
}

func normalizeFacadeProviderFamily(providerFamily string) (string, error) {
	providerFamily = NormalizeProviderType(providerFamily)
	switch providerFamily {
	case ProviderFamilyOpenAI, ProviderFamilyAnthropic:
		return providerFamily, nil
	default:
		return "", fmt.Errorf("unsupported llm provider family %q", providerFamily)
	}
}

func resolvedTarget(provider Provider, model Model, wireAPI string) (ResolvedTarget, error) {
	wireAPI = NormalizeWireAPI(wireAPI)
	headers, err := ProviderForwardHeaders(provider)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ResolvedTarget{
		Provider: provider,
		Model:    model,
		WireAPI:  wireAPI,
		Endpoint: EndpointForProvider(provider, wireAPI),
		Headers:  headers,
	}, nil
}

func facadeEnvironmentDefaultProviderID(providerFamily string) string {
	if NormalizeProviderType(providerFamily) == ProviderFamilyAnthropic {
		return ProviderIDDefaultAnthropic
	}
	return ProviderIDDefaultOpenAI
}

func facadeEnvironmentOverrides(providerFamily string, items []domain.SandboxEnvVar) *FacadeEnvironment {
	sources := providerEnvSources{envItemsSource(items)}
	providerFamily = NormalizeProviderType(providerFamily)
	overrides := &FacadeEnvironment{ProviderType: providerFamily}
	if providerFamily == ProviderFamilyAnthropic {
		environment := sources.anthropicEnvironment()
		overrides.Endpoint = environment.endpoint
		overrides.APIKey = environment.apiKey
		if environment.apiKey != "" {
			overrides.AuthHeader = environment.authHeader
			overrides.AuthScheme = environment.authScheme
		}
		return overrides
	}
	environment := sources.openAIEnvironment()
	overrides.Endpoint = environment.endpoint
	overrides.Protocol = environment.protocol
	overrides.APIKey = environment.apiKey
	return overrides
}

func facadeEnvironmentSource(environment FacadeEnvironment) providerEnvSource {
	return func(key string) string {
		switch NormalizeProviderType(environment.ProviderType) {
		case ProviderFamilyAnthropic:
			switch strings.ToUpper(strings.TrimSpace(key)) {
			case "ANTHROPIC_BASE_URL":
				return environment.Endpoint
			case "ANTHROPIC_API_KEY":
				if !strings.EqualFold(environment.AuthHeader, "Authorization") {
					return environment.APIKey
				}
			case "ANTHROPIC_AUTH_TOKEN":
				if strings.EqualFold(environment.AuthHeader, "Authorization") {
					return environment.APIKey
				}
			}
		default:
			switch strings.ToUpper(strings.TrimSpace(key)) {
			case "LLM_API_ENDPOINT":
				return environment.Endpoint
			case "LLM_API_PROTOCOL":
				return environment.Protocol
			case "LLM_API_KEY":
				return environment.APIKey
			}
		}
		return ""
	}
}
