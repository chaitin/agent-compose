package llm

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/labstack/echo/v4"

	"agent-compose/pkg/storage"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
)

type Service struct {
	config   *appconfig.Config
	store    *storage.Store
	configDB *storage.ConfigStore
	client   *LLMClient
}

func NewService(config *appconfig.Config, store *storage.Store, configDB *storage.ConfigStore, client *LLMClient) *Service {
	return &Service{config: config, store: store, configDB: configDB, client: client}
}

func RegisterRuntimeFacadeRoutes(app *echo.Echo, service *Service) {
	if app == nil || service == nil {
		return
	}
	registerRuntimeLLMFacadeRoutes(app, service)
}

func (s *Service) Generate(ctx context.Context, req *connect.Request[agentcomposev1.GenerateLLMRequest]) (*connect.Response[agentcomposev1.GenerateLLMResponse], error) {
	if s == nil || s.client == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("llm client is unavailable"))
	}
	result, err := s.client.Generate(ctx, req.Msg.GetPrompt(), req.Msg.GetModel(), req.Msg.GetOutputSchema())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev1.GenerateLLMResponse{
		Text:         result.Text,
		Model:        result.Model,
		ResponseId:   result.ResponseID,
		FinishReason: result.FinishReason,
		Json:         JSONResponseText(result.Text, req.Msg.GetOutputSchema()),
	}), nil
}

func JSONResponseText(text, outputSchemaJSON string) string {
	if strings.TrimSpace(outputSchemaJSON) == "" {
		return ""
	}
	return strings.TrimSpace(text)
}
