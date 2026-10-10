package egress

import (
	"encoding/binary"
	"hash"
	"hash/fnv"
)

// Action is the outcome of an egress decision.
type Action string

const (
	// Allow permits the request.
	Allow Action = "allow"
	// Deny refuses the request.
	Deny Action = "deny"
)

// Generation identifies one policy snapshot. A decision records the generation
// it was evaluated against, so a consumer holding a newer policy can tell that
// the decision no longer applies.
type Generation uint64

// MatchMode selects how a rule interprets its Names. It stays inside the single
// policy model on purpose: a declared network allowance is an ordinary rule,
// not a second policy representation that could drift from this one.
type MatchMode string

const (
	// MatchExact compares each name with the request name literally. It is the
	// zero value, so every rule that does not opt into endpoint matching keeps
	// its existing behavior.
	MatchExact MatchMode = "exact"
	// MatchEndpoint treats each name as an endpoint pattern
	// "host:port/protocol" as produced by FormatEndpointPattern. The host part
	// is evaluated label by label, where "*" matches exactly one label.
	MatchEndpoint MatchMode = "endpoint"
)

// Rule is one entry in a policy. Rules are evaluated in order and the first one
// whose Names contain the requested resource decides the request; a rule with
// no Names matches any name.
type Rule struct {
	// ID identifies the rule in decision records. It must stay stable across
	// policy rebuilds so a record can be traced back to the rule that produced
	// it.
	ID string
	// Match selects how Names are interpreted. The zero value is MatchExact.
	Match MatchMode
	// Names are the resource names this rule matches, interpreted according to
	// Match. Empty matches any name.
	Names []string
	// Action is the outcome when this rule matches. Any value other than Allow
	// is normalized to Deny, so an unset action fails closed.
	Action Action
	// Target is the canonical upstream resource an allowed request resolves to.
	// Empty means "resolve to the requested name unchanged". A matched deny may
	// also set Target to report the resource it would have resolved to.
	Target string
	// Reason is a short, stable explanation of a deny. Consumers map it onto
	// their own transport error; it never carries secrets.
	Reason string
}

// Policy is one immutable snapshot of the rules governing a consumer's egress.
// Build it with NewPolicy, which copies the rules and derives the generation.
type Policy struct {
	defaultAction Action
	rules         []Rule
	generation    Generation
}

// NewPolicy returns a policy that denies any request no rule matches unless
// defaultAction is Allow. The generation is derived from the policy content, so
// rebuilding the same rules yields the same generation and any change to a
// rule, the rule order, or the default action yields a new one.
func NewPolicy(defaultAction Action, rules ...Rule) Policy {
	copied := make([]Rule, len(rules))
	for i, rule := range rules {
		copied[i] = rule
		copied[i].Action = normalizeAction(rule.Action)
		copied[i].Match = normalizeMatch(rule.Match)
		copied[i].Names = append([]string(nil), rule.Names...)
	}
	policy := Policy{defaultAction: normalizeAction(defaultAction), rules: copied}
	policy.generation = policy.contentGeneration()
	return policy
}

// WithGeneration returns a copy of the policy stamped with a revision the
// caller owns, for a policy store that counts revisions instead of deriving
// them from content. The zero generation marks an unversioned policy.
func (p Policy) WithGeneration(generation Generation) Policy {
	p.generation = generation
	return p
}

// Default reports the action applied when no rule matches.
func (p Policy) Default() Action {
	return p.defaultAction
}

// Generation reports the snapshot identifier decisions against this policy
// carry.
func (p Policy) Generation() Generation {
	return p.generation
}

// Rules returns a copy of the policy's rules in evaluation order.
func (p Policy) Rules() []Rule {
	copied := make([]Rule, len(p.rules))
	for i, rule := range p.rules {
		copied[i] = rule
		copied[i].Names = append([]string(nil), rule.Names...)
	}
	return copied
}

func (r Rule) matches(name string) bool {
	if r.Match == MatchEndpoint {
		return r.matchesEndpoint(name)
	}
	if len(r.Names) == 0 {
		return true
	}
	for _, candidate := range r.Names {
		if candidate == name {
			return true
		}
	}
	return false
}

// matchesEndpoint matches a canonical endpoint name against the rule's
// patterns. An unparsable request name matches nothing, so a malformed request
// falls through to the policy default instead of being allowed by accident.
func (r Rule) matchesEndpoint(name string) bool {
	if len(r.Names) == 0 {
		return true
	}
	target, err := ParseEndpoint(name)
	if err != nil {
		return false
	}
	for _, candidate := range r.Names {
		pattern, err := parseEndpointPattern(candidate)
		if err != nil {
			continue
		}
		if pattern.matches(target) {
			return true
		}
	}
	return false
}

func normalizeAction(action Action) Action {
	if action == Allow {
		return Allow
	}
	return Deny
}

func normalizeMatch(mode MatchMode) MatchMode {
	if mode == MatchEndpoint {
		return MatchEndpoint
	}
	return MatchExact
}

// contentGeneration hashes every field that can change a decision, with length
// prefixes so that distinct rule shapes cannot hash to the same bytes.
func (p Policy) contentGeneration() Generation {
	h := fnv.New64a()
	writeField(h, string(p.defaultAction))
	for _, rule := range p.rules {
		writeField(h, rule.ID)
		// Match is always written so an endpoint rule and an exact rule with a
		// name that happens to read like a match mode cannot collide.
		writeField(h, string(rule.Match))
		writeField(h, string(rule.Action))
		writeField(h, rule.Target)
		writeField(h, rule.Reason)
		for _, name := range rule.Names {
			writeField(h, name)
		}
		// Terminate the name list so ["ab", "c"] and ["a", "bc"] differ.
		writeField(h, "\x00")
	}
	return Generation(h.Sum64())
}

func writeField(h hash.Hash64, value string) {
	var length [8]byte
	binary.LittleEndian.PutUint64(length[:], uint64(len(value)))
	// hash.Hash never fails a write; the error is always nil.
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}
