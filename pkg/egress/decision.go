package egress

// Result is the single outcome structure of an egress decision: the action, the
// rule that decided it (empty when the policy default applied), the canonical
// upstream resource, and the generation of the policy it was evaluated against.
// Call sites read this structure instead of assembling their own answer, so
// every consumer reports the same fields.
type Result struct {
	Action Action
	// RuleID names the matched rule; empty means no rule matched and the
	// policy default decided.
	RuleID string
	// Target is the canonical upstream resource. For an allow it is the rule
	// target, or the requested name when the rule names none; for a deny it is
	// populated only when the matched rule recorded one.
	Target string
	// Reason explains a deny. It is empty for an allow.
	Reason string
	// Generation is the policy generation this result was evaluated against.
	Generation Generation
}

// Allowed reports whether the action permits the request.
func (r Result) Allowed() bool {
	return r.Action == Allow
}

// IsStale reports whether current is a different policy snapshot than the one
// this result was evaluated against. A consumer must not act on a stale result:
// the policy may have changed the outcome after the decision was made.
func (r Result) IsStale(current Policy) bool {
	return r.Generation != current.generation
}

// Decide evaluates one request against one policy snapshot. It is pure: the
// only inputs are the policy and the request, and it reads no clock, performs
// no I/O, and touches no package state.
//
// Rules are evaluated in order and the first match decides. A rule with an empty
// Target resolves an allow to the requested name unchanged. When no rule
// matches, the policy default applies; any default other than Allow denies.
func Decide(policy Policy, request Request) Result {
	for _, rule := range policy.rules {
		if !rule.matches(request.Name) {
			continue
		}
		target := rule.Target
		if rule.Action == Allow && target == "" {
			target = request.Name
		}
		return Result{
			Action:     rule.Action,
			RuleID:     rule.ID,
			Target:     target,
			Reason:     rule.Reason,
			Generation: policy.generation,
		}
	}
	result := Result{
		Action:     policy.defaultAction,
		Generation: policy.generation,
	}
	if result.Action == Allow {
		result.Target = request.Name
	}
	return result
}
