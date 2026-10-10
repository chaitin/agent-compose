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
			if result.Allowed() && result.Reason != "" {
				t.Fatalf("allow decision %+v carries deny reason %q", result, result.Reason)
			}

			gotModel, gotAllowed := tc.token.ResolveUpstreamModel(tc.requested)
			if gotModel != result.Target || gotAllowed != result.Allowed() {
				t.Fatalf("ResolveUpstreamModel() = (%q, %t), decision = (%q, %t)", gotModel, gotAllowed, result.Target, result.Allowed())
			}
		})
	}
}

// TestFacadeEgressPolicyAllowCarriesNoDenyReason pins the Result.Reason
// contract directly: a reason explains a deny, so an allow must never carry one.
// The guest-model rule once set the not-authorized reason on its allow branch,
// which made a permitted request look unauthorized in the decision record.
func TestFacadeEgressPolicyAllowCarriesNoDenyReason(t *testing.T) {
	allowed := FacadeToken{SandboxID: "sandbox-1", ProviderID: "baizhi", Model: "baizhi/deepseek-v4", GuestModel: "agent-compose/baizhi/deepseek-v4"}
	result := egress.Decide(FacadeEgressPolicy(allowed), FacadeEgressRequest(allowed, "agent-compose/baizhi/deepseek-v4"))
	if !result.Allowed() {
		t.Fatalf("decision = %+v, want allow", result)
	}
	if result.Reason != "" {
		t.Fatalf("allow decision carries reason %q, want empty", result.Reason)
	}

	refused := FacadeToken{SandboxID: "sandbox-1", GuestModel: "agent-compose/gpt"}
	denied := egress.Decide(FacadeEgressPolicy(refused), FacadeEgressRequest(refused, "agent-compose/gpt"))
	if denied.Allowed() {
		t.Fatalf("decision = %+v, want deny", denied)
	}
	if denied.Reason != reasonModelNotAuthorized {
		t.Fatalf("deny reason = %q, want %q", denied.Reason, reasonModelNotAuthorized)
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
