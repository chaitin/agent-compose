package projects

import (
	"testing"

	domain "agent-compose/pkg/model"
)

func TestLegacyDefaultNormalizedProjectPreservesAgentConfiguration(t *testing.T) {
	agents := []domain.AgentDefinition{
		{
			ID:           "agent-b",
			Name:         "worker-b",
			Enabled:      false,
			Provider:     "claude",
			Driver:       "docker",
			GuestImage:   "guest:b",
			ConfigJSON:   `{"jupyter":{"enabled":true,"guest_port":9999}}`,
			EnvItems:     []domain.SandboxEnvVar{{Name: "TOKEN", Value: "secret", Secret: true}},
			Volumes:      []domain.VolumeMountSpec{{Type: "bind", Source: "/host", Target: "/guest", ReadOnly: true}},
			CapsetIDs:    []string{"tools"},
			Skills:       []domain.AgentSkill{{Name: "review", Source: "local", Path: "skills/review"}},
			SystemPrompt: "review carefully",
		},
		{ID: "agent-a", Name: "worker-a-Z", Enabled: true, Provider: "codex", ConfigJSON: "{}"},
	}

	project, err := legacyDefaultNormalizedProject(agents)
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProject returned error: %v", err)
	}
	if project.Spec.Name != LegacyDefaultProjectName || len(project.Spec.Agents) != 2 || project.Spec.Agents[0].Name != "worker-a-z" {
		t.Fatalf("project = %#v", project.Spec)
	}
	worker := project.Spec.Agents[1]
	if worker.Name != "worker-b" || worker.Status != "disabled" || worker.Provider != "claude" || worker.Driver.Name != "docker" || worker.Image != "guest:b" {
		t.Fatalf("worker identity/runtime = %#v", worker)
	}
	if !worker.Env["TOKEN"].Secret || worker.Env["TOKEN"].Value != "secret" || worker.Jupyter == nil || !worker.Jupyter.Enabled || worker.Jupyter.GuestPort != 9999 {
		t.Fatalf("worker env/jupyter = %#v", worker)
	}
	if len(worker.Volumes) != 1 || !worker.Volumes[0].ReadOnly || len(worker.Skills) != 1 || worker.SystemPrompt != "review carefully" {
		t.Fatalf("worker configuration = %#v", worker)
	}

	reversed, err := legacyDefaultNormalizedProject([]domain.AgentDefinition{agents[1], agents[0]})
	if err != nil {
		t.Fatalf("reversed project returned error: %v", err)
	}
	if reversed.SpecHash != project.SpecHash {
		t.Fatalf("hash depends on database ordering: %s != %s", reversed.SpecHash, project.SpecHash)
	}
}

func TestLegacyDefaultNormalizedProjectRejectsLossyMappings(t *testing.T) {
	_, err := legacyDefaultNormalizedProject([]domain.AgentDefinition{{ID: "agent-1", Name: "worker", Enabled: true, Provider: "codex", WorkspaceID: "workspace-1"}})
	if err == nil {
		t.Fatal("expected workspace preset compatibility error")
	}

	_, err = legacyDefaultNormalizedProject([]domain.AgentDefinition{{ID: "agent-1", Name: "worker", Enabled: true}, {ID: "agent-2", Name: "worker", Enabled: true}})
	if err == nil {
		t.Fatal("expected duplicate agent name error")
	}
}
