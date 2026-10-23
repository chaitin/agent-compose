package egress

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// KindNetworkEndpoint is the resource family for one declared outbound network
// destination. A policy of this kind is scoped to a single sandbox: the
// declaration layer in pkg/compose compiles a declared policy into one Policy,
// and a later driver-side mediator (SEC-6) asks this package whether a concrete
// destination is allowed.
const KindNetworkEndpoint ResourceKind = "network-endpoint"

// Protocol is the transport an allowance covers. The set is deliberately
// closed: a value the engine does not recognize must fail validation rather
// than silently widening the allowance.
type Protocol string

const (
	// ProtocolAny covers every transport on the endpoint, which also means the
	// traffic is not L7-inspectable. It is the value an omitted protocol
	// normalizes to.
	ProtocolAny Protocol = "any"
	// ProtocolHTTP covers plain HTTP, which an L7 mediator can inspect.
	ProtocolHTTP Protocol = "http"
	// ProtocolHTTPS covers TLS-terminated HTTP, which an L7 mediator can
	// inspect only after terminating TLS.
	ProtocolHTTPS Protocol = "https"
	// ProtocolTCP covers opaque TCP, which an L7 mediator cannot inspect.
	ProtocolTCP Protocol = "tcp"
	// ProtocolUDP covers opaque UDP, which an L7 mediator cannot inspect.
	ProtocolUDP Protocol = "udp"
)

// Inspectable reports whether an L7 mediator can inspect this protocol instead
// of merely forwarding bytes. The declaration layer surfaces this distinction
// so an opaque allowance is never presented as a checked one.
func (p Protocol) Inspectable() bool {
	return p == ProtocolHTTP || p == ProtocolHTTPS
}

// ParseProtocol normalizes a declared protocol value. An empty value means
// ProtocolAny: a host and port without a protocol is an opaque allowance, not
// an HTTP one, because claiming inspectability the engine does not have would
// be a false assurance.
func ParseProtocol(value string) (Protocol, error) {
	switch normalized := Protocol(strings.ToLower(strings.TrimSpace(value))); normalized {
	case "":
		return ProtocolAny, nil
	case ProtocolAny, ProtocolHTTP, ProtocolHTTPS, ProtocolTCP, ProtocolUDP:
		return normalized, nil
	default:
		return "", fmt.Errorf("protocol must be one of %q, %q, %q, %q, or %q",
			ProtocolAny, ProtocolHTTP, ProtocolHTTPS, ProtocolTCP, ProtocolUDP)
	}
}

// Endpoint identifies one network destination. Host may be a host pattern when
// the endpoint is used inside a policy rule; see ValidateHostPattern.
type Endpoint struct {
	Host     string
	Port     int
	Protocol Protocol
}

// NewEndpoint validates and normalizes a destination. The host is lowercased
// because host names are case-insensitive.
func NewEndpoint(host string, port int, protocol Protocol) (Endpoint, error) {
	normalizedHost, err := NormalizeHost(host)
	if err != nil {
		return Endpoint{}, err
	}
	if err := validatePort(port); err != nil {
		return Endpoint{}, err
	}
	normalizedProtocol, err := ParseProtocol(string(protocol))
	if err != nil {
		return Endpoint{}, err
	}
	return Endpoint{Host: normalizedHost, Port: port, Protocol: normalizedProtocol}, nil
}

// Name returns the canonical endpoint identifier used as an egress request
// name. It always renders the concrete protocol, so ParseEndpoint round-trips
// it; rule patterns use FormatEndpointPattern, which renders "any" as "*".
func (e Endpoint) Name() string {
	protocol, err := ParseProtocol(string(e.Protocol))
	if err != nil {
		protocol = ProtocolAny
	}
	return formatHostPort(e.Host, e.Port, string(protocol))
}

