package llms

import (
	"context"
	"fmt"
	"os"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
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

type catalogDefaultStore interface {
	DefaultLLMModelReference(ctx context.Context) (providerID, modelID string, ok bool, err error)
}

// configuredProviderStore exposes provider identities regardless of whether
// their credentials currently make them enabled. Explicit provider/model
// references must be able to distinguish an unavailable provider from an
// unqualified literal model name.
type configuredProviderStore interface {
	HasLLMProvider(ctx context.Context, providerID string) (bool, error)
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
	if !hasCompleteDefaultOpenAIProvider(defaultLLMEnvProviderLookup(ctx, config, store)) {
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
	_, err := EnsureOpenAIEnvProvider(ctx, store, defaultLLMEnvProviderLookup(ctx, config, store), EnvProviderRegistration{
		ProviderID: ProviderIDDefaultOpenAI, Name: "default", Scope: ProviderScopeEnvDefault, RequestedModel: requestedModel, DefaultModel: true,
	})
	return err
}

func resolveLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) (ResolvedTarget, error) {
	return resolveLLMTargetForProviderFamily(ctx, store, llmTargetForProviderFamilyQuery{Config: config, ProviderFamily: ProviderFamilyOpenAI, RequestedModel: requestedModel})
}

func ResolveLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel string) (ResolvedTarget, error) {
	return resolveLLMTarget(ctx, config, store, requestedModel)
}

// RuntimeLLMTargetQuery groups the shared inputs for resolving a runtime LLM
// target: which model/provider was requested, from which session/family, and
// (for the WithEnv variant) the session's own provider env items.
type RuntimeLLMTargetQuery struct {
	Config                  *appconfig.Config
	SessionID               string
	PreferredProviderFamily string
	// ProviderFamilyIsRequired forbids a bare model from resolving against a
	// default connection of another family. Protocol-fixed agents (codex) set it:
	// the runtime bridge cannot present a non-OpenAI connection to them.
	ProviderFamilyIsRequired bool
	RequestedModel           string
	ProviderID               string
	EnvItems                 []domain.SandboxEnvVar
}

func resolveRuntimeLLMTarget(ctx context.Context, store LLMResolverStore, q RuntimeLLMTargetQuery) (ResolvedTarget, error) {
	config, requestedModel, providerID := q.Config, q.RequestedModel, q.ProviderID
	if strings.TrimSpace(providerID) == "" {
		if selectedProvider, selectedModel, ok := SplitProviderModelReference(requestedModel); ok && hasEnabledLLMProviderID(ctx, store, selectedProvider) {
			providerID, requestedModel = selectedProvider, selectedModel
		}
	}
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
	return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{Config: config, RequestedModel: requestedModel, ProviderID: providerID})
}

func ResolveRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, requestedModel, providerID string) (ResolvedTarget, error) {
	return resolveRuntimeLLMTarget(ctx, store, RuntimeLLMTargetQuery{Config: config, RequestedModel: requestedModel, ProviderID: providerID})
}

// providerBootstrapContext groups the routing signals
// bootstrapSessionOrDefaultProviders needs to decide which env-backed
// provider(s), if any, to ensure exist for this request.
type providerBootstrapContext struct {
	Config                       *appconfig.Config
	SessionID                    string
	EnvItems                     []domain.SandboxEnvVar
	RequestedModel               string
	ProviderID                   string
	PreferredProviderFamily      string
	GenericSessionProviderFamily string
	HasSessionEnvProvider        bool
	ExplicitProvider             bool
	DefaultLookup                EnvProviderLookup
}

