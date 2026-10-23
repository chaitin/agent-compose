package egress

import (
	"fmt"
	"net"
	"strings"
)

// LayerNetworkDeclarations composes the daemon-wide policy with one agent's
// declaration. The daemon policy is the outer bound: an agent may narrow it but
// can never widen it. A nil outer declaration is the undeclared daemon policy
// and leaves the agent declaration unchanged; a nil inner declaration leaves the
// daemon policy as the effective policy for that agent.
//
// The composition is an intersection of what each declaration permits. Denials
// from both layers always survive. The two permissive cases are simple: an outer
// denylist leaves the inner declaration's allowlist or denylist intact, and an
// inner allow-all cannot add destinations to an outer allowlist. The remaining
// case, two allowlists, keeps only the inner allowances that are provably inside
// an outer allowance; an allowance that is not provably inside is rejected
// rather than silently widening the daemon boundary.
func LayerNetworkDeclarations(outer, inner *NetworkDeclaration) (*NetworkDeclaration, error) {
	if outer == nil {
		return cloneNetworkDeclaration(inner), nil
	}
	if err := outer.Validate(); err != nil {
		return nil, fmt.Errorf("daemon network policy: %w", err)
	}
	if inner == nil {
		return cloneNetworkDeclaration(outer), nil
	}
	if err := inner.Validate(); err != nil {
		return nil, fmt.Errorf("agent network policy: %w", err)
	}
	if !outer.Restricts() {
		return cloneNetworkDeclaration(inner), nil
	}
	if !inner.Restricts() {
		return cloneNetworkDeclaration(outer), nil
	}

	layered := &NetworkDeclaration{
		Default: Deny,
		Deny:    mergeDenyEntries(outer.Deny, inner.Deny),
	}
	switch {
	case outer.Default == Allow:
		// The daemon layer is a denylist. The agent may choose either mode;
		// its allowlist becomes the positive set and the daemon denials still
		// subtract from it.
		layered.Default = inner.Default
		if inner.Default == Deny {
			layered.Allow = cloneAllowEntries(inner.Allow)
		}
	case inner.Default == Deny:
		// Both layers are allowlists: the agent's allowances must be inside
		// the daemon's allowances.
		allow, err := restrictAllowances(inner.Allow, outer.Allow)
		if err != nil {
			return nil, err
		}
		layered.Allow = allow
	default:
		// The agent is a denylist inside a daemon allowlist. It cannot add a
		// positive destination, so the daemon's allowances remain the set and
		// the agent's denials subtract from it.
		layered.Allow = cloneAllowEntries(outer.Allow)
	}
	return layered, nil
}

// restrictAllowances keeps the inner allowances that are provably covered by an
// outer allowance. It returns an error instead of dropping an uncovered entry,
// because dropping would silently make the agent policy narrower than declared
// while keeping it would widen the daemon boundary.
func restrictAllowances(inner, outer []AllowEntry) ([]AllowEntry, error) {
	if len(inner) == 0 {
		return nil, nil
	}
	result := make([]AllowEntry, 0, len(inner))
	for _, entry := range inner {
		covered, ok := coveringAllowance(entry, outer)
		if !ok {
			return nil, fmt.Errorf("agent allow entry %q port %d is outside the daemon network policy", entry.Host, entry.Port)
		}
		effective := cloneAllowEntry(entry)
		allowedIPs, err := intersectAllowedIPs(entry.AllowedIPs, covered.AllowedIPs)
		if err != nil {
			return nil, fmt.Errorf("agent allow entry %q port %d: %w", entry.Host, entry.Port, err)
		}
		effective.AllowedIPs = allowedIPs
		result = append(result, effective)
	}
	return result, nil
}

func coveringAllowance(inner AllowEntry, outer []AllowEntry) (AllowEntry, bool) {
	for _, candidate := range outer {
		if candidate.Port != inner.Port {
			continue
		}
		if candidate.Protocol != ProtocolAny && inner.Protocol != candidate.Protocol {
			continue
		}
		if !hostCoveredBy(inner.Host, candidate.Host) {
			continue
		}
		if !allowedIPsNarrower(inner.AllowedIPs, candidate.AllowedIPs) {
			continue
		}
		return candidate, true
	}
	return AllowEntry{}, false
}

