package projects

import (
	"strings"
	"testing"

	"agent-compose/pkg/compose"
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

	project, err := legacyDefaultNormalizedProject(agents, nil)
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

	reversed, err := legacyDefaultNormalizedProject([]domain.AgentDefinition{agents[1], agents[0]}, nil)
	if err != nil {
		t.Fatalf("reversed project returned error: %v", err)
	}
	if reversed.SpecHash != project.SpecHash {
		t.Fatalf("hash depends on database ordering: %s != %s", reversed.SpecHash, project.SpecHash)
	}
}

func TestLegacyDefaultNormalizedProjectRequiresLoadedWorkspacePreset(t *testing.T) {
	_, err := legacyDefaultNormalizedProject([]domain.AgentDefinition{{ID: "agent-1", Name: "worker", Enabled: true, Provider: "codex", WorkspaceID: "workspace-1"}}, nil)
	if err == nil || !strings.Contains(err.Error(), "was not loaded") {
		t.Fatalf("legacyDefaultNormalizedProject error = %v", err)
	}
}

func TestLegacyDefaultNormalizedProjectUsesStableCompatibilityNames(t *testing.T) {
	agents := []domain.AgentDefinition{
		{ID: "agent-chinese", Name: "ai资讯推送", Description: "每天汇总 AI 资讯", Enabled: true, Provider: "codex"},
		{ID: "agent-duplicate-b", Name: "worker", Enabled: true, Provider: "codex"},
		{ID: "agent-duplicate-a", Name: "worker", Enabled: true, Provider: "codex"},
		{ID: "agent-valid", Name: "Reviewer-Z", Enabled: true, Provider: "codex"},
	}

	project, err := legacyDefaultNormalizedProject(agents, nil)
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProject returned error: %v", err)
	}
	if len(project.Spec.Agents) != len(agents) {
		t.Fatalf("project agents = %#v", project.Spec.Agents)
	}
	names := make(map[string]struct{}, len(project.Spec.Agents))
	var compatibilityName string
	var compatibilityAgent compose.NormalizedAgentSpec
	duplicateCount := 0
	for _, agent := range project.Spec.Agents {
		if !domain.IsProjectStableIdentifier(agent.Name) {
			t.Fatalf("projected agent name %q is not a stable identifier", agent.Name)
		}
		if _, exists := names[agent.Name]; exists {
			t.Fatalf("duplicate projected agent name %q", agent.Name)
		}
		names[agent.Name] = struct{}{}
		if strings.HasPrefix(agent.Name, "legacy-agent-") {
			compatibilityName = agent.Name
			compatibilityAgent = agent
		}
		if strings.HasPrefix(agent.Name, "worker-") {
			duplicateCount++
		}
	}
	if compatibilityName == "" || duplicateCount != 2 {
		t.Fatalf("compatibility names = %#v", names)
	}
	if compatibilityAgent.DisplayName != "ai资讯推送" || compatibilityAgent.Description != "每天汇总 AI 资讯" {
		t.Fatalf("compatibility agent metadata = %#v", compatibilityAgent)
	}
	if _, exists := names["reviewer-z"]; !exists {
		t.Fatalf("valid normalized name missing from %#v", names)
	}

	reversed := append([]domain.AgentDefinition(nil), agents...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	reversedProject, err := legacyDefaultNormalizedProject(reversed, nil)
	if err != nil {
		t.Fatalf("reversed project returned error: %v", err)
	}
	if reversedProject.SpecHash != project.SpecHash {
		t.Fatalf("hash depends on input order: %s != %s", reversedProject.SpecHash, project.SpecHash)
	}

	renamed, err := legacyDefaultNormalizedProject([]domain.AgentDefinition{{ID: "agent-chinese", Name: "另一个中文名", Enabled: true, Provider: "codex"}}, nil)
	if err != nil {
		t.Fatalf("renamed invalid agent returned error: %v", err)
	}
	if renamed.Spec.Agents[0].Name != compatibilityName {
		t.Fatalf("compatibility name changed after display-name edit: %q != %q", renamed.Spec.Agents[0].Name, compatibilityName)
	}
}

