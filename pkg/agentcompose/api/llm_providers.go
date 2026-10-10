package api

import (
	"context"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/llms"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// LLMProviderStore supplies persistent API-owned upstream provider management
// and the effective default model reference.
type LLMProviderStore interface {
	CreateLLMProvider(context.Context, llms.ProviderReplacement) (llms.Provider, error)
	GetManagedLLMProvider(context.Context, string) (llms.Provider, error)
	ListManagedLLMProviders(context.Context) ([]llms.Provider, error)
	UpdateLLMProvider(context.Context, llms.ProviderReplacement) (llms.Provider, error)
	DeleteLLMProvider(context.Context, string) error
	ListLLMProviderModelConfigs(context.Context) ([]llms.ProviderModelBinding, error)
	DefaultLLMModelReference(context.Context) (string, string, bool, error)
	SetDefaultLLMModel(context.Context, llms.ModelReference) (llms.ModelReference, error)
	ClearDefaultLLMModel(context.Context) error
}

// CreateProvider creates an API-owned upstream model provider.
func (h *LLMHandler) CreateProvider(ctx context.Context, req *connect.Request[agentcomposev2.CreateProviderRequest]) (*connect.Response[agentcomposev2.CreateProviderResponse], error) {
	input, err := providerReplacementFromV2(req.Msg.GetProvider())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	provider, err := h.providers.CreateLLMProvider(ctx, input)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	h.probeUpstream(ctx, provider)
	response, err := h.providerResponse(ctx, provider)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.CreateProviderResponse{Provider: response}), nil
}

// GetProvider returns public configuration for an API-owned provider.
func (h *LLMHandler) GetProvider(ctx context.Context, req *connect.Request[agentcomposev2.GetProviderRequest]) (*connect.Response[agentcomposev2.GetProviderResponse], error) {
	provider, err := h.providers.GetManagedLLMProvider(ctx, req.Msg.GetId())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	response, err := h.providerResponse(ctx, provider)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.GetProviderResponse{Provider: response}), nil
}

// ListProviders lists API-owned providers, including disabled entries.
func (h *LLMHandler) ListProviders(ctx context.Context, req *connect.Request[agentcomposev2.ListProvidersRequest]) (*connect.Response[agentcomposev2.ListProvidersResponse], error) {
	providers, err := h.providers.ListManagedLLMProviders(ctx)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	bindings, err := h.providers.ListLLMProviderModelConfigs(ctx)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	page, total, err := paginateList(providers, req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	result := make([]*agentcomposev2.LLMProvider, 0, len(page))
	for _, provider := range page {
		converted, err := providerToV2(provider, bindings, h.cachedCapabilities(provider))
		if err != nil {
			return nil, ConnectErrorForDomain(err)
		}
		result = append(result, converted)
	}
	return connect.NewResponse(&agentcomposev2.ListProvidersResponse{Providers: result, Total: total}), nil
}

// UpdateProvider applies explicit fields and preserves omitted values.
func (h *LLMHandler) UpdateProvider(ctx context.Context, req *connect.Request[agentcomposev2.UpdateProviderRequest]) (*connect.Response[agentcomposev2.UpdateProviderResponse], error) {
	input, err := providerReplacementFromV2(req.Msg.GetProvider())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	provider, err := h.providers.UpdateLLMProvider(ctx, input)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	h.probeUpstream(ctx, provider)
	response, err := h.providerResponse(ctx, provider)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.UpdateProviderResponse{Provider: response}), nil
}

// probeUpstream runs the synchronous post-write protocol check, bounded by the
// configured probe timeout. A connection that declares models is probed with one
// of them; otherwise the probe names one the endpoint advertises.
func (h *LLMHandler) probeUpstream(ctx context.Context, provider llms.Provider) {
	if h == nil || h.prober == nil {
		return
	}
	h.prober.ProbeConnection(ctx, provider, h.declaredProbeModel(ctx, provider.ID))
}

// declaredProbeModel returns the first declared model id in stable order, or ""
// when the connection declares none. A failed read is deliberately ignored: the
// probe is advisory, and an unnamed model only costs the /v1/models lookup.
func (h *LLMHandler) declaredProbeModel(ctx context.Context, providerID string) string {
	bindings, err := h.providers.ListLLMProviderModelConfigs(ctx)
	if err != nil {
		return ""
	}
	model := ""
	for _, binding := range bindings {
		if binding.ProviderID != providerID {
			continue
		}
		if model == "" || binding.ModelID < model {
			model = binding.ModelID
		}
	}
	return model
}

// DeleteProvider removes API-owned configuration and its facade credentials.
func (h *LLMHandler) DeleteProvider(ctx context.Context, req *connect.Request[agentcomposev2.DeleteProviderRequest]) (*connect.Response[agentcomposev2.DeleteProviderResponse], error) {
	if err := h.providers.DeleteLLMProvider(ctx, req.Msg.GetId()); err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.DeleteProviderResponse{}), nil
}
