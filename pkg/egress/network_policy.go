package egress

import "fmt"

// AllowEntry is one declared network allowance: a host (which may be a
// label-wise pattern) with a concrete port and protocol.
type AllowEntry struct {
	Host     string
	Port     int
	Protocol Protocol
}

// NetworkDeclaration is a declared sandbox outbound network policy. It is the
// declaration layer's representation before compilation; it is not itself a
// decision model, so the only decision representation in the engine remains
// Policy.
type NetworkDeclaration struct {
	// Default is the action applied when neither an engine-owned endpoint nor
	// a declared allowance matches. It is only evaluated because a declaration
	// exists: an absent declaration is not default-deny (D3), and is modeled by
	// passing a nil declaration to EffectiveNetworkPolicy.
	Default Action
	// Allow are the declared allowances. Order does not change the outcome
	// because every entry allows, but it is preserved so a report can echo the
	// declaration.
	Allow []AllowEntry
}

// Validate rejects a declaration the engine cannot compile. It exists so a
// later Rego backend receives the same normalized input this pure Go
// evaluation does; validation must not move into the compiler.
func (d NetworkDeclaration) Validate() error {
	if d.Default != Allow && d.Default != Deny {
		return fmt.Errorf("network default must be %q or %q, got %q", Allow, Deny, d.Default)
	}
	for i, entry := range d.Allow {
		if err := ValidateHostPattern(entry.Host); err != nil {
			return fmt.Errorf("allow[%d]: %w", i, err)
		}
		if err := validatePort(entry.Port); err != nil {
			return fmt.Errorf("allow[%d]: %w", i, err)
		}
		if _, err := ParseProtocol(string(entry.Protocol)); err != nil {
			return fmt.Errorf("allow[%d]: %w", i, err)
		}
	}
	return nil
}

// CompileNetworkPolicy compiles a declared network policy into the single
// egress decision model. Engine-owned endpoints are emitted first and only
// when the default is deny, so no declared allowance can shadow them and a
// permissive declaration is not misreported as relying on them.
//
// This is the documented seam for a future Rego backend: a Rego compiler
// produces the same Policy from the same validated NetworkDeclaration, so no
// caller and no decision record has to change.
func CompileNetworkPolicy(declaration NetworkDeclaration, engine []EngineEndpoint) (Policy, error) {
	if err := declaration.Validate(); err != nil {
		return Policy{}, err
	}
	rules := make([]Rule, 0, len(engine)+len(declaration.Allow))
	if declaration.Default == Deny {
		engineRules, err := EngineAllowRules(engine)
		if err != nil {
			return Policy{}, err
		}
		rules = append(rules, engineRules...)
	}
	for _, entry := range declaration.Allow {
		rules = append(rules, Rule{
			ID:     "network.allow",
			Match:  MatchEndpoint,
			Names:  []string{FormatEndpointPattern(entry.Host, entry.Port, entry.Protocol)},
			Action: Allow,
		})
	}
	return NewPolicy(declaration.Default, rules...), nil
}

// EffectiveNetworkPolicy returns the policy that governs one sandbox. A nil
// declaration returns a pass-through policy, which is byte-for-byte the
// behavior the engine had before the declaration existed: undeclared is not
// deny (D3). Default-deny applies only because a declaration asked for it.
func EffectiveNetworkPolicy(declaration *NetworkDeclaration, engine []EngineEndpoint) (Policy, error) {
	if declaration == nil {
		return NewPolicy(Allow), nil
	}
	return CompileNetworkPolicy(*declaration, engine)
}

// NetworkRequest describes one attempt to reach one network endpoint as an
// egress request, so a caller records and decides it through the same entry
// point as every other mediated path.
func NetworkRequest(consumer string, endpoint Endpoint) Request {
	return Request{Consumer: consumer, Kind: KindNetworkEndpoint, Name: endpoint.Name()}
}
