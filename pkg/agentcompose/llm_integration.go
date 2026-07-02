package agentcompose

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	llmpkg "agent-compose/pkg/llm"
	"agent-compose/pkg/model"
)

const (
	llmAPIProtocolResponses       = llmpkg.APIProtocolResponses
	llmAPIProtocolChatCompletions = llmpkg.APIProtocolChatCompletions
	llmAPIProtocolMessages        = llmpkg.APIProtocolMessages
)

type LLMGenerateResult = model.LLMGenerateResult

type LLMClient struct {
	config   *appconfig.Config
	configDB *ConfigStore
	client   *http.Client
}

func newLLMClient(di do.Injector) (*LLMClient, error) {
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

func envItemsFromMap(values map[string]string, secret bool) []SessionEnvVar {
	return llmpkg.EnvItemsFromMap(values, secret)
}
