package agentcompose

import (
	"context"
	"net/http"

	protocolbridge "github.com/chaitin/ai-api-protocol-bridge"
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

func (s *Service) handleRuntimeLLMResponses(c echo.Context) error {
	return s.runtimeLLMFacade().HandleResponses(c)
}

func (s *Service) handleRuntimeLLMChatCompletions(c echo.Context) error {
	return s.runtimeLLMFacade().HandleChatCompletions(c)
}

func (s *Service) handleRuntimeLLMAnthropicMessages(c echo.Context) error {
	return s.runtimeLLMFacade().HandleAnthropicMessages(c)
}

func (s *Service) handleRuntimeLLM(c echo.Context, inboundProtocol protocolbridge.Protocol, facadeWireAPI string) error {
	return s.runtimeLLMFacade().Handle(c, inboundProtocol, facadeWireAPI)
}

func runtimeLLMUseGenericResponsesTextParts(target LLMResolvedTarget, upstreamProtocol protocolbridge.Protocol) bool {
	return llmdomain.RuntimeLLMUseGenericResponsesTextParts(target, upstreamProtocol)
}

func forbiddenRuntimeLLMHeader(name string) bool {
	return llmdomain.ForbiddenRuntimeLLMHeader(name)
}
