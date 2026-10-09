package driver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// This file owns the driver-boundary sandbox network policy: the plain data a
// caller populates and every driver maps into its own enforcement mechanism.
//
// It is deliberately not the compose declaration and not a decision engine. The
// compose layer (SEC-5) validates its own SandboxNetworkSpec and publishes a
// normalized form; SandboxNetworkPolicyFromDeclaration is the one seam that
// converts that normalized form (egress.NetworkDeclaration) plus the
// engine-owned endpoints (egress.EngineEndpoint) into this driver-boundary
// value. The allowed/denied decision stays in pkg/egress: the boundary type
// carries egress types and delegates compilation and matching to them, so there
// is no second rule matcher and no mirrored protocol or host validation.
//
// Nothing in this file performs I/O or holds package state.

// SandboxNetworkPolicy is the driver-boundary network policy for one sandbox.
//
// Default is egress.Allow for an undeclared policy (D3): the zero value is not
// default-deny. Allow holds the declared allowances and EngineEndpoints holds
// the engine-owned endpoints the engine derives from its own configuration
// (LLM facade, telemetry). Engine endpoints precede every declared allowance
// whenever Default is egress.Deny, so no user declaration can deny them, and
// they are never hardcoded here — the caller supplies them.
type SandboxNetworkPolicy struct {
	Default egress.Action
	// Allow is the declared allowance list in declaration order. Order does not
	// change the outcome because every entry allows, but it is preserved so a
	// report can echo the declaration.
	Allow []egress.Endpoint
	// EngineEndpoints are the engine-owned endpoints that must stay reachable
	// under egress.Deny.
	EngineEndpoints []egress.EngineEndpoint
	// DenyDomains are exact domain names the driver refuses to resolve. The
	// compose declaration does not carry them yet, so they are engine-supplied.
	DenyDomains []string
}

// NewSandboxNetworkPolicy normalizes and validates a boundary policy. It returns
// an error for a value a driver could not enforce, so a malformed declaration
// fails closed instead of silently widening access.
func NewSandboxNetworkPolicy(defaultAction egress.Action, allow []egress.Endpoint, engineEndpoints []egress.EngineEndpoint, denyDomains []string) (SandboxNetworkPolicy, error) {
	normalizedDefault, err := normalizeSandboxNetworkDefault(defaultAction)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedAllow, err := normalizeSandboxNetworkEndpoints("allow", allow)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedEngine, err := normalizeSandboxNetworkEngineEndpoints(engineEndpoints)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedDenyDomains, err := normalizeSandboxNetworkDenyDomains(denyDomains)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	return SandboxNetworkPolicy{
		Default:         normalizedDefault,
		Allow:           normalizedAllow,
		EngineEndpoints: normalizedEngine,
		DenyDomains:     normalizedDenyDomains,
	}, nil
}

// SandboxNetworkPolicyFromDeclaration is the explicit seam between the compose
// declaration layer and the drivers. It converts the declaration layer's
// normalized network declaration plus the engine-owned endpoints into the
// driver-boundary policy.
//
// A nil declaration means no policy was declared, so the result permits all
// egress (D3); the engine-owned endpoints are then irrelevant because nothing is
// denied.
func SandboxNetworkPolicyFromDeclaration(declaration *egress.NetworkDeclaration, engineEndpoints []egress.EngineEndpoint, denyDomains []string) (SandboxNetworkPolicy, error) {
	if declaration == nil {
		return NewSandboxNetworkPolicy(egress.Allow, nil, engineEndpoints, denyDomains)
	}
	if err := declaration.Validate(); err != nil {
		return SandboxNetworkPolicy{}, err
	}
	allow := make([]egress.Endpoint, 0, len(declaration.Allow))
	for _, entry := range declaration.Allow {
		allow = append(allow, egress.Endpoint(entry))
	}
	return NewSandboxNetworkPolicy(declaration.Default, allow, engineEndpoints, denyDomains)
}

// DenyByDefault reports whether the policy refuses unmatched destinations. An
// unrepresentable default is treated as deny so an invalid policy can never
// widen access.
func (p SandboxNetworkPolicy) DenyByDefault() bool {
	normalized, err := normalizeSandboxNetworkDefault(p.Default)
	if err != nil {
		return true
	}
	return normalized == egress.Deny
}