func TestLegacyDefaultNormalizedProjectAdoptsLegacyLoaders(t *testing.T) {
	agents := []domain.AgentDefinition{{
		ID:         "agent-1",
		Name:       "worker",
		Enabled:    true,
		Provider:   "codex",
		Driver:     "docker",
		GuestImage: "guest:latest",
	}}
	loaders := []domain.Loader{
		{
			Summary: domain.LoaderSummary{
				ID:                "loader-b",
				Name:              "Second task",
				Description:       "Second task description",
				Enabled:           true,
				Runtime:           domain.LoaderRuntimeScheduler,
				AgentID:           "agent-1",
				DefaultAgent:      "codex",
				SandboxPolicy:     domain.LoaderSandboxPolicyNew,
				ConcurrencyPolicy: domain.LoaderConcurrencyPolicyParallel,
			},
			Script: "scheduler.on('topic.b', 'event-b', function eventB() {});",
			Triggers: []domain.LoaderTrigger{{
				ID: "event-b", Kind: domain.LoaderTriggerKindEvent, Topic: "topic.b", Enabled: true,
			}},
		},
		{
			Summary: domain.LoaderSummary{
				ID:            "loader-a",
				Name:          "First task",
				Description:   "First task description",
				Enabled:       false,
				Runtime:       domain.LoaderRuntimeScheduler,
				AgentID:       "agent-1",
				DefaultAgent:  "codex",
				SandboxPolicy: domain.LoaderSandboxPolicySticky,
			},
			Script: "scheduler.interval('interval-a', function intervalA() {}, 60000);",
			Triggers: []domain.LoaderTrigger{{
				ID: "interval-a", Kind: domain.LoaderTriggerKindInterval, IntervalMs: 60000, Enabled: false,
			}},
		},
	}

	project, err := legacyDefaultNormalizedProject(agents, loaders)
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProject returned error: %v", err)
	}
	if len(project.Spec.Agents) != 2 || len(project.managedLoaderOverrides) != 2 {
		t.Fatalf("project agents/overrides = %#v/%#v", project.Spec.Agents, project.managedLoaderOverrides)
	}
	worker := findLegacyProjectAgent(t, project, "worker")
	if worker.Scheduler == nil || worker.Scheduler.Enabled || worker.Scheduler.Script != loaders[1].Script || worker.Scheduler.DisplayName != "First task" || worker.Scheduler.Description != "First task description" {
		t.Fatalf("worker scheduler = %#v", worker.Scheduler)
	}
	workerLoader := project.managedLoaderOverrides["worker"]
	if workerLoader.Summary.ID != "loader-a" || workerLoader.Summary.Enabled || len(workerLoader.Triggers) != 1 || workerLoader.Triggers[0].Enabled {
		t.Fatalf("worker loader override = %#v", workerLoader)
	}

	var clonedName string
	for _, agent := range project.Spec.Agents {
		if strings.HasPrefix(agent.Name, "worker-loader-") {
			clonedName = agent.Name
			if agent.Scheduler == nil || !agent.Scheduler.Enabled || agent.Scheduler.Script != loaders[0].Script || agent.Scheduler.DisplayName != "Second task" || agent.Scheduler.Description != "Second task description" {
				t.Fatalf("cloned scheduler = %#v", agent.Scheduler)
			}
		}
	}
	if clonedName == "" || project.managedLoaderOverrides[clonedName].Summary.ID != "loader-b" {
		t.Fatalf("second loader was not projected: name=%q overrides=%#v", clonedName, project.managedLoaderOverrides)
	}

	reversedProject, err := legacyDefaultNormalizedProject(agents, []domain.Loader{loaders[1], loaders[0]})
	if err != nil {
		t.Fatalf("reversed loaders returned error: %v", err)
	}
	if reversedProject.SpecHash != project.SpecHash {
		t.Fatalf("loader projection hash depends on input order: %s != %s", reversedProject.SpecHash, project.SpecHash)
	}
}

func TestLegacyDefaultNormalizedProjectKeepsUnboundLoaderVisibleButDisabled(t *testing.T) {
	loader := domain.Loader{
		Summary: domain.LoaderSummary{
			ID:           "loader-unbound",
			Name:         "未绑定任务",
			Description:  "迁移后保持可见但禁用",
			Enabled:      true,
			Runtime:      domain.LoaderRuntimeScheduler,
			DefaultAgent: "codex",
		},
		Script: "scheduler.on('topic', 'event', function event() {});",
		Triggers: []domain.LoaderTrigger{{
			ID: "event", Kind: domain.LoaderTriggerKindEvent, Topic: "topic", Enabled: true,
		}},
	}

	project, err := legacyDefaultNormalizedProject(nil, []domain.Loader{loader})
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProject returned error: %v", err)
	}
	if len(project.Spec.Agents) != 1 || !strings.HasPrefix(project.Spec.Agents[0].Name, "legacy-loader-") {
		t.Fatalf("compatibility agent = %#v", project.Spec.Agents)
	}
	agent := project.Spec.Agents[0]
	if agent.Status != "disabled" || agent.Scheduler == nil || agent.Scheduler.Enabled {
		t.Fatalf("unbound compatibility agent = %#v", agent)
	}
	if agent.DisplayName != "未绑定任务" || agent.Description != "迁移后保持可见但禁用" {
		t.Fatalf("unbound compatibility agent metadata = %#v", agent)
	}
	if agent.Scheduler.DisplayName != "未绑定任务" || agent.Scheduler.Description != "迁移后保持可见但禁用" {
		t.Fatalf("unbound scheduler metadata = %#v", agent.Scheduler)
	}
	if adopted := project.managedLoaderOverrides[agent.Name]; adopted.Summary.ID != loader.Summary.ID || adopted.Summary.Enabled {
		t.Fatalf("unbound loader override = %#v", adopted)
	}

	loader.Summary.Enabled = false
	loader.Summary.ManagedAgentName = agent.Name
	reprojected, err := legacyDefaultNormalizedProject(nil, []domain.Loader{loader})
	if err != nil {
		t.Fatalf("reproject adopted unbound loader: %v", err)
	}
	if len(reprojected.Spec.Agents) != 1 || reprojected.Spec.Agents[0].Status != "disabled" || reprojected.Spec.Agents[0].Scheduler.Enabled {
		t.Fatalf("reprojected unbound compatibility agent = %#v", reprojected.Spec.Agents)
	}
}

func findLegacyProjectAgent(t *testing.T, project NormalizedProject, name string) compose.NormalizedAgentSpec {
	t.Helper()
	for _, agent := range project.Spec.Agents {
		if agent.Name == name {
			return agent
		}
	}
	t.Fatalf("agent %q not found in %#v", name, project.Spec.Agents)
	return compose.NormalizedAgentSpec{}
}
