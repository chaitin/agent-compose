package adapters

import (
	"encoding/json"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestSchedulerEffectiveSandboxConfigRetainsHistoricalHashSchemaKey(t *testing.T) {
	payload, err := json.Marshal(schedulerEffectiveSandboxConfig{SchedulerConfigHash: "sha256:scheduler"})
	if err != nil {
		t.Fatalf("marshal scheduler sandbox config: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatalf("decode scheduler sandbox config: %v", err)
	}
	if fields["loader_config_hash"] != "sha256:scheduler" {
		t.Fatalf("loader_config_hash = %#v, want frozen scheduler hash", fields["loader_config_hash"])
	}
	if _, exists := fields["scheduler_config_hash"]; exists {
		t.Fatalf("scheduler_config_hash must not replace the frozen hash key: %s", payload)
	}
}

func TestSchedulerRequestSandboxConfigHashIgnoresVolumeMountOrder(t *testing.T) {
	mounts := []domain.SandboxVolumeMount{
		{ID: "volume-data", Type: domain.VolumeMountTypeVolume, Source: "data", Target: "/workspace/data", HostPath: "/volumes/data", VolumeID: "volume-1"},
		{ID: "bind-cache", Type: domain.VolumeMountTypeBind, Source: "./cache", Target: "/workspace/cache", HostPath: "/project/cache", ProjectPath: "/project"},
	}
	first, err := schedulerRequestSandboxConfigHash("sha256:scheduler", domain.SchedulerAgentRequest{}, nil, nil, nil, nil, "docker", "guest:v1", mounts)
	if err != nil {
		t.Fatalf("schedulerRequestSandboxConfigHash returned error: %v", err)
	}
	reordered := []domain.SandboxVolumeMount{mounts[1], mounts[0]}
	second, err := schedulerRequestSandboxConfigHash("sha256:scheduler", domain.SchedulerAgentRequest{}, nil, nil, nil, nil, "docker", "guest:v1", reordered)
	if err != nil {
		t.Fatalf("schedulerRequestSandboxConfigHash reordered returned error: %v", err)
	}
	if second != first {
		t.Fatalf("volume mount ordering changed scheduler sandbox hash: got %q want %q", second, first)
	}
}

func TestSchedulerRequestSandboxConfigHashUsesEffectiveAgentConfigInsteadOfProjectRevision(t *testing.T) {
	base := domain.AgentDefinition{
		ID:              "agent-1",
		Name:            "worker",
		Provider:        "codex",
		Model:           "model-1",
		SystemPrompt:    "prompt-1",
		Driver:          "docker",
		GuestImage:      "guest:v1",
		WorkspaceID:     "workspace-1",
		EnvItems:        []domain.SandboxEnvVar{{Name: "A", Value: "1"}},
		Volumes:         []domain.VolumeMountSpec{{Type: domain.VolumeMountTypeVolume, Source: "data", Target: "/data"}},
		ConfigJSON:      `{"jupyter":{"enabled":true}}`,
		CapsetIDs:       []string{"capset-1"},
		Skills:          []domain.AgentSkill{{Name: "skill-1", Provider: "git", URL: "https://example.invalid/repo", Ref: "v1"}},
		ProjectID:       "project-1",
		ProjectRevision: 1,
		AgentName:       "worker",
	}
	baseHash := mustSchedulerRequestSandboxConfigHash(t, &base)
	revisionOnly := base
	revisionOnly.ProjectRevision = 2
	if got := mustSchedulerRequestSandboxConfigHash(t, &revisionOnly); got != baseHash {
		t.Fatalf("unrelated project revision changed effective agent hash: got %q want %q", got, baseHash)
	}

	for name, mutate := range map[string]func(*domain.AgentDefinition){
		"provider":      func(item *domain.AgentDefinition) { item.Provider = "claude" },
		"model":         func(item *domain.AgentDefinition) { item.Model = "model-2" },
		"system prompt": func(item *domain.AgentDefinition) { item.SystemPrompt = "prompt-2" },
		"driver":        func(item *domain.AgentDefinition) { item.Driver = "boxlite" },
		"guest image":   func(item *domain.AgentDefinition) { item.GuestImage = "guest:v2" },
		"workspace":     func(item *domain.AgentDefinition) { item.WorkspaceID = "workspace-2" },
		"environment":   func(item *domain.AgentDefinition) { item.EnvItems = []domain.SandboxEnvVar{{Name: "A", Value: "2"}} },
		"volumes": func(item *domain.AgentDefinition) {
			item.Volumes = []domain.VolumeMountSpec{{Type: domain.VolumeMountTypeVolume, Source: "cache", Target: "/data"}}
		},
		"agent config": func(item *domain.AgentDefinition) { item.ConfigJSON = `{"jupyter":{"enabled":false}}` },
		"capsets":      func(item *domain.AgentDefinition) { item.CapsetIDs = []string{"capset-2"} },
		"skills": func(item *domain.AgentDefinition) {
			item.Skills = []domain.AgentSkill{{Name: "skill-1", Provider: "git", URL: "https://example.invalid/repo", Ref: "v2"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := base
			mutate(&changed)
			if got := mustSchedulerRequestSandboxConfigHash(t, &changed); got == baseHash {
				t.Fatalf("effective agent config hash did not change for %s", name)
			}
		})
	}
}

func mustSchedulerRequestSandboxConfigHash(t *testing.T, agentDefinition *domain.AgentDefinition) string {
	t.Helper()
	hash, err := schedulerRequestSandboxConfigHash(
		"sha256:scheduler",
		domain.SchedulerAgentRequest{},
		agentDefinition,
		nil,
		nil,
		nil,
		"docker",
		"guest:v1",
		nil,
	)
	if err != nil {
		t.Fatalf("schedulerRequestSandboxConfigHash returned error: %v", err)
	}
	return hash
}
