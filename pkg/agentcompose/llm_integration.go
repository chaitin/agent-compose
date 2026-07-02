package agentcompose

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"

	appconfig "agent-compose/pkg/config"
	llmpkg "agent-compose/pkg/llm"
	"agent-compose/pkg/model"
	"agent-compose/pkg/storage"
)

const (
	llmAPIProtocolResponses       = llmpkg.APIProtocolResponses
	llmAPIProtocolChatCompletions = llmpkg.APIProtocolChatCompletions
	llmAPIProtocolMessages        = llmpkg.APIProtocolMessages
)

func registerRuntimeLLMFacadeRoutes(app *echo.Echo, service *Service) {
	llmpkg.RegisterRuntimeFacadeRoutes(app, llmpkg.NewService(service.config, service.store, service.configDB, service.llm))
}

func IsRuntimeLLMFacadeRequest(r *http.Request) bool {
	return llmpkg.IsRuntimeFacadeRequest(r)
}

func ensureSessionLLMFacadeConfig(ctx context.Context, config *appconfig.Config, configDB *storage.ConfigStore, session *model.Session, agent, modelName, source, runID string) (map[string]string, error) {
	return llmpkg.EnsureSessionLLMFacadeConfig(ctx, config, configDB, session, agent, modelName, source, runID)
}

func envItemsFromMap(values map[string]string, secret bool) []model.SessionEnvVar {
	return llmpkg.EnvItemsFromMap(values, secret)
}
