package egress

import (
	"slices"
	"strings"
)

// AutoAllowedEndpoint is one engine-owned endpoint as it appears in a report.
// It exists so the auto-allow is an auditable fact rather than an invisible
// exemption: the endpoint, why it is allowed, where it came from, and whether
// the engine can actually inspect the traffic are all stated.
type AutoAllowedEndpoint struct {
	Purpose     EndpointPurpose
	Endpoint    string
	Inspectable bool
	Source      string
}

// DeclaredAllowance is one declared allowance as it appears in a report.
type DeclaredAllowance struct {
	// Endpoint is the canonical host:port/protocol pattern.
	Endpoint string
	// Inspectable is false for an opaque allowance (protocol any/tcp/udp),
	// which the report must not present as a checked one.
	Inspectable bool
}

// NetworkPolicyReport is the auditable account of one sandbox's declared
// network policy. A caller renders it into a capability or audit surface; it
// is derived from the same declaration the decision policy is compiled from, so
// it cannot describe a policy the engine does not have.
type NetworkPolicyReport struct {
	// Declared is false when the sandbox declared no policy. In that case the
	// engine is unrestricted and there is no exemption to report.
	Declared bool
	// Default is the action applied to traffic no entry matches.
	Default Action
	// Allow lists the declared allowances.
	Allow []DeclaredAllowance
	// EngineAutoAllowed lists the engine-owned endpoints that the compiled
	// policy allows ahead of every declared entry. It is empty unless the
	// default is deny, because that is the only case where they exist as an
	// exemption.
	EngineAutoAllowed []AutoAllowedEndpoint
}

// ReportNetworkPolicy returns the auditable account of a declaration. It
// compiles the policy first, so a report that disagrees with the decision
// model is impossible. It is pure and reads no sandbox state.
func ReportNetworkPolicy(declaration *NetworkDeclaration, engine []EngineEndpoint) (NetworkPolicyReport, error) {
	if declaration == nil {
		// Undeclared is not deny (D3): there is no policy to report and no
		// engine exemption in effect.
		return NetworkPolicyReport{Default: Allow}, nil
	}
	if _, err := CompileNetworkPolicy(*declaration, engine); err != nil {
		return NetworkPolicyReport{}, err
	}
	report := NetworkPolicyReport{Declared: true, Default: declaration.Default}
	for _, entry := range declaration.Allow {
		protocol, err := ParseProtocol(string(entry.Protocol))
		if err != nil {
			// CompileNetworkPolicy already rejected this; the guard keeps the
			// report from claiming inspectability it could not verify.
			protocol = ProtocolAny
		}
		report.Allow = append(report.Allow, DeclaredAllowance{
			Endpoint:    FormatEndpointPattern(entry.Host, entry.Port, entry.Protocol),
			Inspectable: protocol.Inspectable(),
		})
	}
	if declaration.Default != Deny {
		return report, nil
	}
	// EngineAllowRules collapses endpoints that render to the same pattern, so
	// the report must collapse them the same way: listing two exemptions for
	// one compiled rule would describe an exemption the decision model does not
	// have. The first occurrence wins, matching the compiler.
	seen := make(map[string]struct{}, len(engine))
	for _, endpoint := range engine {
		if err := endpoint.Validate(); err != nil {
			return NetworkPolicyReport{}, err
		}
		pattern := FormatEndpointPattern(endpoint.Endpoint.Host, endpoint.Endpoint.Port, endpoint.Endpoint.Protocol)
		if _, duplicate := seen[pattern]; duplicate {
			continue
		}
		seen[pattern] = struct{}{}
		report.EngineAutoAllowed = append(report.EngineAutoAllowed, AutoAllowedEndpoint{
			Purpose:     endpoint.Purpose,
			Endpoint:    endpoint.Endpoint.Name(),
			Inspectable: endpoint.Endpoint.Protocol.Inspectable(),
			Source:      endpoint.Source,
		})
	}
	slices.SortFunc(report.EngineAutoAllowed, func(a, b AutoAllowedEndpoint) int {
		if a.Purpose != b.Purpose {
			return strings.Compare(string(a.Purpose), string(b.Purpose))
		}
		if a.Endpoint != b.Endpoint {
			return strings.Compare(a.Endpoint, b.Endpoint)
		}
		return strings.Compare(a.Source, b.Source)
	})
	return report, nil
}
