package driver

import (
	"fmt"
	"sort"
	"strings"
)

// This file owns the driver-boundary sandbox network policy: the plain data a
// caller populates and every driver maps into its own enforcement mechanism.
//
// It is deliberately not the compose declaration and not a decision engine.
// The compose layer (SEC-5) validates its own SandboxNetworkSpec and maps the
// normalized form into this type; a driver only consumes this type. The mapping
// from the compose declaration into SandboxNetworkPolicy lives outside
// pkg/driver because pkg/driver must not depend on a compose representation,
// and the allow/deny decision model lives in pkg/egress so no second decision
// engine is introduced here.
//
// Nothing in this file performs I/O or holds package state.

// SandboxNetworkDefault is the action applied when no allow entry and no
// engine-owned endpoint matches.
type SandboxNetworkDefault string

const (
	// SandboxNetworkDefaultAllowAll leaves egress unrestricted. It is the value
	// an omitted declaration normalizes to: an undeclared policy is not
	// default-deny (D3).
	SandboxNetworkDefaultAllowAll SandboxNetworkDefault = "allow-all"
	// SandboxNetworkDefaultDeny refuses every destination no allow entry or
	// engine-owned endpoint matches.
	SandboxNetworkDefaultDeny SandboxNetworkDefault = "deny"
)

// SandboxNetworkProtocol is the transport an entry covers. The set mirrors the
// declaration layer's protocol values so the two cannot drift. http and https
// record an L7 intent; a driver whose enforcement surface is L3/L4 treats them
// as TCP.
type SandboxNetworkProtocol string

const (
	// SandboxNetworkProtocolAny covers every transport on the endpoint, which
	// also means the traffic is not L7-inspectable. An omitted protocol
	// normalizes to it.
	SandboxNetworkProtocolAny SandboxNetworkProtocol = "any"
	// SandboxNetworkProtocolHTTP covers plain HTTP.
	SandboxNetworkProtocolHTTP SandboxNetworkProtocol = "http"
	// SandboxNetworkProtocolHTTPS covers TLS-terminated HTTP.
	SandboxNetworkProtocolHTTPS SandboxNetworkProtocol = "https"
	// SandboxNetworkProtocolTCP covers opaque TCP.
	SandboxNetworkProtocolTCP SandboxNetworkProtocol = "tcp"
	// SandboxNetworkProtocolUDP covers opaque UDP.
	SandboxNetworkProtocolUDP SandboxNetworkProtocol = "udp"
)

// SandboxNetworkEntry is one destination an entry permits. Host may be a
// label-wise host pattern ("*.example.com") where a driver supports patterns;
// see SandboxNetworkPolicy.Validate.
type SandboxNetworkEntry struct {
	Host     string
	Port     int
	Protocol SandboxNetworkProtocol
}

// Name renders the canonical "host:port/protocol" identifier. It mirrors the
// declaration layer's endpoint naming so a decision record produced for a
// driver-boundary endpoint carries the same name the declaration compiled.
func (e SandboxNetworkEntry) Name() string {
	return fmt.Sprintf("%s:%d/%s", strings.ToLower(strings.TrimSpace(e.Host)), e.Port, e.normalizedProtocol())
}

func (e SandboxNetworkEntry) normalizedProtocol() SandboxNetworkProtocol {
	switch normalized := SandboxNetworkProtocol(strings.ToLower(strings.TrimSpace(string(e.Protocol)))); normalized {
	case "", SandboxNetworkProtocolAny:
		return SandboxNetworkProtocolAny
	case SandboxNetworkProtocolHTTP:
		return SandboxNetworkProtocolHTTP
	case SandboxNetworkProtocolHTTPS:
		return SandboxNetworkProtocolHTTPS
	case SandboxNetworkProtocolTCP:
		return SandboxNetworkProtocolTCP
	case SandboxNetworkProtocolUDP:
		return SandboxNetworkProtocolUDP
	default:
		return SandboxNetworkProtocolAny
	}
}

// SandboxNetworkPolicy is the driver-boundary network policy for one sandbox.
//
// EngineEndpoints is engine-owned data: the LLM facade and telemetry endpoints
// the engine derives from its own configuration. They take precedence over
// every declared entry whenever Default is deny, so no user declaration can
// deny them, and they are never hardcoded here — the caller supplies them.
type SandboxNetworkPolicy struct {
	Default SandboxNetworkDefault
	// Allow is the declared allowance list in declaration order.
	Allow []SandboxNetworkEntry
	// EngineEndpoints are the engine-owned endpoints that must stay reachable
	// under Default deny.
	EngineEndpoints []SandboxNetworkEntry
	// DenyDomains are exact domain names the driver refuses to resolve.
	DenyDomains []string
}

// NewSandboxNetworkPolicy normalizes and validates a policy. It returns an
// error for a value a driver could not enforce, so a malformed declaration
// fails closed instead of silently widening access.
func NewSandboxNetworkPolicy(defaultAction string, allow, engineEndpoints []SandboxNetworkEntry, denyDomains []string) (SandboxNetworkPolicy, error) {
	normalizedDefault, err := normalizeSandboxNetworkDefault(defaultAction)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedAllow, err := normalizeSandboxNetworkEntries("allow", allow)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedEngine, err := normalizeSandboxNetworkEntries("engine endpoint", engineEndpoints)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	normalizedDenyDomains, err := normalizeSandboxNetworkDenyDomains(denyDomains)
	if err != nil {
		return SandboxNetworkPolicy{}, err
	}
	policy := SandboxNetworkPolicy{
		Default:         normalizedDefault,
		Allow:           normalizedAllow,
		EngineEndpoints: normalizedEngine,
		DenyDomains:     normalizedDenyDomains,
	}
	if err := policy.Validate(); err != nil {
		return SandboxNetworkPolicy{}, err
	}
	return policy, nil
}

