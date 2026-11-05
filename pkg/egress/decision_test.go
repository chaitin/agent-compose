package egress

import (
	"reflect"
	"testing"
)

func TestDecideTableDriven(t *testing.T) {
	policy := NewPolicy(Deny,
		Rule{ID: "capability.capset", Names: []string{"dev"}, Action: Allow},
		Rule{ID: "capability.deny-other", Action: Deny, Reason: "not granted"},
	)
	identityPolicy := NewPolicy(Deny, Rule{ID: "forward", Action: Allow})
	allowAll := NewPolicy(Allow)
	unsetAction := NewPolicy(Deny, Rule{ID: "unset", Action: Action("")})

	tests := []struct {
		name       string
		policy     Policy
		request    Request
		wantAction Action
		wantRuleID string
		wantTarget string
		wantReason string
	}{
		{
			name:       "matching rule allows and carries its target",
			policy:     policy,
			request:    Request{Kind: KindCapabilityCapset, Name: "dev"},
			wantAction: Allow,
			wantRuleID: "capability.capset",
			wantTarget: "dev",
		},
		{
			name:       "allow rule with no target resolves the requested name",
			policy:     identityPolicy,
			request:    Request{Kind: KindLLMModel, Name: "baizhi/other"},
			wantAction: Allow,
			wantRuleID: "forward",
			wantTarget: "baizhi/other",
		},
		{
			name:       "later catch-all rule decides an unmatched name",
			policy:     policy,
			request:    Request{Kind: KindCapabilityCapset, Name: "other"},
			wantAction: Deny,
			wantRuleID: "capability.deny-other",
			wantTarget: "",
			wantReason: "not granted",
		},
		{
			name:       "default denies when no rule matches",
			policy:     NewPolicy(Deny),
			request:    Request{Kind: KindLLMModel, Name: "unknown"},
			wantAction: Deny,
		},
		{
			name:       "allow default applies when no rule matches",
			policy:     allowAll,
			request:    Request{Kind: KindLLMModel, Name: "anything"},
			wantAction: Allow,
			wantTarget: "anything",
		},
		{
			name:       "unset default action fails closed",
			policy:     NewPolicy(Action(""), Rule{ID: "never", Names: []string{"x"}, Action: Allow}),
			request:    Request{Kind: KindLLMModel, Name: "y"},
			wantAction: Deny,
		},
		{
			name:       "unset rule action fails closed",
			policy:     unsetAction,
			request:    Request{Kind: KindLLMModel, Name: "anything"},
			wantAction: Deny,
			wantRuleID: "unset",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Decide(tc.policy, tc.request)
			if got.Action != tc.wantAction || got.RuleID != tc.wantRuleID || got.Target != tc.wantTarget || got.Reason != tc.wantReason {
				t.Fatalf("Decide() = %+v, want action=%q rule=%q target=%q reason=%q",
					got, tc.wantAction, tc.wantRuleID, tc.wantTarget, tc.wantReason)
			}
			if got.Generation != tc.policy.Generation() {
				t.Fatalf("generation = %d, want %d", got.Generation, tc.policy.Generation())
			}
		})
	}
}