// hostCoveredBy reports whether every destination named by inner is also named
// by outer. Exact names and exact addresses compare directly; a CIDR contains
// an address or a narrower CIDR; a wildcard pattern covers only an exact host
// it matches or a pattern with the same shape. A pattern that is broader than
// the outer pattern is not covered, even when it happens to overlap.
func hostCoveredBy(inner, outer string) bool {
	innerHost, innerErr := NormalizeHost(inner)
	outerHost, outerErr := NormalizeHost(outer)
	if innerErr != nil || outerErr != nil {
		return false
	}
	if _, outerNetwork, err := net.ParseCIDR(outerHost); err == nil {
		if _, innerNetwork, err := net.ParseCIDR(innerHost); err == nil {
			return cidrWithin(innerNetwork, outerNetwork)
		}
		if literal := net.ParseIP(innerHost); literal != nil {
			return outerNetwork.Contains(literal)
		}
		return false
	}
	if outerIP := net.ParseIP(outerHost); outerIP != nil {
		innerIP := net.ParseIP(innerHost)
		return innerIP != nil && outerIP.Equal(innerIP)
	}
	if innerIP := net.ParseIP(innerHost); innerIP != nil {
		return false
	}
	if strings.Contains(innerHost, "*") || strings.Contains(outerHost, "*") {
		return innerHost == outerHost || HostPatternMatches(outerHost, innerHost) && !strings.Contains(innerHost, "*")
	}
	return innerHost == outerHost
}

func cidrWithin(inner, outer *net.IPNet) bool {
	innerBits, _ := inner.Mask.Size()
	outerPrefixBits, _ := outer.Mask.Size()
	if innerBits < outerPrefixBits {
		return false
	}
	return outer.Contains(inner.IP)
}

// allowedIPsNarrower reports whether the inner address constraints can be
// layered under the outer ones. An empty outer set constrains nothing, and an
// empty inner set is compatible with any outer set because the effective entry
// inherits the outer constraint. A non-empty inner set must be a subset.
func allowedIPsNarrower(inner, outer []string) bool {
	if len(outer) == 0 || len(inner) == 0 {
		return true
	}
	return cidrsSubset(inner, outer)
}

// intersectAllowedIPs returns the tighter address constraint. When the outer
// layer has no constraint, the inner (possibly empty) constraint is kept; when
// it does, an empty inner constraint inherits it so the effective entry cannot
// widen the daemon bound.
func intersectAllowedIPs(inner, outer []string) ([]string, error) {
	if len(outer) == 0 {
		return append([]string(nil), inner...), nil
	}
	if len(inner) == 0 {
		return append([]string(nil), outer...), nil
	}
	if !cidrsSubset(inner, outer) {
		return nil, fmt.Errorf("allowed_ips is outside the daemon network policy")
	}
	return append([]string(nil), inner...), nil
}

func cidrsSubset(inner, outer []string) bool {
	for _, value := range inner {
		_, innerNetwork, err := net.ParseCIDR(value)
		if err != nil {
			return false
		}
		covered := false
		for _, candidate := range outer {
			_, outerNetwork, err := net.ParseCIDR(candidate)
			if err != nil {
				continue
			}
			if cidrWithin(innerNetwork, outerNetwork) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

func cloneNetworkDeclaration(declaration *NetworkDeclaration) *NetworkDeclaration {
	if declaration == nil {
		return nil
	}
	cloned := &NetworkDeclaration{
		Default: declaration.Default,
		Allow:   cloneAllowEntries(declaration.Allow),
		Deny:    cloneDenyEntries(declaration.Deny),
	}
	return cloned
}

func cloneAllowEntries(entries []AllowEntry) []AllowEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]AllowEntry, len(entries))
	for i, entry := range entries {
		cloned[i] = cloneAllowEntry(entry)
	}
	return cloned
}

func cloneAllowEntry(entry AllowEntry) AllowEntry {
	entry.AllowedIPs = append([]string(nil), entry.AllowedIPs...)
	return entry
}

// mergeDenyEntries keeps one entry per destination when both layers deny the
// same endpoint. A duplicate would otherwise reach the driver boundary, which
// rejects duplicate denials as an ambiguous declaration and prevents the
// sandbox from starting even though the effective policy is unambiguous.
func mergeDenyEntries(outer, inner []DenyEntry) []DenyEntry {
	if len(outer) == 0 && len(inner) == 0 {
		return nil
	}
	merged := make([]DenyEntry, 0, len(outer)+len(inner))
	seen := make(map[string]struct{}, len(outer)+len(inner))
	for _, entries := range [][]DenyEntry{outer, inner} {
		for _, entry := range entries {
			key := denyEntryKey(entry)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			merged = append(merged, entry)
		}
	}
	return merged
}

func denyEntryKey(entry DenyEntry) string {
	endpoint, err := NewEndpoint(entry.Host, entry.Port, entry.Protocol)
	if err != nil {
		return fmt.Sprintf("%s\x00%d\x00%s", entry.Host, entry.Port, entry.Protocol)
	}
	return endpoint.Name()
}

func cloneDenyEntries(entries []DenyEntry) []DenyEntry {
	if len(entries) == 0 {
		return nil
	}
	return append([]DenyEntry(nil), entries...)
}
