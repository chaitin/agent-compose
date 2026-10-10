package llms

import (
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// reasonModelNotAuthorized is the diagnostic reason a facade token's policy
// records when it refuses a requested model. The transport keeps its own,
// unchanged error message; this reason exists for decision records.
const reasonModelNotAuthorized = "model is not authorized by the facade token"

// FacadeEgressPolicy returns the egress policy a facade token grants: which
// upstream model a request through that token may reach, and which literal
// upstream model it resolves to.
//
// The rules mirror FacadeToken.ResolveUpstreamModel in the same order:
//
//  1. a request for the guest model the token recorded resolves to the literal
//     upstream model, and is denied when the token names no model;
//  2. a token with no connection pins one model, so only that model is
//     authorized;
//  3. a token with a connection authorizes the connection, so any model is
//     forwarded verbatim and the upstream decides;
//  4. a token with neither a connection nor a model forwards verbatim, and the
//     proxy then rejects it for having no connection.
//
// Rule 3 is intentionally permissive: the daemon is a weak caller that holds an
// address and a credential and never parses a model reference, so narrowing it
// would reject models the upstream serves. See FacadeToken.ResolveUpstreamModel
// before changing any branch.
func FacadeEgressPolicy(token FacadeToken) egress.Policy {
	pinned := strings.TrimSpace(token.Model)
	rules := make([]egress.Rule, 0, 4)
	if guestModel := strings.TrimSpace(token.GuestModel); guestModel != "" {
		action := egress.Deny
		if pinned != "" {
			action = egress.Allow
		}
		rules = append(rules, egress.Rule{
			ID:     "llm.guest-model",
			Names:  []string{guestModel},
			Action: action,
			Target: pinned,
			Reason: reasonModelNotAuthorized,
		})
	}
	if strings.TrimSpace(token.ProviderID) == "" && pinned != "" {
		// A token with no connection pins one model: the pinned name is
		// authorized and every other name is refused, still reporting the pin.
		rules = append(rules,
			egress.Rule{ID: "llm.pinned-model", Names: []string{pinned}, Action: egress.Allow, Target: pinned},
			egress.Rule{ID: "llm.pinned-model-mismatch", Action: egress.Deny, Target: pinned, Reason: reasonModelNotAuthorized},
		)
	}
	switch {
	case strings.TrimSpace(token.ProviderID) != "":
		rules = append(rules, egress.Rule{ID: "llm.connection-bound", Action: egress.Allow})
	case pinned == "":
		rules = append(rules, egress.Rule{ID: "llm.unbound-forward", Action: egress.Allow})
	}
	return egress.NewPolicy(egress.Deny, rules...)
}

// FacadeEgressRequest describes one facade call as an egress request. The
// consumer is the sandbox the token was issued for.
func FacadeEgressRequest(token FacadeToken, requested string) egress.Request {
	return egress.Request{
		Consumer: strings.TrimSpace(token.SandboxID),
		Kind:     egress.KindLLMModel,
		Name:     strings.TrimSpace(requested),
	}
}
