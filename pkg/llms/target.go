package llms

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
