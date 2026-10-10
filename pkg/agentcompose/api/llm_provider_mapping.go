package api

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func providerReplacementFromV2(spec *agentcomposev2.LLMProviderSpec) (llms.ProviderReplacement, error) {
	if spec == nil {
		return llms.ProviderReplacement{}, fmt.Errorf("%w: provider is required", domain.ErrInvalidArgument)
	}
	auth, err := providerAuthFromV2(spec.Auth)
	if err != nil {
		return llms.ProviderReplacement{}, err
	}
	return llms.ProviderReplacement{
		ID: spec.GetId(), Name: spec.GetName(), BaseURL: spec.GetBaseUrl(),
		Protocol: spec.GetProtocol(), APIKey: spec.ApiKey, Enabled: spec.Enabled,
		Auth: auth, Models: modelSpecsFromV2(spec.GetModels()),
	}, nil
}

// modelSpecsFromV2 maps the declared model set. The wrapper message's presence
// is what separates "leave the stored set alone" from "clear it": an absent
// field returns nil, while a present empty one returns a non-nil empty slice.
func modelSpecsFromV2(models *agentcomposev2.LLMProviderModels) *[]llms.ModelSpec {
	if models == nil {
		return nil
	}
	converted := make([]llms.ModelSpec, 0, len(models.GetModels()))
	for _, model := range models.GetModels() {
		if model == nil {
			continue
		}
		converted = append(converted, llms.ModelSpec{
			ID: model.GetId(), Name: model.GetName(), Protocol: model.GetProtocol(),
			BaseURL: model.GetBaseUrl(), Headers: model.GetHeaders(),
			MaxOutputTokens: int(model.GetMaxOutputTokens()),
		})
	}
	return &converted
}

// providerAuthFromV2 maps the optional presence onto the replacement's explicit
// intent. An absent field leaves Auth nil, which takes the protocol convention on
// create and preserves the stored override on update, including protocol
// changes; an explicit value — including the unspecified presentation that
// clears an override — is carried through.
func providerAuthFromV2(auth *agentcomposev2.LLMProviderAuth) (*llms.ProviderAuth, error) {
	if auth == nil {
		return nil, nil
	}
	presentation := llms.ProviderAuth("")
	switch *auth {
	case agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_UNSPECIFIED:
		presentation = ""
	case agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY:
		presentation = llms.ProviderAuthXAPIKey
	case agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER:
		presentation = llms.ProviderAuthBearer
	default:
		return nil, fmt.Errorf("%w: auth must be x-api-key or bearer", domain.ErrInvalidArgument)
	}
	return &presentation, nil
}

func providerAuthToV2(auth llms.ProviderAuth) agentcomposev2.LLMProviderAuth {
	switch auth {
	case llms.ProviderAuthXAPIKey:
		return agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_X_API_KEY
	case llms.ProviderAuthBearer:
		return agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_BEARER
	default:
		return agentcomposev2.LLMProviderAuth_LLM_PROVIDER_AUTH_UNSPECIFIED
	}
}

// providerToV2 converts one provider from an already-read binding set and probe
// verdict. It stays pure so mapping is testable without a store or a prober, and
// it returns a raw error so every transport caller maps it exactly once; a nil
// verdict means the connection has not been probed since startup.
func providerToV2(provider llms.Provider, bindings []llms.ProviderModelBinding, capabilities *llms.UpstreamProbeResult) (*agentcomposev2.LLMProvider, error) {
	models, err := modelSpecsToV2(provider.ID, bindings)
	if err != nil {
		return nil, err
	}
	converted := &agentcomposev2.LLMProvider{
		Id: provider.ID, Name: provider.Name,
		BaseUrl: provider.BaseURL, Protocol: provider.DefaultWireAPI,
		Enabled: provider.Enabled, ApiKeySet: provider.APIKey != "",
		// The stored override, not the effective header, so a client can send the
		// response back through Update without hardening the protocol convention
		// into an explicit override.
		Auth:      providerAuthToV2(provider.Auth),
		CreatedAt: timestamppb.New(provider.CreatedAt), UpdatedAt: timestamppb.New(provider.UpdatedAt),
		Models: models,
	}
	if capabilities != nil {
		converted.Capabilities = capabilitiesToV2(*capabilities)
	}
	return converted, nil
}

