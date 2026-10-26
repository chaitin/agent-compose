package egress

import (
	"testing"
)

func TestHostPatternMatches(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		host    string
		want    bool
	}{
		{name: "exact", pattern: "api.github.com", host: "api.github.com", want: true},
		{name: "case insensitive", pattern: "API.GitHub.com", host: "api.github.com", want: true},
		{name: "exact does not match a longer host", pattern: "github.com", host: "api.github.com", want: false},
		{name: "one label wildcard", pattern: "*.example.com", host: "api.example.com", want: true},
		{name: "one label wildcard does not match the apex", pattern: "*.example.com", host: "example.com", want: false},
		{name: "one label wildcard does not cross labels", pattern: "*.example.com", host: "a.b.example.com", want: false},
		{name: "leading wildcard", pattern: "*.example.com", host: "*.example.com", want: true},
		{name: "multiple wildcards", pattern: "*.*.example.com", host: "a.b.example.com", want: true},
		{name: "ipv4 literal", pattern: "10.0.0.1", host: "10.0.0.1", want: true},
		{name: "ipv4 wildcard label", pattern: "10.0.*.1", host: "10.0.7.1", want: true},
		{name: "different label count", pattern: "a.example.com", host: "a.example.com.", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HostPatternMatches(tc.pattern, tc.host); got != tc.want {
				t.Fatalf("HostPatternMatches(%q, %q) = %v, want %v", tc.pattern, tc.host, got, tc.want)
			}
		})
	}
}

func TestValidateHostPattern(t *testing.T) {
	valid := []string{"api.github.com", "*.example.com", "*", "10.0.0.1", "2001:db8::1", "10.0.0.0/8", "2001:db8::/32", "a-b.example.com"}
	for _, pattern := range valid {
		if err := ValidateHostPattern(pattern); err != nil {
			t.Fatalf("ValidateHostPattern(%q) = %v, want nil", pattern, err)
		}
	}
	invalid := map[string]string{
		"":                    "empty",
		"*.":                  "empty label",
		".example.com":        "empty label",
		"*.example..com":      "empty label",
		"foo*bar.example.com": "wildcard inside a label",
		"exa mple.com":        "space",
		"2001:db8::zz":        "invalid ipv6",
		"10.0.0.0/33":         "invalid cidr",
		"-bad.example.com":    "leading dash",
	}
	for pattern, reason := range invalid {
		if err := ValidateHostPattern(pattern); err == nil {
			t.Fatalf("ValidateHostPattern(%q) succeeded, want an error (%s)", pattern, reason)
		}
	}
}

func TestEndpointNameRoundTrip(t *testing.T) {
	endpoint, err := NewEndpoint("API.GitHub.com", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatalf("NewEndpoint returned error: %v", err)
	}
	if got := endpoint.Name(); got != "api.github.com:443/https" {
		t.Fatalf("Name() = %q, want api.github.com:443/https", got)
	}
	parsed, err := ParseEndpoint(endpoint.Name())
	if err != nil {
		t.Fatalf("ParseEndpoint returned error: %v", err)
	}
	if parsed != endpoint {
		t.Fatalf("ParseEndpoint(Name()) = %+v, want %+v", parsed, endpoint)
	}

	anyEndpoint, err := NewEndpoint("api.github.com", 443, ProtocolAny)
	if err != nil {
		t.Fatalf("NewEndpoint(any) returned error: %v", err)
	}
	if got := anyEndpoint.Name(); got != "api.github.com:443/any" {
		t.Fatalf("Name(any) = %q, want a concrete protocol", got)
	}
	if _, err := ParseEndpoint(anyEndpoint.Name()); err != nil {
		t.Fatalf("ParseEndpoint(Name(any)) returned error: %v", err)
	}

	ipv6Endpoint, err := NewEndpoint("2001:0db8::1", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatalf("NewEndpoint(ipv6) returned error: %v", err)
	}
	if got := ipv6Endpoint.Name(); got != "[2001:db8::1]:443/https" {
		t.Fatalf("Name(ipv6) = %q, want [2001:db8::1]:443/https", got)
	}
	if parsed, err := ParseEndpoint(ipv6Endpoint.Name()); err != nil || parsed != ipv6Endpoint {
		t.Fatalf("ParseEndpoint(ipv6) = %+v, %v, want %+v", parsed, err, ipv6Endpoint)
	}
	cidrEndpoint, err := NewEndpoint("10.2.16.21/8", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatalf("NewEndpoint(cidr) returned error: %v", err)
	}
	if got := cidrEndpoint.Name(); got != "10.0.0.0/8:443/https" {
		t.Fatalf("Name(cidr) = %q, want 10.0.0.0/8:443/https", got)
	}
	if parsed, err := ParseEndpoint(cidrEndpoint.Name()); err != nil || parsed != cidrEndpoint {
		t.Fatalf("ParseEndpoint(cidr) = %+v, %v, want %+v", parsed, err, cidrEndpoint)
	}

	for _, raw := range []string{"api.github.com", "api.github.com:443", "api.github.com:443/", ":443/tcp", "api.github.com:abc/tcp"} {
		if _, err := ParseEndpoint(raw); err == nil {
			t.Fatalf("ParseEndpoint(%q) succeeded, want an error", raw)
		}
	}
}

