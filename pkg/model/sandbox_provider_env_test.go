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