// bootstrapSessionOrDefaultProviders ensures the OpenAI and/or Anthropic
// env-backed provider(s) implied by bc exist (session-scoped when the
// session supplies its own credentials, otherwise the daemon default),
// unless the request already names a provider that exists. It returns the
// session-scoped provider id chosen along the way, if any.
func bootstrapSessionOrDefaultProviders(ctx context.Context, store LLMResolverStore, bc providerBootstrapContext) (string, error) {
	config, sessionID, envItems, requestedModel, providerID := bc.Config, bc.SessionID, bc.EnvItems, bc.RequestedModel, bc.ProviderID
	preferredProviderFamily, hasSessionEnvProvider, defaultLookup := bc.PreferredProviderFamily, bc.HasSessionEnvProvider, bc.DefaultLookup
	// Skip the env/default bootstrap entirely when the request already names a
	// provider that exists. The facade hot path always passes a concrete
	// provider id from the token scope, so this avoids a redundant pair of
	// idempotent provider upserts on every LLM request.
	bootstrapProviders := !bc.ExplicitProvider && (providerID == "" || !IsSessionEnvProviderID(providerID)) && !hasEnabledLLMProviderID(ctx, store, providerID)
	sessionProviderID := ""
	bootstrapOpenAI := preferredProviderFamily == "" ||
		preferredProviderFamily == ProviderFamilyOpenAI ||
		bc.GenericSessionProviderFamily == ProviderFamilyOpenAI
	if bootstrapProviders && bootstrapOpenAI {
		openAIModel := firstNonEmptyTrimmed(requestedModel, EnvItemValue(envItems, "LLM_MODEL"))
		hasOpenAIEnvProvider := HasOpenAIEnvProviderInput(envItems)
		if hasSessionEnvProvider && hasOpenAIEnvProvider {
			id, err := ensureSessionOpenAIEnvProviderWithConfig(ctx, store, SessionEnvProviderQuery{Config: config, SessionID: sessionID, RequestedModel: openAIModel, EnvItems: envItems})
			if err != nil {
				return "", err
			}
			sessionProviderID = ChooseSessionEnvProviderID(sessionProviderID, id, ProviderFamilyOpenAI, preferredProviderFamily)
		} else if hasCompleteDefaultOpenAIProvider(defaultLookup) && (preferredProviderFamily == ProviderFamilyOpenAI || !hasSessionEnvProvider) {
			if err := ensureDefaultOpenAIEnvProvider(ctx, config, store, openAIModel); err != nil {
				return "", err
			}
		}
	}
	bootstrapAnthropic := preferredProviderFamily == "" ||
		preferredProviderFamily == ProviderFamilyAnthropic ||
		bc.GenericSessionProviderFamily == ProviderFamilyAnthropic
	if bootstrapProviders && bootstrapAnthropic {
		anthropicModel := firstNonEmptyTrimmed(requestedModel, SessionAnthropicEnvModel(envItems))
		hasAnthropicEnvProvider := HasAnthropicEnvProviderInput(envItems)
		if hasSessionEnvProvider && hasAnthropicEnvProvider {
			id, err := ensureSessionAnthropicEnvProviderWithConfig(ctx, store, SessionEnvProviderQuery{Config: config, SessionID: sessionID, RequestedModel: anthropicModel, EnvItems: envItems})
			if err != nil {
				return "", err
			}
			sessionProviderID = ChooseSessionEnvProviderID(sessionProviderID, id, ProviderFamilyAnthropic, preferredProviderFamily)
		} else if hasCompleteDefaultAnthropicProvider(defaultLookup) && (preferredProviderFamily == ProviderFamilyAnthropic || !hasSessionEnvProvider) {
			if err := ensureDefaultAnthropicEnvProvider(ctx, config, store, anthropicModel); err != nil {
				return "", err
			}
		}
	}
	return sessionProviderID, nil
}

// providerModelRefinementInput groups refineProviderAndModelFromReference's inputs.
type providerModelRefinementInput struct {
	HasSessionEnvProvider   bool
	ProviderID              string
	RequestedModel          string
	EnvItems                []domain.SandboxEnvVar
	PreferredProviderFamily string
	DefaultLookup           EnvProviderLookup
}

