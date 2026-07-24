package llms

import (
	"context"
	"fmt"
	"os"
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

// EnvProviderLookup resolves an environment value for LLM provider bootstrap.
// It accepts candidate keys and returns the first non-empty value scanning
// sources in priority order (source-major): an earlier source wins across all
// candidate keys before a later source is consulted. This preserves the exact
// precedence the bootstrap paths relied on when they used nested firstNonEmpty.
// defaultLLMEnvProviderLookup reads from global env, then the process env, then
// daemon config. Used by the env_default bootstrap providers.
func defaultLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store LLMResolverStore) EnvProviderLookup {
	return func(keys ...string) string {
		for _, key := range keys {
			if v := lookupEnvValue(ctx, store, key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		for _, key := range keys {
			if v := os.Getenv(key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		for _, key := range keys {
			if v := configLLMEnvValue(config, key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
}

func DefaultLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store LLMResolverStore) EnvProviderLookup {
	return defaultLLMEnvProviderLookup(ctx, config, store)
}

// sessionLLMEnvProviderLookup reads only from per-session env items. Used by the
// session_env bootstrap providers.
func sessionLLMEnvProviderLookup(envItems []domain.SandboxEnvVar) EnvProviderLookup {
	return func(keys ...string) string {
		for _, key := range keys {
			if v := EnvItemValue(envItems, key); strings.TrimSpace(v) != "" {
				return v
			}
		}
		return ""
	}
}

// layeredLLMEnvProviderLookup resolves the sandbox-owned layer before the
// default Global Env/process/config layers. Candidate aliases are exhausted
// within a layer before falling through, preserving source-major precedence.
func layeredLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store LLMResolverStore, envItems []domain.SandboxEnvVar) EnvProviderLookup {
	sessionLookup := sessionLLMEnvProviderLookup(envItems)
	defaultLookup := defaultLLMEnvProviderLookup(ctx, config, store)
	return func(keys ...string) string {
		if value := sessionLookup(keys...); strings.TrimSpace(value) != "" {
			return value
		}
		return defaultLookup(keys...)
	}
}

func configLLMEnvValue(config *appconfig.Config, key string) string {
	if config == nil {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "LLM_API_ENDPOINT":
		return config.LLMAPIEndpoint
	case "LLM_API_PROTOCOL":
		return config.LLMAPIProtocol
	case "LLM_API_KEY":
		return config.LLMAPIKey
	case "LLM_MODEL":
		return config.LLMModel
	default:
		return ""
	}
}

func ensureDefaultOpenAIEnvProvider(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) error {
	_, err := EnsureOpenAIEnvProvider(ctx, store, defaultLLMEnvProviderLookup(ctx, config, store), ProviderIDDefaultOpenAI, "default", ProviderScopeEnvDefault, requestedModel, true)
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
	hasSessionEnvProvider := sessionID != "" && HasSessionEnvProviderInput(envItems)
	genericSessionProviderFamily := ""
	if sessionID != "" {
		genericSessionProviderFamily = genericLLMEnvProviderFamily(envItems)
	}
	sessionProviderID := ""
	// Reuse an already-persisted session-env provider when this session can no
	// longer supply a key from env. The raw key env (Session.ProviderEnvItems) is
	// intentionally not persisted, so after a stop/resume the only durable home
	// for a session-scoped credential is the llm_provider row written at creation.
	// Pin its provider id here so resolution selects it (session-env providers are
	// otherwise skipped without an explicit id) and does not clobber its key with
	// the now-empty env. Only when the env still has no key for the family — an env
	// that carries a (possibly rotated) key must keep re-bootstrapping it.
	if providerID == "" && sessionID != "" && preferredProviderFamily != "" && !EnvHasProviderKeyForFamily(envItems, preferredProviderFamily) {
		if candidate := SessionEnvProviderID(sessionID, preferredProviderFamily); hasEnabledLLMProviderID(ctx, store, candidate) {
			providerID = candidate
		}
	}
	// Skip the env/default bootstrap entirely when the request already names a
	// provider that exists. The facade hot path always passes a concrete
	// provider id from the token scope, so this avoids a redundant pair of
	// idempotent provider upserts on every LLM request.
	bootstrapProviders := (providerID == "" || !IsSessionEnvProviderID(providerID)) && !hasEnabledLLMProviderID(ctx, store, providerID)
	bootstrapOpenAI := preferredProviderFamily == "" ||
		preferredProviderFamily == ProviderFamilyOpenAI ||
		genericSessionProviderFamily == ProviderFamilyOpenAI
	if bootstrapProviders && bootstrapOpenAI && !hasConfiguredLLMProviderForFamily(ctx, store, ProviderFamilyOpenAI) {
		openAIModel := firstNonEmptyTrimmed(requestedModel, EnvItemValue(envItems, "LLM_MODEL"))
		hasOpenAIEnvProvider := HasOpenAIEnvProviderInput(envItems)
		if sessionID != "" && hasOpenAIEnvProvider {
			id, err := ensureSessionOpenAIEnvProviderWithConfig(ctx, config, store, sessionID, openAIModel, envItems)
			if err != nil {
				return ResolvedTarget{}, err
			}
			sessionProviderID = ChooseSessionEnvProviderID(sessionProviderID, id, ProviderFamilyOpenAI, preferredProviderFamily)
		} else if preferredProviderFamily == ProviderFamilyOpenAI || !hasSessionEnvProvider {
			if err := ensureDefaultOpenAIEnvProvider(ctx, config, store, openAIModel); err != nil {
				return ResolvedTarget{}, err
			}
		}
	}
	bootstrapAnthropic := preferredProviderFamily == "" ||
		preferredProviderFamily == ProviderFamilyAnthropic ||
		genericSessionProviderFamily == ProviderFamilyAnthropic
	if bootstrapProviders && bootstrapAnthropic && !hasConfiguredLLMProviderForFamily(ctx, store, ProviderFamilyAnthropic) {
		anthropicModel := firstNonEmptyTrimmed(requestedModel, SessionAnthropicEnvModel(envItems))
		hasAnthropicEnvProvider := HasAnthropicEnvProviderInput(envItems)
		if sessionID != "" && hasAnthropicEnvProvider {
			id, err := ensureSessionAnthropicEnvProviderWithConfig(ctx, config, store, sessionID, anthropicModel, envItems)
			if err != nil {
				return ResolvedTarget{}, err
			}
			sessionProviderID = ChooseSessionEnvProviderID(sessionProviderID, id, ProviderFamilyAnthropic, preferredProviderFamily)
		} else if preferredProviderFamily == ProviderFamilyAnthropic || !hasSessionEnvProvider {
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
	lookup := defaultLLMEnvProviderLookup(ctx, config, store)
	if !hasDefaultAnthropicEnvProviderInput(lookup) {
		return nil
	}
	credential := layeredAnthropicCredential(ctx, config, store, nil)
	_, err := ensureAnthropicEnvProvider(ctx, store, lookup, credential, ProviderIDDefaultAnthropic, "anthropic", ProviderScopeEnvDefault, requestedModel, false)
	return err
}

func ensureSessionOpenAIEnvProvider(ctx context.Context, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	return ensureSessionOpenAIEnvProviderWithConfig(ctx, nil, store, sessionID, requestedModel, envItems)
}

func ensureSessionOpenAIEnvProviderWithConfig(ctx context.Context, config *appconfig.Config, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	providerID := SessionEnvProviderID(sessionID, ProviderFamilyOpenAI)
	return EnsureOpenAIEnvProvider(ctx, store, layeredLLMEnvProviderLookup(ctx, config, store, envItems), providerID, providerID, ProviderScopeSessionEnv, requestedModel, false)
}

func EnsureSessionOpenAIEnvProvider(ctx context.Context, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	return ensureSessionOpenAIEnvProvider(ctx, store, sessionID, requestedModel, envItems)
}

func ensureSessionAnthropicEnvProvider(ctx context.Context, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	return ensureSessionAnthropicEnvProviderWithConfig(ctx, nil, store, sessionID, requestedModel, envItems)
}

func ensureSessionAnthropicEnvProviderWithConfig(ctx context.Context, config *appconfig.Config, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	providerID := SessionEnvProviderID(sessionID, ProviderFamilyAnthropic)
	lookup := layeredLLMEnvProviderLookup(ctx, config, store, envItems)
	credential := layeredAnthropicCredential(ctx, config, store, envItems)
	return ensureAnthropicEnvProvider(ctx, store, lookup, credential, providerID, providerID, ProviderScopeSessionEnv, requestedModel, false)
}

func EnsureSessionAnthropicEnvProvider(ctx context.Context, store LLMResolverStore, sessionID, requestedModel string, envItems []domain.SandboxEnvVar) (string, error) {
	return ensureSessionAnthropicEnvProvider(ctx, store, sessionID, requestedModel, envItems)
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

func lookupEnvValue(ctx context.Context, store LLMResolverStore, key string) string {
	if store == nil {
		return ""
	}
	items, err := store.ListGlobalEnv(ctx)
	if err != nil {
		return ""
	}
	return EnvItemValue(items, key)
}

func LookupEnvValue(ctx context.Context, store LLMResolverStore, key string) string {
	return lookupEnvValue(ctx, store, key)
}
