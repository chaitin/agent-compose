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
// The families are fixed (see startupFacadeFamilies) because the image, not the
// selected agent, decides which family it reads, so the request's Agent field is
// not consulted at all. Model is read only to decide whether a declaration names
// enough to import as a connection.
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
	// Each family answers from its own sources, in one order. The model this run
	// resolved is deliberately not one of them: a run resolves its model against
	// one connection, so offering that name to the other family publishes an alias
	// naming a model that family's connection need not serve — the failure this
	// facade prevents, moved from startup to the first request. Which family a run
	// addresses is also not knowable for every agent kind, so any rule that had to
	// know it would leave the agents it cannot attribute publishing the same wrong
	// name.
	//
	// The family's own names come first for the family the run does address too,
	// and that agrees with the run rather than contradicting it: directModelFromEnv
	// reads these same names when an agent owns its upstream, and when the daemon
	// serves a family the agent's own configuration overrides this alias anyway.
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
