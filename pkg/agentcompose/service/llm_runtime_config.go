package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"errors"
	"os"
	"strings"

	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

const (
	// llmFacadeTokenSourceAgent marks a per-agent-run facade token. Such a token is
	// only used for the duration of a single bounded agent run, so the caller is
	// expected to delete it when that run completes (see executeAgentRun) rather
	// than letting it live for the whole session lifetime.
	llmFacadeTokenSourceAgent = "agent"

	// llmFacadeTokenSourceLoaderCommand marks a per-loader-command facade token.
	// The caller must delete it when the bounded command returns.
	llmFacadeTokenSourceLoaderCommand = "loader_command"
)

type agentRuntimeLLMConfig struct {
	Env map[string]string
}

func ensureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, agent, model, source, runID string) (map[string]string, error) {
	runtimeConfig, err := ensureSessionAgentRuntimeLLMConfig(ctx, config, configDB, session, agent, model, source, runID)
	if err != nil {
		return nil, err
	}
	return runtimeConfig.Env, nil
}

func ensureSessionAgentRuntimeLLMConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, agent, model, source, runID string) (agentRuntimeLLMConfig, error) {
	if config == nil || configDB == nil || session == nil {
		return agentRuntimeLLMConfig{}, nil
	}
	switch domain.NormalizeAgentKind(agent) {
	case "codex":
		env, err := ensureSessionCodexLLMFacadeConfig(ctx, config, configDB, session, model, source, runID)
		return agentRuntimeLLMConfig{Env: env}, err
	case "claude":
		env, err := ensureSessionClaudeLLMFacadeConfig(ctx, config, configDB, session, model, source, runID)
		return agentRuntimeLLMConfig{Env: env}, err
	case "opencode":
		env, err := ensureSessionOpenCodeLLMFacadeConfig(ctx, config, configDB, session, model, source, runID)
		return agentRuntimeLLMConfig{Env: env}, err
	default:
		return agentRuntimeLLMConfig{}, nil
	}
}

func ensureSessionCodexLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, model, source, runID string) (map[string]string, error) {
	target, err := resolveRuntimeLLMTargetWithEnv(ctx, config, configDB, session.Summary.ID, llms.ProviderFamilyOpenAI, model, "", sessionLLMProviderEnvItems(session))
	if err != nil {
		if isOptionalLLMFacadeConfigError(err) {
			return nil, nil
		}
		return nil, err
	}
	baseURL := guestRuntimeLLMBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	facadeWireAPI := llms.APIProtocolResponses
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, facadeWireAPI, source, runID)
	if err != nil {
		return nil, err
	}
	if err := configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sessions/" + session.Summary.ID + "/llm/openai/v1"
	if err := writeCodexLLMConfig(session, target.Model.Name, openAIBaseURL, facadeWireAPI); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SESSION_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            facadeWireAPI,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
	}, nil
}

func ensureSessionClaudeLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, model, source, runID string) (map[string]string, error) {
	baseURL := guestRuntimeLLMBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	providerEnv := sessionLLMProviderEnvItems(session)
	target, err := resolveRuntimeLLMTargetWithEnv(ctx, config, configDB, session.Summary.ID, llms.ProviderFamilyAnthropic, model, "", providerEnv)
	tokenModel := ""
	tokenProvider := ""
	if err != nil {
		if !isOptionalLLMFacadeConfigError(err) || !hasAnthropicProviderKey(ctx, config, configDB) {
			return nil, err
		}
	} else {
		tokenModel = target.Model.Name
		tokenProvider = target.Provider.ID
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, tokenModel, tokenProvider, llms.APIProtocolMessages, source, runID)
	if err != nil {
		return nil, err
	}
	if err := configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sessions/" + session.Summary.ID + "/llm/anthropic"
	env := map[string]string{
		"AGENT_COMPOSE_SESSION_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolMessages,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
	}
	if tokenModel != "" {
		env["ANTHROPIC_MODEL"] = tokenModel
		env["CLAUDE_MODEL"] = tokenModel
	}
	return env, nil
}

func ensureSessionOpenCodeLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, model, source, runID string) (map[string]string, error) {
	providerID, modelName, err := llms.SplitOpenCodeModel(model)
	if err != nil {
		return nil, err
	}
	baseURL := guestRuntimeLLMBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	switch providerID {
	case "opencode":
		return nil, nil
	case "anthropic":
		return ensureSessionOpenCodeAnthropicFacadeConfig(ctx, config, configDB, session, modelName, source, runID)
	case "openai":
		return ensureSessionOpenCodeOpenAIFacadeEnv(ctx, config, configDB, session, modelName, source, runID)
	default:
		return ensureSessionOpenCodeCustomProviderConfig(ctx, config, configDB, session, providerID, modelName, source, runID)
	}
}

func ensureSessionOpenCodeAnthropicFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, model, source, runID string) (map[string]string, error) {
	baseURL := guestRuntimeLLMBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	providerEnv := sessionLLMProviderEnvItems(session)
	target, err := resolveRuntimeLLMTargetWithEnv(ctx, config, configDB, session.Summary.ID, llms.ProviderFamilyAnthropic, model, "", providerEnv)
	if err != nil {
		return nil, err
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolMessages, source, runID)
	if err != nil {
		return nil, err
	}
	if err := configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sessions/" + session.Summary.ID + "/llm/anthropic"
	if err := writeOpenCodeAnthropicLLMConfig(session, target.Model.Name, anthropicBaseURL+"/v1"); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SESSION_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolMessages,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
		"OPENCODE_CONFIG":             guestOpenCodeLLMConfigPath(config),
	}, nil
}

