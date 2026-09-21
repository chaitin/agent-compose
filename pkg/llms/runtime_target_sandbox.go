package llms

import (
	"context"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SandboxRuntimeLLMTarget resolves the upstream target for one proxied runtime
// call from the sandbox that owns it.
//
// An orchestrator that injects a provider environment into a sandbox publishes
// LLM_API_ENDPOINT, LLM_API_KEY, LLM_MODEL and LLM_API_PROTOCOL together, and
// the upstream answering on that endpoint serves exactly the published wire
// api. The runtime LLM proxy therefore has to resolve with the sandbox in hand.
// Resolving from daemon or catalog state instead let a previously stored
// connection decide the protocol, so a chat-completions model was posted to a
// responses endpoint and a gateway that routes by wire api rejected it.
//
// The precedence is the one the facade agents already apply: a sandbox that
// supplies its own provider environment owns the connection, model and wire
// api, and the daemon configuration answers only when the sandbox publishes no
// provider environment of its own.
func SandboxRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, sandbox *domain.Sandbox, providerFamily, requestedModel, providerID string) (ResolvedTarget, error) {
	if sandbox == nil {
		return ResolveRuntimeLLMTarget(ctx, config, store, requestedModel, providerID)
	}
	envItems, err := SandboxProviderEnvItems(ctx, store, sandbox, providerFamily)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if len(envItems) == 0 {
		return ResolveRuntimeLLMTarget(ctx, config, store, requestedModel, providerID)
	}
	return ResolveRuntimeLLMTargetWithEnv(ctx, store, RuntimeLLMTargetQuery{
		Config:                  config,
		SessionID:               sandbox.Summary.ID,
		PreferredProviderFamily: providerFamily,
		RequestedModel:          requestedModel,
		ProviderID:              providerID,
		EnvItems:                envItems,
	})
}
