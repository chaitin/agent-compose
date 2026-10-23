package egress

import "fmt"

// AllowEntry is one declared network allowance: a host (which may be a
// label-wise pattern) with a concrete port and protocol.
type AllowEntry struct {
	Host     string
	Port     int
	Protocol Protocol
	// AllowedIPs constrains which addresses a name may resolve to for this
	// allowance to apply. It is empty for an allowance that names an address
	// directly (nothing is resolved) or that deliberately trusts every address
	// a name resolves to. Each entry is a canonical CIDR, as produced by
	// NormalizeAllowedIPs.
	AllowedIPs []string
}

// DenyEntry is one declared refusal: a host (which may be a label-wise
// pattern), a concrete port, and a protocol. A denial refuses the destination
// itself; it deliberately has no AllowedIPs field, because constraining a
// refusal to selected addresses would make one entry mean two different things.
type DenyEntry struct {
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
	// a declared entry matches. It is only evaluated because a declaration
	// exists: an absent declaration is not default-deny (D3), and is modeled by
	// passing a nil declaration to EffectiveNetworkPolicy.
	//
	// Deny is allowlist mode; Allow is denylist mode. A Deny entry wins over an
	// Allow entry in either mode.
	Default Action
	// Allow are the declared allowances. Order does not change the outcome
	// because every entry allows, but it is preserved so a report can echo the
	// declaration.
	Allow []AllowEntry
	// Deny are the declared refusals. Order does not change the outcome because
	// every entry denies, but it is preserved so a report can echo the
	// declaration.
	Deny []DenyEntry
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
		for _, allowed := range entry.AllowedIPs {
			if _, err := ParseAllowedIP(allowed); err != nil {
				return fmt.Errorf("allow[%d]: %w", i, err)
			}
		}
	}
	for i, entry := range d.Deny {
		if err := ValidateHostPattern(entry.Host); err != nil {
			return fmt.Errorf("deny[%d]: %w", i, err)
		}
		if err := validatePort(entry.Port); err != nil {
			return fmt.Errorf("deny[%d]: %w", i, err)
		}
		if _, err := ParseProtocol(string(entry.Protocol)); err != nil {
			return fmt.Errorf("deny[%d]: %w", i, err)
		}
	}
	return nil
}

// Restricts reports whether the declaration refuses any destination. It is true
// for allowlist mode (default deny) and for any declared denial, but false for a
// declaration whose default is allow and whose Deny list is empty: such a
// declaration cannot narrow egress.
func (d NetworkDeclaration) Restricts() bool {
	return d.Default == Deny || len(d.Deny) > 0
}

// CompileNetworkPolicy compiles a declared network policy into the single
// egress decision model. Engine-owned endpoints are emitted first whenever the
// declaration restricts, so no declared denial or allowance can shadow them and
// a permissive declaration is not misreported as relying on them.
//
// This is the documented seam for a future Rego backend: a Rego compiler
// produces the same Policy from the same validated NetworkDeclaration, so no
// caller and no decision record has to change.
func CompileNetworkPolicy(declaration NetworkDeclaration, engine []EngineEndpoint) (Policy, error) {
	if err := declaration.Validate(); err != nil {
		return Policy{}, err
	}
	rules := make([]Rule, 0, len(engine)+len(declaration.Allow)+len(declaration.Deny))
	if declaration.Restricts() {
		// Engine-owned endpoints precede every declared rule, including a
		// denial: a project must not be able to cut off the facade or telemetry
		// endpoint the sandbox itself needs, whether the restriction comes from
		// a deny default or from a denylist entry.
		engineRules, err := EngineAllowRules(engine)
		if err != nil {
			return Policy{}, err
		}
		rules = append(rules, engineRules...)
	}
	// Denials precede allowances, so a denial wins over an allowance that
	// matches the same destination in either mode.
	for _, entry := range declaration.Deny {
		rules = append(rules, Rule{
			ID:     "network.deny",
			Match:  MatchEndpoint,
			Names:  []string{FormatEndpointPattern(entry.Host, entry.Port, entry.Protocol)},
			Action: Deny,
		})
	}
	for _, entry := range declaration.Allow {
		allowed, err := canonicalAllowedIPs(entry.AllowedIPs)
		if err != nil {
			return Policy{}, fmt.Errorf("allow entry %q port %d: %w", entry.Host, entry.Port, err)
		}
		rules = append(rules, Rule{
			ID:         "network.allow",
			Match:      MatchEndpoint,
			Names:      []string{FormatEndpointPattern(entry.Host, entry.Port, entry.Protocol)},
			Action:     Allow,
			AllowedIPs: allowed,
		})
	}
	return NewPolicy(declaration.Default, rules...), nil
}

// canonicalAllowedIPs returns address constraints in the canonical CIDR form
// the rule matches with. Validate accepts the shorthand a declaration may use
// (a bare address, an unmasked prefix), so compiling the raw strings would
// store an entry whose decision-time parse never matches and would enforce a
// narrower allowance than the declaration asked for.
func canonicalAllowedIPs(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	canonical := make([]string, 0, len(values))
	for _, value := range values {
		prefix, err := ParseAllowedIP(value)
		if err != nil {
			return nil, err
		}
		canonical = append(canonical, prefix)
	}
	return canonical, nil
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
