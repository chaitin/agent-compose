package egress

import (
	"fmt"
	"strings"
)

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
	// A rule whose allowance is constrained by address can match the name and
	// still not permit the request. Evaluation continues past it, so a later
	// rule may still allow the destination, and the first such refusal is kept
	// so the caller learns which declared allowance the destination fell
	// outside instead of only that the default denied it.
	var constrained *Result
	for _, rule := range policy.rules {
		if !rule.matches(request) {
			continue
		}
		if rule.Action == Allow {
			if refusal, allowed := rule.allowsAddresses(request); !allowed {
				// An address constraint narrows one declared allowance. Under a
				// policy that already allows anything no rule denies, that
				// allowance is not what authorizes the request: the default does,
				// so the constraint must not turn it into a denial. Anything the
				// declaration does not allow is still denied by the rules that do
				// match, and an explicit deny still wins below.
				if policy.defaultAction == Allow {
					continue
				}
				if constrained == nil {
					constrained = &refusal
				}
				continue
			}
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
	if constrained != nil {
		constrained.Generation = policy.generation
		return *constrained
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

// allowsAddresses reports whether a matched allow rule also permits the
// addresses the request resolved to. A rule that declares no allowed addresses
// constrains nothing. A rule that does requires every resolved address to fall
// inside one of them: the connection is opened to one of those addresses, and a
// name that answers with an address outside the declaration must not be
// reachable merely because another answer was inside it.
func (r Rule) allowsAddresses(request Request) (Result, bool) {
	if len(r.AllowedIPs) == 0 {
		return Result{}, true
	}
	refusal := Result{
		Action: Deny,
		RuleID: r.ID,
		Target: request.Name,
	}
	if len(request.ResolvedIPs) == 0 {
		refusal.Reason = fmt.Sprintf("the rule allows only %s, and the destination address was not resolved", strings.Join(r.AllowedIPs, ", "))
		return refusal, false
	}
	for _, address := range request.ResolvedIPs {
		if !AddressAllowed(address, r.AllowedIPs) {
			refusal.Reason = fmt.Sprintf("the destination resolves to %s, which is outside the allowed addresses %s", address, strings.Join(r.AllowedIPs, ", "))
			return refusal, false
		}
	}
	return Result{}, true
}
