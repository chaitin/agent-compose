package projects

import (
	"path/filepath"
	"testing"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/workspaces"
)

func TestMapLegacyWorkspaceConfigToExistingV2Shapes(t *testing.T) {
	t.Run("git", func(t *testing.T) {
		workspace, err := mapLegacyWorkspaceConfig(nil, domain.WorkspaceConfig{
			ID:   "git-workspace",
			Type: "git",
			ConfigJSON: `{
				"provider":"git",
				"url":"https://example.test/team/repo.git",
				"ref":"abc123",
				"target":"source",
				"username":"user",
				"token":"${TOKEN}"
			}`,
		})
		if err != nil {
			t.Fatalf("mapLegacyWorkspaceConfig returned error: %v", err)
		}
		if workspace.Provider != "git" || workspace.URL != "https://example.test/team/repo.git" || workspace.Ref != "abc123" || workspace.Target != "source" || workspace.Username != "user" || workspace.Token != "${TOKEN}" {
			t.Fatalf("mapped git workspace = %#v", workspace)
		}
	})

	t.Run("git legacy persistence aliases", func(t *testing.T) {
		workspace, err := mapLegacyWorkspaceConfig(nil, domain.WorkspaceConfig{
			ID:   "legacy-git-workspace",
			Type: "git",
			ConfigJSON: `{
				"url":"https://example.test/team/repo.git",
				"branch":"main",
				"commit":"abc123",
				"path":"source",
				"credential":"legacy-user:legacy-password"
			}`,
		})
		if err != nil {
			t.Fatalf("mapLegacyWorkspaceConfig returned error: %v", err)
		}
		if workspace.Provider != "git" || workspace.Ref != "abc123" || workspace.Target != "source" || workspace.Username != "legacy-user" || workspace.Password != "legacy-password" {
			t.Fatalf("mapped legacy git workspace = %#v", workspace)
		}
	})

	t.Run("file", func(t *testing.T) {
		root := t.TempDir()
		config := &appconfig.Config{DataRoot: root}
		workspaceID := "file-workspace"
		workspace, err := mapLegacyWorkspaceConfig(config, domain.WorkspaceConfig{
			ID:         workspaceID,
			Type:       "file",
			ConfigJSON: workspaces.DefaultFileConfigJSON(config, workspaceID),
		})
		if err != nil {
			t.Fatalf("mapLegacyWorkspaceConfig returned error: %v", err)
		}
		wantPath := filepath.ToSlash(filepath.Join("workspaces", workspaceID, workspaces.FileWorkspaceContentDirName))
		if workspace.Provider != "file" || workspace.Path != wantPath {
			t.Fatalf("mapped file workspace = %#v, want path %q", workspace, wantPath)
		}
	})
}

func TestMapLegacyWorkspaceConfigPreservesCommitPin(t *testing.T) {
	workspace, err := mapLegacyWorkspaceConfig(nil, domain.WorkspaceConfig{
		ID:         "git-workspace",
		Type:       "git",
		ConfigJSON: `{"provider":"git","url":"https://example.test/repo.git","ref":"abc123"}`,
	})
	if err != nil {
		t.Fatalf("mapLegacyWorkspaceConfig returned error: %v", err)
	}
	if workspace.Ref != "abc123" {
		t.Fatalf("mapped ref = %q, want abc123", workspace.Ref)
	}
}