// ParseEndpoint decodes a canonical endpoint identifier produced by
// Endpoint.Name. It is strict: an unparsable name never matches an endpoint
// rule, so a malformed request falls through to the policy default.
func ParseEndpoint(name string) (Endpoint, error) {
	host, port, protocol, err := splitEndpointName(name)
	if err != nil {
		return Endpoint{}, err
	}
	normalizedProtocol, err := ParseProtocol(protocol)
	if err != nil {
		return Endpoint{}, err
	}
	return NewEndpoint(host, port, normalizedProtocol)
}

// FormatEndpointPattern renders a host pattern, port, and protocol in the
// canonical "host:port/protocol" form used by endpoint-match rule names. A
// protocol of "any" is rendered as "*" so a pattern never carries the concrete
// protocol name it would otherwise be mistaken for.
//
// An unparsable protocol is rendered verbatim rather than as "*": the result is
// then a pattern parseEndpointPattern rejects, so a malformed pattern can never
// silently widen into an any-protocol allowance.
func FormatEndpointPattern(host string, port int, protocol Protocol) string {
	normalizedProtocol, err := ParseProtocol(string(protocol))
	if err != nil {
		return fmt.Sprintf("%s:%d/%s", strings.ToLower(strings.TrimSpace(host)), port, strings.TrimSpace(string(protocol)))
	}
	rendered := string(normalizedProtocol)
	if normalizedProtocol == ProtocolAny {
		rendered = "*"
	}
	return formatHostPort(host, port, rendered)
}

// NormalizeHost canonicalizes a destination target. It accepts a DNS name or
// label-wise host pattern, an IPv4 or IPv6 literal, and an IPv4 or IPv6 CIDR.
// Literals and CIDRs are returned in net package canonical form; names are
// lowercased. Brackets around an IPv6 literal are accepted on input and are not
// part of the canonical value.
func NormalizeHost(value string) (string, error) {
	normalized := strings.ToLower(strings.Trim(strings.TrimSpace(value), "[]"))
	if normalized == "" {
		return "", errors.New("host must not be empty")
	}
	if strings.Contains(normalized, "/") {
		_, network, err := net.ParseCIDR(normalized)
		if err != nil {
			return "", fmt.Errorf("host %q must be a host name, IP address, or CIDR range", normalized)
		}
		return network.String(), nil
	}
	if literal := net.ParseIP(strings.Trim(normalized, "[]")); literal != nil {
		return literal.String(), nil
	}
	if err := ValidateHostPattern(normalized); err != nil {
		return "", err
	}
	return normalized, nil
}

// ValidateHostPattern rejects a host pattern the engine cannot interpret. A
// pattern is a sequence of DNS labels separated by ".", where "*" matches
// exactly one label. There is no substring or cross-label wildcard: "*" is the
// only metacharacter, so "*.example.com" matches "api.example.com" but neither
// "example.com" nor "a.b.example.com". IP literals and CIDR ranges are accepted
// separately by NormalizeHost.
func ValidateHostPattern(pattern string) error {
	pattern = strings.ToLower(strings.Trim(strings.TrimSpace(pattern), "[]"))
	if pattern == "" {
		return errors.New("host must not be empty")
	}
	if strings.Contains(pattern, "/") {
		if _, _, err := net.ParseCIDR(pattern); err == nil {
			return nil
		}
		return fmt.Errorf("host %q must be a valid CIDR range", pattern)
	}
	if net.ParseIP(pattern) != nil {
		return nil
	}
	if len(pattern) > 253 {
		return fmt.Errorf("host pattern %q is longer than 253 characters", pattern)
	}
	if strings.HasPrefix(pattern, ".") || strings.HasSuffix(pattern, ".") {
		return fmt.Errorf("host pattern %q must not start or end with %q", pattern, ".")
	}
	for _, label := range strings.Split(pattern, ".") {
		if label == "*" {
			continue
		}
		if label == "" {
			return fmt.Errorf("host pattern %q contains an empty label", pattern)
		}
		if len(label) > 63 {
			return fmt.Errorf("host pattern %q label %q is longer than 63 characters", pattern, label)
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return fmt.Errorf("host pattern %q label %q must not start or end with %q", pattern, label, "-")
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return fmt.Errorf("host pattern %q label %q may contain only letters, digits, and %q", pattern, label, "-")
		}
	}
	return nil
}

