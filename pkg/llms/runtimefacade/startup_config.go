package runtimefacade

import (
	"context"
	"errors"
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

// EnsureSessionStartupFacadeConfig restores the provider-specific startup
// compatibility facade used by older guest images. New images use the common
// LLM_* variables emitted by EnsureSessionAgentRuntimeConfig; older images
// still need one provider-specific token and endpoint for the provider family
// the selected agent addresses, which is derived from the agent, the declared
// upstream or the connection the catalog would resolve.
//
// The returned variables are published before the managed environment, so the
// selected agent's own configuration overwrites every name it writes. Only the
// names an agent does not write survive: for an agent like dsh that publishes no
// provider-specific name, this facade's whole set does, which is its purpose. A
// dialect writer that publishes one family name therefore publishes that
// family's whole set — see llms.ProviderFamilyEnv.
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
	// The families are resolved before the declared connections are imported, so
	// only the family the selected agent can use is imported: a credential from
	// an unselected family leaves a daemon-held secret no run can reach. It also
	// keeps the two resolutions consistent, because a family this facade derives
	// from the catalog now sees the same catalog the managed path sees, rather
	// than one that a connection imported in this call has already extended.
	families, err := startupFamilies(ctx, req, providerEnv)
	if err != nil {
		return nil, err
	}
	if err := ensureDeclaredStartupProviders(ctx, req.Store, req, providerEnv, families); err != nil {
		return nil, err
	}

	providers, err := req.Store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list startup facade providers: %w", err)
	}
	env := make(map[string]string)
	for _, family := range families {
		provider, ok := startupProvider(providers, req.Session.Summary.ID, family)
		if !ok {
			continue
		}
		model, err := startupModel(ctx, req, provider, family, providerEnv)
		if err != nil {
			return nil, err
		}
		if model == "" {
			// A startup token must be bound to one model. If the selected
			// provider has no model declaration, it cannot safely be exposed by
			// the compatibility facade.
			continue
		}
		guestModel := model
		if dialect, dialectErr := llms.DialectFor(req.Agent); dialectErr == nil {
			guestModel = dialect.GuestModel(model)
		}
		rawToken, token, err := llms.NewFacadeToken(llms.NewFacadeTokenRequest{
			SandboxID:  req.Session.Summary.ID,
			Model:      model,
			ProviderID: provider.ID,
			WireAPI:    startupWireAPI(family),
			GuestModel: guestModel,
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

// startupFamilies limits compatibility aliases to the family the selected
// agent can actually use. Every caller names the agent: a sandbox start, a
// release resume, a run and a scheduler command all resolve the selected agent
// before preparing the facade. An agent with no dialect gets no compatibility
// aliases at all, rather than one family's aliases that no selected agent can
// use and that would cost a facade token per family.
func startupFamilies(ctx context.Context, req SessionFacadeConfigRequest, providerEnv []domain.SandboxEnvVar) ([]string, error) {
	dialect, err := llms.DialectFor(req.Agent)
	if err != nil {
		if errors.Is(err, llms.ErrUnsupportedAgentDialect) {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve startup facade agent %q: %w", req.Agent, err)
	}
	switch dialect.Kind {
	case "codex":
		return []string{llms.ProviderFamilyOpenAI}, nil
	case "claude":
		return []string{llms.ProviderFamilyAnthropic}, nil
	}
	if declared, ok := llms.DeclaredUpstreamFromAgentEnv(req.Session.Summary.ID, providerEnv, dialect, req.Model); ok {
		return []string{llms.NormalizeProviderType(declared.Provider.ProviderType)}, nil
	}
	catalog, err := llms.LoadCatalog(ctx, req.Store)
	if err != nil {
		return nil, fmt.Errorf("load startup facade catalog: %w", err)
	}
	model, err := catalog.SelectModel(req.Model)
	if err != nil {
		return nil, nil
	}
	target, err := catalog.Resolve("", model, dialect.PreferredProtocols())
	if err != nil {
		return nil, nil
	}
	return []string{llms.NormalizeProviderType(target.Provider.ProviderType)}, nil
}

func startupModel(ctx context.Context, req SessionFacadeConfigRequest, provider llms.Provider, family string, providerEnv []domain.SandboxEnvVar) (string, error) {
	dialect, dialectErr := llms.DialectFor(req.Agent)
	if dialectErr == nil {
		if declared, ok := llms.DeclaredUpstreamFromAgentEnv(req.Session.Summary.ID, providerEnv, dialect, req.Model); ok && declared.Provider.ID == provider.ID {
			return strings.TrimSpace(declared.Model), nil
		}
	}
	if model := strings.TrimSpace(req.Model); model != "" {
		return model, nil
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
// Only the families the selected agent can use are imported, and a declaration
// that names no model is skipped. The second rule mirrors
// llms.PrepareAgentLLM, which refuses the same declaration for the same reason:
// without a model the credential cannot serve a request, so importing it would
// only leave a managed secret no run can use. The request model counts as a
// name, because that is what llms.PrepareAgentLLM passes.
func ensureDeclaredStartupProviders(ctx context.Context, store FacadeStore, req SessionFacadeConfigRequest, providerEnv []domain.SandboxEnvVar, families []string) error {
	sandboxID := req.Session.Summary.ID
	for _, family := range families {
		kind := "codex"
		if family == llms.ProviderFamilyAnthropic {
			kind = "claude"
		}
		dialect, err := llms.DialectFor(kind)
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
