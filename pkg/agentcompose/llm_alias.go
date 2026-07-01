package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	llmpkg "agent-compose/pkg/llm"
	"agent-compose/pkg/model"
	"context"
	"net/http"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"
)

const (
	llmAPIProtocolResponses       = llmpkg.APIProtocolResponses
	llmAPIProtocolChatCompletions = llmpkg.APIProtocolChatCompletions
	llmAPIProtocolMessages        = llmpkg.APIProtocolMessages

	llmProviderFamilyOpenAI       = llmpkg.ProviderFamilyOpenAI
	llmProviderFamilyAnthropic    = llmpkg.ProviderFamilyAnthropic
	llmProviderScopeSystem        = llmpkg.ProviderScopeSystem
	llmProviderScopeEnvDefault    = llmpkg.ProviderScopeEnvDefault
	llmProviderScopeSessionEnv    = llmpkg.ProviderScopeSessionEnv
	llmProviderIDDefaultOpenAI    = llmpkg.ProviderIDDefaultOpenAI
	llmProviderIDDefaultAnthropic = llmpkg.ProviderIDDefaultAnthropic
)

type LLMGenerateResult = model.LLMGenerateResult
type LLMProvider = model.LLMProvider
type LLMModel = model.LLMModel
type LLMResolvedTarget = model.LLMResolvedTarget
type LLMFacadeToken = model.LLMFacadeToken

type LLMClient struct {
	config   *appconfig.Config
	configDB *ConfigStore
	client   *http.Client
}

func NewLLMClient(di do.Injector) (*LLMClient, error) {
	config := do.MustInvoke[*appconfig.Config](di)
	client := llmpkg.NewClient(config, do.MustInvoke[*ConfigStore](di), nil)
	return &LLMClient{config: config, configDB: do.MustInvoke[*ConfigStore](di), client: clientHTTPClient(client, config)}, nil
}

func clientHTTPClient(client *llmpkg.LLMClient, config *appconfig.Config) *http.Client {
	if config == nil {
		return &http.Client{}
	}
	return &http.Client{Timeout: config.LLMTimeout}
}

func (c *LLMClient) componentClient() *llmpkg.LLMClient {
	if c == nil {
		return nil
	}
	return llmpkg.NewClient(c.config, c.configDB, c.client)
}

func (c *LLMClient) Generate(ctx context.Context, prompt, modelName, outputSchemaJSON string) (LLMGenerateResult, error) {
	return c.componentClient().Generate(ctx, prompt, modelName, outputSchemaJSON)
}

func registerRuntimeLLMFacadeRoutes(app *echo.Echo, service *Service) {
	llmpkg.RegisterRuntimeFacadeRoutes(app, llmpkg.NewService(service.config, service.store, service.configDB, service.llm.componentClient()))
}

func IsRuntimeLLMFacadeRequest(r *http.Request) bool {
	return llmpkg.IsRuntimeFacadeRequest(r)
}

func ensureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *ConfigStore, session *Session, agent, modelName, source, runID string) (map[string]string, error) {
	return llmpkg.EnsureSessionLLMFacadeConfig(ctx, config, configDB, session, agent, modelName, source, runID)
}

func newLLMFacadeToken(sessionID, modelName, providerID, wireAPI, source, runID string) (string, LLMFacadeToken, error) {
	return llmpkg.NewFacadeToken(sessionID, modelName, providerID, wireAPI, source, runID)
}

func resolveRuntimeLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel, providerID string) (LLMResolvedTarget, error) {
	return llmpkg.ResolveRuntimeTarget(ctx, config, store, requestedModel, providerID)
}

func resolveLLMTarget(ctx context.Context, config *appconfig.Config, store *ConfigStore, requestedModel string) (LLMResolvedTarget, error) {
	return llmpkg.ResolveTarget(ctx, config, store, requestedModel)
}

func resolveRuntimeLLMTargetWithEnv(ctx context.Context, config *appconfig.Config, store *ConfigStore, sessionID, preferredProviderFamily, requestedModel, providerID string, envItems []SessionEnvVar) (LLMResolvedTarget, error) {
	return llmpkg.ResolveRuntimeTargetWithEnv(ctx, config, store, sessionID, preferredProviderFamily, requestedModel, providerID, envItems)
}

func resolveLLMTargetForProviderFamily(ctx context.Context, config *appconfig.Config, store *ConfigStore, providerFamily, requestedModel string) (LLMResolvedTarget, error) {
	return llmpkg.ResolveTargetForProviderFamily(ctx, config, store, providerFamily, requestedModel)
}

func providerForwardHeaders(provider LLMProvider) (http.Header, error) {
	return llmpkg.ProviderForwardHeaders(provider)
}

func runtimeLLMUseGenericResponsesTextParts(target LLMResolvedTarget, upstreamProtocol protocolbridge.Protocol) bool {
	return llmpkg.RuntimeUseGenericResponsesTextParts(target, upstreamProtocol)
}

func forbiddenRuntimeLLMHeader(name string) bool {
	return llmpkg.ForbiddenRuntimeHeader(name)
}

func envItemsFromMap(values map[string]string, secret bool) []SessionEnvVar {
	return llmpkg.EnvItemsFromMap(values, secret)
}

func runtimeEnvMap(items []SessionEnvVar) map[string]string {
	return llmpkg.RuntimeEnvMap(items)
}

func managedRuntimeEnvMap(items []SessionEnvVar) map[string]string {
	return llmpkg.ManagedRuntimeEnvMap(items)
}

func llmProviderKeyName(name string) bool {
	return llmpkg.ProviderKeyName(name)
}

func guestRuntimeLLMBaseURL(config *appconfig.Config, session *Session) string {
	return llmpkg.GuestRuntimeBaseURL(config, session)
}

func splitOpenCodeModel(modelName string) (string, string, error) {
	return llmpkg.SplitOpenCodeModel(modelName)
}

func sessionEnvProviderID(sessionID, providerFamily string) string {
	return llmpkg.SessionEnvProviderID(sessionID, providerFamily)
}

func ensureSessionAnthropicEnvProvider(ctx context.Context, store *ConfigStore, sessionID, requestedModel string, envItems []SessionEnvVar) (string, error) {
	return llmpkg.EnsureSessionAnthropicEnvProvider(ctx, store, sessionID, requestedModel, envItems)
}
