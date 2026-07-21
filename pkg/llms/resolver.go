package llms

import (
	"context"
	"fmt"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

// LLMResolverStore is the persistence surface the LLM target-resolution and
// provider-bootstrap logic needs. *configstore.ConfigStore satisfies it. Keeping
// the dependency as a locally-defined interface (rather than importing
// configstore) keeps llms free of any storage dependency and avoids an import
// cycle, while letting the resolution logic live in its own domain package.
type LLMResolverStore interface {
	DefaultConfigStore
	ProviderListStore
	ProviderModelWireAPIStore
	GlobalEnvStore
	ListEnabledLLMModels(ctx context.Context) ([]Model, error)
}

// firstNonEmptyTrimmed returns the first value that is non-empty after trimming,
// returning the trimmed form. It is intentionally distinct from firstNonEmpty
// (which returns the raw value) to preserve the exact trimming behavior the LLM
// resolution paths relied on before this logic moved out of the config store.
func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func bootstrapDefaultLLMConfig(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) error {
	if hasConfiguredLLMProviderForFamily(ctx, store, ProviderFamilyOpenAI) {
		return nil
	}
	return ensureDefaultOpenAIEnvProvider(ctx, config, store, requestedModel)
}

func BootstrapDefaultLLMConfig(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) error {
	return bootstrapDefaultLLMConfig(ctx, config, store, requestedModel)
}

func defaultLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store LLMResolverStore) (EnvProviderLookup, error) {
	sources, err := defaultProviderEnvSources(ctx, config, store)
	if err != nil {
		return nil, err
	}
	return providerEnvironmentLookup(sources), nil
}

func DefaultLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store LLMResolverStore) (EnvProviderLookup, error) {
	return defaultLLMEnvProviderLookup(ctx, config, store)
}

func ensureDefaultOpenAIEnvProvider(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) error {
	sources, err := defaultProviderEnvSources(ctx, config, store)
	if err != nil {
		return err
	}
	environment := sources.openAIEnvironment()
	_, err = ensureOpenAIProviderEnvironment(ctx, store, environment, ProviderIDDefaultOpenAI, "default", ProviderScopeEnvDefault, requestedModel, true)
	return err
}

func resolveLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) (ResolvedTarget, error) {
	return resolveLLMTargetForProviderFamily(ctx, config, store, ProviderFamilyOpenAI, requestedModel)
}

func ResolveLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) (ResolvedTarget, error) {
	return resolveLLMTarget(ctx, config, store, requestedModel)
}

func resolveRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel, providerID string) (ResolvedTarget, error) {
	// Preserve strict model/provider resolution for legacy providerless tokens
	// and callers that need the configured default. Only a concrete provider and
	// requested model opt into runtime facade model passthrough.
	if strings.TrimSpace(providerID) != "" && strings.TrimSpace(requestedModel) != "" {
		target, ok, err := resolveRuntimeLLMProviderTarget(ctx, store, requestedModel, providerID)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if ok {
			return target, nil
		}
	}
	return resolveRuntimeLLMTargetWithEnv(ctx, config, store, "", "", requestedModel, providerID, nil)
}

func ResolveRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel, providerID string) (ResolvedTarget, error) {
	return resolveRuntimeLLMTarget(ctx, config, store, requestedModel, providerID)
}

