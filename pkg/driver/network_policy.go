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
// default-deny. Allow holds the declared allowances, Deny holds the declared
// refusals, and EngineEndpoints holds the engine-owned endpoints the engine
// derives from its own configuration (LLM facade, telemetry). Engine endpoints
// precede every declared rule whenever the policy restricts, so no user
// declaration can deny them, and they are never hardcoded here — the caller
// supplies them.
type SandboxNetworkPolicy struct {
	Default egress.Action
	// Allow is the declared allowance list in declaration order. Order does not
	// change the outcome because every entry allows, but it is preserved so a
	// report can echo the declaration. It is the declaration layer's own entry
	// type rather than a narrowed endpoint so an entry's allowed address range
	// survives the seam: a boundary policy that quietly dropped a constraint
	// would let a driver enforce less than the project declared.
	Allow []egress.AllowEntry
	// Deny is the declared refusal list in declaration order. A denial wins over
	// an allowance that matches the same destination.
	Deny []egress.DenyEntry
	// EngineEndpoints are the engine-owned endpoints that must stay reachable
	// whenever the policy restricts.
	EngineEndpoints []egress.EngineEndpoint
	// DenyDomains are exact domain names the driver refuses to resolve. The
	// compose declaration does not carry them yet, so they are engine-supplied.
	DenyDomains []string
	// MediatedByForwardProxy reports that the engine, not the driver, decides
	// this sandbox's egress: the guest was given the engine's forward proxy as
	// its egress path, so the declaration is enforced on a driver that has no
	// enforcement surface of its own.
	//
	// It is set by the caller that actually routed the guest through the proxy,
	// in the same step that added the proxy environment, so a driver can never
	// believe in a mediation the engine did not wire. A driver reads it only to
	// report enforcement honestly and to skip the unenforced-declaration gate.
	MediatedByForwardProxy bool
}

// NewSandboxNetworkPolicy normalizes and validates a boundary policy. It returns
// an error for a value a driver could not enforce, so a malformed declaration
// fails closed instead of silently widening access.
func NewSandboxNetworkPolicy(defaultAction egress.Action, allow []egress.AllowEntry, deny []egress.DenyEntry, engineEndpoints []egress.EngineEndpoint, denyDomains []string) (SandboxNetworkPolicy, error) {
	normalizedDefault, err := normalizeSandboxNetworkDefault(defaultAction)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedAllow, err := normalizeSandboxNetworkEndpoints("allow", allow)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedDeny, err := normalizeSandboxNetworkDenyEndpoints("deny", deny)
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
		Deny:            normalizedDeny,
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
		return NewSandboxNetworkPolicy(egress.Allow, nil, nil, engineEndpoints, denyDomains)
	}
	if err := declaration.Validate(); err != nil {
		return SandboxNetworkPolicy{}, err
	}
	allow := append([]egress.AllowEntry(nil), declaration.Allow...)
	deny := append([]egress.DenyEntry(nil), declaration.Deny...)
	return NewSandboxNetworkPolicy(declaration.Default, allow, deny, engineEndpoints, denyDomains)
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

// Restricts reports whether the policy can refuse any destination: a deny
// default, or at least one declared denial. An allow default with no denial
// cannot narrow egress, which is the legacy behavior of an allow list without
// default: deny-all.
func (p SandboxNetworkPolicy) Restricts() bool {
	return p.DenyByDefault() || len(p.Deny) > 0
}

// AddressConstrainedAllowances counts the declared allowances that constrain
// which addresses a name may resolve to. A mechanism that applies the
// destinations but not these ranges is applying less than the project declared,
// so it has to say so rather than run as if the constraint were in place.
func (p SandboxNetworkPolicy) AddressConstrainedAllowances() int {
	return egress.AddressConstrainedAllowances(p.Allow)
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
	if _, err := normalizeSandboxNetworkDenyEndpoints("deny", p.Deny); err != nil {
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

// engineEndpoints returns the engine-owned endpoints a restricting policy must
// allow before any declared rule. They are meaningful whenever the policy
// restricts, because a denial could otherwise cut off the facade or telemetry
// endpoint the sandbox needs.
func (p SandboxNetworkPolicy) engineEndpoints() []egress.Endpoint {
	if !p.Restricts() {
		return nil
	}
	entries := make([]egress.Endpoint, 0, len(p.EngineEndpoints))
	seen := make(map[string]struct{}, len(p.EngineEndpoints))
	for _, engineEndpoint := range p.EngineEndpoints {
		name := engineEndpoint.Endpoint.Name()
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		entries = append(entries, engineEndpoint.Endpoint)
	}
	return entries
}

// allowingEndpoints returns the declared allowances in declaration order. They
// only matter under a deny default; under an allow default they cannot narrow
// anything. Engine endpoints are returned separately so a driver can place them
// ahead of denials.
func (p SandboxNetworkPolicy) allowingEndpoints() []egress.Endpoint {
	if !p.DenyByDefault() {
		return nil
	}
	entries := make([]egress.Endpoint, 0, len(p.Allow))
	for _, entry := range p.Allow {
		entries = append(entries, egress.Endpoint{Host: entry.Host, Port: entry.Port, Protocol: entry.Protocol})
	}
	return entries
}

// denyingEndpoints returns the declared refusals in declaration order. A
// driver maps them ahead of every allowance so a denial wins.
func (p SandboxNetworkPolicy) denyingEndpoints() []egress.Endpoint {
	entries := make([]egress.Endpoint, 0, len(p.Deny))
	for _, entry := range p.Deny {
		entries = append(entries, egress.Endpoint(entry))
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

func normalizeSandboxNetworkEndpoints(field string, entries []egress.AllowEntry) ([]egress.AllowEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	normalized := make([]egress.AllowEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		endpoint, err := egress.NewEndpoint(entry.Host, entry.Port, entry.Protocol)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, index, err)
		}
		allowed, err := egress.NormalizeAllowedIPs(entry.AllowedIPs)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, index, err)
		}
		key := endpoint.Name()
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%s[%d]: duplicate entry %q", field, index, key)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, egress.AllowEntry{
			Host:       endpoint.Host,
			Port:       endpoint.Port,
			Protocol:   endpoint.Protocol,
			AllowedIPs: allowed,
		})
	}
	return normalized, nil
}

func normalizeSandboxNetworkDenyEndpoints(field string, entries []egress.DenyEntry) ([]egress.DenyEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	normalized := make([]egress.DenyEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		endpoint, err := egress.NewEndpoint(entry.Host, entry.Port, entry.Protocol)
		if err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, index, err)
		}
		key := endpoint.Name()
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("%s[%d]: duplicate entry %q", field, index, key)
		}
		seen[key] = struct{}{}
		normalized = append(normalized, egress.DenyEntry(endpoint))
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
