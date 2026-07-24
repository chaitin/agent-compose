package llms

import (
	"context"
	"errors"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

// CodexFacadeStore is the persistence surface needed to resolve a Codex model
// and issue its run-scoped runtime facade token.
type CodexFacadeStore interface {
	LLMResolverStore
	SaveLLMFacadeToken(context.Context, FacadeToken) error
}

// EnsureCodexFacadeConfig resolves the managed Codex provider and model,
// requires a sandbox-reachable facade, and returns its managed environment.
// A missing managed provider remains a no-op so Codex can use its own login.
func EnsureCodexFacadeConfig(ctx context.Context, config *appconfig.Config, store CodexFacadeStore, sandbox *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	providerEnv, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyOpenAI)
	if err != nil {
		return nil, err
	}
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandbox.Summary.ID, ProviderFamilyOpenAI, model, "", providerEnv)
	if err != nil {
		if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrFailedPrecondition) {
			return nil, nil
		}
		return nil, err
	}
	baseURL, err := RequireGuestRuntimeBaseURL(config, sandbox)
	if err != nil {
		return nil, err
	}

	tokenValue, token, err := NewFacadeToken(sandbox.Summary.ID, target.Model.Name, target.Provider.ID, APIProtocolResponses, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := WriteCodexRuntimeConfig(sandbox, target.Model.Name, openAIBaseURL, APIProtocolResponses, CodexRuntimePolicyFromConfig(config)); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            APIProtocolResponses,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
	}, nil
}
