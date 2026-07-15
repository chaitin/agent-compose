package llms

import (
	"context"
	"fmt"
	"strings"
)

type runtimeLLMProviderTargetStore interface {
	ProviderListStore
	ProviderModelWireAPIStore
	ListEnabledLLMModels(ctx context.Context) ([]Model, error)
}

// resolveRuntimeLLMProviderTarget pins a runtime request to its token provider.
// The requested model is not an authorization boundary: unknown models use the
// provider's default wire API and are left for the upstream to accept or reject.
func resolveRuntimeLLMProviderTarget(ctx context.Context, store runtimeLLMProviderTargetStore, requestedModel, providerID string) (ResolvedTarget, bool, error) {
	requestedModel = strings.TrimSpace(requestedModel)
	providerID = strings.TrimSpace(providerID)

	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		return ResolvedTarget{}, false, fmt.Errorf("list enabled llm providers for runtime facade: %w", err)
	}
	var provider Provider
	for _, candidate := range providers {
		if candidate.ID == providerID {
			provider = candidate
			break
		}
	}
	if provider.ID == "" {
		return ResolvedTarget{}, false, nil
	}

	wireAPI := NormalizeWireAPI(provider.DefaultWireAPI)
	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		return ResolvedTarget{}, false, fmt.Errorf("list enabled llm models for runtime facade: %w", err)
	}
	if configuredModel := SelectModel(models, requestedModel); configuredModel.ID != "" {
		configuredWireAPI, ok, err := store.LLMProviderModelWireAPI(ctx, provider.ID, configuredModel.ID)
		if err != nil {
			return ResolvedTarget{}, false, fmt.Errorf("resolve runtime facade provider model wire api: %w", err)
		}
		if ok && strings.TrimSpace(configuredWireAPI) != "" {
			wireAPI = NormalizeWireAPI(configuredWireAPI)
		}
	}

	headers, err := ProviderForwardHeaders(provider)
	if err != nil {
		return ResolvedTarget{}, false, err
	}
	return ResolvedTarget{
		Provider: provider,
		Model:    Model{ID: requestedModel, Name: requestedModel, Enabled: true},
		WireAPI:  wireAPI,
		Endpoint: EndpointForProvider(provider, wireAPI),
		Headers:  headers,
	}, true, nil
}
