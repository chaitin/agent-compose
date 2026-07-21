package runs

import (
	"context"
	"testing"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestPrepareProjectRunKeepsGlobalEnvOutOfSandboxProviderOverrides(t *testing.T) {
	store := &fakePreparationStore{
		project: domain.ProjectRecord{ID: "project-1", Name: "project"},
		revision: domain.ProjectRevisionRecord{
			ProjectID: "project-1",
			Revision:  1,
			SpecJSON:  `{"variables":[{"name":"PROJECT_VALUE","value":"project"}],"agents":[{"name":"worker"}]}`,
		},
		agent: domain.AgentDefinition{
			ID:       "agent-1",
			EnvItems: []domain.SandboxEnvVar{{Name: "AGENT_VALUE", Value: "agent"}},
		},
		global: []domain.SandboxEnvVar{
			{Name: "GLOBAL_VALUE", Value: "global"},
			{Name: "LLM_API_KEY", Value: "global-key", Secret: true},
		},
	}
	prepared, err := PrepareProjectRun(context.Background(), store, nil, domain.ProjectRunRecord{
		ProjectID:       "project-1",
		ProjectRevision: 1,
		AgentName:       "worker",
		ManagedAgentID:  "agent-1",
	}, []*agentcomposev2.EnvVarSpec{{Name: "REQUEST_VALUE", Value: "request"}})
	if err != nil {
		t.Fatalf("PrepareProjectRun returned error: %v", err)
	}
	runtimeEnv := domain.SandboxEnvMap(prepared.EnvItems)
	if runtimeEnv["GLOBAL_VALUE"] != "global" || runtimeEnv["LLM_API_KEY"] != "" {
		t.Fatalf("runtime env = %#v", runtimeEnv)
	}
	providerEnv := domain.SandboxEnvMap(prepared.ProviderEnvItems)
	if providerEnv["GLOBAL_VALUE"] != "" || providerEnv["LLM_API_KEY"] != "" {
		t.Fatalf("provider overrides contain Global Env: %#v", providerEnv)
	}
	for name, want := range map[string]string{
		"PROJECT_VALUE": "project",
		"AGENT_VALUE":   "agent",
		"REQUEST_VALUE": "request",
	} {
		if providerEnv[name] != want {
			t.Fatalf("provider env %s = %q, want %q", name, providerEnv[name], want)
		}
	}
}