func TestLegacyDefaultNormalizedProjectMapsNamedWorkspaces(t *testing.T) {
	projection := legacyWorkspaceProjection{
		workspaces: map[string]compose.WorkspaceSpec{
			"source": {Provider: "git", URL: "https://example.test/source.git", Target: "."},
			"tasks":  {Provider: "git", URL: "https://example.test/tasks.git", Target: "."},
		},
		nameByID: map[string]string{
			"workspace-source": "source",
			"workspace-tasks":  "tasks",
		},
	}
	agents := []domain.AgentDefinition{{
		ID:          "agent-1",
		Name:        "worker",
		Enabled:     true,
		Provider:    "codex",
		WorkspaceID: "workspace-source",
	}}
	loaders := []domain.Loader{{
		Summary: domain.LoaderSummary{
			ID:          "loader-1",
			Name:        "task",
			Enabled:     false,
			Runtime:     domain.LoaderRuntimeScheduler,
			AgentID:     "agent-1",
			WorkspaceID: "workspace-tasks",
		},
		Script: "scheduler.interval('task', function task() {}, 60000);",
	}}

	project, err := legacyDefaultNormalizedProjectWithWorkspaces(agents, loaders, projection)
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProjectWithWorkspaces returned error: %v", err)
	}
	if len(project.Spec.Workspaces) != 2 || len(project.Spec.Agents) != 2 {
		t.Fatalf("project workspaces/agents = %#v/%#v", project.Spec.Workspaces, project.Spec.Agents)
	}
	source := findLegacyProjectAgent(t, project, "worker")
	if source.Workspace == nil || source.Workspace.Name != "source" || source.Scheduler != nil {
		t.Fatalf("source agent = %#v", source)
	}
	var taskAgent compose.NormalizedAgentSpec
	for _, agent := range project.Spec.Agents {
		if agent.Scheduler != nil {
			taskAgent = agent
		}
	}
	if taskAgent.Workspace == nil || taskAgent.Workspace.Name != "tasks" || project.managedLoaderOverrides[taskAgent.Name].Summary.ID != "loader-1" {
		t.Fatalf("task agent/override = %#v/%#v", taskAgent, project.managedLoaderOverrides)
	}
}

func TestLegacyProjectWorkspaceNamesAreStableAndValid(t *testing.T) {
	configs := []domain.WorkspaceConfig{
		{ID: "workspace-z", Name: "共享空间"},
		{ID: "workspace-b", Name: "source"},
		{ID: "workspace-a", Name: "source"},
		{ID: "workspace-valid", Name: "review-repo"},
	}
	names := legacyProjectWorkspaceNames(configs)
	if names["workspace-valid"] != "review-repo" {
		t.Fatalf("valid workspace name = %q", names["workspace-valid"])
	}
	seen := make(map[string]struct{}, len(names))
	for id, name := range names {
		if !domain.IsProjectStableIdentifier(name) {
			t.Fatalf("workspace %s mapped to invalid name %q", id, name)
		}
		if _, exists := seen[name]; exists {
			t.Fatalf("duplicate mapped workspace name %q", name)
		}
		seen[name] = struct{}{}
	}

	reversed := []domain.WorkspaceConfig{configs[3], configs[2], configs[1], configs[0]}
	reversedNames := legacyProjectWorkspaceNames(reversed)
	for id, name := range names {
		if reversedNames[id] != name {
			t.Fatalf("workspace %s name depends on input order: %q != %q", id, reversedNames[id], name)
		}
	}
}

func TestLegacySingleWorkspaceDoesNotBecomeDefaultForUnassignedAgents(t *testing.T) {
	projection := legacyWorkspaceProjection{
		workspaces: map[string]compose.WorkspaceSpec{
			"source": {Provider: "git", URL: "https://example.test/source.git", Target: "."},
		},
		nameByID: map[string]string{"workspace-source": "source"},
	}
	agents := []domain.AgentDefinition{
		{ID: "agent-with", Name: "with-workspace", Enabled: true, Provider: "codex", WorkspaceID: "workspace-source"},
		{ID: "agent-without", Name: "without-workspace", Enabled: true, Provider: "codex"},
	}

	project, err := legacyDefaultNormalizedProjectWithWorkspaces(agents, nil, projection)
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProjectWithWorkspaces returned error: %v", err)
	}
	if len(project.Spec.Workspaces) != 0 {
		t.Fatalf("single global workspace would become an implicit default: %#v", project.Spec.Workspaces)
	}
	withWorkspace := findLegacyProjectAgent(t, project, "with-workspace")
	withoutWorkspace := findLegacyProjectAgent(t, project, "without-workspace")
	if withWorkspace.Workspace == nil || withWorkspace.Workspace.Provider != "git" || withoutWorkspace.Workspace != nil {
		t.Fatalf("projected workspaces = with %#v, without %#v", withWorkspace.Workspace, withoutWorkspace.Workspace)
	}
}

