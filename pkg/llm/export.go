package llm

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"net/http"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
)

const (
	APIProtocolResponses       = llmAPIProtocolResponses
	APIProtocolChatCompletions = llmAPIProtocolChatCompletions
	APIProtocolMessages        = llmAPIProtocolMessages

	ProviderFamilyOpenAI       = llmProviderFamilyOpenAI
	ProviderFamilyAnthropic    = llmProviderFamilyAnthropic
	ProviderScopeSystem        = llmProviderScopeSystem
	ProviderScopeEnvDefault    = llmProviderScopeEnvDefault
	ProviderScopeSessionEnv    = llmProviderScopeSessionEnv
	ProviderIDDefaultOpenAI    = llmProviderIDDefaultOpenAI
	ProviderIDDefaultAnthropic = llmProviderIDDefaultAnthropic

	FacadeTokenSourceAgent = llmFacadeTokenSourceAgent
)

func IsRuntimeFacadeRequest(r *http.Request) bool {
	return IsRuntimeLLMFacadeRequest(r)
}

func EnsureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, agent, model, source, runID string) (map[string]string, error) {
	return ensureSessionLLMFacadeConfig(ctx, config, configDB, session, agent, model, source, runID)
}

func NewFacadeToken(sessionID, model, providerID, wireAPI, source, runID string) (string, LLMFacadeToken, error) {
	return newLLMFacadeToken(sessionID, model, providerID, wireAPI, source, runID)
}

func ResolveRuntimeTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel, providerID string) (LLMResolvedTarget, error) {
	return resolveRuntimeLLMTarget(ctx, config, store, requestedModel, providerID)
}

func ResolveTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) (LLMResolvedTarget, error) {
	return resolveLLMTarget(ctx, config, store, requestedModel)
}

func ResolveRuntimeTargetWithEnv(ctx context.Context, config *appconfig.Config, store *ConfigStore, sessionID, preferredProviderFamily, requestedModel, providerID string, envItems []SessionEnvVar) (LLMResolvedTarget, error) {
	return resolveRuntimeLLMTargetWithEnv(ctx, config, store, sessionID, preferredProviderFamily, requestedModel, providerID, envItems)
}

func ResolveTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store *ConfigStore, providerFamily, requestedModel string) (LLMResolvedTarget, error) {
	return resolveLLMTargetForProviderFamily(ctx, config, store, providerFamily, requestedModel)
}

func ProviderForwardHeaders(provider LLMProvider) (http.Header, error) {
	return providerForwardHeaders(provider)
}

func NormalizeAPIEndpoint(raw string) string {
	return normalizeLLMAPIEndpoint(raw)
}

func NormalizeAPIEndpointForProtocol(raw, protocol string) string {
	return normalizeLLMAPIEndpointForProtocol(raw, protocol)
}

func EndpointForProvider(provider LLMProvider, wireAPI string) string {
	return llmEndpointForProvider(provider, wireAPI)
}

func RuntimeUseGenericResponsesTextParts(target LLMResolvedTarget, upstreamProtocol protocolbridge.Protocol) bool {
	return runtimeLLMUseGenericResponsesTextParts(target, upstreamProtocol)
}

func ForbiddenRuntimeHeader(name string) bool {
	return forbiddenRuntimeLLMHeader(name)
}

func MergeManagedExecEnv(base map[string]string, managed map[string]string) map[string]string {
	return mergeManagedExecEnv(base, managed)
}

func EnvItemsFromMap(values map[string]string, secret bool) []SessionEnvVar {
	return envItemsFromMap(values, secret)
}

func FilterPersistedRuntimeEnv(items []SessionEnvVar) []SessionEnvVar {
	return filterPersistedRuntimeEnv(items)
}

func RuntimeEnvMap(items []SessionEnvVar) map[string]string {
	return runtimeEnvMap(items)
}

func ManagedRuntimeEnvMap(items []SessionEnvVar) map[string]string {
	return managedRuntimeEnvMap(items)
}

func ProviderKeyName(name string) bool {
	return llmProviderKeyName(name)
}

func GuestRuntimeBaseURL(config *appconfig.Config, session *Session) string {
	return guestRuntimeLLMBaseURL(config, session)
}

func SplitOpenCodeModel(model string) (string, string, error) {
	return splitOpenCodeModel(model)
}

func SessionEnvProviderID(sessionID, providerFamily string) string {
	return sessionEnvProviderID(sessionID, providerFamily)
}

func EnsureSessionAnthropicEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	return ensureSessionAnthropicEnvProvider(ctx, store, sessionID, requestedModel, envItems)
}

func (c *LLMClient) ResolveProtocol(ctx context.Context) string {
	return c.resolveProtocol(ctx)
}

func (c *LLMClient) ResolveEndpoint(ctx context.Context) string {
	return c.resolveEndpoint(ctx)
}

func (c *LLMClient) ResolveSetting(ctx context.Context, fallback string, keys ...string) string {
	return c.resolveSetting(ctx, fallback, keys...)
}
