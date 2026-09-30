package runtimefacade

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// globalEnvStore is implemented by the daemon config store. Global provider
// variables are deliberately read only to decide which compatibility aliases
// are needed; the values themselves are imported into a daemon-side connection
// and never returned to the guest.
type globalEnvStore interface {
	ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error)
}

// startupFacadeFamilies is the fixed pair of provider families the startup
// compatibility facade publishes.
//
// Which family an image reads is a property of the image's entrypoint, not of
// the agent the daemon selected: a sandbox whose agent is codex can run an image
// whose entrypoint reads ANTHROPIC_API_KEY, and a facade keyed on the selected
// agent leaves that image without the variables it needs to start. Both families
// are therefore resolved independently, and one is skipped only when it has no
// enabled provider or no model to bind.
//
// Publishing both is deliberate and widens what a sandbox start does. Each
// family gets its own token, so a run mints one it will not use, and a
// credential declared for the family the selected agent does not address is
// imported as a daemon connection too. This facade's tokens record no model, so
// the upstream each one names decides which models it serves — see
// llms.FacadeToken.ResolveUpstreamModel. The managed path is unaffected: the
// selected agent's own configuration overwrites its family's names, and that is
// what a run the daemon configures actually reads.
var startupFacadeFamilies = []string{llms.ProviderFamilyAnthropic, llms.ProviderFamilyOpenAI}