// TestFormatEndpointPatternDoesNotWidenAnInvalidProtocol pins that a malformed
// protocol renders a pattern that matches nothing instead of silently becoming
// the any-protocol wildcard, which would widen an allowance.
func TestFormatEndpointPatternDoesNotWidenAnInvalidProtocol(t *testing.T) {
	if got := FormatEndpointPattern("api.github.com", 443, ProtocolAny); got != "api.github.com:443/*" {
		t.Fatalf("FormatEndpointPattern(any) = %q, want api.github.com:443/*", got)
	}
	invalid := FormatEndpointPattern("api.github.com", 443, Protocol("htps"))
	if invalid == "api.github.com:443/*" {
		t.Fatalf("FormatEndpointPattern(bad protocol) = %q, widened to any", invalid)
	}
	policy := NewPolicy(Deny, Rule{ID: "allow", Match: MatchEndpoint, Names: []string{invalid}, Action: Allow})
	result := Decide(policy, Request{Kind: KindNetworkEndpoint, Name: "api.github.com:443/any"})
	if result.Allowed() {
		t.Fatalf("a malformed allow pattern matched: %+v", result)
	}
}

func TestProtocolInspectable(t *testing.T) {
	for protocol, want := range map[Protocol]bool{
		ProtocolHTTP:  true,
		ProtocolHTTPS: true,
		ProtocolAny:   false,
		ProtocolTCP:   false,
		ProtocolUDP:   false,
	} {
		if got := protocol.Inspectable(); got != want {
			t.Fatalf("%s.Inspectable() = %v, want %v", protocol, got, want)
		}
	}
}

func TestEngineEndpointsFromURLs(t *testing.T) {
	endpoints, err := EngineEndpoints("http://host.docker.internal:7410", "https://otel.example.com")
	if err != nil {
		t.Fatalf("EngineEndpoints returned error: %v", err)
	}
	if len(endpoints) != 2 {
		t.Fatalf("endpoints = %+v, want the facade and telemetry endpoints", endpoints)
	}
	if endpoints[0].Purpose != PurposeLLMFacade || endpoints[0].Endpoint.Name() != "host.docker.internal:7410/http" {
		t.Fatalf("facade endpoint = %+v, want host.docker.internal:7410/http", endpoints[0])
	}
	if endpoints[1].Purpose != PurposeTelemetry || endpoints[1].Endpoint.Name() != "otel.example.com:443/https" {
		t.Fatalf("telemetry endpoint = %+v, want otel.example.com:443/https", endpoints[1])
	}
	if endpoints[0].Source != SourceRuntimeBaseURL || endpoints[1].Source != SourceAgentTelemetryOTLPEnd {
		t.Fatalf("endpoint sources = %q, %q", endpoints[0].Source, endpoints[1].Source)
	}

	empty, err := EngineEndpoints("  ", "")
	if err != nil {
		t.Fatalf("EngineEndpoints(empty) returned error: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("EngineEndpoints(empty) = %+v, want none", empty)
	}

	if _, err := EngineEndpoints("ftp://example.com", ""); err == nil {
		t.Fatal("EngineEndpoints accepted a non-HTTP scheme")
	}
}

func TestCompileNetworkPolicyDenyAllowsDeclaredAndEngineEndpoints(t *testing.T) {
	endpoints, err := EngineEndpoints("http://host.docker.internal:7410", "https://otel.example.com")
	if err != nil {
		t.Fatalf("EngineEndpoints returned error: %v", err)
	}
	declaration := NetworkDeclaration{
		Default: Deny,
		Allow: []AllowEntry{
			{Host: "api.github.com", Port: 443, Protocol: ProtocolHTTPS},
			{Host: "*.example.com", Port: 8080, Protocol: ProtocolTCP},
		},
	}
	policy, err := CompileNetworkPolicy(declaration, endpoints)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy returned error: %v", err)
	}

	allowedByDeclaration, err := NewEndpoint("api.github.com", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", allowedByDeclaration)); !got.Allowed() || got.RuleID != "network.allow" {
		t.Fatalf("declared endpoint decision = %+v, want an allow by network.allow", got)
	}

	wildcard, err := NewEndpoint("api.example.com", 8080, ProtocolTCP)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", wildcard)); !got.Allowed() {
		t.Fatalf("wildcard endpoint decision = %+v, want an allow", got)
	}

	denied, err := NewEndpoint("evil.example.net", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", denied)); got.Allowed() {
		t.Fatalf("undeclared endpoint decision = %+v, want a deny", got)
	}

	// The engine's own endpoints stay reachable through engine rules, not user
	// rules, so the audit trail names the exemption.
	engineRequests := map[string]Endpoint{
		"engine." + string(PurposeLLMFacade): endpoints[0].Endpoint,
		"engine." + string(PurposeTelemetry): endpoints[1].Endpoint,
	}
	for wantRule, endpoint := range engineRequests {
		got := Decide(policy, NetworkRequest("sandbox-1", endpoint))
		if !got.Allowed() || got.RuleID != wantRule {
			t.Fatalf("engine endpoint %s decision = %+v, want an allow by %s", endpoint.Name(), got, wantRule)
		}
	}
}

