package egress

import (
	"fmt"
	"strings"
)

// EndpointPurpose identifies why the engine auto-allows an endpoint. The
// values are part of the audit surface, so they are stable strings.
type EndpointPurpose string

const (
	// PurposeLLMFacade is the runtime LLM facade the guest calls to reach a
	// model. Cutting it off under deny semantics makes the agent unable to
	// reason at all.
	PurposeLLMFacade EndpointPurpose = "llm-facade"
	// PurposeTelemetry is the OTLP endpoint the guest exports to. Cutting it
	// off under deny semantics silently removes observability and audit
	// evidence.
	PurposeTelemetry EndpointPurpose = "telemetry"
)

// Sources name where an engine-owned endpoint came from, so a report can point
// at the configuration that produced it.
const (
	SourceRuntimeBaseURL        = "runtime_base_url"
	SourceAgentTelemetryOTLPEnd = "agent_telemetry_otlp_endpoint"
)

// EngineEndpoint is one engine-owned endpoint the engine auto-allows once a
// sandbox declares deny semantics. It is engine-side data: a user cannot
// declare, remove, or override it.
type EngineEndpoint struct {
	Purpose  EndpointPurpose
	Endpoint Endpoint
	// Source names the configuration the endpoint was derived from.
	Source string
}

// Validate rejects an engine endpoint the policy could not act on. The engine
// builds these from its own configuration, so an invalid one is a programming
// error rather than user input.
func (e EngineEndpoint) Validate() error {
	switch e.Purpose {
	case PurposeLLMFacade, PurposeTelemetry:
	default:
		return fmt.Errorf("engine endpoint purpose must be %q or %q, got %q", PurposeLLMFacade, PurposeTelemetry, e.Purpose)
	}
	if _, err := NewEndpoint(e.Endpoint.Host, e.Endpoint.Port, e.Endpoint.Protocol); err != nil {
		return fmt.Errorf("engine endpoint %s: %w", e.Purpose, err)
	}
	if strings.TrimSpace(e.Source) == "" {
		return fmt.Errorf("engine endpoint %s names no source", e.Purpose)
	}
	return nil
}

// EngineEndpoints derives the engine-owned endpoints from the addresses the
// daemon actually hands to a guest: the runtime LLM facade base URL and the
// OTLP telemetry endpoint. An empty address contributes nothing, which is the
// correct behavior when that facility is not configured. It is pure so the
// auto-allow list can be tested without a live sandbox.
func EngineEndpoints(runtimeBaseURL, telemetryEndpoint string) ([]EngineEndpoint, error) {
	endpoints := make([]EngineEndpoint, 0, 2)
	for _, declaration := range []struct {
		rawURL  string
		purpose EndpointPurpose
		source  string
	}{
		{rawURL: runtimeBaseURL, purpose: PurposeLLMFacade, source: SourceRuntimeBaseURL},
		{rawURL: telemetryEndpoint, purpose: PurposeTelemetry, source: SourceAgentTelemetryOTLPEnd},
	} {
		raw := strings.TrimSpace(declaration.rawURL)
		if raw == "" {
			continue
		}
		endpoint, err := EndpointFromURL(raw)
		if err != nil {
			return nil, fmt.Errorf("derive %s endpoint from %q: %w", declaration.purpose, declaration.source, err)
		}
		candidate := EngineEndpoint{Purpose: declaration.purpose, Endpoint: endpoint, Source: declaration.source}
		if err := candidate.Validate(); err != nil {
			return nil, err
		}
		endpoints = append(endpoints, candidate)
	}
	return endpoints, nil
}

// EngineAllowRules converts engine-owned endpoints into rules that precede
// every user rule, which is what makes them non-overridable: rules are
// first-match-wins, so no user declaration can deny them.
func EngineAllowRules(endpoints []EngineEndpoint) ([]Rule, error) {
	seen := make(map[string]struct{}, len(endpoints))
	rules := make([]Rule, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if err := endpoint.Validate(); err != nil {
			return nil, err
		}
		name := FormatEndpointPattern(endpoint.Endpoint.Host, endpoint.Endpoint.Port, endpoint.Endpoint.Protocol)
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		rules = append(rules, Rule{
			ID:     "engine." + string(endpoint.Purpose),
			Match:  MatchEndpoint,
			Names:  []string{name},
			Action: Allow,
		})
	}
	return rules, nil
}
