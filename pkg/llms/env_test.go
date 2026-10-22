package llms

import "testing"

func TestSchedulerCommandFacadeAgentModel(t *testing.T) {
	tests := []struct {
		name     string
		env      map[string]string
		provider string
		model    string
	}{
		{
			name:     "dsh reads DSH_MODEL",
			env:      map[string]string{"PROJECT_AGENT_PROVIDER": "dsh", "DSH_MODEL": "deepseek-flash"},
			provider: "dsh",
			model:    "deepseek-flash",
		},
		{
			name:     "dsh falls back to LLM_MODEL",
			env:      map[string]string{"PROJECT_AGENT_PROVIDER": "dsh", "LLM_MODEL": "shared-model"},
			provider: "dsh",
			model:    "shared-model",
		},
		{
			// dsh has no --model flag, so an empty model is not a reason to
			// decline the facade: the daemon resolves its default and writes it
			// into the guest environment the profile reads.
			name:     "dsh without a model keeps the facade",
			env:      map[string]string{"PROJECT_AGENT_PROVIDER": "dsh"},
			provider: "dsh",
			model:    "",
		},
		{
			name:     "dsh alias normalizes",
			env:      map[string]string{"AGENT_PROVIDER": "deepseek-harness", "DSH_MODEL": "m"},
			provider: "dsh",
			model:    "m",
		},
		{
			name:     "opencode without a model declines",
			env:      map[string]string{"AGENT_PROVIDER": "opencode"},
			provider: "",
			model:    "",
		},
		{
			name:     "unknown provider declines",
			env:      map[string]string{"AGENT_PROVIDER": "unknown-agent"},
			provider: "",
			model:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider, model := SchedulerCommandFacadeAgentModel(tt.env)
			if provider != tt.provider || model != tt.model {
				t.Fatalf("provider=%q model=%q, want provider=%q model=%q", provider, model, tt.provider, tt.model)
			}
		})
	}
}
