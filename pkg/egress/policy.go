package egress

import (
	"encoding/binary"
	"fmt"
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

// Enforcement selects how a matched allowance is applied.
type Enforcement string

const (
	// EnforcementEnforce applies the allowance to the connection. It is the
	// zero value so an entry that does not state one keeps the previous
	// behavior.
	EnforcementEnforce Enforcement = "enforce"
	// EnforcementAudit records the match and allows the request. It is an
	// observation mode for one declared allowance, not a second decision.
	EnforcementAudit Enforcement = "audit"
)

// ParseEnforcement validates a declared per-entry enforcement value. An empty
// value means enforcement: the global behavior, which the declaration may only
// narrow by asking for audit.
func ParseEnforcement(value string) (Enforcement, error) {
	switch normalized := Enforcement(value); normalized {
	case "", EnforcementEnforce:
		return EnforcementEnforce, nil
	case EnforcementAudit:
		return EnforcementAudit, nil
	default:
		return "", fmt.Errorf("enforcement must be %q or %q", EnforcementEnforce, EnforcementAudit)
	}
}

func normalizeEnforcement(value Enforcement) Enforcement {
	normalized, err := ParseEnforcement(string(value))
	if err != nil {
		return EnforcementEnforce
	}
	return normalized
}

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
	// AllowedIPs, on an allow rule, constrains the addresses a matched name may
	// resolve to. The rule allows the request only when every resolved address
	// falls inside one of these canonical CIDRs, so a name the declaration
	// trusts cannot be pointed at an address it does not. An empty list leaves
	// the allowance unconstrained, which is a different declaration from a list
	// that happens to allow everything.
	AllowedIPs []string
	// Binaries scopes the rule to processes whose executable is one of these
	// canonical absolute paths. An allow rule with names matches only a
	// declared binary; an empty request binary never satisfies it. A deny rule
	// with binaries refuses an unknown binary as well, so omitting the
	// attribution cannot bypass a binary-scoped refusal.
	Binaries []string
	// Enforcement selects how a matched allow rule is applied. It is enforced
	// by default; audit records the match and allows the request.
	Enforcement Enforcement
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
		copied[i].AllowedIPs = append([]string(nil), rule.AllowedIPs...)
		copied[i].Binaries = append([]string(nil), rule.Binaries...)
		copied[i].Enforcement = normalizeEnforcement(rule.Enforcement)
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
		copied[i].AllowedIPs = append([]string(nil), rule.AllowedIPs...)
		copied[i].Binaries = append([]string(nil), rule.Binaries...)
	}
	return copied
}

// hasBinaryConstraints reports whether any rule scopes its outcome to an
// executable. A policy with such a rule cannot decide an anonymous connection
// by its default alone: an unknown binary is denied instead, so a permissive
// default cannot silently stand in for an attribution the policy requires.
func (p Policy) hasBinaryConstraints() bool {
	for _, rule := range p.rules {
		if len(rule.Binaries) > 0 {
			return true
		}
	}
	return false
}

func (r Rule) matches(request Request) bool {
	if !r.matchesBinary(request.Binary) {
		return false
	}
	if r.Match == MatchEndpoint {
		return r.matchesEndpoint(request)
	}
	name := request.Name
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

// matchesBinary reports whether the rule's declared executable scope covers the
// request. A rule that declares no binaries is unrestricted. A binary-scoped
// allow requires a matching binary, so an unknown binary never satisfies it. A
// binary-scoped deny treats an unknown binary as in scope, because failing to
// attribute the connection must not let it bypass the refusal.
func (r Rule) matchesBinary(binary string) bool {
	if len(r.Binaries) == 0 {
		return true
	}
	if r.Action == Deny && binary == "" {
		return true
	}
	return BinaryMatches(binary, r.Binaries)
}

// matchesEndpoint matches a canonical endpoint name against the rule's
// patterns. A rule with no names is the same catch-all as in MatchExact. An
// unparsable request name matches no pattern, so it falls through to the policy
// default instead of being allowed by accident; an unparsable rule pattern is
// skipped deliberately, so a pattern the engine cannot interpret never matches.
func (r Rule) matchesEndpoint(request Request) bool {
	if len(r.Names) == 0 {
		return true
	}
	target, err := ParseEndpoint(request.Name)
	if err != nil {
		return false
	}
	for _, candidate := range r.Names {
		pattern, err := parseEndpointPattern(candidate)
		if err != nil {
			continue
		}
		if pattern.matches(target, request.ResolvedIPs, r.Action) {
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
		// The address constraint is part of the rule: tightening or widening it
		// changes what the policy permits, so it must change the generation that
		// decision records carry and staleness checks compare.
		for _, address := range rule.AllowedIPs {
			writeField(h, address)
		}
		writeField(h, "\x00")
		// The remaining fields are written only when they carry a non-default
		// value, so a rule that existed before binary scoping and per-entry
		// audit keeps the same generation after an upgrade. A generation is
		// recorded with a running sandbox, and changing it for an unchanged
		// declaration would make that record look stale.
		if enforcement := normalizeEnforcement(rule.Enforcement); enforcement != EnforcementEnforce {
			writeField(h, "enforcement")
			writeField(h, string(enforcement))
		}
		if len(rule.Binaries) > 0 {
			// Binary scoping changes which requests a rule can decide, so it
			// must change the generation for the same reason.
			writeField(h, "binaries")
			for _, binary := range rule.Binaries {
				writeField(h, binary)
			}
			writeField(h, "\x00")
		}
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
