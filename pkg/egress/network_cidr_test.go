package egress

import "testing"

func TestCIDRPolicyMatchesResolvedAddresses(t *testing.T) {
	policy := NewPolicy(Deny, Rule{
		ID:     "network.allow",
		Match:  MatchEndpoint,
		Names:  []string{FormatEndpointPattern("10.0.0.0/8", 443, ProtocolAny)},
		Action: Allow,
	})
	tests := []struct {
		name        string
		request     Request
		wantAllowed bool
	}{
		{
			name:        "name resolving inside the range",
			request:     Request{Name: "service.example:443/https", ResolvedIPs: []string{"10.2.16.21"}},
			wantAllowed: true,
		},
		{
			name:        "name resolving outside the range",
			request:     Request{Name: "service.example:443/https", ResolvedIPs: []string{"203.0.113.7"}},
			wantAllowed: false,
		},
		{
			name:        "direct address inside the range",
			request:     Request{Name: "10.2.16.21:443/https"},
			wantAllowed: true,
		},
		{
			name:        "one allowed and one disallowed answer",
			request:     Request{Name: "service.example:443/https", ResolvedIPs: []string{"10.2.16.21", "203.0.113.7"}},
			wantAllowed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Decide(policy, tt.request).Allowed(); got != tt.wantAllowed {
				t.Fatalf("Decide() allowed = %v, want %v", got, tt.wantAllowed)
			}
		})
	}
}

func TestIPv6PolicyMatchesDirectAndResolvedAddresses(t *testing.T) {
	policy := NewPolicy(Deny, Rule{
		ID:     "network.allow",
		Match:  MatchEndpoint,
		Names:  []string{FormatEndpointPattern("2001:db8::/32", 443, ProtocolAny)},
		Action: Allow,
	})
	for _, request := range []Request{
		{Name: "[2001:db8::1]:443/https"},
		{Name: "service.example:443/https", ResolvedIPs: []string{"2001:db8::1"}},
	} {
		if !Decide(policy, request).Allowed() {
			t.Fatalf("Decide(%+v) denied an IPv6 destination inside the range", request)
		}
	}
	if Decide(policy, Request{Name: "[2001:db9::1]:443/https"}).Allowed() {
		t.Fatal("Decide() allowed an IPv6 destination outside the range")
	}
}

// TestCIDRPolicyAppliesTheDeclaredPortAndProtocol pins that a CIDR destination
// stays a destination: the port and protocol an entry declares are part of what
// it names, so resolving an address inside the range cannot widen the entry to
// another port. The reverse direction matters too: a CIDR denial must not
// over-block traffic to the same address on a port it did not name.
func TestCIDRPolicyAppliesTheDeclaredPortAndProtocol(t *testing.T) {
	allowInsideRange := NewPolicy(Deny, Rule{
		ID:     "network.allow",
		Match:  MatchEndpoint,
		Names:  []string{FormatEndpointPattern("10.0.0.0/8", 443, ProtocolAny)},
		Action: Allow,
	})
	allowHTTPSOnly := NewPolicy(Deny, Rule{
		ID:     "network.allow",
		Match:  MatchEndpoint,
		Names:  []string{FormatEndpointPattern("10.0.0.0/8", 443, ProtocolHTTPS)},
		Action: Allow,
	})
	denyOn22 := NewPolicy(Allow, Rule{
		ID:     "network.deny",
		Match:  MatchEndpoint,
		Names:  []string{FormatEndpointPattern("10.0.0.0/8", 22, ProtocolTCP)},
		Action: Deny,
	})
	tests := []struct {
		name        string
		policy      Policy
		request     Request
		wantAllowed bool
	}{
		{
			name:        "allow CIDR does not cover a direct address on another port",
			policy:      allowInsideRange,
			request:     Request{Name: "10.1.2.3:22/tcp", ResolvedIPs: []string{"10.1.2.3"}},
			wantAllowed: false,
		},
		{
			name:        "allow CIDR does not cover a name resolving inside the range on another port",
			policy:      allowInsideRange,
			request:     Request{Name: "service.example:22/tcp", ResolvedIPs: []string{"10.1.2.3"}},
			wantAllowed: false,
		},
		{
			name:        "allow CIDR covers the declared port for a name resolving inside the range",
			policy:      allowInsideRange,
			request:     Request{Name: "service.example:443/https", ResolvedIPs: []string{"10.1.2.3"}},
			wantAllowed: true,
		},
		{
			name:        "allow CIDR still denies an address outside the range",
			policy:      allowInsideRange,
			request:     Request{Name: "service.example:443/https", ResolvedIPs: []string{"203.0.113.7"}},
			wantAllowed: false,
		},
		{
			name:        "allow CIDR with an https protocol does not cover another protocol",
			policy:      allowHTTPSOnly,
			request:     Request{Name: "service.example:443/tcp", ResolvedIPs: []string{"10.1.2.3"}},
			wantAllowed: false,
		},
		{
			name:        "deny CIDR does not over-block another port on the same address",
			policy:      denyOn22,
			request:     Request{Name: "service.example:443/https", ResolvedIPs: []string{"10.1.2.3"}},
			wantAllowed: true,
		},
		{
			name:        "deny CIDR refuses the port it declares",
			policy:      denyOn22,
			request:     Request{Name: "service.example:22/tcp", ResolvedIPs: []string{"10.1.2.3"}},
			wantAllowed: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decide(tt.policy, tt.request)
			if got.Allowed() != tt.wantAllowed {
				t.Fatalf("Decide(%s) = %+v, want allowed=%v", tt.request.Name, got, tt.wantAllowed)
			}
		})
	}
}
