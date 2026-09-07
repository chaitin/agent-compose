package runs

import (
	"context"
	"errors"
	"os"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// promptAttachFacadeTarget bundles the config, credential store, and target
// sandbox a prompt-attach LLM facade helper needs to resolve and mint a
// token against.
type promptAttachFacadeTarget struct {
	Config  *appconfig.Config
	Store   llmFacadeStore
	Sandbox *domain.Sandbox
}

func ensurePromptAttachClaudeLLMFacadeEnv(ctx context.Context, facade promptAttachFacadeTarget, model, runID string) (map[string]string, error) {
	config := facade.Config
	store := facade.Store
	sandbox := facade.Sandbox
	baseURL := llms.GuestRuntimeBaseURL(config, sandbox)
	if strings.TrimSpace(baseURL) == "" {
		return nil, nil
	}
	providerEnv, err := llms.SandboxProviderEnvItems(ctx, store, sandbox, llms.ProviderFamilyAnthropic)
	if err != nil {
		return nil, err
	}
	target, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, store, llms.RuntimeLLMTargetQuery{
		Config: config, SessionID: sandbox.Summary.ID, PreferredProviderFamily: llms.ProviderFamilyAnthropic, RequestedModel: model, ProviderID: "", EnvItems: providerEnv,
	})
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
	tokenValue, token, err := llms.NewFacadeToken(llms.NewFacadeTokenRequest{
		SandboxID: sandbox.Summary.ID, Model: tokenModel, ProviderID: tokenProvider, WireAPI: llms.APIProtocolMessages, Source: "agent", RunID: runID,
	})
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

func (c *Controller) ensurePromptAttachLLMFacadeEnv(ctx context.Context, sandbox *domain.Sandbox, agent execution.AgentConfig, runID string) (map[string]string, error) {
	store, ok := c.configDB.(llmFacadeStore)
	if !ok || c.config == nil || sandbox == nil {
		return nil, nil
	}
	switch domain.NormalizeAgentKind(agent.Provider) {
	case "claude":
		return ensurePromptAttachClaudeLLMFacadeEnv(ctx, promptAttachFacadeTarget{Config: c.config, Store: store, Sandbox: sandbox}, agent.Model, runID)
	case "opencode":
		return llms.EnsureOpenCodeFacadeConfig(ctx, llms.OpenCodeFacadeConfigRequest{
			Config: c.config, Store: store, Sandbox: sandbox, Model: agent.Model, Source: "agent", RunID: runID,
		})
	case "pi":
		return llms.EnsurePiFacadeConfig(ctx, llms.PiFacadeConfigRequest{
			Config: c.config, Store: store, Sandbox: sandbox, Model: agent.Model, Source: "agent", RunID: runID,
		})
	case "codex":
		return llms.EnsureCodexFacadeConfig(ctx, llms.CodexFacadeConfigRequest{
			Config: c.config, Store: store, Sandbox: sandbox, Model: agent.Model, Source: "agent", RunID: runID,
		})
	case "dsh":
		return llms.EnsureDshFacadeConfig(ctx, llms.DshFacadeConfigRequest{
			Config: c.config, Store: store, Sandbox: sandbox, Model: agent.Model, Source: "agent", RunID: runID,
		})
	default:
		return nil, nil
	}
}

func (c *Controller) deletePromptAttachLLMFacadeToken(ctx context.Context, token string) {
	store, ok := c.configDB.(llmFacadeTokenDeleter)
	if !ok || strings.TrimSpace(token) == "" {
		return
	}
	_ = store.DeleteLLMFacadeToken(ctx, token)
}

// promptAttachRuntimeModel returns the model the guest runner should be told to
// use, which is not always the one the agent configured.
//
// The facade resolves an agent-compose provider/model pair and then republishes
// it in whatever namespace the guest agent addresses models by. opencode is the
// only provider where the two namespaces differ: its model must carry the
// provider key WriteOpenCodeRuntimeConfig writes into the guest's opencode.json,
// so EnsureOpenCodeFacadeConfig exports the resolved pair as OPENCODE_MODEL.
// Forwarding the configured model instead would hand opencode a provider name it
// has no entry for, and it exits without diagnosing the failure.
//
// The one-shot run path performs the same substitution through
// runtimefacade.AgentRuntimeConfig.Model; this is its prompt-attach counterpart.
func promptAttachRuntimeModel(agent execution.AgentConfig, managedEnv map[string]string) string {
	if domain.NormalizeAgentKind(agent.Provider) != "opencode" {
		return agent.Model
	}
	if model := strings.TrimSpace(managedEnv["OPENCODE_MODEL"]); model != "" {
		return model
	}
	return agent.Model
}