// DenyByDefault reports whether the policy refuses unmatched destinations.
func (p SandboxNetworkPolicy) DenyByDefault() bool {
	return normalizedSandboxNetworkDefault(p.Default) == SandboxNetworkDefaultDeny
}

// Validate rejects a policy a driver could not enforce. Only the values the
// declaration layer can produce are accepted, so an unknown default or protocol
// never widens access.
func (p SandboxNetworkPolicy) Validate() error {
	if _, err := normalizeSandboxNetworkDefault(string(p.Default)); err != nil {
		return err
	}
	if _, err := normalizeSandboxNetworkEntries("allow", p.Allow); err != nil {
		return err
	}
	if _, err := normalizeSandboxNetworkEntries("engine endpoint", p.EngineEndpoints); err != nil {
		return err
	}
	if _, err := normalizeSandboxNetworkDenyDomains(p.DenyDomains); err != nil {
		return err
	}
	return nil
}

// permittingEntries returns every entry an enforcing driver must allow, with
// the engine-owned endpoints first. Engine endpoints are only meaningful under
// default-deny, which mirrors the declaration compiler: a permissive policy is
// not misreported as relying on them.
func (p SandboxNetworkPolicy) permittingEntries() []SandboxNetworkEntry {
	if !p.DenyByDefault() {
		return nil
	}
	entries := make([]SandboxNetworkEntry, 0, len(p.EngineEndpoints)+len(p.Allow))
	seen := make(map[string]struct{}, cap(entries))
	for _, entry := range append(append([]SandboxNetworkEntry(nil), p.EngineEndpoints...), p.Allow...) {
		key := entry.Name()
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		entries = append(entries, entry)
	}
	return entries
}

func normalizeSandboxNetworkDefault(value string) (SandboxNetworkDefault, error) {
	switch normalized := strings.ToLower(strings.TrimSpace(value)); normalized {
	case "":
		return SandboxNetworkDefaultAllowAll, nil
	case string(SandboxNetworkDefaultAllowAll):
		return SandboxNetworkDefaultAllowAll, nil
	case string(SandboxNetworkDefaultDeny):
		return SandboxNetworkDefaultDeny, nil
	default:
		return "", fmt.Errorf("network default must be %q or %q, got %q", SandboxNetworkDefaultAllowAll, SandboxNetworkDefaultDeny, value)
	}
}

func normalizedSandboxNetworkDefault(value SandboxNetworkDefault) SandboxNetworkDefault {
	normalized, err := normalizeSandboxNetworkDefault(string(value))
	if err != nil {
		// Validate rejects an unrepresentable default, so an unvalidated value
		// falls back to deny: an unknown default must never widen access.
		return SandboxNetworkDefaultDeny
	}
	return normalized
}

func normalizeSandboxNetworkEntries(field string, entries []SandboxNetworkEntry) ([]SandboxNetworkEntry, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	normalized := make([]SandboxNetworkEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for index, entry := range entries {
		candidate := SandboxNetworkEntry{
			Host:     strings.ToLower(strings.TrimSpace(entry.Host)),
			Port:     entry.Port,
			Protocol: entry.normalizedProtocol(),
		}
		if err := validateSandboxNetworkHost(candidate.Host, true); err != nil {
			return nil, fmt.Errorf("%s[%d]: %w", field, index, err)
		}
		if candidate.Port < 1 || candidate.Port > 65535 {
			return nil, fmt.Errorf("%s[%d]: port must be between 1 and 65535, got %d", field, index, candidate.Port)
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

func normalizeSandboxNetworkDenyDomains(domains []string) ([]string, error) {
	if len(domains) == 0 {
		return nil, nil
	}
	normalized := make([]string, 0, len(domains))
	seen := make(map[string]struct{}, len(domains))
	for index, domain := range domains {
		candidate := strings.ToLower(strings.TrimSpace(domain))
		if err := validateSandboxNetworkHost(candidate, false); err != nil {
			return nil, fmt.Errorf("deny_domain[%d]: %w", index, err)
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

// validateSandboxNetworkHost mirrors the declaration layer's host-pattern
// validation. It is duplicated rather than imported because the declaration
// validator lives in pkg/egress on the SEC-5 branch; when that lands this must
// be replaced by it so the two cannot diverge.
func validateSandboxNetworkHost(host string, allowPattern bool) error {
	if host == "" {
		return fmt.Errorf("host must not be empty")
	}
	if len(host) > 253 {
		return fmt.Errorf("host %q is longer than 253 characters", host)
	}
	if strings.HasPrefix(host, ".") || strings.HasSuffix(host, ".") {
		return fmt.Errorf("host %q must not start or end with %q", host, ".")
	}
	for _, label := range strings.Split(host, ".") {
		if label == "*" {
			if allowPattern {
				continue
			}
			return fmt.Errorf("host %q must not contain a wildcard label", host)
		}
		if label == "" {
			return fmt.Errorf("host %q contains an empty label", host)
		}
		if len(label) > 63 {
			return fmt.Errorf("host %q label %q is longer than 63 characters", host, label)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("host %q label %q must not start or end with %q", host, label, "-")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return fmt.Errorf("host %q label %q may contain only letters, digits, and %q", host, label, "-")
		}
	}
	return nil
}