// TestDecideConstrainsAnAllowanceToItsAddresses pins the difference between an
// allowance that names a destination and one that also constrains where that
// name may resolve.
func TestDecideConstrainsAnAllowanceToItsAddresses(t *testing.T) {
	endpoint := Endpoint{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS}
	policy := NewPolicy(Deny, Rule{
		ID:         "network.allow",
		Match:      MatchEndpoint,
		Names:      []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)},
		Action:     Allow,
		AllowedIPs: []string{"203.0.113.0/24", "198.51.100.7/32"},
	})

	tests := []struct {
		name       string
		resolved   []string
		wantAction Action
		wantReason string
		wantRuleID string
	}{
		{name: "an address inside the range is allowed", resolved: []string{"203.0.113.10"}, wantAction: Allow},
		{name: "the whole address list must be inside", resolved: []string{"203.0.113.10", "203.0.113.11"}, wantAction: Allow},
		{name: "a single allowed address is allowed", resolved: []string{"198.51.100.7"}, wantAction: Allow},
		{
			name:       "an address outside the range is denied by the allowance",
			resolved:   []string{"198.51.100.7", "10.1.2.3"},
			wantAction: Deny,
			wantRuleID: "network.allow",
			wantReason: "the destination resolves to 10.1.2.3, which is outside the allowed addresses 203.0.113.0/24, 198.51.100.7/32",
		},
		{
			name:       "an unresolved destination cannot satisfy the constraint",
			wantAction: Deny,
			wantRuleID: "network.allow",
			wantReason: "the rule allows only 203.0.113.0/24, 198.51.100.7/32, and the destination address was not resolved",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := NetworkRequest("sandbox-1", endpoint)
			request.ResolvedIPs = test.resolved
			result := Decide(policy, request)
			if result.Action != test.wantAction {
				t.Fatalf("Action = %q, want %q (%s)", result.Action, test.wantAction, result.Reason)
			}
			if test.wantAction == Allow {
				if result.RuleID != "network.allow" || result.Target != endpoint.Name() {
					t.Fatalf("allowed result = %+v, want the allowance to resolve the request", result)
				}
				return
			}
			if result.RuleID != test.wantRuleID {
				t.Fatalf("RuleID = %q, want %q", result.RuleID, test.wantRuleID)
			}
			if result.Reason != test.wantReason {
				t.Fatalf("Reason = %q, want %q", result.Reason, test.wantReason)
			}
		})
	}
}

// TestDecideFallsThroughAConstrainedAllowanceToALaterRule pins that a
// constrained allowance which does not apply is not the last word: evaluation
// continues, so a later rule can still allow the destination.
func TestDecideFallsThroughAConstrainedAllowanceToALaterRule(t *testing.T) {
	policy := NewPolicy(Deny,
		Rule{
			ID:         "network.allow",
			Match:      MatchEndpoint,
			Names:      []string{FormatEndpointPattern("api.example.com", 443, ProtocolAny)},
			Action:     Allow,
			AllowedIPs: []string{"203.0.113.0/24"},
		},
		Rule{
			ID:     "network.allow-explicit",
			Match:  MatchEndpoint,
			Names:  []string{FormatEndpointPattern("api.example.com", 443, ProtocolAny)},
			Action: Allow,
		},
	)
	request := NetworkRequest("sandbox-1", Endpoint{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS})
	request.ResolvedIPs = []string{"198.51.100.7"}
	result := Decide(policy, request)
	if !result.Allowed() || result.RuleID != "network.allow-explicit" {
		t.Fatalf("Decide() = %+v, want the unconstrained rule to allow the request", result)
	}
}

// TestDecideMatchesAnAddressOnlyByItself pins the address half of host
// matching: a wildcard is a name pattern, so it can never reach an address the
// declaration did not name, and an address never matches a name.
func TestDecideMatchesAnAddressOnlyByItself(t *testing.T) {
	tests := []struct {
		name       string
		pattern    string
		host       string
		wantAction Action
	}{
		{name: "a wildcard does not match an address", pattern: "*", host: "10.0.0.5", wantAction: Deny},
		{name: "a wildcard host does not match a deeper wildcard address", pattern: "*.*.*.*", host: "10.0.0.5", wantAction: Deny},
		{name: "the same address matches itself", pattern: "10.0.0.5", host: "10.0.0.5", wantAction: Allow},
		{name: "a different address does not match", pattern: "10.0.0.5", host: "10.0.0.6", wantAction: Deny},
		{name: "an address pattern does not match a name", pattern: "10.0.0.5", host: "ten.example.com", wantAction: Deny},
		{name: "a name pattern matches a name", pattern: "*.example.com", host: "ten.example.com", wantAction: Allow},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			policy := NewPolicy(Deny, Rule{
				ID:     "network.allow",
				Match:  MatchEndpoint,
				Names:  []string{FormatEndpointPattern(test.pattern, 443, ProtocolAny)},
				Action: Allow,
			})
			result := Decide(policy, NetworkRequest("sandbox-1", Endpoint{Host: test.host, Port: 443, Protocol: ProtocolHTTPS}))
			if result.Action != test.wantAction {
				t.Fatalf("Decide(pattern %q, host %q) = %q, want %q", test.pattern, test.host, result.Action, test.wantAction)
			}
		})
	}
}