// Validate rejects a policy a driver could not enforce. The destinations and
// protocols are validated by pkg/egress, so the boundary type cannot accept a
// value the decision model would reject.
func (p SandboxNetworkPolicy) Validate() error {
	if _, err := normalizeSandboxNetworkDefault(p.Default); err != nil {
		return err
	}
	if _, err := normalizeSandboxNetworkEndpoints("allow", p.Allow); err != nil {
		return err
	}
	if _, err := normalizeSandboxNetworkEngineEndpoints(p.EngineEndpoints); err != nil {
		return err
	}
	if _, err := normalizeSandboxNetworkDenyDomains(p.DenyDomains); err != nil {
		return err
	}
	return nil
}

// permittingEndpoints returns every destination an enforcing driver must allow,
// with the engine-owned endpoints first and duplicates removed. The engine
// endpoints are only meaningful under default deny, which mirrors the
// declaration compiler: a permissive policy is not misreported as relying on
// them.
func (p SandboxNetworkPolicy) permittingEndpoints() []egress.Endpoint {
	if !p.DenyByDefault() {
		return nil
	}
	entries := make([]egress.Endpoint, 0, len(p.EngineEndpoints)+len(p.Allow))
	seen := make(map[string]struct{}, cap(entries))
	for _, engineEndpoint := range p.EngineEndpoints {
		name := engineEndpoint.Endpoint.Name()
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, engineEndpoint.Endpoint)
	}
	for _, endpoint := range p.Allow {
		name := endpoint.Name()
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, endpoint)
	}
	return entries
}

func normalizeSandboxNetworkDefault(action egress.Action) (egress.Action, error) {
	switch action {
	case "":
		return egress.Allow, nil
	case egress.Allow, egress.Deny:
		return action, nil
	default:
		return "", fmt.Errorf("network default must be %q or %q, got %q", egress.Allow, egress.Deny, action)
	}
}

func normalizeSandboxNetworkEndpoints(field string, endpoints []egress.Endpoint) ([]egress.Endpoint, error) {
	if len(endpoints) == 0 {
		return nil, nil
	}
	normalized := make([]egress.Endpoint, 0, len(endpoints))
	seen := make(map[string]struct{}, len(endpoints))
	for index, endpoint := range endpoints {
		candidate, err := egress.NewEndpoint(endpoint.Host, endpoint.Port, endpoint.Protocol)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, index, err)
		}
		key := candidate.Name()
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%s[%d]: duplicate entry %q", field, index, key)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, candidate)
	}
	return normalized, nil
}

func normalizeSandboxNetworkEngineEndpoints(endpoints []egress.EngineEndpoint) ([]egress.EngineEndpoint, error) {
	if len(endpoints) == 0 {
		return nil, nil
	}
	normalized := make([]egress.EngineEndpoint, 0, len(endpoints))
	seen := make(map[string]struct{}, len(endpoints))
	for index, endpoint := range endpoints {
		if err := endpoint.Validate(); err != nil {
			return nil, fmt.Errorf("engine endpoint[%d]: %w", index, err)
		}
		candidate, err := egress.NewEndpoint(endpoint.Endpoint.Host, endpoint.Endpoint.Port, endpoint.Endpoint.Protocol)
		if err != nil {
			return nil, fmt.Errorf("engine endpoint[%d]: %w", index, err)
		}
		key := candidate.Name()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		endpoint.Endpoint = candidate
		normalized = append(normalized, endpoint)
	}
	return normalized, nil
}

func normalizeSandboxNetworkDenyDomains(domains []string) ([]string, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for index, domain := range domains {
		candidate := strings.ToLower(strings.TrimSpace(domain))
		if err := egress.ValidateHostPattern(candidate); err != nil {
			return nil, fmt.Errorf("deny_domain[%d]: %w", index, err)
		}
		if strings.Contains(candidate, "*") {
			return nil, fmt.Errorf("deny_domain[%d]: deny domains must be exact names, got %q", index, domain)
		}
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		normalized = append(normalized, candidate)
	}
	sort.Strings(normalized)
	return normalized, nil
}