func TestCompileNetworkPolicyRejectsInvalidDeclaration(t *testing.T) {
	if _, err := CompileNetworkPolicy(NetworkDeclaration{Default: Action("")}, nil); err == nil {
		t.Fatal("CompileNetworkPolicy accepted an unset default action")
	}
	if _, err := CompileNetworkPolicy(NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{Host: "", Port: 443}}}, nil); err == nil {
		t.Fatal("CompileNetworkPolicy accepted an empty host")
	}
	if _, err := CompileNetworkPolicy(NetworkDeclaration{Default: Deny, Allow: []AllowEntry{{Host: "api.github.com", Port: 0}}}, nil); err == nil {
		t.Fatal("CompileNetworkPolicy accepted port 0")
	}
	if _, err := CompileNetworkPolicy(NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{Host: "", Port: 443}}}, nil); err == nil {
		t.Fatal("CompileNetworkPolicy accepted an empty denied host")
	}
	if _, err := CompileNetworkPolicy(NetworkDeclaration{Default: Allow, Deny: []DenyEntry{{Host: "api.github.com", Port: 443, Protocol: "sctp"}}}, nil); err == nil {
		t.Fatal("CompileNetworkPolicy accepted an unknown denied protocol")
	}
}

func TestEffectiveNetworkPolicyUndeclaredIsUnrestricted(t *testing.T) {
	policy, err := EffectiveNetworkPolicy(nil, nil)
	if err != nil {
		t.Fatalf("EffectiveNetworkPolicy(nil) returned error: %v", err)
	}
	if policy.Default() != Allow {
		t.Fatalf("undeclared default = %q, want %q (D3: undeclared is not deny)", policy.Default(), Allow)
	}
	endpoint, err := NewEndpoint("anything.example.net", 1234, ProtocolTCP)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", endpoint)); !got.Allowed() {
		t.Fatalf("undeclared policy denied an endpoint: %+v", got)
	}
}

func TestCompileNetworkPolicyEngineRulesPrecedeDeclaredRules(t *testing.T) {
	endpoint, err := NewEndpoint("host.docker.internal", 7410, ProtocolHTTP)
	if err != nil {
		t.Fatal(err)
	}
	engine := []EngineEndpoint{{Purpose: PurposeLLMFacade, Endpoint: endpoint, Source: SourceRuntimeBaseURL}}
	policy, err := CompileNetworkPolicy(NetworkDeclaration{
		Default: Deny,
		Allow:   []AllowEntry{{Host: "host.docker.internal", Port: 7410, Protocol: ProtocolHTTP}},
	}, engine)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy returned error: %v", err)
	}
	got := Decide(policy, NetworkRequest("sandbox-1", endpoint))
	if got.RuleID != "engine."+string(PurposeLLMFacade) {
		t.Fatalf("rule id = %q, want the engine rule to win so the exemption is attributable", got.RuleID)
	}
}