// refineProviderAndModelFromReference splits a legacy <provider>/<model>
// reference out of requestedModel when no explicit provider was given,
// preferring the session's own env-backed provider when the session
// supplies one. It returns the (possibly updated) providerID/requestedModel
// and whether a provider is now explicit.
func refineProviderAndModelFromReference(ctx context.Context, store LLMResolverStore, in providerModelRefinementInput) (providerID, requestedModel string, explicitProvider bool) {
	providerID, requestedModel = in.ProviderID, in.RequestedModel
	explicitProvider = providerID != ""
	if in.HasSessionEnvProvider && providerID == "" {
		if envModel := SessionEnvModel(in.EnvItems, in.PreferredProviderFamily); envModel != "" {
			requestedModel = envModel
		} else if _, selectedModel, ok := SplitProviderModelReference(requestedModel); ok {
			requestedModel = selectedModel
		}
		return providerID, requestedModel, explicitProvider
	}
	if providerID != "" {
		return providerID, requestedModel, explicitProvider
	}
	if selectedProvider, selectedModel, ok := SplitProviderModelReference(requestedModel); ok {
		switch {
		case legacyReferenceUsesDefaultEnv(selectedProvider, in.DefaultLookup):
			requestedModel = selectedModel
		case hasEnabledLLMProviderID(ctx, store, selectedProvider) || hasConfiguredProviderID(ctx, store, selectedProvider):
			providerID, requestedModel = selectedProvider, selectedModel
			explicitProvider = true
		}
	}
	return providerID, requestedModel, explicitProvider
}

// sessionHasEnvProvider reports whether the session supplies its own
// env-backed provider input for the (legacy-reference-stripped) requested model.
func sessionHasEnvProvider(sessionID, requestedModel string, envItems []domain.SandboxEnvVar) bool {
	if sessionID == "" || !HasSessionEnvProviderInput(envItems) {
		return false
	}
	sessionRequestedModel := requestedModel
	if _, modelID, ok := SplitProviderModelReference(sessionRequestedModel); ok {
		sessionRequestedModel = modelID
	}
	return firstNonEmptyTrimmed(SessionAnthropicEnvModel(envItems), EnvItemValue(envItems, "LLM_MODEL"), sessionRequestedModel) != ""
}

