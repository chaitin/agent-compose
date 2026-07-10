package api

import (
	"testing"

	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestProjectSpecToProtoIncludesSchedulerScript(t *testing.T) {
	const script = `scheduler.interval("hourly-review", "1h");`
	spec := &compose.NormalizedProjectSpec{
		Name:    "inline-script",
		Network: &compose.NetworkSpec{Mode: "default"},
		Agents: []compose.NormalizedAgentSpec{{
			Name: "reviewer",
			Driver: &compose.NormalizedDriverSpec{
				Name:    compose.DriverBoxlite,
				Boxlite: &compose.BoxliteDriverSpec{},
			},
			Scheduler: &compose.NormalizedSchedulerSpec{
				Enabled:       true,
				SandboxPolicy: "sticky",
				Script:        script,
			},
		}},
	}

	response := ProjectSpecToProto(spec)
	if response == nil || len(response.GetAgents()) != 1 || response.GetAgents()[0].GetScheduler() == nil {
		t.Fatalf("ProjectSpecToProto scheduler missing: %#v", response)
	}
	scheduler := response.GetAgents()[0].GetScheduler()
	if scheduler.GetScript() != script {
		t.Fatalf("scheduler script = %q, want %q", scheduler.GetScript(), script)
	}
	if scheduler.GetSandboxPolicy() != "sticky" {
		t.Fatalf("scheduler sandbox policy = %q, want sticky", scheduler.GetSandboxPolicy())
	}
	shape := SchedulerYAMLShape(scheduler)
	if shape["sandbox_policy"] != "sticky" {
		t.Fatalf("scheduler YAML shape = %#v", shape)
	}
	if got := len(scheduler.GetTriggers()); got != 0 {
		t.Fatalf("scheduler triggers = %d, want 0", got)
	}
}

func TestProjectSpecToProtoIncludesJupyter(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name:    "jupyter",
		Network: &compose.NetworkSpec{Mode: "default"},
		Agents: []compose.NormalizedAgentSpec{{
			Name: "reviewer",
			Driver: &compose.NormalizedDriverSpec{
				Name:   compose.DriverDocker,
				Docker: &compose.DockerDriverSpec{},
			},
			Jupyter: &compose.JupyterSpec{Enabled: true, GuestPort: 8888},
		}},
	}

	response := ProjectSpecToProto(spec)
	if response == nil || len(response.GetAgents()) != 1 || response.GetAgents()[0].GetJupyter() == nil {
		t.Fatalf("ProjectSpecToProto jupyter missing: %#v", response)
	}
	jupyter := response.GetAgents()[0].GetJupyter()
	if !jupyter.GetEnabled() || jupyter.GetGuestPort() != 8888 {
		t.Fatalf("jupyter = %#v, want enabled guest port 8888", jupyter)
	}
}

func TestIntegrationProjectSpecToProtoIncludesWorkspaceRegistry(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "workspace-registry",
		Workspaces: map[string]compose.WorkspaceSpec{
			"docs": {Name: "docs", Provider: "git", URL: "https://example.test/docs.git", Path: "docs"},
			"repo": {Name: "repo", Provider: "local", Path: "."},
		},
		Agents: []compose.NormalizedAgentSpec{{
			Name:      "reviewer",
			Workspace: &compose.WorkspaceSpec{Provider: "local", Path: "."},
			Driver:    &compose.NormalizedDriverSpec{Name: compose.DriverDocker, Docker: &compose.DockerDriverSpec{}},
		}},
	}

	response := ProjectSpecToProto(spec)
	if response == nil || len(response.GetWorkspaces()) != 2 {
		t.Fatalf("ProjectSpecToProto workspaces = %#v", response)
	}
	if response.GetWorkspaces()[0].GetName() != "docs" || response.GetWorkspaces()[0].GetWorkspace().GetProvider() != "git" {
		t.Fatalf("first workspace = %#v", response.GetWorkspaces()[0])
	}
	if response.GetWorkspaces()[1].GetName() != "repo" || response.GetWorkspaces()[1].GetWorkspace().GetName() != "" {
		t.Fatalf("second workspace = %#v", response.GetWorkspaces()[1])
	}
}