// TestRuleMatchModeChangesGeneration guards the content hash against two rules
// that are identical except for Match. Both rules carry the same names, so the
// generations can only differ because Match is hashed.
func TestRuleMatchModeChangesGeneration(t *testing.T) {
	names := []string{"api.github.com:443/https"}
	exact := NewPolicy(Deny, Rule{ID: "r", Names: names, Action: Allow})
	endpoint := NewPolicy(Deny, Rule{ID: "r", Match: MatchEndpoint, Names: names, Action: Allow})
	if exact.Generation() == endpoint.Generation() {
		t.Fatal("an exact rule and an endpoint rule with the same names produced the same generation")
	}
	allowed := Decide(endpoint, Request{Kind: KindNetworkEndpoint, Name: "api.github.com:443/https"})
	if !allowed.Allowed() {
		t.Fatalf("endpoint rule did not match its declared endpoint: %+v", allowed)
	}
	notMatched := Decide(endpoint, Request{Kind: KindNetworkEndpoint, Name: "other.example.com:443/https"})
	if notMatched.Allowed() {
		t.Fatalf("endpoint rule matched a different endpoint: %+v", notMatched)
	}
	literal := Decide(exact, Request{Kind: KindNetworkEndpoint, Name: "api.github.com:443/https"})
	if !literal.Allowed() {
		t.Fatalf("exact rule did not match the literal name: %+v", literal)
	}
	if other := Decide(exact, Request{Kind: KindNetworkEndpoint, Name: "other.example.com:443/https"}); other.Allowed() {
		t.Fatalf("exact rule matched a different endpoint name: %+v", other)
	}
}

// TestEndpointRuleWithoutNamesMatchesAnyEndpoint pins the documented catch-all:
// a rule with no names matches every request of its match mode.
func TestEndpointRuleWithoutNamesMatchesAnyEndpoint(t *testing.T) {
	policy := NewPolicy(Deny, Rule{ID: "r", Match: MatchEndpoint, Action: Allow})
	allowed := Decide(policy, Request{Kind: KindNetworkEndpoint, Name: "api.github.com:443/https"})
	if !allowed.Allowed() || allowed.RuleID != "r" {
		t.Fatalf("endpoint rule with no names did not match: %+v", allowed)
	}
}

func TestEndpointRuleDoesNotMatchMalformedRequest(t *testing.T) {
	policy := NewPolicy(Deny, Rule{ID: "network.allow", Match: MatchEndpoint, Names: []string{"api.github.com:443/https"}, Action: Allow})
	malformed := Decide(policy, Request{Kind: KindNetworkEndpoint, Name: "not-an-endpoint"})
	if malformed.Allowed() {
		t.Fatalf("malformed request matched the endpoint rule: %+v", malformed)
	}
	valid := Decide(policy, Request{Kind: KindNetworkEndpoint, Name: "api.github.com:443/https"})
	if !valid.Allowed() || valid.RuleID != "network.allow" {
		t.Fatalf("valid endpoint request = %+v, want an allow by network.allow", valid)
	}
}

// TestCompileNetworkPolicyCanonicalizesDeclaredAddresses pins that the compiled
// rule carries the canonical form of a declared address constraint. Validation
// accepts a bare address or an unmasked prefix, so compiling the raw string
// would store a rule the decision layer can never match and would deny a
// destination the declaration allowed.
func TestCompileNetworkPolicyCanonicalizesDeclaredAddresses(t *testing.T) {
	declaration := NetworkDeclaration{
		Default: Deny,
		Allow: []AllowEntry{{
			Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS,
			AllowedIPs: []string{"203.0.113.7", "198.51.100.0/24"},
		}},
	}
	if err := declaration.Validate(); err != nil {
		t.Fatalf("the declaration layer rejected the shorthand form: %v", err)
	}
	policy, err := CompileNetworkPolicy(declaration, nil)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy returned error: %v", err)
	}
	endpoint, err := NewEndpoint("api.example.com", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	request := NetworkRequest("sandbox-1", endpoint)
	request.ResolvedIPs = []string{"203.0.113.7"}
	if got := Decide(policy, request); !got.Allowed() {
		t.Fatalf("decision = %+v, want the declared bare address to allow its destination", got)
	}
	request.ResolvedIPs = []string{"198.51.100.9"}
	if got := Decide(policy, request); !got.Allowed() {
		t.Fatalf("decision = %+v, want an address inside the declared range to allow its destination", got)
	}
	request.ResolvedIPs = []string{"203.0.113.8"}
	if got := Decide(policy, request); got.Allowed() {
		t.Fatalf("decision = %+v, want an address in neither declared range to be denied", got)
	}
}

