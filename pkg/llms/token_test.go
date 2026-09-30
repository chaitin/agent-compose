package llms

import "testing"

// TestFacadeTokenResolveUpstreamModel pins the asymmetry between a token that
// records a model and one that does not.
//
// A token that records a model — every token the daemon mints for a run it
// configures itself — is pinned to that model. A token that records none — only
// the startup compatibility facade mints those — forwards verbatim, because the
// image it serves decides which model to ask for and the daemon cannot predict
// it. Widening the pinned case or narrowing the unbound one breaks a guarantee
// the managed path depends on; see the doc comment on ResolveUpstreamModel.
func TestFacadeTokenResolveUpstreamModel(t *testing.T) {
	tests := []struct {
		name      string
		token     FacadeToken
		requested string
		wantModel string
		wantOK    bool
	}{
		{
			name:      "guest model maps to the literal upstream model",
			token:     FacadeToken{ProviderID: "baizhi", Model: "baizhi/deepseek-v4", GuestModel: "agent-compose/baizhi/deepseek-v4"},
			requested: "agent-compose/baizhi/deepseek-v4",
			wantModel: "baizhi/deepseek-v4",
			wantOK:    true,
		},
		{
			name:      "connection-bound token rejects a model it does not name",
			token:     FacadeToken{ProviderID: "baizhi", Model: "baizhi/deepseek-v4", GuestModel: "agent-compose/baizhi/deepseek-v4"},
			requested: "baizhi/other-model",
			wantModel: "",
			wantOK:    false,
		},
		{
			name:      "connection-bound token without a guest model forwards verbatim",
			token:     FacadeToken{ProviderID: "baizhi", Model: "baizhi/deepseek-v4"},
			requested: "baizhi/deepseek-v4",
			wantModel: "baizhi/deepseek-v4",
			wantOK:    true,
		},
		{
			name:      "request model is matched after trimming",
			token:     FacadeToken{ProviderID: "baizhi", Model: "baizhi/deepseek-v4", GuestModel: "agent-compose/baizhi/deepseek-v4"},
			requested: "  agent-compose/baizhi/deepseek-v4  ",
			wantModel: "baizhi/deepseek-v4",
			wantOK:    true,
		},
		{
			name:      "legacy pinned token accepts its pinned model",
			token:     FacadeToken{Model: "gpt"},
			requested: "gpt",
			wantModel: "gpt",
			wantOK:    true,
		},
		{
			name:      "legacy pinned token rejects another model",
			token:     FacadeToken{Model: "gpt"},
			requested: "other",
			wantModel: "",
			wantOK:    false,
		},
		{
			name:      "token without a model forwards verbatim",
			token:     FacadeToken{ProviderID: "baizhi"},
			requested: "anything",
			wantModel: "anything",
			wantOK:    true,
		},
		{
			// Forwarding here is not a capability: the proxy rejects a token
			// with no connection immediately after this call.
			name:      "token without a model or a connection forwards verbatim",
			token:     FacadeToken{},
			requested: "anything",
			wantModel: "anything",
			wantOK:    true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			gotModel, gotOK := tc.token.ResolveUpstreamModel(tc.requested)
			if gotModel != tc.wantModel || gotOK != tc.wantOK {
				t.Fatalf("ResolveUpstreamModel(%q) = (%q, %t), want (%q, %t)", tc.requested, gotModel, gotOK, tc.wantModel, tc.wantOK)
			}
		})
	}
}