// EnsureSessionStartupFacadeConfig restores the provider-specific startup
// compatibility facade used by older guest images. New images use the common
// LLM_* variables emitted by EnsureSessionAgentRuntimeConfig; older images
// still need provider-specific tokens and endpoints, one set per provider
// family, because the image's entrypoint decides which family it reads and the
// daemon does not configure that image. See startupFacadeFamilies.
//
// The returned variables are published before the managed environment, so the
// selected agent's own configuration overwrites every name it writes. Only the
// names an agent does not write survive: for an agent like dsh that publishes no
// provider-specific name, this facade's whole set does, which is its purpose. A
// dialect writer that publishes one family name therefore publishes that
// family's whole set — see llms.ProviderFamilyEnv.
//
// The request's Agent and Model fields are read only to resolve each family's
// model, never to choose the families: those are fixed (see
// startupFacadeFamilies) because the image, not the selected agent, decides
// which family it reads.
func EnsureSessionStartupFacadeConfig(ctx context.Context, req SessionFacadeConfigRequest) (map[string]string, error) {
	if req.Config == nil || req.Store == nil || req.Session == nil {
		return nil, nil
	}
	baseURL := strings.TrimRight(strings.TrimSpace(llms.GuestRuntimeBaseURL(req.Config, req.Session)), "/")
	if baseURL == "" {
		return nil, nil
	}

	providerEnv := append([]domain.SandboxEnvVar(nil), req.AgentEnv...)
	if globals, ok := req.Store.(globalEnvStore); ok {
		items, err := globals.ListGlobalEnv(ctx)
		if err != nil {
			return nil, fmt.Errorf("load global environment for startup facade: %w", err)
		}
		providerEnv = domain.MergeEnvItems(items, providerEnv)
	}
	// Both families are imported, because both are published: an image may read
	// either one's credential. A declaration that names no model is skipped
	// inside, so a credential that cannot be bound to a model does not become a
	// connection.
	if err := ensureDeclaredStartupProviders(ctx, req.Store, req, providerEnv, startupFacadeFamilies); err != nil {
		return nil, err
	}

	providers, err := req.Store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list startup facade providers: %w", err)
	}
	env := make(map[string]string)
	for _, family := range startupFacadeFamilies {
		provider, ok := startupProvider(providers, req.Session.Summary.ID, family)
		if !ok {
			continue
		}
		model, err := startupModel(ctx, req, provider, family, providerEnv)
		if err != nil {
			return nil, err
		}
		if model == "" {
			// The family's provider declares no model, so there is none to
			// publish beside its token. An image reading only this family would
			// have no model name to send, which is the whole point of the
			// aliases, so the family is skipped rather than half-published.
			continue
		}
		// The token records no model, on purpose. This facade serves images the
		// daemon does not configure, so the model an entrypoint sends cannot be
		// predicted and has to be forwarded verbatim; GuestModel stays empty for
		// the same reason, because a guest name here would resolve to the empty
		// recorded model instead of reaching the upstream. The model resolved
		// above is still published as this family's model alias.
		rawToken, token, err := llms.NewFacadeToken(llms.NewFacadeTokenRequest{
			SandboxID:  req.Session.Summary.ID,
			ProviderID: provider.ID,
			WireAPI:    startupWireAPI(family),
			Source:     req.Source,
			RunID:      req.RunID,
		})
		if err != nil {
			return nil, fmt.Errorf("issue %s startup facade token: %w", family, err)
		}
		if err := req.Store.SaveLLMFacadeToken(ctx, token); err != nil {
			return nil, fmt.Errorf("save %s startup facade token: %w", family, err)
		}
		route := baseURL + "/api/runtime/sandboxes/" + req.Session.Summary.ID + "/llm/openai/v1"
		if family == llms.ProviderFamilyAnthropic {
			route = baseURL + "/api/runtime/sandboxes/" + req.Session.Summary.ID + "/llm/anthropic"
		}
		for name, value := range llms.ProviderFamilyEnv(family, rawToken, route, model) {
			env[name] = value
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
}

func startupModel(ctx context.Context, req SessionFacadeConfigRequest, provider llms.Provider, family string, providerEnv []domain.SandboxEnvVar) (string, error) {
	// req.Model is the model this run resolved, and a run resolves it against one
	// connection, so it is spelled in that connection's family. Offering it to
	// the other family publishes an alias naming a model that family's connection
	// need not serve — the failure this facade prevents, moved from startup to
	// the first request. The family's own sources answer instead.
	//
	// Only an agent that addresses exactly one family can be told apart from the
	// other, so only those are protected. For opencode, pi and dsh both families
	// keep req.Model, and the unselected family's alias can name a model only the
	// other connection serves. Withholding it from both instead would be worse:
	// a family with no model from any source is skipped by the caller, so an
	// image reading that family loses the variables it needs to start at all —
	// trading the case this facade exists for against the accuracy of an alias
	// nothing may read. See startupAgentFamily.
	requested := strings.TrimSpace(req.Model)
	if agentFamily := startupAgentFamily(req.Agent); agentFamily != "" && agentFamily != family {
		requested = ""
	}
	// The declaration is read with the family's own dialect, not the selected
	// agent's. The two must agree: ensureDeclaredStartupProviders creates the
	// connection with that dialect, so reading the same declaration back with
	// another one can find no model where a model exists — which is how a codex
	// sandbox lost the Anthropic family entirely, because the codex dialect does
	// not read ANTHROPIC_MODEL and the family was skipped as model-less.
	if dialect, dialectErr := startupFamilyDialect(family); dialectErr == nil {
		if declared, ok := llms.DeclaredUpstreamFromAgentEnv(req.Session.Summary.ID, providerEnv, dialect, requested); ok && declared.Provider.ID == provider.ID {
			// Fall through when the declaration names no model, so the sources
			// below still get their turn; a match on the connection alone must
			// not end the search with nothing.
			if model := strings.TrimSpace(declared.Model); model != "" {
				return model, nil
			}
		}
	}
	if requested != "" {
		return requested, nil
	}
	if model := startupFamilyModel(providerEnv, family); model != "" {
		return model, nil
	}
	if req.Config != nil && strings.TrimSpace(req.Config.LLMModel) != "" {
		return strings.TrimSpace(req.Config.LLMModel), nil
	}
	defaultProvider, defaultModel, ok, err := req.Store.DefaultLLMModelReference(ctx)
	if err != nil {
		return "", fmt.Errorf("read startup facade default model: %w", err)
	}
	if ok && strings.TrimSpace(defaultProvider) == strings.TrimSpace(provider.ID) {
		return strings.TrimSpace(defaultModel), nil
	}
	bindings, err := req.Store.ListLLMProviderModelConfigs(ctx)
	if err != nil {
		return "", fmt.Errorf("list %s startup facade models: %w", family, err)
	}
	models := make([]string, 0, len(bindings))
	for _, binding := range bindings {
		if strings.TrimSpace(binding.ProviderID) == strings.TrimSpace(provider.ID) && strings.TrimSpace(binding.ModelID) != "" {
			models = append(models, strings.TrimSpace(binding.ModelID))
		}
	}
	sort.Strings(models)
	if len(models) > 0 {
		return models[0], nil
	}
	return "", nil
}

func startupFamilyModel(env []domain.SandboxEnvVar, family string) string {
	var keys []string
	if family == llms.ProviderFamilyAnthropic {
		keys = []string{"ANTHROPIC_MODEL", "CLAUDE_MODEL", "LLM_MODEL"}
	} else {
		keys = []string{"CODEX_MODEL", "OPENAI_MODEL", "LLM_MODEL"}
	}
	for _, key := range keys {
		if value := strings.TrimSpace(llms.EnvItemValue(env, key)); value != "" {
			return value
		}
	}
	return ""
}

func startupWireAPI(family string) string {
	if family == llms.ProviderFamilyAnthropic {
		return llms.APIProtocolMessages
	}
	return ""
}

func startupProvider(providers []llms.Provider, sandboxID, family string) (llms.Provider, bool) {
	prefix := llms.DeclaredConnectionPrefix + strings.TrimSpace(sandboxID) + ":" + family + ":"
	candidates := make([]llms.Provider, 0, len(providers))
	for _, provider := range providers {
		if llms.NormalizeProviderType(provider.ProviderType) != family {
			continue
		}
		if llms.IsDeclaredConnectionID(provider.ID) && !strings.HasPrefix(provider.ID, prefix) {
			continue
		}
		candidates = append(candidates, provider)
	}
	if len(candidates) == 0 {
		return llms.Provider{}, false
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		leftPreferred := strings.HasPrefix(left.ID, prefix)
		rightPreferred := strings.HasPrefix(right.ID, prefix)
		if leftPreferred != rightPreferred {
			return leftPreferred
		}
		if left.Weight != right.Weight {
			return left.Weight < right.Weight
		}
		return left.ID < right.ID
	})
	return candidates[0], true
}

