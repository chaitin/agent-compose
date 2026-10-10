package llms

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/egress"
)

// TestFacadeEgressPolicyGolden is the contract table for routing the runtime LLM
// facade decision through the shared egress entry point. Every case pins what
// the pre-refactor FacadeToken.ResolveUpstreamModel returned, and the test
// asserts both that the shared decision reproduces it and that the token
// accessor delegates to the same result rather than drifting.
func TestFacadeEgressPolicyGolden(t *testing.T) {
	tests := []struct {
		name        string
		token       FacadeToken
		requested   string
		wantModel   string
		wantAllowed bool
	}{
		{
			name:        "guest model maps to the literal upstream model",
			token:       FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi", Model: "baizhi/deepseek-v4", GuestModel: "agent-compose/baizhi/deepseek-v4"},
			requested:   "agent-compose/baizhi/deepseek-v4",
			wantModel:   "baizhi/deepseek-v4",
			wantAllowed: true,
		},
		{
			name:        "connection-bound token forwards a model it does not name",
			token:       FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi", Model: "baizhi/deepseek-v4", GuestModel: "agent-compose/baizhi/deepseek-v4"},
			requested:   "baizhi/other-model",
			wantModel:   "baizhi/other-model",
			wantAllowed: true,
		},
		{
			name:        "connection-bound token without a guest model forwards verbatim",
			token:       FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi", Model: "baizhi/deepseek-v4"},
			requested:   "baizhi/deepseek-v4",
			wantModel:   "baizhi/deepseek-v4",
			wantAllowed: true,
		},
		{
			name:        "request model is matched after trimming",
			token:       FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi", Model: "baizhi/deepseek-v4", GuestModel: "agent-compose/baizhi/deepseek-v4"},
			requested:   "  agent-compose/baizhi/deepseek-v4  ",
			wantModel:   "baizhi/deepseek-v4",
			wantAllowed: true,
		},
		{
			name:        "legacy pinned token accepts its pinned model",
			token:       FacadeToken{SandboxID: "sandbox-1", Model: "gpt"},
			requested:   "gpt",
			wantModel:   "gpt",
			wantAllowed: true,
		},
		{
			name:        "a token with no connection rejects another model, returning its pin",
			token:       FacadeToken{SandboxID: "sandbox-1", Model: "gpt"},
			requested:   "other",
			wantModel:   "gpt",
			wantAllowed: false,
		},
		{
			name:        "token without a model forwards verbatim",
			token:       FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi"},
			requested:   "anything",
			wantModel:   "anything",
			wantAllowed: true,
		},
		{
			name:        "token without a model or a connection forwards verbatim",
			token:       FacadeToken{SandboxID: "sandbox-1"},
			requested:   "anything",
			wantModel:   "anything",
			wantAllowed: true,
		},
		{
			name:        "guest model recorded without a literal model is refused",
			token:       FacadeToken{SandboxID: "sandbox-1", GuestModel: "agent-compose/gpt"},
			requested:   "agent-compose/gpt",
			wantModel:   "",
			wantAllowed: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			policy := FacadeEgressPolicy(tc.token)
			request := FacadeEgressRequest(tc.token, tc.requested)
			result := egress.Decide(policy, request)

			if result.Target != tc.wantModel || result.Allowed() != tc.wantAllowed {
				t.Fatalf("Decide() = %+v, want model=%q allowed=%t", result, tc.wantModel, tc.wantAllowed)
			}
			if result.Generation != policy.Generation() {
				t.Fatalf("decision generation = %d, want %d", result.Generation, policy.Generation())
			}
			if request.Kind != egress.KindLLMModel || request.Consumer != tc.token.SandboxID {
				t.Fatalf("request = %+v, want kind %q and consumer %q", request, egress.KindLLMModel, tc.token.SandboxID)
			}
			if result.Allowed() && result.RuleID == "" {
				t.Fatalf("allow decision %+v names no rule", result)
			}

			gotModel, gotAllowed := tc.token.ResolveUpstreamModel(tc.requested)
			if gotModel != result.Target || gotAllowed != result.Allowed() {
				t.Fatalf("ResolveUpstreamModel() = (%q, %t), decision = (%q, %t)", gotModel, gotAllowed, result.Target, result.Allowed())
			}
		})
	}
}