func ensureSessionOpenCodeOpenAIFacadeEnv(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, model, source, runID string) (map[string]string, error) {
	target, err := resolveRuntimeLLMTargetWithEnv(ctx, config, configDB, session.Summary.ID, llms.ProviderFamilyOpenAI, model, "", sessionLLMProviderEnvItems(session))
	if err != nil {
		return nil, err
	}
	baseURL := guestRuntimeLLMBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolResponses, source, runID)
	if err != nil {
		return nil, err
	}
	if err := configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sessions/" + session.Summary.ID + "/llm/openai/v1"
	if err := writeOpenCodeLLMConfig(session, "openai", target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SESSION_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolResponses,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
		"OPENCODE_CONFIG":             guestOpenCodeLLMConfigPath(config),
	}, nil
}

func ensureSessionOpenCodeCustomProviderConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, providerID, model, source, runID string) (map[string]string, error) {
	target, err := resolveOpenCodeCustomProviderTarget(ctx, config, configDB, session, providerID, model)
	if err != nil {
		return nil, err
	}
	baseURL := guestRuntimeLLMBaseURL(config, session)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	tokenValue, token, err := llms.NewFacadeToken(session.Summary.ID, target.Model.Name, target.Provider.ID, llms.APIProtocolChatCompletions, source, runID)
	if err != nil {
		return nil, err
	}
	if err := configDB.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	openAIBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sessions/" + session.Summary.ID + "/llm/openai/v1"
	if err := writeOpenCodeLLMConfig(session, providerID, target.Model.Name, openAIBaseURL); err != nil {
		return nil, err
	}
	return map[string]string{
		"AGENT_COMPOSE_SESSION_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            openAIBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolChatCompletions,
		"OPENAI_API_KEY":              tokenValue,
		"OPENAI_BASE_URL":             openAIBaseURL,
		"OPENCODE_CONFIG":             guestOpenCodeLLMConfigPath(config),
	}, nil
}

func resolveOpenCodeCustomProviderTarget(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, providerID, model string) (llms.ResolvedTarget, error) {
	envItems := sessionLLMProviderEnvItems(session)
	sessionID := ""
	if session != nil {
		sessionID = session.Summary.ID
	}
	if hasEnabledLLMProviderID(ctx, configDB, providerID) {
		return resolveRuntimeLLMTargetWithEnv(ctx, config, configDB, sessionID, llms.ProviderFamilyOpenAI, model, providerID, envItems)
	}
	if sessionID != "" && llms.HasOpenAIEnvProviderInput(envItems) {
		sessionProviderID, err := ensureSessionOpenAIEnvProvider(ctx, configDB, sessionID, model, envItems)
		if err != nil {
			return llms.ResolvedTarget{}, err
		}
		if strings.TrimSpace(sessionProviderID) != "" {
			return resolveRuntimeLLMTargetWithEnv(ctx, config, configDB, sessionID, llms.ProviderFamilyOpenAI, model, sessionProviderID, envItems)
		}
	}
	if _, err := llms.EnsureOpenAIEnvProvider(ctx, configDB, defaultLLMEnvProviderLookup(ctx, config, configDB), providerID, providerID, llms.ProviderScopeEnvDefault, model, false); err != nil {
		return llms.ResolvedTarget{}, err
	}
	return resolveRuntimeLLMTargetWithEnv(ctx, config, configDB, sessionID, llms.ProviderFamilyOpenAI, model, providerID, envItems)
}

func isOptionalLLMFacadeConfigError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, ErrRequired) || errors.Is(err, ErrFailedPrecondition)
}

// hasAnthropicProviderKey reports whether a daemon-level Anthropic credential
// exists. It intentionally ignores per-session env items: request-time provider
// resolution runs without session env, so a session-scoped key (without a model
// to persist a provider) would never let a runtime request resolve. Tolerating a
// missing model is only safe when a daemon-level key can bootstrap a provider
// from the request's model at call time.
func hasAnthropicProviderKey(ctx context.Context, config *appconfig.Config, configDB *ConfigStore) bool {
	configKey := ""
	if config != nil {
		configKey = config.LLMAPIKey
	}
	return strings.TrimSpace(firstNonEmpty(
		lookupEnvValue(ctx, configDB, "ANTHROPIC_API_KEY"),
		lookupEnvValue(ctx, configDB, "ANTHROPIC_AUTH_TOKEN"),
		lookupEnvValue(ctx, configDB, "LLM_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		os.Getenv("LLM_API_KEY"),
		configKey,
	)) != ""
}

func sessionLLMProviderEnvItems(session *Session) []SessionEnvVar {
	if session == nil {
		return nil
	}
	if len(session.ProviderEnvItems) > 0 {
		return session.ProviderEnvItems
	}
	return session.EnvItems
}

func writeCodexLLMConfig(session *Session, model, baseURL, wireAPI string) error {
	return llms.WriteCodexRuntimeConfig(session, model, baseURL, wireAPI)
}

func writeOpenCodeLLMConfig(session *Session, providerID, model, baseURL string) error {
	return llms.WriteOpenCodeRuntimeConfig(session, providerID, model, baseURL)
}

func writeOpenCodeAnthropicLLMConfig(session *Session, model, baseURL string) error {
	return llms.WriteOpenCodeAnthropicRuntimeConfig(session, model, baseURL)
}

func guestOpenCodeLLMConfigPath(config *appconfig.Config) string {
	return llms.GuestOpenCodeConfigPath(config)
}

func guestRuntimeLLMBaseURL(config *appconfig.Config, session *Session) string {
	return llms.GuestRuntimeBaseURL(config, session)
}