func TestCompileNetworkPolicyDenyList(t *testing.T) {
	declaration := NetworkDeclaration{
		Default: Allow,
		Deny: []DenyEntry{
			{Host: "metadata.example.com", Port: 80, Protocol: ProtocolTCP},
			{Host: "*.blocked.example", Port: 443, Protocol: ProtocolHTTPS},
		},
	}
	if !declaration.Restricts() {
		t.Fatal("a declaration with denials does not report that it restricts")
	}
	policy, err := CompileNetworkPolicy(declaration, nil)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy returned error: %v", err)
	}
	if policy.Default() != Allow {
		t.Fatalf("default = %q, want %q", policy.Default(), Allow)
	}
	blocked, err := NewEndpoint("metadata.example.com", 80, ProtocolTCP)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", blocked)); got.Allowed() || got.RuleID != "network.deny" {
		t.Fatalf("denied endpoint decision = %+v, want a deny by network.deny", got)
	}
	allowed, err := NewEndpoint("public.example.com", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", allowed)); !got.Allowed() {
		t.Fatalf("undeclared endpoint decision = %+v, want the allow default", got)
	}
}

func TestCompileNetworkPolicyDenyWinsOverAllow(t *testing.T) {
	declaration := NetworkDeclaration{
		Default: Deny,
		Allow:   []AllowEntry{{Host: "*.example.com", Port: 443, Protocol: ProtocolHTTPS}},
		Deny:    []DenyEntry{{Host: "bad.example.com", Port: 443, Protocol: ProtocolHTTPS}},
	}
	policy, err := CompileNetworkPolicy(declaration, nil)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy returned error: %v", err)
	}
	bad, err := NewEndpoint("bad.example.com", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", bad)); got.Allowed() || got.RuleID != "network.deny" {
		t.Fatalf("denied endpoint decision = %+v, want the denial to win over the wildcard allowance", got)
	}
	good, err := NewEndpoint("api.example.com", 443, ProtocolHTTPS)
	if err != nil {
		t.Fatal(err)
	}
	if got := Decide(policy, NetworkRequest("sandbox-1", good)); !got.Allowed() {
		t.Fatalf("allowed endpoint decision = %+v, want the allowance", got)
	}
}

func TestCompileNetworkPolicyDenyListKeepsEngineEndpointsReachable(t *testing.T) {
	engineEndpoint, err := NewEndpoint("host.docker.internal", 7410, ProtocolHTTP)
	if err != nil {
		t.Fatal(err)
	}
	engine := []EngineEndpoint{{Purpose: PurposeLLMFacade, Endpoint: engineEndpoint, Source: SourceRuntimeBaseURL}}
	declaration := NetworkDeclaration{
		Default: Allow,
		Deny:    []DenyEntry{{Host: "host.docker.internal", Port: 7410, Protocol: ProtocolHTTP}},
	}
	policy, err := CompileNetworkPolicy(declaration, engine)
	if err != nil {
		t.Fatalf("CompileNetworkPolicy returned error: %v", err)
	}
	got := Decide(policy, NetworkRequest("sandbox-1", engineEndpoint))
	if !got.Allowed() || got.RuleID != "engine."+string(PurposeLLMFacade) {
		t.Fatalf("engine endpoint decision = %+v, want the engine rule to precede the denial", got)
	}
}

func TestReportNetworkPolicyDenyList(t *testing.T) {
	declaration := NetworkDeclaration{
		Default: Allow,
		Deny:    []DenyEntry{{Host: "metadata.example.com", Port: 80, Protocol: ProtocolTCP}},
	}
	report, err := ReportNetworkPolicy(&declaration, nil)
	if err != nil {
		t.Fatalf("ReportNetworkPolicy returned error: %v", err)
	}
	if !report.Restricts || report.Default != Allow {
		t.Fatalf("report = %+v, want Restricts true with allow default", report)
	}
	if len(report.Deny) != 1 || report.Deny[0].Endpoint != "metadata.example.com:80/tcp" {
		t.Fatalf("report.Deny = %+v, want the declared denial", report.Deny)
	}

	allowOnly := NetworkDeclaration{Default: Allow, Allow: []AllowEntry{{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS}}}
	report, err = ReportNetworkPolicy(&allowOnly, nil)
	if err != nil {
		t.Fatalf("ReportNetworkPolicy returned error: %v", err)
	}
	if report.Restricts {
		t.Fatalf("report = %+v, want an allow-only declaration not to restrict", report)
	}
}

func TestNormalizeHostCanonicalizesBracketedIPv6(t *testing.T) {
	tests := map[string]string{
		"[2001:db8::1]":   "2001:db8::1",
		"[2001:db8::/32]": "2001:db8::/32",
		"2001:0DB8:0::1":  "2001:db8::1",
		"10.2.16.21/8":    "10.0.0.0/8",
	}
	for input, want := range tests {
		got, err := NormalizeHost(input)
		if err != nil {
			t.Fatalf("NormalizeHost(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("NormalizeHost(%q) = %q, want %q", input, got, want)
		}
	}
}
