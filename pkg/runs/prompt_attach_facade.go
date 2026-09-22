package runs

import (
	"context"
	"strings"

	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ensurePromptAttachLLMFacadeEnv prepares the managed LLM configuration for an
// attached prompt. It shares the runtime facade's single entry point, so the
// two paths cannot drift apart.
func (c *Controller) ensurePromptAttachLLMFacadeEnv(ctx context.Context, sandbox *domain.Sandbox, agent execution.AgentConfig, runID string) (map[string]string, error) {
	store, ok := c.configDB.(llmFacadeStore)
	if !ok || c.config == nil || sandbox == nil {
		return nil, nil
	}
	prepared, err := llms.PrepareAgentLLM(ctx, llms.AgentLLMRequest{
		Config:       c.config,
		Store:        store,
		Sandbox:      sandbox,
		AgentKind:    agent.Provider,
		Model:        agent.Model,
		ConnectionID: agent.LLMConnection,
		AgentEnv:     agent.EnvItems,
		Source:       "agent",
		RunID:        runID,
	})
	if err != nil {
		if llms.IsUnmanagedAgentLLMError(err) {
			return nil, nil
		}
		return nil, err
	}
	return prepared.Env, nil
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
// The facade republishes the resolved model in whatever namespace the guest
// agent addresses models by, under llms.GuestModelEnvName, and that value is
// forwarded verbatim. Forwarding the configured model instead makes the agent
// CLI disagree with the facade token: an unset model reaches the CLI verbatim,
// and the call fails or hangs without naming the cause.
func promptAttachRuntimeModel(agent execution.AgentConfig, managedEnv map[string]string) string {
	if model := strings.TrimSpace(managedEnv[llms.GuestModelEnvName]); model != "" {
		return model
	}
	return agent.Model
}