func resolveRuntimeLLMTargetWithEnv(ctx context.Context, config *appconfig.Config, store LLMResolverStore, sessionID, preferredProviderFamily, requestedModel, providerID string, envItems []domain.SandboxEnvVar) (ResolvedTarget, error) {
	sessionID = strings.TrimSpace(sessionID)
	preferredProviderFamily = NormalizeOptionalProviderType(preferredProviderFamily)
	requestedModel = strings.TrimSpace(requestedModel)
	providerID = strings.TrimSpace(providerID)
	providerEnvSources, sandboxEnvItems, err := layeredProviderEnvSources(ctx, config, store, envItems)
	if err != nil {
		return ResolvedTarget{}, err
	}
	sessionProviderID := ""
	// Skip the env/default bootstrap entirely when the request already names a
	// provider that exists. The facade hot path always passes a concrete
	// provider id from the token scope, so this avoids a redundant pair of
	// idempotent provider upserts on every LLM request.
	bootstrapProviders := (providerID == "" || !IsSessionEnvProviderID(providerID)) && !hasEnabledLLMProviderID(ctx, store, providerID)
	bootstrapOpenAI := preferredProviderFamily == "" || preferredProviderFamily == ProviderFamilyOpenAI
	if bootstrapProviders && bootstrapOpenAI && !hasConfiguredLLMProviderForFamily(ctx, store, ProviderFamilyOpenAI) {
		openAIModel := firstNonEmptyTrimmed(requestedModel, EnvItemValue(sandboxEnvItems, "LLM_MODEL"))
		if sessionID != "" && HasOpenAIEnvProviderInput(sandboxEnvItems) {
			id, err := ensureSessionOpenAIProviderEnvironment(ctx, store, sessionID, openAIModel, providerEnvSources.openAIEnvironment())
			if err != nil {
				return ResolvedTarget{}, err
			}
			sessionProviderID = ChooseSessionEnvProviderID(sessionProviderID, id, ProviderFamilyOpenAI, preferredProviderFamily)
		} else {
			if err := ensureDefaultOpenAIEnvProvider(ctx, config, store, openAIModel); err != nil {
				return ResolvedTarget{}, err
			}
		}
	}
	bootstrapAnthropic := preferredProviderFamily == "" || preferredProviderFamily == ProviderFamilyAnthropic
	if bootstrapProviders && bootstrapAnthropic && !hasConfiguredLLMProviderForFamily(ctx, store, ProviderFamilyAnthropic) {
		anthropicModel := firstNonEmptyTrimmed(requestedModel, SessionAnthropicEnvModel(sandboxEnvItems))
		if sessionID != "" && HasAnthropicEnvProviderInput(sandboxEnvItems) {
			id, err := ensureSessionAnthropicProviderEnvironment(ctx, store, sessionID, anthropicModel, providerEnvSources.anthropicEnvironment())
			if err != nil {
				return ResolvedTarget{}, err
			}
			sessionProviderID = ChooseSessionEnvProviderID(sessionProviderID, id, ProviderFamilyAnthropic, preferredProviderFamily)
		} else {
			if err := ensureDefaultAnthropicEnvProvider(ctx, config, store, anthropicModel); err != nil {
				return ResolvedTarget{}, err
			}
		}
	}
	providerID = firstNonEmptyTrimmed(providerID, sessionProviderID)
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(models) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrRequired, "llm model is required", nil)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	model, provider, wireAPI, ok, err := SelectModelAndProvider(ctx, store, models, providers, requestedModel, preferredProviderFamily, providerID)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !ok {
		if requestedModel != "" && providerID != "" {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured for provider %q", requestedModel, providerID), nil)
		}
		if requestedModel != "" {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured", requestedModel), nil)
		}
		if providerID != "" {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm provider %q is not configured", providerID), nil)
		}
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	endpoint := EndpointForProvider(provider, wireAPI)
	headers, err := ProviderForwardHeaders(provider)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ResolvedTarget{Provider: provider, Model: model, WireAPI: wireAPI, Endpoint: endpoint, Headers: headers}, nil
}

func ResolveRuntimeLLMTargetWithEnv(ctx context.Context, config *appconfig.Config, store LLMResolverStore, sessionID, preferredProviderFamily, requestedModel, providerID string, envItems []domain.SandboxEnvVar) (ResolvedTarget, error) {
	return resolveRuntimeLLMTargetWithEnv(ctx, config, store, sessionID, preferredProviderFamily, requestedModel, providerID, envItems)
}

func bootstrapAnthropicLLMConfig(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) error {
	if hasConfiguredLLMProviderForFamily(ctx, store, ProviderFamilyAnthropic) {
		return nil
	}
	return ensureDefaultAnthropicEnvProvider(ctx, config, store, requestedModel)
}

func BootstrapAnthropicLLMConfig(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) error {
	return bootstrapAnthropicLLMConfig(ctx, config, store, requestedModel)
}