// HostPatternMatches reports whether host matches pattern under the
// label-wise wildcard rules ValidateHostPattern describes. The comparison is
// case-insensitive.
func HostPatternMatches(pattern, host string) bool {
	patternLabels := strings.Split(strings.ToLower(strings.TrimSpace(pattern)), ".")
	hostLabels := strings.Split(strings.ToLower(strings.TrimSpace(host)), ".")
	if len(patternLabels) != len(hostLabels) {
		return false
	}
	for i, label := range patternLabels {
		if label == "*" {
			continue
		}
		if label != hostLabels[i] {
			return false
		}
	}
	return true
}

// EndpointFromURL derives an endpoint from an http(s) URL. A missing port
// defaults from the scheme, which is how the engine's own HTTP endpoints are
// addressed. The path is ignored: an egress allowance is host and port, not a
// route.
func EndpointFromURL(rawURL string) (Endpoint, error) {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return Endpoint{}, fmt.Errorf("parse url: %w", err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return Endpoint{}, fmt.Errorf("url scheme must be %q or %q", "http", "https")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return Endpoint{}, errors.New("url must name a host")
	}
	port := 80
	if scheme == "https" {
		port = 443
	}
	if rawPort := parsed.Port(); rawPort != "" {
		port, err = strconv.Atoi(rawPort)
		if err != nil {
			return Endpoint{}, fmt.Errorf("parse port: %w", err)
		}
	}
	protocol := ProtocolHTTP
	if scheme == "https" {
		protocol = ProtocolHTTPS
	}
	return NewEndpoint(host, port, protocol)
}

// endpointPattern is a parsed rule name for MatchEndpoint rules.
type endpointPattern struct {
	host     string
	port     int
	protocol Protocol
}

func parseEndpointPattern(value string) (endpointPattern, error) {
	host, port, protocol, err := splitEndpointName(value)
	if err != nil {
		return endpointPattern{}, err
	}
	if strings.TrimSpace(protocol) == "*" {
		protocol = string(ProtocolAny)
	}
	normalizedProtocol, err := ParseProtocol(protocol)
	if err != nil {
		return endpointPattern{}, err
	}
	normalizedHost, err := NormalizeHost(host)
	if err != nil {
		return endpointPattern{}, err
	}
	if err := validatePort(port); err != nil {
		return endpointPattern{}, err
	}
	return endpointPattern{host: normalizedHost, port: port, protocol: normalizedProtocol}, nil
}

// matches reports whether the pattern names the request's destination. The
// declared port and protocol are part of that destination, so they are checked
// once, before either the host match or the resolved-address match: a CIDR
// allowance for 10.0.0.0/8:443 covers that range only on the declared port, not
// on any port a request happens to resolve into it.
func (p endpointPattern) matches(target Endpoint, resolvedIPs []string, action Action) bool {
	if p.port != target.Port {
		return false
	}
	if p.protocol != ProtocolAny && p.protocol != target.Protocol {
		return false
	}
	if endpointHostMatches(p.host, target.Host) {
		return true
	}
	return p.matchesResolvedAddresses(resolvedIPs, action)
}