// ResolveRuntimeLLMTargetWithEnv resolves the upstream target for one runtime
// call. It is the single selection stage shared by the facade agents and the
// runtime LLM proxy, and applies this precedence:
//
//  1. an explicit <connection>/<model> reference or provider id selects that
//     connection and passes the literal model through;
//  2. the bootstrap or session environment, then the catalog default, supply a
//     connection and model when the caller names neither;
//  3. a registered model with a provider binding selects its bound connection;
//  4. any remaining literal model resolves against the daemon's default
//     connection, when that connection is unambiguous.
//
// Model bindings are metadata rather than an authorization boundary, so steps 3
// and 4 accept models that are absent from the model catalog.
func ResolveRuntimeLLMTargetWithEnv(ctx context.Context, store LLMResolverStore, q RuntimeLLMTargetQuery) (ResolvedTarget, error) {
	config, requestedModel, providerID, envItems := q.Config, q.RequestedModel, q.ProviderID, q.EnvItems
	sessionID := strings.TrimSpace(q.SessionID)
	preferredProviderFamily := NormalizeOptionalProviderType(q.PreferredProviderFamily)
	requestedModel = strings.TrimSpace(requestedModel)
	providerID = strings.TrimSpace(providerID)
	// A reference that leaves a side empty is a typo in every agent, not only in
	// the facade agents that parse the value themselves. Rejecting it here keeps
	// codex and claude from forwarding an impossible model to an upstream.
	if _, _, err := SplitModelReference(requestedModel); err != nil {
		return ResolvedTarget{}, err
	}
	hasSessionEnvProvider := sessionHasEnvProvider(sessionID, requestedModel, envItems)
	defaultLookup := defaultLLMEnvProviderLookup(ctx, config, store)
	providerID, requestedModel, explicitProvider := refineProviderAndModelFromReference(ctx, store, providerModelRefinementInput{
		HasSessionEnvProvider: hasSessionEnvProvider, ProviderID: providerID, RequestedModel: requestedModel, EnvItems: envItems,
		PreferredProviderFamily: preferredProviderFamily, DefaultLookup: defaultLookup,
	})
	genericSessionProviderFamily := ""
	if sessionID != "" {
		genericSessionProviderFamily = genericLLMEnvProviderFamily(envItems)
	}
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
	sessionProviderID, err := bootstrapSessionOrDefaultProviders(ctx, store, providerBootstrapContext{
		Config: config, SessionID: sessionID, EnvItems: envItems, RequestedModel: requestedModel, ProviderID: providerID,
		PreferredProviderFamily: preferredProviderFamily, GenericSessionProviderFamily: genericSessionProviderFamily,
		HasSessionEnvProvider: hasSessionEnvProvider, ExplicitProvider: explicitProvider, DefaultLookup: defaultLookup,
	})
	if err != nil {
		return ResolvedTarget{}, err
	}
	if explicitProvider && !hasEnabledLLMProviderID(ctx, store, providerID) {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm provider %q is not configured", providerID), nil)
	}
	providerID = firstNonEmptyTrimmed(providerID, sessionProviderID)
	if providerID == "" && !hasSessionEnvProvider {
		defaultModel := firstNonEmptyTrimmed(defaultLookup("LLM_MODEL"), defaultLookup("ANTHROPIC_MODEL", "CLAUDE_MODEL"))
		selectedModel := firstNonEmptyTrimmed(requestedModel, defaultModel)
		switch {
		case selectedModel != "" && preferredProviderFamily == ProviderFamilyAnthropic && hasCompleteDefaultAnthropicProvider(defaultLookup):
			providerID, requestedModel = ProviderIDDefaultAnthropic, selectedModel
		case selectedModel != "" && preferredProviderFamily == ProviderFamilyOpenAI && hasCompleteDefaultOpenAIProvider(defaultLookup):
			providerID, requestedModel = ProviderIDDefaultOpenAI, selectedModel
		case selectedModel != "" && NormalizeWireAPI(defaultLookup("LLM_API_PROTOCOL")) == APIProtocolMessages && hasCompleteDefaultAnthropicProvider(defaultLookup):
			providerID, requestedModel = ProviderIDDefaultAnthropic, selectedModel
		case selectedModel != "" && hasCompleteDefaultOpenAIProvider(defaultLookup):
			providerID, requestedModel = ProviderIDDefaultOpenAI, selectedModel
		}
	}
	if providerID == "" && requestedModel == "" {
		if defaults, ok := store.(catalogDefaultStore); ok {
			selectedProvider, selectedModel, found, err := defaults.DefaultLLMModelReference(ctx)
			if err != nil {
				return ResolvedTarget{}, err
			}
			if found {
				providerID, requestedModel = selectedProvider, selectedModel
			}
		}
	}
	// Provider model declarations add behavior; they do not restrict literal IDs.
	if providerID != "" && requestedModel != "" {
		target, ok, err := resolveRuntimeLLMProviderTarget(ctx, store, requestedModel, providerID)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if ok {
			return target, nil
		}
	}
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	if len(models) > 0 {
		model, provider, wireAPI, ok, err := SelectModelAndProvider(ctx, store, ModelProviderSelection{Models: models, Providers: providers, RequestedModel: requestedModel, ProviderFamily: preferredProviderFamily, ProviderID: providerID})
		if err != nil {
			return ResolvedTarget{}, err
		}
		if ok {
			return BuildResolvedTarget(ctx, store, ResolvedTargetInput{Provider: provider, Model: model, WireAPI: wireAPI})
		}
	}
	// Model/provider declarations are optional metadata, not an authorization
	// boundary: a model that is not registered still resolves against the
	// daemon's default connection. An explicit provider prefix already pinned a
	// connection above, so only the provider-less case falls back.
	if providerID == "" {
		target, err := resolveLiteralModelTarget(ctx, store, requestedModel, preferredProviderFamily, q.ProviderFamilyIsRequired)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if target != nil {
			return *target, nil
		}
	}
	if requestedModel == "" {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrRequired, "llm model is required", nil)
	}
	if providerID != "" {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured for provider %q", requestedModel, providerID), nil)
	}
	return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured", requestedModel), nil)
}