func ensureDefaultAnthropicEnvProvider(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) error {
	sources, err := defaultProviderEnvSources(ctx, config, store)
	if err != nil {
		return err
	}
	environment := sources.anthropicEnvironment()
	_, err = ensureAnthropicProviderEnvironment(ctx, store, environment, ProviderIDDefaultAnthropic, "anthropic", ProviderScopeEnvDefault, requestedModel, false)
	return err
}

func ensureSessionOpenAIProviderEnvironment(ctx context.Context, store LLMResolverStore, sessionID, requestedModel string, environment providerEnvironment) (string, error) {
	providerID := SessionEnvProviderID(sessionID, ProviderFamilyOpenAI)
	return ensureOpenAIProviderEnvironment(ctx, store, environment, providerID, providerID, ProviderScopeSessionEnv, requestedModel, false)
}

// EnsureSessionOpenAIEnvProviderWithConfig resolves each session provider field
// from sandbox Provider Env, Global Env, then daemon process/config values.
func EnsureSessionOpenAIEnvProviderWithConfig(ctx context.Context, config *appconfig.Config, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	sources, _, err := layeredProviderEnvSources(ctx, config, store, envItems)
	if err != nil {
		return "", err
	}
	return ensureSessionOpenAIProviderEnvironment(ctx, store, sessionID, requestedModel, sources.openAIEnvironment())
}

func ensureSessionAnthropicProviderEnvironment(ctx context.Context, store LLMResolverStore, sessionID, requestedModel string, environment providerEnvironment) (string, error) {
	providerID := SessionEnvProviderID(sessionID, ProviderFamilyAnthropic)
	return ensureAnthropicProviderEnvironment(ctx, store, environment, providerID, providerID, ProviderScopeSessionEnv, requestedModel, false)
}

func EnsureSessionAnthropicEnvProvider(ctx context.Context, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	sources, _, err := layeredProviderEnvSources(ctx, nil, store, envItems)
	if err != nil {
		return "", err
	}
	return ensureSessionAnthropicProviderEnvironment(ctx, store, sessionID, requestedModel, sources.anthropicEnvironment())
}

func hasEnabledLLMProviderID(ctx context.Context, store LLMResolverStore, providerID string) bool {
	return HasEnabledProviderID(ctx, store, providerID)
}

func HasEnabledLLMProviderID(ctx context.Context, store LLMResolverStore, providerID string) bool {
	return hasEnabledLLMProviderID(ctx, store, providerID)
}

func hasConfiguredLLMProviderForFamily(ctx context.Context, store LLMResolverStore, providerFamily string) bool {
	return HasConfiguredProviderForFamily(ctx, store, providerFamily)
}

func resolveLLMTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store LLMResolverStore, providerFamily, requestedModel string) (ResolvedTarget, error) {
	if strings.TrimSpace(providerFamily) != "" {
		providerFamily = NormalizeProviderType(providerFamily)
	}
	switch providerFamily {
	case ProviderFamilyAnthropic:
		if err := bootstrapAnthropicLLMConfig(ctx, config, store, strings.TrimSpace(requestedModel)); err != nil {
			return ResolvedTarget{}, err
		}
	default:
		if err := bootstrapDefaultLLMConfig(ctx, config, store, strings.TrimSpace(requestedModel)); err != nil {
			return ResolvedTarget{}, err
		}
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(models) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrRequired, "llm model is required", nil)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	model, provider, wireAPI, ok, err := SelectModelAndProvider(ctx, store, models, providers, requestedModel, providerFamily, "")
	if err != nil {
		return ResolvedTarget{}, err
	}
	if !ok {
		if strings.TrimSpace(requestedModel) != "" {
			return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured for provider family %q", strings.TrimSpace(requestedModel), providerFamily), nil)
		}
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm provider is not configured for provider family %q", providerFamily), nil)
	}
	endpoint := EndpointForProvider(provider, wireAPI)
	headers, err := ProviderForwardHeaders(provider)
	if err != nil {
		return ResolvedTarget{}, err
	}
	return ResolvedTarget{Provider: provider, Model: model, WireAPI: wireAPI, Endpoint: endpoint, Headers: headers}, nil
}

func ResolveLLMTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store LLMResolverStore, providerFamily, requestedModel string) (ResolvedTarget, error) {
	return resolveLLMTargetForProviderFamily(ctx, config, store, providerFamily, requestedModel)
}