// matchesResolvedAddresses reports whether the pattern's CIDR host covers the
// addresses the request resolved to. The caller has already matched the port and
// protocol, so an address match cannot widen the pattern past the destination it
// declares. An allowance constrained by a CIDR must cover every answer, because
// the proxy may dial any of them. A denial only needs one answer inside the
// range to refuse the destination.
func (p endpointPattern) matchesResolvedAddresses(addresses []string, action Action) bool {
	if len(addresses) == 0 {
		return false
	}
	_, network, err := net.ParseCIDR(p.host)
	if err != nil {
		return false
	}
	matched := 0
	for _, address := range addresses {
		literal := net.ParseIP(strings.Trim(strings.TrimSpace(address), "[]"))
		if literal != nil && network.Contains(literal) {
			matched++
		}
	}
	if action == Allow {
		return matched == len(addresses)
	}
	return matched > 0
}

// endpointHostMatches matches a declared host pattern against the host a request
// named. A destination that names an address is matched only by that same
// address: a pattern matches names, so a wildcard can never reach an address the
// declaration did not name, and an address pattern never matches a name. The
// reverse direction — a name that resolves to an address — is constrained by the
// rule's allowed addresses, not by pattern matching.
func endpointHostMatches(pattern, host string) bool {
	normalizedPattern, err := NormalizeHost(pattern)
	if err != nil {
		return false
	}
	normalizedHost, err := NormalizeHost(host)
	if err != nil {
		return false
	}
	if _, network, err := net.ParseCIDR(normalizedPattern); err == nil {
		literal := net.ParseIP(normalizedHost)
		return literal != nil && network.Contains(literal)
	}
	if patternIP := net.ParseIP(normalizedPattern); patternIP != nil {
		hostIP := net.ParseIP(normalizedHost)
		return hostIP != nil && patternIP.Equal(hostIP)
	}
	if net.ParseIP(normalizedHost) != nil {
		return false
	}
	return HostPatternMatches(normalizedPattern, normalizedHost)
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", port)
	}
	return nil
}

// formatHostPort renders the canonical host:port/protocol identifier, wrapping
// an IPv6 literal or CIDR in brackets so its colons cannot be mistaken for the
// port separator.
func formatHostPort(host string, port int, protocol string) string {
	normalized := strings.ToLower(strings.TrimSpace(host))
	if strings.Contains(normalized, ":") {
		normalized = "[" + strings.Trim(normalized, "[]") + "]"
	}
	return fmt.Sprintf("%s:%d/%s", normalized, port, protocol)
}

// splitEndpointName splits "host:port/protocol" from the right, so a host that
// itself contains a separator cannot be misread.
func splitEndpointName(value string) (string, int, string, error) {
	trimmed := strings.TrimSpace(value)
	slash := strings.LastIndex(trimmed, "/")
	if slash < 0 {
		return "", 0, "", fmt.Errorf("endpoint %q must be formatted as host:port/protocol", trimmed)
	}
	hostPort, protocol := trimmed[:slash], trimmed[slash+1:]
	var host, rawPort string
	if strings.HasPrefix(hostPort, "[") {
		closing := strings.LastIndex(hostPort, "]")
		if closing < 0 || closing+1 >= len(hostPort) || hostPort[closing+1] != ':' {
			return "", 0, "", fmt.Errorf("endpoint %q must be formatted as [host]:port/protocol", trimmed)
		}
		host, rawPort = hostPort[1:closing], hostPort[closing+2:]
	} else {
		colon := strings.LastIndex(hostPort, ":")
		if colon < 0 {
			return "", 0, "", fmt.Errorf("endpoint %q must be formatted as host:port/protocol", trimmed)
		}
		host, rawPort = hostPort[:colon], hostPort[colon+1:]
	}
	if strings.TrimSpace(host) == "" {
		return "", 0, "", fmt.Errorf("endpoint %q has an empty host", trimmed)
	}
	port, err := strconv.Atoi(strings.TrimSpace(rawPort))
	if err != nil {
		return "", 0, "", fmt.Errorf("endpoint %q has a non-numeric port", trimmed)
	}
	if strings.TrimSpace(protocol) == "" {
		return "", 0, "", fmt.Errorf("endpoint %q has an empty protocol", trimmed)
	}
	return host, port, protocol, nil
}
