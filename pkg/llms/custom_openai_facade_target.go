package llms

import (
	"context"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

func resolveCustomOpenAIFacadeTarget(ctx context.Context, config *appconfig.Config, store LLMResolverStore, sandbox *domain.Sandbox, providerID, model string) (ResolvedTarget, error) {
	envItems, err := SandboxProviderEnvItems(ctx, store, sandbox, ProviderFamilyOpenAI)
	if err != nil {
		return ResolvedTarget{}, err
	}
	sandboxID := ""
	if sandbox != nil {
		sandboxID = sandbox.Summary.ID
	}
	if HasEnabledLLMProviderID(ctx, store, providerID) {
		return ResolveRuntimeLLMTargetWithEnv(ctx, config, store, sandboxID, ProviderFamilyOpenAI, model, providerID, envItems)
	}
	if sandboxID != "" && HasOpenAIEnvProviderInput(envItems) {
		sessionProviderID, err := ensureSessionOpenAIEnvProviderWithConfig(ctx, config, store, sandboxID, model, envItems)
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