func TestIntegrationProjectSpecYAMLShapeIncludesWorkspaceRegistry(t *testing.T) {
	shape, issues := ProjectSpecYAMLShape(&agentcomposev2.ProjectSpec{
		Name: "workspace-shape",
		Workspaces: []*agentcomposev2.NamedWorkspaceSpec{{
			Name:      "repo",
			Workspace: &agentcomposev2.WorkspaceSpec{Provider: "local", Path: "."},
		}},
		Agents: []*agentcomposev2.AgentSpec{{
			Name:      "reviewer",
			Workspace: &agentcomposev2.WorkspaceSpec{Name: "repo"},
		}},
	})
	if len(issues) != 0 {
		t.Fatalf("ProjectSpecYAMLShape issues = %#v", issues)
	}
	workspaces, ok := shape["workspaces"].(map[string]any)
	if !ok || len(workspaces) != 1 {
		t.Fatalf("workspaces shape = %#v", shape["workspaces"])
	}
	agents, ok := shape["agents"].(map[string]any)
	if !ok {
		t.Fatalf("agents shape = %#v", shape["agents"])
	}
	reviewer, ok := agents["reviewer"].(map[string]any)
	if !ok {
		t.Fatalf("reviewer shape = %#v", agents["reviewer"])
	}
	workspace, ok := reviewer["workspace"].(map[string]any)
	if !ok || workspace["name"] != "repo" {
		t.Fatalf("reviewer workspace = %#v", reviewer["workspace"])
	}
}

func TestProjectSpecToProtoIncludesMCPs(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "mcp",
		MCPs: map[string]compose.NormalizedMCPServerSpec{
			"docs": {Type: "remote", Transport: "http", URL: "https://docs.example.com/mcp"},
		},
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Provider: "codex",
			Driver: &compose.NormalizedDriverSpec{
				Name:   compose.DriverDocker,
				Docker: &compose.DockerDriverSpec{},
			},
			MCPs: map[string]compose.NormalizedMCPServerSpec{
				"docs": {Type: "remote", Transport: "http", URL: "https://docs.example.com/mcp"},
			},
		}},
	}

	response := ProjectSpecToProto(spec)
	if response == nil || len(response.GetMcps()) != 1 {
		t.Fatalf("project mcps missing: %#v", response)
	}
	if response.GetMcps()[0].GetName() != "docs" || response.GetMcps()[0].GetUrl() != "https://docs.example.com/mcp" {
		t.Fatalf("project mcps = %#v", response.GetMcps())
	}
	if len(response.GetAgents()) != 1 || len(response.GetAgents()[0].GetMcps()) != 1 {
		t.Fatalf("agent mcps missing: %#v", response.GetAgents())
	}
}

func TestProjectSpecYAMLShapeIncludesMCPs(t *testing.T) {
	raw, issues := ProjectSpecYAMLShape(ProjectSpecToProto(&compose.NormalizedProjectSpec{
		Name: "mcp",
		MCPs: map[string]compose.NormalizedMCPServerSpec{
			"filesystem": {Type: "local", Command: "npx", Args: []string{"server"}},
		},
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Provider: "claude",
			Driver: &compose.NormalizedDriverSpec{
				Name:   compose.DriverDocker,
				Docker: &compose.DockerDriverSpec{},
			},
			MCPs: map[string]compose.NormalizedMCPServerSpec{
				"filesystem": {Type: "local", Command: "npx", Args: []string{"server"}},
			},
		}},
	}))
	if len(issues) > 0 {
		t.Fatalf("issues = %#v", issues)
	}
	projectMCPs, ok := raw["mcps"].(map[string]any)
	if !ok || len(projectMCPs) != 1 {
		t.Fatalf("project mcps = %#v", raw["mcps"])
	}
	agents, ok := raw["agents"].(map[string]any)
	if !ok {
		t.Fatalf("agents = %#v", raw["agents"])
	}
	reviewer, ok := agents["reviewer"].(map[string]any)
	if !ok {
		t.Fatalf("reviewer = %#v", agents["reviewer"])
	}
	agentMCPs, ok := reviewer["mcps"].([]map[string]any)
	if !ok || len(agentMCPs) != 1 || agentMCPs[0]["name"] != "filesystem" {
		t.Fatalf("agent mcps = %#v", reviewer["mcps"])
	}
}
