package egress

import "testing"

func TestNewPolicyGenerationIsContentDerived(t *testing.T) {
	base := NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Allow, Target: "dev"})
	rebuilt := NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Allow, Target: "dev"})
	if base.Generation() != rebuilt.Generation() {
		t.Fatalf("identical policies produced different generations: %d vs %d", base.Generation(), rebuilt.Generation())
	}

	changes := map[string]Policy{
		"changed rule id":    NewPolicy(Deny, Rule{ID: "other", Names: []string{"dev"}, Action: Allow, Target: "dev"}),
		"changed action":     NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Deny, Target: "dev"}),
		"changed name":       NewPolicy(Deny, Rule{ID: "r", Names: []string{"staging"}, Action: Allow, Target: "dev"}),
		"changed target":     NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Allow, Target: "dev-v1"}),
		"changed reason":     NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Allow, Target: "dev", Reason: "x"}),
		"changed default":    NewPolicy(Allow, Rule{ID: "r", Names: []string{"dev"}, Action: Allow, Target: "dev"}),
		"added rule":         NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Allow, Target: "dev"}, Rule{ID: "s", Action: Deny}),
		"no rules":           NewPolicy(Deny),
		"rule action unset":  NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Action(""), Target: "dev"}),
		"reordered rule ids": NewPolicy(Deny, Rule{ID: "s", Action: Deny}, Rule{ID: "r", Names: []string{"dev"}, Action: Allow, Target: "dev"}),
	}
	for name, policy := range changes {
		if policy.Generation() == base.Generation() {
			t.Fatalf("%s produced the same generation %d as the base policy", name, policy.Generation())
		}
	}

	// The hash must separate distinct name lists that concatenate to the same
	// bytes, which is why fields are length-prefixed.
	first := NewPolicy(Deny, Rule{ID: "r", Names: []string{"ab", "c"}, Action: Allow})
	second := NewPolicy(Deny, Rule{ID: "r", Names: []string{"a", "bc"}, Action: Allow})
	if first.Generation() == second.Generation() {
		t.Fatal("distinct name lists produced the same generation")
	}
}

func TestNewPolicyCopiesCallerRules(t *testing.T) {
	names := []string{"dev"}
	rules := []Rule{{ID: "r", Names: names, Action: Allow, Target: "dev"}}
	policy := NewPolicy(Deny, rules...)

	names[0] = "mutated"
	rules[0].Action = Deny
	decision := Decide(policy, Request{Kind: KindCapabilityCapset, Name: "dev"})
	if !decision.Allowed() || decision.Target != "dev" {
		t.Fatalf("policy changed after the caller mutated its inputs: %+v", decision)
	}
	if got := Decide(policy, Request{Kind: KindCapabilityCapset, Name: "mutated"}); got.Allowed() {
		t.Fatalf("policy matched a name the caller injected after construction: %+v", got)
	}
}

func TestRulesReturnsCopy(t *testing.T) {
	policy := NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Allow})
	returned := policy.Rules()
	returned[0].Names[0] = "mutated"
	returned[0].Action = Deny
	if got := Decide(policy, Request{Kind: KindCapabilityCapset, Name: "dev"}); !got.Allowed() {
		t.Fatalf("mutating the returned rules changed the policy: %+v", got)
	}
}

func TestWithGenerationOverridesContentGeneration(t *testing.T) {
	policy := NewPolicy(Deny, Rule{ID: "r", Action: Allow}).WithGeneration(7)
	if policy.Generation() != 7 {
		t.Fatalf("generation = %d, want 7", policy.Generation())
	}
	if got := Decide(policy, Request{Kind: KindLLMModel, Name: "m"}); got.Generation != 7 {
		t.Fatalf("decision generation = %d, want 7", got.Generation)
	}
}

// TestDecisionStaleness covers the re-validation a consumer performs before it
// acts on a decision: when the policy snapshot it is about to apply differs from
// the one the decision was evaluated against, the decision is stale.
func TestDecisionStaleness(t *testing.T) {
	original := NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev"}, Action: Allow})
	result := Decide(original, Request{Kind: KindCapabilityCapset, Name: "dev"})
	if result.IsStale(original) {
		t.Fatal("a decision is stale against the policy it was evaluated against")
	}

	changed := NewPolicy(Deny, Rule{ID: "r", Names: []string{"dev", "staging"}, Action: Allow})
	if !result.IsStale(changed) {
		t.Fatal("a decision evaluated against an older policy was not recognized as stale")
	}

	record := NewRecord(Request{Kind: KindCapabilityCapset, Name: "dev"}, result)
	if record.IsStale(original) {
		t.Fatal("record is stale against the policy it was evaluated against")
	}
	if !record.IsStale(changed) {
		t.Fatal("record evaluated against an older policy was not recognized as stale")
	}
	if !record.Allowed() {
		t.Fatalf("record.Allowed() = false, want the allow decision to be preserved")
	}

	revisioned := original.WithGeneration(original.Generation() + 1)
	if !result.IsStale(revisioned) {
		t.Fatal("a revision bump on an identical policy was not recognized as stale")
	}
}
