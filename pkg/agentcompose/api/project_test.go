package api

import (
	"context"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sources"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"gopkg.in/yaml.v3"
)

func TestProjectToProtoOnlyIncludesCurrentRevisionArtifacts(t *testing.T) {
	project := domain.ProjectRecord{ID: "project", CurrentRevision: 2}
	agents := []domain.ProjectAgentRecord{{ProjectID: "project", AgentName: "old", Revision: 1}, {ProjectID: "project", AgentName: "current", Revision: 2}}
	schedulers := []domain.ProjectSchedulerRecord{{ProjectID: "project", SchedulerID: "old", Revision: 1}, {ProjectID: "project", SchedulerID: "current", Revision: 2}}
	result := ProjectToProto(project, nil, agents, schedulers)
	if len(result.GetAgents()) != 1 || result.GetAgents()[0].GetAgentName() != "current" {
		t.Fatalf("agents = %#v", result.GetAgents())
	}
	if len(result.GetSchedulers()) != 1 || result.GetSchedulers()[0].GetSchedulerId() != "current" {
		t.Fatalf("schedulers = %#v", result.GetSchedulers())
	}
	if result.GetSummary().GetAgentCount() != 1 || result.GetSummary().GetSchedulerCount() != 1 {
		t.Fatalf("summary = %#v", result.GetSummary())
	}
}

func TestResolvedTriggerPreservesDeclaredSpec(t *testing.T) {
	declared := &agentcomposev2.TriggerSpec{Name: "later", Kind: "timeout", Timeout: "2s", Prompt: "continue", SandboxPolicy: "sticky"}
	trigger := domain.LoaderTrigger{ID: "trigger-id", Kind: domain.LoaderTriggerKindTimeout, IntervalMs: 2000, Enabled: true, NextFireAt: time.Unix(10, 0)}
	resolved := resolvedTriggerToProto(trigger, declared)
	if resolved.GetTriggerId() != "trigger-id" || resolved.GetSpec().GetName() != "later" || resolved.GetSpec().GetTimeout() != "2s" || resolved.GetSpec().GetPrompt() != "continue" || resolved.GetSpec().GetInterval() != "" {
		t.Fatalf("resolved = %#v", resolved)
	}
	declared.Prompt = "changed"
	if resolved.GetSpec().GetPrompt() != "continue" {
		t.Fatal("resolved trigger aliases declared spec")
	}
}

func TestProjectSpecToProtoRejectsUnresolvedSchedulerScriptURL(t *testing.T) {
	raw, err := compose.Parse([]byte("name: unresolved-url\nagents:\n  reviewer:\n    scheduler:\n      script:\n        provider: file\n        path: ./scheduler.js\n"))
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := compose.Normalize(raw, compose.NormalizeOptions{ComposePath: "/project/agent-compose.yml"})
	if err != nil {
		t.Fatal(err)
	}
	if result := ProjectSpecToProto(normalized); result != nil {
		t.Fatalf("ProjectSpecToProto unresolved result = %#v", result)
	}
	if _, err := ProjectSpecToProtoChecked(normalized); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("ProjectSpecToProtoChecked error = %v", err)
	}
	if result := SchedulerSpecToProto(normalized.Agents[0].Scheduler); result != nil {
		t.Fatalf("SchedulerSpecToProto unresolved result = %#v", result)
	}
}

