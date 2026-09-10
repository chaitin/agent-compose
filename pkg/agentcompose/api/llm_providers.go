package api

import (
	"context"
	"fmt"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// LLMProviderStore supplies persistent API-owned upstream provider management.
type LLMProviderStore interface {
	CreateLLMProvider(context.Context, llms.ProviderReplacement) (llms.Provider, error)
	GetManagedLLMProvider(context.Context, string) (llms.Provider, error)
	ListManagedLLMProviders(context.Context) ([]llms.Provider, error)
	UpdateLLMProvider(context.Context, llms.ProviderReplacement) (llms.Provider, error)
	DeleteLLMProvider(context.Context, string) error
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
	return connect.NewResponse(&agentcomposev2.CreateProviderResponse{Provider: providerToV2(provider)}), nil
}

// GetProvider returns public configuration for an API-owned provider.
func (h *LLMHandler) GetProvider(ctx context.Context, req *connect.Request[agentcomposev2.GetProviderRequest]) (*connect.Response[agentcomposev2.GetProviderResponse], error) {
	provider, err := h.providers.GetManagedLLMProvider(ctx, req.Msg.GetId())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.GetProviderResponse{Provider: providerToV2(provider)}), nil
}

// ListProviders lists API-owned providers, including disabled entries.
func (h *LLMHandler) ListProviders(ctx context.Context, req *connect.Request[agentcomposev2.ListProvidersRequest]) (*connect.Response[agentcomposev2.ListProvidersResponse], error) {
	providers, err := h.providers.ListManagedLLMProviders(ctx)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	page, total, err := paginateList(providers, req.Msg.GetOffset(), req.Msg.GetLimit())
	if err != nil {
		return nil, err
	}
	result := make([]*agentcomposev2.LLMProvider, 0, len(page))
	for _, provider := range page {
		result = append(result, providerToV2(provider))
	}
	return connect.NewResponse(&agentcomposev2.ListProvidersResponse{Providers: result, Total: total}), nil
}

// UpdateProvider replaces public configuration and optionally rotates the key.
func (h *LLMHandler) UpdateProvider(ctx context.Context, req *connect.Request[agentcomposev2.UpdateProviderRequest]) (*connect.Response[agentcomposev2.UpdateProviderResponse], error) {
	input, err := providerReplacementFromV2(req.Msg.GetProvider())
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	provider, err := h.providers.UpdateLLMProvider(ctx, input)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.UpdateProviderResponse{Provider: providerToV2(provider)}), nil
}

// DeleteProvider removes API-owned configuration and its facade credentials.
func (h *LLMHandler) DeleteProvider(ctx context.Context, req *connect.Request[agentcomposev2.DeleteProviderRequest]) (*connect.Response[agentcomposev2.DeleteProviderResponse], error) {
	if err := h.providers.DeleteLLMProvider(ctx, req.Msg.GetId()); err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return connect.NewResponse(&agentcomposev2.DeleteProviderResponse{}), nil
}

func providerReplacementFromV2(spec *agentcomposev2.LLMProviderSpec) (llms.ProviderReplacement, error) {
	if spec == nil {
		return llms.ProviderReplacement{}, fmt.Errorf("%w: provider is required", domain.ErrInvalidArgument)
	}
	enabled := spec.Enabled == nil || spec.GetEnabled()
	return llms.ProviderReplacement{ID: spec.GetId(), Name: spec.GetName(), BaseURL: spec.GetBaseUrl(), Protocol: spec.GetProtocol(), APIKey: spec.ApiKey, Enabled: enabled}, nil
}

func providerToV2(provider llms.Provider) *agentcomposev2.LLMProvider {
	return &agentcomposev2.LLMProvider{
		Id: provider.ID, Name: provider.Name, BaseUrl: provider.BaseURL,
		Protocol: provider.DefaultWireAPI, Enabled: provider.Enabled, ApiKeySet: provider.APIKey != "",
		CreatedAt: timestamppb.New(provider.CreatedAt), UpdatedAt: timestamppb.New(provider.UpdatedAt),
	}
}
