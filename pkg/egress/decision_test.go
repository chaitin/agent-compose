package egress

import "testing"

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
	if first != second {
		t.Fatalf("consumer and kind must not change the outcome: %+v vs %+v", first, second)
	}
}
