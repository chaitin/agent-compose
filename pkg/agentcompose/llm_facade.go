package agentcompose

import (
	"context"
	"net/http"

	"github.com/labstack/echo/v4"

	llmdomain "agent-compose/internal/agentcompose/llm"
	"agent-compose/internal/agentcompose/transport/httpapi"
)

func IsRuntimeLLMFacadeRequest(r *http.Request) bool {
	return llmdomain.IsRuntimeLLMFacadeRequest(r)
}

func registerRuntimeLLMFacadeRoutes(app *echo.Echo, service *Service) {
	facade := service.runtimeLLMFacade()
	httpapi.RegisterRuntimeLLMFacadeRoutes(app, httpapi.RuntimeLLMHandlers{
		HandleResponses:         facade.HandleResponses,
		HandleChatCompletions:   facade.HandleChatCompletions,
		HandleAnthropicMessages: facade.HandleAnthropicMessages,
	})
}

func (s *Service) runtimeLLMFacade() llmdomain.RuntimeFacade {
	var client llmdomain.FacadeHTTPClient
	if s != nil && s.llm != nil {
		client = s.llm.client
	}
	return llmdomain.RuntimeFacade{
		Tokens:   s.configDB,
		Sessions: s.store,
		Client:   client,
		ResolveTarget: func(ctx context.Context, requestedModel, providerID string) (llmdomain.ResolvedTarget, error) {
			return resolveRuntimeLLMTarget(ctx, s.config, s.configDB, requestedModel, providerID)
		},
	}
}
