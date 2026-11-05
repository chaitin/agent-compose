package egress

import (
	"strings"
	"testing"
)

func TestPolicyHashIsStableForTheSamePolicy(t *testing.T) {
	rules := []Rule{
		{ID: "allow-api", Match: MatchEndpoint, Names: []string{"api.github.com:443/*"}, Action: Allow, Target: "api.github.com", AllowedIPs: []string{"140.82.112.0/20"}},
		{ID: "deny-all", Action: Deny, Reason: "not declared"},
	}
	base := NewPolicy(Deny, rules...)
	rebuilt := NewPolicy(Deny,
		Rule{ID: "allow-api", Match: MatchEndpoint, Names: []string{"api.github.com:443/*"}, Action: Allow, Target: "api.github.com", AllowedIPs: []string{"140.82.112.0/20"}},
		Rule{ID: "deny-all", Action: Deny, Reason: "not declared"},
	)
	if got, want := PolicyHash(rebuilt), PolicyHash(base); got != want {
		t.Fatalf("identical policies hashed differently: %q vs %q", got, want)
	}
	if !strings.HasPrefix(PolicyHash(base), PolicyHashPrefix) {
		t.Fatalf("hash %q does not name its algorithm", PolicyHash(base))
	}
}

func TestPolicyHashChangesWhenADecisionChanges(t *testing.T) {
	base := NewPolicy(Deny,
		Rule{ID: "r", Match: MatchExact, Names: []string{"a"}, Action: Allow},
		Rule{ID: "s", Action: Deny, Reason: "default"},
	)
	tests := []struct {
		name   string
		policy Policy
	}{
		{name: "default action", policy: NewPolicy(Allow, Rule{ID: "r", Match: MatchExact, Names: []string{"a"}, Action: Allow}, Rule{ID: "s", Action: Deny, Reason: "default"})},
		{name: "rule id", policy: NewPolicy(Deny, Rule{ID: "other", Match: MatchExact, Names: []string{"a"}, Action: Allow}, Rule{ID: "s", Action: Deny, Reason: "default"})},
		{name: "match mode", policy: NewPolicy(Deny, Rule{ID: "r", Match: MatchEndpoint, Names: []string{"a"}, Action: Allow}, Rule{ID: "s", Action: Deny, Reason: "default"})},
		{name: "name", policy: NewPolicy(Deny, Rule{ID: "r", Match: MatchExact, Names: []string{"b"}, Action: Allow}, Rule{ID: "s", Action: Deny, Reason: "default"})},
		{name: "rule order", policy: NewPolicy(Deny, Rule{ID: "s", Action: Deny, Reason: "default"}, Rule{ID: "r", Match: MatchExact, Names: []string{"a"}, Action: Allow})},
		{name: "allowed addresses", policy: NewPolicy(Deny, Rule{ID: "r", Match: MatchExact, Names: []string{"a"}, Action: Allow, AllowedIPs: []string{"10.0.0.0/8"}}, Rule{ID: "s", Action: Deny, Reason: "default"})},
		{name: "reason", policy: NewPolicy(Deny, Rule{ID: "r", Match: MatchExact, Names: []string{"a"}, Action: Allow}, Rule{ID: "s", Action: Deny, Reason: "changed"})},
	}
	baseHash := PolicyHash(base)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := PolicyHash(test.policy); got == baseHash {
				t.Fatalf("changing %s produced the same hash %q", test.name, got)
			}
		})
	}
}

func TestPolicyHashIgnoresAnOverriddenGeneration(t *testing.T) {
	base := NewPolicy(Deny, Rule{ID: "r", Action: Allow})
	revisioned := base.WithGeneration(base.Generation() + 7)
	if got, want := PolicyHash(revisioned), PolicyHash(base); got != want {
		t.Fatalf("an explicit generation changed the policy hash: %q vs %q", got, want)
	}
}
