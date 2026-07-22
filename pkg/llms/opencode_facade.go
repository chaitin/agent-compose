package llms

import (
	"context"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

// OpenCodeFacadeStore is the persistence surface needed to resolve an
// OpenCode provider target and issue a runtime facade token.
type OpenCodeFacadeStore interface {
	LLMResolverStore
	SaveLLMFacadeToken(context.Context, FacadeToken) error
}

// EnsureOpenCodeFacadeConfig resolves an OpenCode provider/model pair, writes
// its guest runtime config, and returns the managed facade environment.
func EnsureOpenCodeFacadeConfig(ctx context.Context, config *appconfig.Config, store OpenCodeFacadeStore, sandbox *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	providerID, modelName, err := SplitOpenCodeModel(model)
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	switch providerID {
	case "opencode":
		return nil, nil
	case "anthropic":
		return ensureOpenCodeAnthropicFacadeConfig(ctx, config, store, sandbox, modelName, source, runID)
	case "openai":
		return ensureOpenCodeOpenAIFacadeConfig(ctx, config, store, sandbox, modelName, source, runID)
	default:
		return ensureOpenCodeCustomFacadeConfig(ctx, config, store, sandbox, providerID, modelName, source, runID)
	}
}

func ensureOpenCodeAnthropicFacadeConfig(ctx context.Context, config *appconfig.Config, store OpenCodeFacadeStore, sandbox *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandbox.Summary.ID, ProviderFamilyAnthropic, model, "", openCodeProviderEnvItems(sandbox))
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	tokenValue, token, err := NewFacadeToken(sandbox.Summary.ID, target.Model.Name, target.Provider.ID, APIProtocolMessages, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/anthropic"
	if err := WriteOpenCodeAnthropicRuntimeConfig(sandbox, target.Model.Name, anthropicBaseURL+"/v1"); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            APIProtocolMessages,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_AUTH_TOKEN":        tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
		"OPENCODE_CONFIG":             GuestOpenCodeConfigPath(config),
	}, nil
}

func ensureOpenCodeOpenAIFacadeConfig(ctx context.Context, config *appconfig.Config, store OpenCodeFacadeStore, sandbox *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	target, err := ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandbox.Summary.ID, ProviderFamilyOpenAI, model, "", openCodeProviderEnvItems(sandbox))
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	tokenValue, token, err := NewFacadeToken(sandbox.Summary.ID, target.Model.Name, target.Provider.ID, APIProtocolResponses, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := WriteOpenCodeRuntimeConfig(sandbox, "openai", target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	return openCodeOpenAIEnv(tokenValue, openAIBaseURL, APIProtocolResponses, config), nil
}

func ensureOpenCodeCustomFacadeConfig(ctx context.Context, config *appconfig.Config, store OpenCodeFacadeStore, sandbox *domain.Sandbox, providerID, model, source, runID string) (map[string]string, error) {
	target, err := resolveOpenCodeCustomFacadeTarget(ctx, config, store, sandbox, providerID, model)
	if err != nil {
		return nil, err
	}
	baseURL := GuestRuntimeBaseURL(config, sandbox)
	tokenValue, token, err := NewFacadeToken(sandbox.Summary.ID, target.Model.Name, target.Provider.ID, APIProtocolChatCompletions, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/openai/v1"
	if err := WriteOpenCodeRuntimeConfig(sandbox, providerID, target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	return openCodeOpenAIEnv(tokenValue, openAIBaseURL, APIProtocolChatCompletions, config), nil
}

func openCodeOpenAIEnv(tokenValue, baseURL, protocol string, config *appconfig.Config) map[string]string {
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            baseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            protocol,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             baseURL,
		"OPENCODE_CONFIG":             GuestOpenCodeConfigPath(config),
	}
}

func resolveOpenCodeCustomFacadeTarget(ctx context.Context, config *appconfig.Config, store OpenCodeFacadeStore, sandbox *domain.Sandbox, providerID, model string) (ResolvedTarget, error) {
	envItems := openCodeProviderEnvItems(sandbox)
	sandboxID := ""
	if sandbox != nil {
		sandboxID = sandbox.Summary.ID
	}
	if HasEnabledLLMProviderID(ctx, store, providerID) {
		return ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandboxID, ProviderFamilyOpenAI, model, providerID, envItems)
	}
	if sandboxID != "" && HasOpenAIEnvProviderInput(envItems) {
		sessionProviderID, err := EnsureSessionOpenAIEnvProvider(ctx, store, sandboxID, model, envItems)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if strings.TrimSpace(sessionProviderID) != "" {
			return ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandboxID, ProviderFamilyOpenAI, model, sessionProviderID, envItems)
		}
	}
	if _, err := EnsureOpenAIEnvProvider(ctx, store, DefaultLLMEnvProviderLookup(ctx, config, store), providerID, providerID, ProviderScopeEnvDefault, model, false); err != nil {
		return ResolvedTarget{}, err
	}
	return ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandboxID, ProviderFamilyOpenAI, model, providerID, envItems)
}

func openCodeProviderEnvItems(sandbox *domain.Sandbox) []domain.SandboxEnvVar {
	if sandbox == nil {
		return nil
	}
	if len(sandbox.ProviderEnvItems) > 0 {
		return sandbox.ProviderEnvItems
	}
	return sandbox.EnvItems
}
