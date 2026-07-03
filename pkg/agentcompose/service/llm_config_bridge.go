package agentcompose

import (
	"context"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/storage/configstore"
)

const llmFacadeTokenRetention = configstore.LLMFacadeTokenRetention

func resolveLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) (llms.ResolvedTarget, error) {
	return configstore.ResolveLLMTarget(ctx, config, store.inner(), requestedModel)
}

func resolveRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel, providerID string) (llms.ResolvedTarget, error) {
	return configstore.ResolveRuntimeLLMTarget(ctx, config, store.inner(), requestedModel, providerID)
}

func resolveRuntimeLLMTargetWithEnv(ctx context.Context, config *appconfig.Config, store *ConfigStore, sessionID, preferredProviderFamily, requestedModel, providerID string, envItems []SessionEnvVar) (llms.ResolvedTarget, error) {
	return configstore.ResolveRuntimeLLMTargetWithEnv(ctx, config, store.inner(), sessionID, preferredProviderFamily, requestedModel, providerID, envItems)
}

func ensureSessionOpenAIEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	return configstore.EnsureSessionOpenAIEnvProvider(ctx, store.inner(), sessionID, requestedModel, envItems)
}

func ensureSessionAnthropicEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	return configstore.EnsureSessionAnthropicEnvProvider(ctx, store.inner(), sessionID, requestedModel, envItems)
}

func hasEnabledLLMProviderID(ctx context.Context, store *ConfigStore, providerID string) bool {
	return configstore.HasEnabledLLMProviderID(ctx, store.inner(), providerID)
}

func defaultLLMEnvProviderLookup(ctx context.Context, config *appconfig.Config, store *ConfigStore) llms.EnvProviderLookup {
	return configstore.DefaultLLMEnvProviderLookup(ctx, config, store.inner())
}

func lookupEnvValue(ctx context.Context, store *ConfigStore, key string) string {
	return configstore.LookupEnvValue(ctx, store.inner(), key)
}

func resolveLLMTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store *ConfigStore, providerFamily, requestedModel string) (llms.ResolvedTarget, error) {
	return configstore.ResolveLLMTargetForProviderFamily(ctx, config, store.inner(), providerFamily, requestedModel)
}
