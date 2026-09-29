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

// EnsureSessionStartupFacadeConfig restores the provider-specific startup
// compatibility facade used by older guest images. New images use the common
// LLM_* variables emitted by EnsureSessionAgentRuntimeConfig; older images
// still need one provider-specific token and endpoint for each configured
// provider family.
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
	if err := ensureDeclaredStartupProviders(ctx, req.Store, req.Session.Summary.ID, providerEnv); err != nil {
		return nil, err
	}

	providers, err := req.Store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list startup facade providers: %w", err)
	}
	env := make(map[string]string)
	for _, family := range []string{llms.ProviderFamilyAnthropic, llms.ProviderFamilyOpenAI} {
		provider, ok := startupProvider(providers, req.Session.Summary.ID, family)
		if !ok {
			continue
		}
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
		if family == llms.ProviderFamilyAnthropic {
			env["ANTHROPIC_API_KEY"] = rawToken
			env["ANTHROPIC_AUTH_TOKEN"] = rawToken
			env["ANTHROPIC_BASE_URL"] = baseURL + "/api/runtime/sandboxes/" + req.Session.Summary.ID + "/llm/anthropic"
		} else {
			env["OPENAI_API_KEY"] = rawToken
			env["OPENAI_BASE_URL"] = baseURL + "/api/runtime/sandboxes/" + req.Session.Summary.ID + "/llm/openai/v1"
		}
	}
	if len(env) == 0 {
		return nil, nil
	}
	return env, nil
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
func ensureDeclaredStartupProviders(ctx context.Context, store FacadeStore, sandboxID string, env []domain.SandboxEnvVar) error {
	for _, family := range []string{llms.ProviderFamilyAnthropic, llms.ProviderFamilyOpenAI} {
		kind := "codex"
		if family == llms.ProviderFamilyAnthropic {
			kind = "claude"
		}
		dialect, err := llms.DialectFor(kind)
		if err != nil {
			return err
		}
		filtered := startupFamilyEnv(env, family)
		if len(filtered) == 0 {
			continue
		}
		declared, ok := llms.DeclaredUpstreamFromAgentEnv(sandboxID, filtered, dialect, "")
		if !ok {
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
