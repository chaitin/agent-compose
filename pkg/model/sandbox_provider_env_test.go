package model

import (
	"maps"
	"testing"
)

func TestSandboxDeclaredProviderEnvMergesPreparedAndDefinitionEnv(t *testing.T) {
	tests := []struct {
		name     string
		sandbox  *Sandbox
		agentEnv []SandboxEnvVar
		want     map[string]string
	}{
		{
			name:     "prepared environment alone",
			sandbox:  sandboxWithProviderEnv(SandboxEnvVar{Name: "LLM_API_KEY", Value: "prepared-key"}),
			agentEnv: nil,
			want:     map[string]string{"LLM_API_KEY": "prepared-key"},
		},
		{
			name: "definition environment alone",
			// A sandbox loaded back from storage keeps only provenance names, so
			// the definition's own environment is the declaration that survives.
			sandbox:  &Sandbox{ProviderEnvOverrideNames: []string{"LLM_API_KEY"}},
			agentEnv: []SandboxEnvVar{{Name: "LLM_API_KEY", Value: "definition-key"}},
			want:     map[string]string{"LLM_API_KEY": "definition-key"},
		},
		{
			name: "provenance-less metadata recovers its persisted environment",
			// A sandbox created before provider provenance existed kept the
			// provider environment in EnvItems, and that snapshot is the only
			// record of what it declared.
			sandbox: &Sandbox{EnvItems: []SandboxEnvVar{
				{Name: "LLM_API_KEY", Value: "legacy-key"},
				{Name: "ORDINARY", Value: "ordinary"},
			}},
			agentEnv: nil,
			want:     map[string]string{"LLM_API_KEY": "legacy-key", "ORDINARY": "ordinary"},
		},
		{
			name: "provenance-less metadata never overrides a fresh declaration",
			sandbox: &Sandbox{EnvItems: []SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://legacy.example/v1"},
				{Name: "LLM_API_KEY", Value: "legacy-key"},
			}},
			agentEnv: []SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://definition.example/v1"}},
			want:     map[string]string{"LLM_API_ENDPOINT": "https://definition.example/v1", "LLM_API_KEY": "legacy-key"},
		},
		{
			name: "provenance without values is not recovered",
			// The sandbox already recorded that it declared provider overrides, so
			// its persisted environment is filtered and carries no credentials.
			sandbox:  &Sandbox{EnvItems: []SandboxEnvVar{{Name: "LLM_API_KEY", Value: "filtered-away"}}, ProviderEnvOverrideNames: []string{"LLM_API_KEY"}},
			agentEnv: nil,
			want:     map[string]string{},
		},
		{
			name:     "definition declaration wins over a stale prepared value",
			sandbox:  sandboxWithProviderEnv(SandboxEnvVar{Name: "LLM_API_ENDPOINT", Value: "https://prepared.example/v1"}),
			agentEnv: []SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://definition.example/v1"}},
			want:     map[string]string{"LLM_API_ENDPOINT": "https://definition.example/v1"},
		},
		{
			name: "both sides contribute",
			sandbox: sandboxWithProviderEnv(
				SandboxEnvVar{Name: "LLM_API_ENDPOINT", Value: "https://prepared.example/v1"},
				SandboxEnvVar{Name: "LLM_API_KEY", Value: "prepared-key"},
			),
			agentEnv: []SandboxEnvVar{{Name: "LLM_MODEL", Value: "definition-model"}},
			want: map[string]string{
				"LLM_API_ENDPOINT": "https://prepared.example/v1",
				"LLM_API_KEY":      "prepared-key",
				"LLM_MODEL":        "definition-model",
			},
		},
		{
			name:     "no declaration at all",
			sandbox:  &Sandbox{ProviderEnvItems: []SandboxEnvVar{}, ProviderEnvOverrideNames: []string{}},
			agentEnv: nil,
			want:     map[string]string{},
		},
		{
			name:     "nil sandbox keeps the agent's own declaration",
			sandbox:  nil,
			agentEnv: []SandboxEnvVar{{Name: "LLM_API_KEY", Value: "definition-key"}},
			want:     map[string]string{"LLM_API_KEY": "definition-key"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SandboxEnvMap(test.sandbox.DeclaredProviderEnv(test.agentEnv)); !maps.Equal(got, test.want) {
				t.Fatalf("DeclaredProviderEnv = %#v, want %#v", got, test.want)
			}
		})
	}
}

func sandboxWithProviderEnv(items ...SandboxEnvVar) *Sandbox {
	sandbox := &Sandbox{}
	sandbox.SetProviderEnvItems(items)
	return sandbox
}