func TestProjectSpecToProtoURLSnapshotMatchesInline(t *testing.T) {
	const script = `scheduler.interval("hourly-review", "1h");`
	inlineRaw, _ := compose.Parse([]byte("name: proto-snapshot\nagents:\n  reviewer:\n    scheduler:\n      script: '" + script + "'\n"))
	inline, err := compose.Normalize(inlineRaw, compose.NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	urlRaw, _ := compose.Parse([]byte("name: proto-snapshot\nagents:\n  reviewer:\n    scheduler:\n      script:\n        provider: http\n        url: https://example.test/scheduler.js\n"))
	fromURL, err := compose.Normalize(urlRaw, compose.NormalizeOptions{
		ResolveScriptURLs: true,
		ScriptSourceResolver: compose.ScriptSourceResolverFunc(func(context.Context, sources.Source) ([]byte, error) {
			return []byte(script), nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	inlineProto, err := ProjectSpecToProtoChecked(inline)
	if err != nil {
		t.Fatal(err)
	}
	urlProto, err := ProjectSpecToProtoChecked(fromURL)
	if err != nil {
		t.Fatal(err)
	}
	if inlineProto.String() != urlProto.String() {
		t.Fatalf("proto snapshots differ:\n%s\n%s", inlineProto, urlProto)
	}
}

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
				DisplayName:   "Hourly review",
				Description:   "Reviews pending changes every hour",
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
	if scheduler.GetDisplayName() != "Hourly review" || scheduler.GetDescription() != "Reviews pending changes every hour" {
		t.Fatalf("scheduler presentation = %#v", scheduler)
	}
	shape := SchedulerYAMLShape(scheduler)
	if shape["sandbox_policy"] != "sticky" || shape["display_name"] != "Hourly review" || shape["description"] != "Reviews pending changes every hour" {
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

func TestIntegrationAgentPresentationMetadataRoundTripsThroughProtoAndCompose(t *testing.T) {
	shape, issues := ProjectSpecYAMLShape(&agentcomposev2.ProjectSpec{
		Name: "presentation",
		Agents: []*agentcomposev2.AgentSpec{{
			Name:        "legacy-agent-bfe5286dc77f",
			DisplayName: "通用助手",
			Description: "处理日常通用任务",
			Provider:    "codex",
		}},
	})
	if len(issues) != 0 {
		t.Fatalf("ProjectSpecYAMLShape issues = %#v", issues)
	}
	data, err := yaml.Marshal(shape)
	if err != nil {
		t.Fatalf("marshal project shape: %v", err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		t.Fatalf("parse project shape: %v", err)
	}
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize project shape: %v", err)
	}
	response := ProjectSpecToProto(normalized)
	if response == nil || len(response.GetAgents()) != 1 {
		t.Fatalf("ProjectSpecToProto response = %#v", response)
	}
	agent := response.GetAgents()[0]
	if agent.GetName() != "legacy-agent-bfe5286dc77f" || agent.GetDisplayName() != "通用助手" || agent.GetDescription() != "处理日常通用任务" {
		t.Fatalf("round-tripped agent = %#v", agent)
	}
}

func TestIntegrationProjectSpecToProtoIncludesWorkspaceRegistry(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "workspace-registry",
		Workspaces: map[string]compose.WorkspaceSpec{
			"docs": {Name: "docs", Provider: "git", URL: "https://example.test/docs.git", Ref: "abc123", Target: "docs"},
			"repo": {Name: "repo", Provider: "file", Path: "."},
		},
		Agents: []compose.NormalizedAgentSpec{{
			Name:      "reviewer",
			Workspace: &compose.WorkspaceSpec{Provider: "file", Path: "."},
			Driver:    &compose.NormalizedDriverSpec{Name: compose.DriverDocker, Docker: &compose.DockerDriverSpec{}},
		}},
	}

	response := ProjectSpecToProto(spec)
	if response == nil || len(response.GetWorkspaces()) != 2 {
		t.Fatalf("ProjectSpecToProto workspaces = %#v", response)
	}
	if response.GetWorkspaces()[0].GetName() != "docs" || response.GetWorkspaces()[0].GetWorkspace().GetProvider() != "git" || response.GetWorkspaces()[0].GetWorkspace().GetRef() != "abc123" {
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
			Workspace: &agentcomposev2.WorkspaceSpec{Provider: "git", Url: "https://example.test/repo.git", Ref: "abc123", Target: "."},
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
	repo, ok := workspaces["repo"].(map[string]any)
	if !ok || repo["ref"] != "abc123" || repo["target"] != "." {
		t.Fatalf("repo workspace shape = %#v", workspaces["repo"])
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

func TestProjectSpecToProtoIncludesMCPServers(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "mcp",
		MCPServers: map[string]compose.NormalizedMCPServerSpec{
			"docs": {Type: "remote", Transport: "http", URL: "https://docs.example.com/mcp"},
		},
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Provider: "codex",
			Driver: &compose.NormalizedDriverSpec{
				Name:   compose.DriverDocker,
				Docker: &compose.DockerDriverSpec{},
			},
			MCPServers: map[string]compose.NormalizedMCPServerSpec{
				"docs": {Type: "remote", Transport: "http", URL: "https://docs.example.com/mcp"},
			},
		}},
	}

	response := ProjectSpecToProto(spec)
	if response == nil || len(response.GetMcpServers()) != 1 {
		t.Fatalf("project mcp servers missing: %#v", response)
	}
	if response.GetMcpServers()[0].GetName() != "docs" || response.GetMcpServers()[0].GetUrl() != "https://docs.example.com/mcp" {
		t.Fatalf("project mcp servers = %#v", response.GetMcpServers())
	}
	if len(response.GetAgents()) != 1 || len(response.GetAgents()[0].GetMcpServers()) != 1 {
		t.Fatalf("agent mcp servers missing: %#v", response.GetAgents())
	}
}

func TestProjectSpecYAMLShapeIncludesMCPServers(t *testing.T) {
	raw, issues := ProjectSpecYAMLShape(ProjectSpecToProto(&compose.NormalizedProjectSpec{
		Name: "mcp",
		MCPServers: map[string]compose.NormalizedMCPServerSpec{
			"filesystem": {Type: "local", Command: "npx", Args: []string{"server"}},
		},
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Provider: "claude",
			Driver: &compose.NormalizedDriverSpec{
				Name:   compose.DriverDocker,
				Docker: &compose.DockerDriverSpec{},
			},
			MCPServers: map[string]compose.NormalizedMCPServerSpec{
				"filesystem": {Type: "local", Command: "npx", Args: []string{"server"}},
			},
		}},
	}))
	if len(issues) > 0 {
		t.Fatalf("issues = %#v", issues)
	}
	projectMCPServers, ok := raw["mcp_servers"].(map[string]any)
	if !ok || len(projectMCPServers) != 1 {
		t.Fatalf("project mcp servers = %#v", raw["mcp_servers"])
	}
	agents, ok := raw["agents"].(map[string]any)
	if !ok {
		t.Fatalf("agents = %#v", raw["agents"])
	}
	reviewer, ok := agents["reviewer"].(map[string]any)
	if !ok {
		t.Fatalf("reviewer = %#v", agents["reviewer"])
	}
	agentMCPServers, ok := reviewer["mcp_servers"].([]map[string]any)
	if !ok || len(agentMCPServers) != 1 || agentMCPServers[0]["name"] != "filesystem" {
		t.Fatalf("agent mcp servers = %#v", reviewer["mcp_servers"])
	}
}