func TestLegacyMultipleWorkspacesRemainReapplicableWithUnassignedAgent(t *testing.T) {
	projection := legacyWorkspaceProjection{
		workspaces: map[string]compose.WorkspaceSpec{
			"source": {Provider: "git", URL: "https://example.test/source.git", Target: "."},
			"tasks":  {Provider: "git", URL: "https://example.test/tasks.git", Target: "."},
		},
		nameByID: map[string]string{
			"workspace-source": "source",
			"workspace-tasks":  "tasks",
		},
	}
	agents := []domain.AgentDefinition{
		{ID: "agent-source", Name: "source-agent", Enabled: true, Provider: "codex", WorkspaceID: "workspace-source"},
		{ID: "agent-tasks", Name: "tasks-agent", Enabled: true, Provider: "codex", WorkspaceID: "workspace-tasks"},
		{ID: "agent-empty", Name: "empty-agent", Enabled: true, Provider: "codex"},
	}

	project, err := legacyDefaultNormalizedProjectWithWorkspaces(agents, nil, projection)
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProjectWithWorkspaces returned error: %v", err)
	}
	if len(project.Spec.Workspaces) != 0 {
		t.Fatalf("project workspaces must be inlined when an agent has no workspace: %#v", project.Spec.Workspaces)
	}
	if findLegacyProjectAgent(t, project, "empty-agent").Workspace != nil {
		t.Fatalf("workspace-less agent gained a workspace")
	}
	for _, name := range []string{"source-agent", "tasks-agent"} {
		workspace := findLegacyProjectAgent(t, project, name).Workspace
		if workspace == nil || workspace.Provider != "git" || workspace.Name != "" {
			t.Fatalf("agent %s workspace was not inlined: %#v", name, workspace)
		}
	}

	reapplied := &compose.ProjectSpec{
		Name:       project.Spec.Name,
		Workspaces: project.Spec.Workspaces,
		Agents:     make(map[string]compose.AgentSpec, len(project.Spec.Agents)),
	}
	for _, agent := range project.Spec.Agents {
		reapplied.Agents[agent.Name] = compose.AgentSpec{Provider: agent.Provider, Workspace: agent.Workspace}
	}
	if _, err := compose.Normalize(reapplied, compose.NormalizeOptions{}); err != nil {
		t.Fatalf("reapply migrated spec: %v", err)
	}
}

func TestLegacyLoaderWorkspaceDoesNotOverwriteWorkspaceLessSourceAgent(t *testing.T) {
	projection := legacyWorkspaceProjection{
		workspaces: map[string]compose.WorkspaceSpec{
			"tasks": {Provider: "git", URL: "https://example.test/tasks.git", Target: "."},
		},
		nameByID: map[string]string{"workspace-tasks": "tasks"},
	}
	agents := []domain.AgentDefinition{{ID: "agent-1", Name: "worker", Enabled: true, Provider: "codex"}}
	loaders := []domain.Loader{{
		Summary: domain.LoaderSummary{
			ID: "loader-1", Name: "task", Enabled: true, Runtime: domain.LoaderRuntimeScheduler,
			AgentID: "agent-1", WorkspaceID: "workspace-tasks",
		},
		Script: "scheduler.interval('task', function task() {}, 60000);",
	}}

	project, err := legacyDefaultNormalizedProjectWithWorkspaces(agents, loaders, projection)
	if err != nil {
		t.Fatalf("legacyDefaultNormalizedProjectWithWorkspaces returned error: %v", err)
	}
	source := findLegacyProjectAgent(t, project, "worker")
	if source.Workspace != nil || source.Scheduler != nil {
		t.Fatalf("source agent changed by loader workspace: %#v", source)
	}
	if len(project.Spec.Agents) != 2 {
		t.Fatalf("project agents = %#v, want source and compatibility loader agent", project.Spec.Agents)
	}
	var scheduled compose.NormalizedAgentSpec
	for _, agent := range project.Spec.Agents {
		if agent.Scheduler != nil {
			scheduled = agent
		}
	}
	if scheduled.Workspace == nil || scheduled.Workspace.Provider != "git" || scheduled.Workspace.Target != "." {
		t.Fatalf("compatibility loader agent workspace = %#v", scheduled.Workspace)
	}
}
