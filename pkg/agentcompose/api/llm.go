package api

import (
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/llms"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

type LLMGenerator interface {
	Generate(ctx context.Context, prompt, model, outputSchemaJSON string) (llms.GenerateResult, error)
}

type LLMHandler struct {
	generator LLMGenerator
	providers LLMProviderStore
	prober    LLMUpstreamProber
}

// LLMUpstreamProber verifies which protocols a connection actually serves. The
// service calls it after a provider write so the operator sees the verdict at
// configuration time, not at the first request. A nil prober disables the check.
type LLMUpstreamProber interface {
	ProbeConnection(ctx context.Context, provider llms.Provider, model string)
}

func NewLLMHandler(generator LLMGenerator, providers LLMProviderStore) *LLMHandler {
	return &LLMHandler{generator: generator, providers: providers}
}

// WithUpstreamProbe attaches the post-write protocol probe and returns the
// handler so composition can chain it.
func (h *LLMHandler) WithUpstreamProbe(prober LLMUpstreamProber) *LLMHandler {
	if h != nil {
		h.prober = prober
	}
	return h
}

func (h *LLMHandler) Generate(ctx context.Context, req *connect.Request[agentcomposev2.GenerateLLMRequest]) (*connect.Response[agentcomposev2.GenerateLLMResponse], error) {
	if h == nil || h.generator == nil {
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("llm client is unavailable"))
	}
	result, err := h.generator.Generate(ctx, req.Msg.GetPrompt(), req.Msg.GetModel(), req.Msg.GetOutputSchema())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
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