func TestDecideFirstMatchWins(t *testing.T) {
	policy := NewPolicy(Deny,
		Rule{ID: "first", Names: []string{"dev"}, Action: Allow, Target: "dev-v1"},
		Rule{ID: "second", Names: []string{"dev"}, Action: Deny},
	)
	got := Decide(policy, Request{Kind: KindCapabilityCapset, Name: "dev"})
	if !got.Allowed() || got.RuleID != "first" || got.Target != "dev-v1" {
		t.Fatalf("Decide() = %+v, want the first matching rule to win", got)
	}
}

func TestDecideEmptyRuleMatchesAnyName(t *testing.T) {
	policy := NewPolicy(Deny, Rule{ID: "any", Action: Allow})
	for _, name := range []string{"dev", "", "anything"} {
		got := Decide(policy, Request{Kind: KindLLMModel, Name: name})
		if !got.Allowed() || got.RuleID != "any" || got.Target != name {
			t.Fatalf("Decide(name=%q) = %+v, want allow resolved to the request name", name, got)
		}
	}
}

func TestDecideDoesNotUseConsumerOrKind(t *testing.T) {
	policy := NewPolicy(Deny, Rule{ID: "any", Action: Allow})
	first := Decide(policy, Request{Consumer: "sandbox-a", Kind: KindLLMModel, Name: "m"})
	second := Decide(policy, Request{Consumer: "sandbox-b", Kind: KindCapabilityCapset, Name: "m"})
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("consumer and kind must not change the outcome: %+v vs %+v", first, second)
	}
}

// TestDecideDoesNotLetAnAddressConstraintDenyUnderAnAllowDefault pins that a
// policy which allows by default keeps allowing. An allow entry is not what
// authorizes the request under such a policy, so its address constraint cannot
// deny it: otherwise deleting the entry would permit strictly more than keeping
// it, and the declared address range would silently become a network filter the
// documentation says the declaration is not.
func TestDecideDoesNotLetAnAddressConstraintDenyUnderAnAllowDefault(t *testing.T) {
	endpoint := Endpoint{Host: "api.example.com", Port: 443, Protocol: ProtocolHTTPS}
	allowAll := NewPolicy(Allow, Rule{
		ID:         "network.allow",
		Match:      MatchEndpoint,
		Names:      []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)},
		Action:     Allow,
		AllowedIPs: []string{"203.0.113.0/24"},
	})
	for _, resolved := range [][]string{nil, {"198.51.100.7"}, {"203.0.113.10"}} {
		request := NetworkRequest("sandbox-1", endpoint)
		request.ResolvedIPs = resolved
		result := Decide(allowAll, request)
		if result.Action != Allow {
			t.Fatalf("Action = %q for resolved %v, want Allow under an allow-all default (%s)", result.Action, resolved, result.Reason)
		}
	}

	// The constraint still refuses when the default denies, which is the policy
	// shape the address range was declared for.
	restrictive := NewPolicy(Deny, Rule{
		ID:         "network.allow",
		Match:      MatchEndpoint,
		Names:      []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)},
		Action:     Allow,
		AllowedIPs: []string{"203.0.113.0/24"},
	})
	request := NetworkRequest("sandbox-1", endpoint)
	request.ResolvedIPs = []string{"198.51.100.7"}
	if got := Decide(restrictive, request); got.Action != Deny {
		t.Fatalf("Action = %q, want a refusal under a deny default", got.Action)
	}

	// An explicit deny for the same destination still wins under allow-all: the
	// constraint stops denying, it does not stop the rules from denying.
	denying := NewPolicy(Allow,
		Rule{ID: "network.allow", Match: MatchEndpoint, Names: []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)}, Action: Allow, AllowedIPs: []string{"203.0.113.0/24"}},
		Rule{ID: "network.deny", Match: MatchEndpoint, Names: []string{FormatEndpointPattern(endpoint.Host, endpoint.Port, endpoint.Protocol)}, Action: Deny, Reason: "declared refusal"},
	)
	request = NetworkRequest("sandbox-1", endpoint)
	request.ResolvedIPs = []string{"198.51.100.7"}
	got := Decide(denying, request)
	if got.Action != Deny || got.RuleID != "network.deny" {
		t.Fatalf("result = %+v, want the explicit refusal to decide", got)
	}
}
