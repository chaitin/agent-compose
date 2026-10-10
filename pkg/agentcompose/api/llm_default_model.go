package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// GetDefaultModel reports the model the daemon falls back to for runs that
// declare none.
func (h *LLMHandler) GetDefaultModel(ctx context.Context, _ *connect.Request[agentcomposev2.GetDefaultModelRequest]) (*connect.Response[agentcomposev2.GetDefaultModelResponse], error) {
	providerID, modelID, ok, err := h.providers.DefaultLLMModelReference(ctx)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	response := &agentcomposev2.GetDefaultModelResponse{}
	if ok {
		response.Model = defaultModelToV2(llms.ModelReference{ProviderID: providerID, ModelID: modelID})
	}
	return connect.NewResponse(response), nil
}

// SetDefaultModel makes one provider/model pair the default, replacing any
// earlier reference. models.json keeps the last word: when it declares its own
// default, the next startup projects that reference over this one.
func (h *LLMHandler) SetDefaultModel(ctx context.Context, req *connect.Request[agentcomposev2.SetDefaultModelRequest]) (*connect.Response[agentcomposev2.SetDefaultModelResponse], error) {
	reference := req.Msg.GetModel()
	if reference == nil {
		return nil, ConnectErrorForDomain(fmt.Errorf("%w: model is required", domain.ErrInvalidArgument))
	}
	stored, err := h.providers.SetDefaultLLMModel(ctx, llms.ModelReference{
		ProviderID: reference.GetProviderId(), ModelID: reference.GetModelId(),
	})
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.SetDefaultModelResponse{Model: defaultModelToV2(stored)}), nil
}

// ClearDefaultModel removes the default, so a run that declares no model is
// reported as unconfigured unless models.json declares one.
func (h *LLMHandler) ClearDefaultModel(ctx context.Context, _ *connect.Request[agentcomposev2.ClearDefaultModelRequest]) (*connect.Response[agentcomposev2.ClearDefaultModelResponse], error) {
	if err := h.providers.ClearDefaultLLMModel(ctx); err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.ClearDefaultModelResponse{}), nil
}