// ensureDeclaredStartupProviders imports provider-specific credentials from
// the sandbox/global environment into daemon-owned connections. This keeps
// compatibility aliases useful even when the only credential came from the
// persisted global environment rather than the catalog.
//
// A declaration that names no model is skipped. That rule mirrors
// llms.PrepareAgentLLM, which refuses the same declaration for the same reason:
// without a model the credential cannot serve a request, so importing it would
// only leave a managed secret no run can use. The request model counts as a
// name, because that is what llms.PrepareAgentLLM passes. It is also what keeps
// a stray credential for a family nothing declares from becoming a connection.
func ensureDeclaredStartupProviders(ctx context.Context, store FacadeStore, req SessionFacadeConfigRequest, providerEnv []domain.SandboxEnvVar, families []string) error {
	sandboxID := req.Session.Summary.ID
	for _, family := range families {
		dialect, err := startupFamilyDialect(family)
		if err != nil {
			return err
		}
		filtered := startupFamilyEnv(providerEnv, family)
		if len(filtered) == 0 {
			continue
		}
		declared, ok := llms.DeclaredUpstreamFromAgentEnv(sandboxID, filtered, dialect, req.Model)
		if !ok || strings.TrimSpace(declared.Model) == "" {
			continue
		}
		if err := store.UpsertDeclaredConnection(ctx, declared.Provider); err != nil {
			return fmt.Errorf("save %s startup provider: %w", family, err)
		}
	}
	return nil
}

// startupAgentFamily returns the provider family the selected agent addresses,
// or "" when it does not address exactly one. Codex and Claude each speak one
// family. opencode, pi and dsh resolve theirs from the catalog, and an agent
// with no dialect addresses none, so for those the caller learns nothing.
//
// A "" answer means startupModel cannot tell the two families apart for this
// run and therefore keeps the run's resolved model for both, rather than for
// neither; that tradeoff is argued where it is made.
func startupAgentFamily(agent string) string {
	dialect, err := llms.DialectFor(agent)
	if err != nil {
		return ""
	}
	switch dialect.Kind {
	case "codex":
		return llms.ProviderFamilyOpenAI
	case "claude":
		return llms.ProviderFamilyAnthropic
	default:
		return ""
	}
}

// startupFamilyDialect returns the agent dialect that reads one provider
// family's declared variables. A declaration is recognized and later read back
// through the same dialect, so both readers must use this rather than the
// selected agent's kind: the selected agent decides which family it addresses,
// never how another family's declaration is spelled.
func startupFamilyDialect(family string) (llms.Dialect, error) {
	kind := "codex"
	if family == llms.ProviderFamilyAnthropic {
		kind = "claude"
	}
	return llms.DialectFor(kind)
}

func startupFamilyEnv(env []domain.SandboxEnvVar, family string) []domain.SandboxEnvVar {
	filtered := make([]domain.SandboxEnvVar, 0, len(env))
	for _, item := range domain.NormalizeEnvItems(env) {
		name := strings.ToUpper(strings.TrimSpace(item.Name))
		if strings.HasPrefix(name, "LLM_") ||
			(family == llms.ProviderFamilyAnthropic && (strings.HasPrefix(name, "ANTHROPIC_") || name == "CLAUDE_MODEL")) ||
			(family == llms.ProviderFamilyOpenAI && (strings.HasPrefix(name, "OPENAI_") || strings.HasPrefix(name, "OPENROUTER_") || strings.HasPrefix(name, "AZURE_OPENAI_") || name == "CODEX_API_KEY" || name == "DEEPSEEK_API_KEY")) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}