// providerResponse converts one provider, reading its declared models and its
// cached probe verdict.
func (h *LLMHandler) providerResponse(ctx context.Context, provider llms.Provider) (*agentcomposev2.LLMProvider, error) {
	bindings, err := h.providers.ListLLMProviderModelConfigs(ctx)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	converted, err := providerToV2(provider, bindings, h.cachedCapabilities(provider))
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	return converted, nil
}

// cachedCapabilities returns the advisory probe verdict, or nil when probing is
// disabled or the connection has not been probed yet.
func (h *LLMHandler) cachedCapabilities(provider llms.Provider) *llms.UpstreamProbeResult {
	if h == nil || h.prober == nil {
		return nil
	}
	result, ok := h.prober.Capabilities(provider)
	if !ok {
		return nil
	}
	return &result
}

// modelSpecsToV2 converts the bindings one provider owns, in binding order.
func modelSpecsToV2(providerID string, bindings []llms.ProviderModelBinding) ([]*agentcomposev2.LLMModelSpec, error) {
	var specs []*agentcomposev2.LLMModelSpec
	for _, binding := range bindings {
		if binding.ProviderID != providerID {
			continue
		}
		headers, err := modelHeadersToV2(binding.Config.HeadersJSON)
		if err != nil {
			return nil, fmt.Errorf("provider %q model %q: %w", providerID, binding.ModelID, err)
		}
		spec := &agentcomposev2.LLMModelSpec{
			Id: binding.ModelID, Name: binding.Config.DisplayName,
			Protocol: binding.Config.WireAPI, BaseUrl: binding.Config.BaseURL,
			Headers: headers,
		}
		// Zero means the model inherits the facade default, which is absence on
		// the wire rather than an explicit zero.
		if binding.Config.MaxOutputTokens > 0 {
			maxOutputTokens := int32(binding.Config.MaxOutputTokens)
			spec.MaxOutputTokens = &maxOutputTokens
		}
		specs = append(specs, spec)
	}
	return specs, nil
}

func modelHeadersToV2(raw string) (map[string]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	headers := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &headers); err != nil {
		return nil, fmt.Errorf("decode stored model headers: %w", err)
	}
	if len(headers) == 0 {
		return nil, nil
	}
	return headers, nil
}

// capabilitiesToV2 exposes a probe verdict. Protocol outcomes are reported
// verbatim so a client can tell a proven absence from an unknown.
func capabilitiesToV2(result llms.UpstreamProbeResult) *agentcomposev2.LLMProviderCapabilities {
	capabilities := &agentcomposev2.LLMProviderCapabilities{
		Models: result.Models, ProbedModel: result.Model,
	}
	if !result.ProbedAt.IsZero() {
		capabilities.ProbedAt = timestamppb.New(result.ProbedAt)
	}
	for _, probe := range result.Protocols {
		capabilities.Probes = append(capabilities.Probes, &agentcomposev2.LLMProtocolProbe{
			Protocol: string(probe.Protocol), Outcome: string(probe.Outcome), Detail: probe.Detail,
		})
	}
	return capabilities
}

// defaultModelToV2 converts a stored reference into its transport shape.
func defaultModelToV2(reference llms.ModelReference) *agentcomposev2.LLMModelReference {
	if reference.ProviderID == "" || reference.ModelID == "" {
		return nil
	}
	return &agentcomposev2.LLMModelReference{ProviderId: reference.ProviderID, ModelId: reference.ModelID}
}
