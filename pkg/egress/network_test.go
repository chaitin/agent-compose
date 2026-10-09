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
	valid := []string{"api.github.com", "*.example.com", "*", "10.0.0.1", "a-b.example.com"}
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
		"2001:db8::1":         "ipv6 separator",
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

	for _, raw := range []string{"api.github.com", "api.github.com:443", "api.github.com:443/", ":443/tcp", "api.github.com:abc/tcp"} {
		if _, err := ParseEndpoint(raw); err == nil {
			t.Fatalf("ParseEndpoint(%q) succeeded, want an error", raw)
		}
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

// TestRuleMatchModeChangesGeneration guards the content hash against an exact
// rule and an endpoint rule that could otherwise serialize to the same bytes.
func TestRuleMatchModeChangesGeneration(t *testing.T) {
	exact := NewPolicy(Deny, Rule{ID: "r", Names: []string{"endpoint"}, Action: Allow})
	endpoint := NewPolicy(Deny, Rule{ID: "r", Match: MatchEndpoint, Action: Allow})
	if exact.Generation() == endpoint.Generation() {
		t.Fatal("an exact rule and an endpoint rule produced the same generation")
	}
	allowed := Decide(endpoint, Request{Kind: KindNetworkEndpoint, Name: "api.github.com:443/https"})
	if !allowed.Allowed() {
		t.Fatalf("endpoint rule with no names did not match: %+v", allowed)
	}
	notMatched := Decide(exact, Request{Kind: KindNetworkEndpoint, Name: "api.github.com:443/https"})
	if notMatched.Allowed() {
		t.Fatalf("exact rule matched an endpoint name: %+v", notMatched)
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