func hasCompleteDefaultOpenAIProvider(lookup EnvProviderLookup) bool {
	if lookup == nil || NormalizeWireAPI(lookup("LLM_API_PROTOCOL")) == APIProtocolMessages {
		return false
	}
	return firstNonEmptyTrimmed(lookup("LLM_API_KEY", "OPENAI_API_KEY")) != ""
}

func hasCompleteDefaultAnthropicProvider(lookup EnvProviderLookup) bool {
	if lookup == nil {
		return false
	}
	if firstNonEmptyTrimmed(lookup("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")) != "" {
		return true
	}
	if strings.TrimSpace(lookup("LLM_API_KEY")) == "" {
		return false
	}
	return NormalizeWireAPI(lookup("LLM_API_PROTOCOL")) == APIProtocolMessages || firstNonEmptyTrimmed(
		lookup("ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT"), lookup("ANTHROPIC_MODEL", "CLAUDE_MODEL"),
	) != ""
}

func legacyReferenceUsesDefaultEnv(providerID string, lookup EnvProviderLookup) bool {
	switch strings.TrimSpace(providerID) {
	case "openai":
		return hasCompleteDefaultOpenAIProvider(lookup)
	case "anthropic":
		return hasCompleteDefaultAnthropicProvider(lookup)
	default:
		return false
	}
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
	_, err := ensureAnthropicEnvProvider(ctx, store, lookup, anthropicEnvProviderInput{
		Credential: credential,
		EnvProviderRegistration: EnvProviderRegistration{
			ProviderID: ProviderIDDefaultAnthropic, Name: "anthropic", Scope: ProviderScopeEnvDefault, RequestedModel: requestedModel,
		},
	})
	return err
}

func ensureSessionOpenAIEnvProviderWithConfig(ctx context.Context, store LLMResolverStore, in SessionEnvProviderQuery) (string, error) {
	sessionID, requestedModel, envItems := in.SessionID, in.RequestedModel, in.EnvItems
	providerID := SessionEnvProviderID(sessionID, ProviderFamilyOpenAI)
	return EnsureOpenAIEnvProvider(ctx, store, sessionLLMEnvProviderLookup(envItems), EnvProviderRegistration{
		ProviderID: providerID, Name: providerID, Scope: ProviderScopeSessionEnv, RequestedModel: requestedModel,
	})
}

func ensureSessionAnthropicEnvProvider(ctx context.Context, store LLMResolverStore, q SessionEnvProviderQuery) (string, error) {
	return ensureSessionAnthropicEnvProviderWithConfig(ctx, store, q)
}

// SessionEnvProviderQuery groups ensureSessionAnthropicEnvProviderWithConfig's inputs.
type SessionEnvProviderQuery struct {
	Config         *appconfig.Config
	SessionID      string
	RequestedModel string
	EnvItems       []domain.SandboxEnvVar
}

func ensureSessionAnthropicEnvProviderWithConfig(ctx context.Context, store LLMResolverStore, in SessionEnvProviderQuery) (string, error) {
	sessionID, requestedModel, envItems := in.SessionID, in.RequestedModel, in.EnvItems
	providerID := SessionEnvProviderID(sessionID, ProviderFamilyAnthropic)
	lookup := sessionLLMEnvProviderLookup(envItems)
	credential, _ := anthropicCredentialFromItems(envItems)
	return ensureAnthropicEnvProvider(ctx, store, lookup, anthropicEnvProviderInput{
		Credential: credential,
		EnvProviderRegistration: EnvProviderRegistration{
			ProviderID: providerID, Name: providerID, Scope: ProviderScopeSessionEnv, RequestedModel: requestedModel,
		},
	})
}

