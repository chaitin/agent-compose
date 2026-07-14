package api

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"agent-compose/pkg/llms"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type LLMGenerator interface {
	Generate(ctx context.Context, prompt, model, outputSchemaJSON string) (llms.GenerateResult, error)
}

type LLMHandler struct {
	generator LLMGenerator
}

func NewLLMHandler(generator LLMGenerator) *LLMHandler {
	return &LLMHandler{generator: generator}
}

func (h *LLMHandler) Generate(ctx context.Context, req *connect.Request[agentcomposev2.GenerateLLMRequest]) (*connect.Response[agentcomposev2.GenerateLLMResponse], error) {
	if h == nil || h.generator == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("llm client is unavailable"))
	}
	result, err := h.generator.Generate(ctx, req.Msg.GetPrompt(), req.Msg.GetModel(), req.Msg.GetOutputSchema())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.GenerateLLMResponse{
		Text:         result.Text,
		Model:        result.Model,
		ResponseId:   result.ResponseID,
		FinishReason: result.FinishReason,
		Json:         LLMJSONResponseText(result.Text, req.Msg.GetOutputSchema()),
	}), nil
}

func LLMJSONResponseText(text, outputSchemaJSON string) string {
	if strings.TrimSpace(outputSchemaJSON) == "" {
		return ""
	}
	return strings.TrimSpace(text)
}
