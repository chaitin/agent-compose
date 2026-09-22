package llms

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type ProviderModelConfigStore interface {
	LLMProviderModelConfig(ctx context.Context, providerID, modelID string) (ProviderModelConfig, bool, error)
}

// ResolvedTargetInput groups BuildResolvedTarget's provider/model/wireAPI selection.
type ResolvedTargetInput struct {
	Provider Provider
	Model    Model
	WireAPI  string
}

// BuildResolvedTarget applies provider defaults followed by per-model catalog
// overrides when the selected model has metadata. It looks the binding up from
// the store; prefer Catalog.Resolve on the request path.
func BuildResolvedTarget(ctx context.Context, store ProviderModelWireAPIStore, in ResolvedTargetInput) (ResolvedTarget, error) {
	config := ProviderModelConfig{}
	if configStore, ok := store.(ProviderModelConfigStore); ok {
		resolved, found, err := configStore.LLMProviderModelConfig(ctx, in.Provider.ID, in.Model.ID)
		if err != nil {
			return ResolvedTarget{}, err
		}
		if found {
			config = resolved
		}
	}
	config.WireAPI = firstNonEmptyTrimmed(config.WireAPI, in.WireAPI)
	return NewResolvedTarget(in.Provider, in.Model, config)
}

// NewResolvedTarget builds a ResolvedTarget from an already-loaded connection,
// model, and per-model binding override. It is pure: the upstream protocol,
// endpoint, and headers depend only on its inputs, and the protocol is never
// supplied by the caller.
func NewResolvedTarget(provider Provider, model Model, config ProviderModelConfig) (ResolvedTarget, error) {
	effectiveProvider := provider
	if baseURL := strings.TrimSpace(config.BaseURL); baseURL != "" {
		effectiveProvider.BaseURL = baseURL
	}
	headers, err := ProviderForwardHeaders(effectiveProvider)
	if err != nil {
		return ResolvedTarget{}, err
	}
	if raw := strings.TrimSpace(config.HeadersJSON); raw != "" && raw != "{}" {
		values := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &values); err != nil {
			return ResolvedTarget{}, fmt.Errorf("decode provider %q model %q headers: %w", provider.ID, model.ID, err)
		}
		for key, value := range values {
			if ForbiddenProviderHeader(key, effectiveProvider.AuthHeader) {
				continue
			}
			headers.Set(strings.TrimSpace(key), value)
		}
	}
	wireAPI := NormalizeWireAPI(firstNonEmptyTrimmed(config.WireAPI, provider.DefaultWireAPI))
	return ResolvedTarget{
		Provider: effectiveProvider, Model: model, WireAPI: wireAPI,
		Endpoint: EndpointForProvider(effectiveProvider, wireAPI), Headers: headers,
		MaxOutputTokens: config.MaxOutputTokens,
	}, nil
}
