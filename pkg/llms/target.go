package llms

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProviderForModel returns the connection as a request for model reaches it: a
// declared model that overrides the connection's base url is served on its own
// endpoint (see NewResolvedTarget), so a diagnostic that read the connection
// endpoint instead would report a verdict about an endpoint no request for that
// model ever reaches — an Azure-style deployment, where every model has its own
// endpoint, was reported as not supporting a protocol it serves. A model with no
// override, and one the connection does not declare, keep the connection's
// endpoint.
func ProviderForModel(provider Provider, bindings []ProviderModelBinding, model string) Provider {
	model = strings.TrimSpace(model)
	if model == "" {
		return provider
	}
	for _, binding := range bindings {
		if binding.ProviderID != provider.ID || strings.TrimSpace(binding.ModelID) != model {
			continue
		}
		if baseURL := strings.TrimSpace(binding.Config.BaseURL); baseURL != "" {
			provider.BaseURL = baseURL
		}
		return provider
	}
	return provider
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
		ContextWindow:   config.ContextWindow,
	}, nil
}