func EnsureSessionAnthropicEnvProvider(ctx context.Context, store LLMResolverStore, q SessionEnvProviderQuery) (string, error) {
	return ensureSessionAnthropicEnvProvider(ctx, store, q)
}

func hasEnabledLLMProviderID(ctx context.Context, store LLMResolverStore, providerID string) bool {
	return HasEnabledProviderID(ctx, store, providerID)
}

func hasConfiguredProviderID(ctx context.Context, store LLMResolverStore, providerID string) bool {
	configured, ok := store.(configuredProviderStore)
	if !ok {
		return false
	}
	found, err := configured.HasLLMProvider(ctx, strings.TrimSpace(providerID))
	return err == nil && found
}

func HasEnabledLLMProviderID(ctx context.Context, store LLMResolverStore, providerID string) bool {
	return hasEnabledLLMProviderID(ctx, store, providerID)
}

func hasConfiguredLLMProviderForFamily(ctx context.Context, store LLMResolverStore, providerFamily string) bool {
	return HasConfiguredProviderForFamily(ctx, store, providerFamily)
}

// llmTargetForProviderFamilyQuery groups resolveLLMTargetForProviderFamily's inputs.
type llmTargetForProviderFamilyQuery struct {
	Config         *appconfig.Config
	ProviderFamily string
	RequestedModel string
}

func resolveLLMTargetForProviderFamily(ctx context.Context, store LLMResolverStore, q llmTargetForProviderFamilyQuery) (ResolvedTarget, error) {
	config, providerFamily, requestedModel := q.Config, q.ProviderFamily, q.RequestedModel
	if _, _, err := SplitModelReference(requestedModel); err != nil {
		return ResolvedTarget{}, err
	}
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
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(providers) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, "llm provider is not configured", nil)
	}
	if len(models) > 0 {
		model, provider, wireAPI, ok, err := SelectModelAndProvider(ctx, store, ModelProviderSelection{Models: models, Providers: providers, RequestedModel: requestedModel, ProviderFamily: providerFamily})
		if err != nil {
			return ResolvedTarget{}, err
		}
		if ok {
			endpoint := EndpointForProvider(provider, wireAPI)
			headers, err := ProviderForwardHeaders(provider)
			if err != nil {
				return ResolvedTarget{}, err
			}
			return ResolvedTarget{Provider: provider, Model: model, WireAPI: wireAPI, Endpoint: endpoint, Headers: headers}, nil
		}
	}
	// As in ResolveRuntimeLLMTargetWithEnv, an unregistered model resolves
	// against the default connection for the family instead of failing on a
	// missing model-catalog row. This path states the family explicitly, so it
	// keeps the cross-family default the runtime bridge allows.
	if target, err := resolveLiteralModelTarget(ctx, store, requestedModel, providerFamily, false); err != nil {
		return ResolvedTarget{}, err
	} else if target != nil {
		return *target, nil
	}
	if strings.TrimSpace(requestedModel) == "" && len(models) == 0 {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrRequired, "llm model is required", nil)
	}
	if strings.TrimSpace(requestedModel) != "" {
		return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm model %q is not configured for provider family %q", strings.TrimSpace(requestedModel), providerFamily), nil)
	}
	return ResolvedTarget{}, domain.ClassifyError(domain.ErrFailedPrecondition, fmt.Sprintf("llm provider is not configured for provider family %q", providerFamily), nil)
}

func ResolveLLMTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store LLMResolverStore, providerFamily, requestedModel string) (ResolvedTarget, error) {
	return resolveLLMTargetForProviderFamily(ctx, store, llmTargetForProviderFamilyQuery{Config: config, ProviderFamily: providerFamily, RequestedModel: requestedModel})
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
