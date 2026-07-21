package runtimefacade

import (
	"context"
	"errors"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

// FacadeStore is the persistence surface the runtime LLM facade needs: the LLM
// resolution / provider-bootstrap surface plus facade-token persistence.
// *configstore.ConfigStore satisfies it; depending on this interface keeps the
// facade off a direct configstore import.
//
// Callers that hold a possibly-nil concrete store must pass a true nil
// interface when the store is absent (see adapters.facadeStoreFor); wrapping a
// nil pointer in the interface would bypass the `store == nil` guards here.
type FacadeStore interface {
	llms.LLMResolverStore
	SaveLLMFacadeToken(ctx context.Context, token llms.FacadeToken) error
}

const (
	TokenSourceAgent         = "agent"
	TokenSourceLoaderCommand = "loader_command"
)

type AgentRuntimeConfig struct {
	Env map[string]string
}

func EnsureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, agent, model, source, runID string) (map[string]string, error) {
	runtimeConfig, err := EnsureSessionAgentRuntimeConfig(ctx, config, store, session, agent, model, source, runID)
	if err != nil {
		return nil, err
	}
	return runtimeConfig.Env, nil
}

func EnsureSessionAgentRuntimeConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, agent, model, source, runID string) (AgentRuntimeConfig, error) {
	if config == nil || store == nil || session == nil {
		return AgentRuntimeConfig{}, nil
	}
	switch domain.NormalizeAgentKind(agent) {
	case "codex":
		env, err := ensureSessionCodexConfig(ctx, config, store, session, model, source, runID)
		return AgentRuntimeConfig{Env: env}, err
	case "claude":
		env, err := ensureSessionClaudeConfig(ctx, config, store, session, model, source, runID)
		return AgentRuntimeConfig{Env: env}, err
	case "opencode":
		env, err := ensureSessionOpenCodeConfig(ctx, config, store, session, model, source, runID)
		return AgentRuntimeConfig{Env: env}, err
	default:
		return AgentRuntimeConfig{}, nil
	}
}

func ensureSessionCodexConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	providerEnv, err := sessionProviderEnvItems(ctx, store, session, llms.ProviderFamilyOpenAI)
	if err != nil {
		return nil, err
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, session.Summary.ID, llms.ProviderFamilyOpenAI, model, "", providerEnv)
	if err != nil {
		if isOptionalConfigError(err) {
			return nil, nil
		}
		return nil, err
	}
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	// Codex 0.144.x accepts only Responses as a model-provider wire API. Keep
	// guest ingress on Responses and let the facade bridge to target.WireAPI.
	facadeWireAPI := llms.APIProtocolResponses
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, facadeWireAPI, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + session.Summary.ID + "/llm/openai/v1"
	if err := llms.WriteCodexRuntimeConfig(session, target.Model.Name, openAIBaseURL, facadeWireAPI, llms.CodexRuntimePolicyFromConfig(config)); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            facadeWireAPI,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
	}, nil
}

func ensureSessionClaudeConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	providerEnv, err := sessionProviderEnvItems(ctx, store, session, llms.ProviderFamilyAnthropic)
	if err != nil {
		return nil, err
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, session.Summary.ID, llms.ProviderFamilyAnthropic, model, "", providerEnv)
	if err != nil {
		return nil, err
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolMessages, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + session.Summary.ID + "/llm/anthropic"
	env := map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolMessages,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_AUTH_TOKEN":        tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
	}
	env["ANTHROPIC_MODEL"] = target.Model.Name
	env["CLAUDE_MODEL"] = target.Model.Name
	return env, nil
}

func ensureSessionOpenCodeConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	providerID, modelName, err := llms.SplitOpenCodeModel(model)
	if err != nil {
		return nil, err
	}
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	switch providerID {
	case "opencode":
		return nil, nil
	case "anthropic":
		return ensureOpenCodeAnthropicConfig(ctx, config, store, session, modelName, source, runID)
	case "openai":
		return ensureOpenCodeOpenAIConfig(ctx, config, store, session, modelName, source, runID)
	default:
		return ensureOpenCodeCustomProviderConfig(ctx, config, store, session, providerID, modelName, source, runID)
	}
}

func ensureOpenCodeAnthropicConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	providerEnv, err := sessionProviderEnvItems(ctx, store, session, llms.ProviderFamilyAnthropic)
	if err != nil {
		return nil, err
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, session.Summary.ID, llms.ProviderFamilyAnthropic, model, "", providerEnv)
	if err != nil {
		return nil, err
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolMessages, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + session.Summary.ID + "/llm/anthropic"
	if err := llms.WriteOpenCodeAnthropicRuntimeConfig(session, target.Model.Name, anthropicBaseURL+"/v1"); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolMessages,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_AUTH_TOKEN":        tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
		"OPENCODE_CONFIG":             llms.GuestOpenCodeConfigPath(config),
	}, nil
}

func ensureOpenCodeOpenAIConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, model, source, runID string) (map[string]string, error) {
	providerEnv, err := sessionProviderEnvItems(ctx, store, session, llms.ProviderFamilyOpenAI)
	if err != nil {
		return nil, err
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, session.Summary.ID, llms.ProviderFamilyOpenAI, model, "", providerEnv)
	if err != nil {
		return nil, err
	}
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolResponses, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + session.Summary.ID + "/llm/openai/v1"
	if err := llms.WriteOpenCodeRuntimeConfig(session, "openai", target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolResponses,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
		"OPENCODE_CONFIG":             llms.GuestOpenCodeConfigPath(config),
	}, nil
}

func ensureOpenCodeCustomProviderConfig(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, providerID, model, source, runID string) (map[string]string, error) {
	target, err := resolveOpenCodeCustomProviderTarget(ctx, config, store, session, providerID, model)
	if err != nil {
		return nil, err
	}
	baseURL := llms.GuestRuntimeBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolChatCompletions, source, runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + session.Summary.ID + "/llm/openai/v1"
	if err := llms.WriteOpenCodeRuntimeConfig(session, providerID, target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolChatCompletions,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
		"OPENCODE_CONFIG":             llms.GuestOpenCodeConfigPath(config),
	}, nil
}

func resolveOpenCodeCustomProviderTarget(ctx context.Context, config *appconfig.Config, store FacadeStore, session *domain.Sandbox, providerID, model string) (llms.ResolvedTarget, error) {
	envItems, err := sessionProviderEnvItems(ctx, store, session, llms.ProviderFamilyOpenAI)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	sessionID := ""
	if session != nil {
		sessionID = session.Summary.ID
	}
	if llms.HasEnabledLLMProviderID(ctx, store, providerID) {
		return llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sessionID, llms.ProviderFamilyOpenAI, model, providerID, envItems)
	}
	if sessionID != "" && llms.HasOpenAIEnvProviderInput(envItems) {
		sessionProviderID, err := llms.EnsureSessionOpenAIEnvProviderWithConfig(ctx, config, store, sessionID, model, envItems)
		if err != nil {
			return llms.ResolvedTarget{}, err
		}
		if strings.TrimSpace(sessionProviderID) != "" {
			return llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sessionID, llms.ProviderFamilyOpenAI, model, sessionProviderID, envItems)
		}
	}
	lookup, err := llms.DefaultLLMEnvProviderLookup(ctx, config, store)
	if err != nil {
		return llms.ResolvedTarget{}, err
	}
	if _, err := llms.EnsureOpenAIEnvProvider(ctx, store, lookup, providerID, providerID, llms.ProviderScopeEnvDefault, model, false); err != nil {
		return llms.ResolvedTarget{}, err
	}
	return llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sessionID, llms.ProviderFamilyOpenAI, model, providerID, envItems)
}

func isOptionalConfigError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrFailedPrecondition)
}

func IsOptionalConfigError(err error) bool {
	return isOptionalConfigError(err)
}

func sessionProviderEnvItems(ctx context.Context, store FacadeStore, session *domain.Sandbox, providerFamily string) ([]domain.SandboxEnvVar, error) {
	if session == nil {
		return nil, nil
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return nil, err
	}
	return llms.SandboxProviderEnvItems(session, providerFamily, providers), nil
}
