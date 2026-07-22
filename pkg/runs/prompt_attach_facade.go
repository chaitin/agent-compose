package runs

import (
	"context"
	"errors"
	"os"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func ensurePromptAttachClaudeLLMFacadeEnv(ctx context.Context, config *appconfig.Config, store llmFacadeStore, sandbox *domain.Sandbox, model, runID string) (map[string]string, error) {
	baseURL := llms.GuestRuntimeBaseURL(config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandbox.Summary.ID, llms.ProviderFamilyAnthropic, model, "", promptAttachSandboxProviderEnvItems(sandbox))
	tokenModel := strings.TrimSpace(model)
	tokenProvider := ""
	if err != nil {
		optional := errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrFailedPrecondition)
		if !optional || !promptAttachHasAnthropicProviderKey(ctx, config, store) {
			return nil, err
		}
	} else {
		tokenModel = target.Model.Name
		tokenProvider = target.Provider.ID
	}
	tokenValue, token, err := llms.NewFacadeToken(sandbox.Summary.ID, tokenModel, tokenProvider, llms.APIProtocolMessages, "agent", runID)
	if err != nil {
		return nil, err
	}
	if err := store.SaveLLMFacadeToken(ctx, token); err != nil {
		return nil, err
	}
	anthropicBaseURL := strings.TrimRight(baseURL, "/") + "/api/runtime/sandboxes/" + sandbox.Summary.ID + "/llm/anthropic"
	env := map[string]string{
		"AGENT_COMPOSE_SANDBOX_TOKEN": tokenValue,
		"LLM_API_ENDPOINT":            anthropicBaseURL,
		"LLM_API_KEY":                 tokenValue,
		"LLM_API_PROTOCOL":            llms.APIProtocolMessages,
		"ANTHROPIC_API_KEY":           tokenValue,
		"ANTHROPIC_AUTH_TOKEN":        tokenValue,
		"ANTHROPIC_BASE_URL":          anthropicBaseURL,
	}
	if tokenModel != "" {
		env["ANTHROPIC_MODEL"] = tokenModel
		env["CLAUDE_MODEL"] = tokenModel
	}
	return env, nil
}

func promptAttachHasAnthropicProviderKey(ctx context.Context, config *appconfig.Config, store llmFacadeStore) bool {
	configKey := ""
	if config != nil {
		configKey = config.LLMAPIKey
	}
	for _, value := range []string{
		llms.LookupEnvValue(ctx, store, "ANTHROPIC_API_KEY"),
		llms.LookupEnvValue(ctx, store, "ANTHROPIC_AUTH_TOKEN"),
		llms.LookupEnvValue(ctx, store, "LLM_API_KEY"),
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		os.Getenv("LLM_API_KEY"),
		configKey,
	} {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}