// TestFacadeEgressPolicyGenerationTracksTokenContent pins the generation
// contract for the facade: rebuilding the policy from the same token yields the
// same generation, a changed grant yields a new one, and a decision against the
// old grant is recognized as stale against the new one.
func TestFacadeEgressPolicyGenerationTracksTokenContent(t *testing.T) {
	token := FacadeToken{SandboxID: "sandbox-1", Model: "gpt"}
	policy := FacadeEgressPolicy(token)
	if rebuilt := FacadeEgressPolicy(token); rebuilt.Generation() != policy.Generation() {
		t.Fatalf("rebuilding the same token changed the generation: %d vs %d", rebuilt.Generation(), policy.Generation())
	}

	result := egress.Decide(policy, FacadeEgressRequest(token, "gpt"))
	if result.IsStale(policy) {
		t.Fatal("decision is stale against the policy it was evaluated against")
	}

	changed := FacadeEgressPolicy(FacadeToken{SandboxID: "sandbox-1", Model: "other-model"})
	if !result.IsStale(changed) {
		t.Fatal("decision was not recognized as stale after the token's pinned model changed")
	}

	// A connection-bound token authorizes the connection, not one model on it,
	// so changing its recorded model must not change the policy generation.
	bound := FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi", Model: "baizhi/deepseek-v4"}
	rebound := FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi", Model: "baizhi/other-model"}
	if FacadeEgressPolicy(bound).Generation() != FacadeEgressPolicy(rebound).Generation() {
		t.Fatal("a connection-bound token's recorded model changed the policy generation")
	}
}

// TestFacadeEgressPolicyBlankProviderIDIsUnbound pins the one interpretation of
// "this token names no connection". NewFacadeToken trims ProviderID, but a blank
// value read back from storage must still decide exactly like an empty one, and
// the same predicate the policy uses must answer for the proxy's follow-up
// check, so the two cannot disagree about whether the token is bound.
func TestFacadeEgressPolicyBlankProviderIDIsUnbound(t *testing.T) {
	blank := FacadeToken{SandboxID: "sandbox-1", ProviderID: "   ", Model: "gpt"}
	empty := FacadeToken{SandboxID: "sandbox-1", Model: "gpt"}
	if blank.HasConnection() {
		t.Fatal("a whitespace-only provider ID counted as a connection")
	}

	for _, requested := range []string{"gpt", "other"} {
		got := egress.Decide(FacadeEgressPolicy(blank), FacadeEgressRequest(blank, requested))
		want := egress.Decide(FacadeEgressPolicy(empty), FacadeEgressRequest(empty, requested))
		if got != want {
			t.Fatalf("requested %q: blank provider ID decided %+v, empty provider ID decided %+v", requested, got, want)
		}
	}
	if got := egress.Decide(FacadeEgressPolicy(blank), FacadeEgressRequest(blank, "other")); got.Allowed() {
		t.Fatalf("a blank provider ID authorized an unpinned model: %+v", got)
	}

	// A blank provider ID with no pinned model still forwards at the policy
	// layer; the proxy is what refuses it, and it must refuse it through the
	// same predicate.
	blankUnpinned := FacadeToken{SandboxID: "sandbox-1", ProviderID: "\t"}
	if blankUnpinned.HasConnection() {
		t.Fatal("a tab-only provider ID counted as a connection")
	}
	if got := egress.Decide(FacadeEgressPolicy(blankUnpinned), FacadeEgressRequest(blankUnpinned, "anything")); !got.Allowed() {
		t.Fatalf("unpinned blank-provider token policy = %+v, want the verbatim forward the proxy then refuses", got)
	}
}
