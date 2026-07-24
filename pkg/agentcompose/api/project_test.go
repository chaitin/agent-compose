package api

import (
	"context"
	"path/filepath"
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
		Name: "inline-script",
		Agents: []compose.NormalizedAgentSpec{{
			Name: "reviewer",
			Driver: &compose.NormalizedDriverSpec{
				Name:    compose.DriverBoxlite,
				Boxlite: &compose.BoxliteDriverSpec{},
			},
			Scheduler: &compose.NormalizedSchedulerSpec{
				Enabled:           true,
				SandboxPolicy:     "sticky",
				ConcurrencyPolicy: "parallel",
				DisplayName:       "Hourly review",
				Description:       "Reviews pending changes every hour",
				Script:            script,
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
	if scheduler.GetConcurrencyPolicy() != "parallel" {
		t.Fatalf("scheduler concurrency policy = %q, want parallel", scheduler.GetConcurrencyPolicy())
	}
	if scheduler.GetDisplayName() != "Hourly review" || scheduler.GetDescription() != "Reviews pending changes every hour" {
		t.Fatalf("scheduler presentation = %#v", scheduler)
	}
	shape := SchedulerYAMLShape(scheduler)
	if shape["sandbox_policy"] != "sticky" || shape["concurrency_policy"] != "parallel" || shape["display_name"] != "Hourly review" || shape["description"] != "Reviews pending changes every hour" {
		t.Fatalf("scheduler YAML shape = %#v", shape)
	}
	if got := len(scheduler.GetTriggers()); got != 0 {
		t.Fatalf("scheduler triggers = %d, want 0", got)
	}
}

func TestProjectSpecToProtoIncludesJupyter(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "jupyter",
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

func TestAgentEnablementRoundTripsThroughAPIShape(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "enablement",
		Agents: []compose.NormalizedAgentSpec{{
			Name:     "reviewer",
			Enabled:  false,
			Provider: "codex",
		}},
	}

	protoSpec := ProjectSpecToProto(spec)
	if protoSpec == nil || len(protoSpec.GetAgents()) != 1 || protoSpec.GetAgents()[0].GetEnabled() {
		t.Fatalf("ProjectSpecToProto enablement = %#v", protoSpec)
	}
	yamlAgents, issues := AgentYAMLMap(protoSpec.GetAgents())
	if len(issues) != 0 {
		t.Fatalf("AgentYAMLMap issues = %#v", issues)
	}
	reviewer, ok := yamlAgents["reviewer"].(map[string]any)
	if !ok || reviewer["enabled"] != false {
		t.Fatalf("AgentYAMLMap = %#v", yamlAgents)
	}
	if _, exists := reviewer["status"]; exists {
		t.Fatalf("AgentYAMLMap contains status = %#v", reviewer)
	}
}

func TestAgentYAMLMapPreservesOmittedEnablement(t *testing.T) {
	yamlAgents, issues := AgentYAMLMap([]*agentcomposev2.AgentSpec{{
		Name:     "reviewer",
		Provider: "codex",
	}})
	if len(issues) != 0 {
		t.Fatalf("AgentYAMLMap issues = %#v", issues)
	}
	reviewer, ok := yamlAgents["reviewer"].(map[string]any)
	if !ok {
		t.Fatalf("AgentYAMLMap = %#v", yamlAgents)
	}
	if _, exists := reviewer["enabled"]; exists {
		t.Fatalf("AgentYAMLMap materialized omitted enabled = %#v", reviewer)
	}

	root := map[string]any{"name": "enablement", "agents": yamlAgents}
	data, err := yaml.Marshal(root)
	if err != nil {
		t.Fatalf("marshal YAML shape: %v", err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		t.Fatalf("parse YAML shape: %v", err)
	}
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize YAML shape: %v", err)
	}
	if len(normalized.Agents) != 1 || !normalized.Agents[0].Enabled {
		t.Fatalf("omitted API enablement normalized as disabled: %#v", normalized.Agents)
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

func TestIntegrationSchedulerConcurrencyPolicyRoundTripsThroughProtoAndCompose(t *testing.T) {
	raw, err := compose.Parse([]byte(`
name: scheduler-policy-round-trip
agents:
  reviewer:
    provider: codex
    driver:
      docker: {}
    scheduler:
      enabled: true
      display_name: Review queue
      description: Processes every pending review
      sandbox_policy: sticky
      concurrency_policy: parallel
      triggers:
        - name: hourly
          cron: "0 * * * *"
          prompt: Review hourly changes
        - name: frequent
          interval: 5m
          prompt: Review recent changes
        - name: later
          timeout: 30s
          prompt: Review after delay
        - name: requested
          event:
            topic: review.requested
          prompt: Review requested change
`))
	if err != nil {
		t.Fatalf("parse authoring YAML: %v", err)
	}
	normalized, err := compose.Normalize(raw, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize authoring YAML: %v", err)
	}
	if len(normalized.Agents) != 1 || normalized.Agents[0].Scheduler == nil || normalized.Agents[0].Scheduler.ConcurrencyPolicy != domain.LoaderConcurrencyPolicyParallel {
		t.Fatalf("normalized scheduler = %#v", normalized.Agents)
	}

	wireSpec, err := ProjectSpecToProtoChecked(normalized)
	if err != nil {
		t.Fatalf("convert normalized project to protobuf: %v", err)
	}
	if got := wireSpec.GetAgents()[0].GetScheduler().GetConcurrencyPolicy(); got != domain.LoaderConcurrencyPolicyParallel {
		t.Fatalf("protobuf concurrency policy = %q, want parallel", got)
	}
	shape, issues := ProjectSpecYAMLShape(wireSpec)
	if len(issues) != 0 {
		t.Fatalf("convert protobuf project to YAML shape: %#v", issues)
	}
	data, err := yaml.Marshal(shape)
	if err != nil {
		t.Fatalf("marshal reconstructed YAML: %v", err)
	}
	if !strings.Contains(string(data), "concurrency_policy: parallel") {
		t.Fatalf("reconstructed YAML lost concurrency policy:\n%s", data)
	}

	reparsed, err := compose.Parse(data)
	if err != nil {
		t.Fatalf("parse reconstructed YAML: %v", err)
	}
	roundTripped, err := compose.Normalize(reparsed, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize reconstructed YAML: %v", err)
	}
	if len(roundTripped.Agents) != 1 || roundTripped.Agents[0].Scheduler == nil {
		t.Fatalf("round-tripped agents = %#v", roundTripped.Agents)
	}
	scheduler := roundTripped.Agents[0].Scheduler
	if scheduler.ConcurrencyPolicy != domain.LoaderConcurrencyPolicyParallel || scheduler.SandboxPolicy != domain.LoaderSandboxPolicySticky || len(scheduler.Triggers) != 4 {
		t.Fatalf("round-tripped scheduler = %#v", scheduler)
	}
	for index, kind := range []string{"cron", "interval", "timeout", "event"} {
		if scheduler.Triggers[index].Kind != kind {
			t.Fatalf("round-tripped trigger %d = %#v, want kind %q", index, scheduler.Triggers[index], kind)
		}
	}
}

func TestIntegrationProjectSpecRichConfigurationRoundTripsThroughServiceShape(t *testing.T) {
	raw, err := compose.Parse([]byte(`
name: rich-service-round-trip
variables:
  API_TOKEN:
    value: ${API_TOKEN}
    secret: true
mcp_servers:
  local-tools:
    type: local
    command: npx
    args: ["-y", "@example/mcp-server"]
    env:
      TOOL_MODE: strict
  issue-tracker:
    type: remote
    transport: http
    url: https://issues.example.test/mcp
    headers:
      Authorization: Bearer ${ISSUE_TOKEN}
volumes:
  cache:
    name: shared-review-cache
    driver: local
    external: true
    labels:
      purpose: review
    options:
      device: /var/cache/reviews
agents:
  reviewer:
    enabled: true
    display_name: Release reviewer
    description: Reviews release candidates
    provider: codex
    model: gpt-test
    system_prompt: Review every release carefully.
    image: reviewer:test
    build:
      context: ./guest
      dockerfile: Dockerfile.review
      target: runtime
      args:
        CHANNEL: stable
      platforms: [linux/amd64]
      tags: [reviewer:latest]
      no_cache: true
      pull: true
    driver:
      docker:
        host: unix:///var/run/docker.sock
    env:
      LOG_LEVEL: debug
      SERVICE_TOKEN:
        value: ${SERVICE_TOKEN}
        secret: true
    capset_ids: [engineering, audit]
    skills:
      - name: release-check
        provider: git
        url: https://example.test/skills.git
        path: skills/release-check
        ref: main
        username: ${SKILL_USER}
        password: ${SKILL_PASSWORD}
        token: ${SKILL_TOKEN}
    mcp_servers:
      - local-tools
      - name: audit-api
        type: remote
        transport: sse
        url: https://audit.example.test/sse
        headers:
          Authorization: Bearer ${AUDIT_TOKEN}
    volumes:
      - type: volume
        source: cache
        target: /cache
        read_only: true
      - type: bind
        source: ./reports
        target: /workspace/reports
    scheduler:
      enabled: true
      sandbox_policy: sticky
      concurrency_policy: parallel
      triggers:
        - name: hourly-review
          cron: "0 * * * *"
          prompt: Review current release state
    jupyter:
      enabled: true
      guest_port: 8888
`))
	if err != nil {
		t.Fatalf("parse rich project: %v", err)
	}
	composePath := filepath.Join(t.TempDir(), "agent-compose.yaml")
	normalizeOptions := compose.NormalizeOptions{
		ComposePath: composePath,
		Env: map[string]string{
			"API_TOKEN":      "${API_TOKEN}",
			"ISSUE_TOKEN":    "${ISSUE_TOKEN}",
			"SERVICE_TOKEN":  "${SERVICE_TOKEN}",
			"SKILL_USER":     "${SKILL_USER}",
			"SKILL_PASSWORD": "${SKILL_PASSWORD}",
			"SKILL_TOKEN":    "${SKILL_TOKEN}",
			"AUDIT_TOKEN":    "${AUDIT_TOKEN}",
		},
	}
	normalized, err := compose.Normalize(raw, normalizeOptions)
	if err != nil {
		t.Fatalf("normalize rich project: %v", err)
	}
	wireSpec, err := ProjectSpecToProtoChecked(normalized)
	if err != nil {
		t.Fatalf("convert rich project to protobuf: %v", err)
	}
	shape, issues := ProjectSpecYAMLShape(wireSpec)
	if len(issues) != 0 {
		t.Fatalf("convert rich project to YAML shape: %#v", issues)
	}
	data, err := yaml.Marshal(shape)
	if err != nil {
		t.Fatalf("marshal rich service shape: %v", err)
	}
	reparsed, err := compose.Parse(data)
	if err != nil {
		t.Fatalf("parse rich service shape: %v\n%s", err, data)
	}
	roundTripped, err := compose.Normalize(reparsed, normalizeOptions)
	if err != nil {
		t.Fatalf("normalize rich service shape: %v\n%s", err, data)
	}

	if token := roundTripped.Variables["API_TOKEN"]; token.Value != "${API_TOKEN}" || !token.Secret {
		t.Fatalf("round-tripped project variable = %#v", token)
	}
	volume := roundTripped.Volumes["cache"]
	if volume.Name != "shared-review-cache" || !volume.External || volume.Labels["purpose"] != "review" || volume.Options["device"] != "/var/cache/reviews" {
		t.Fatalf("round-tripped project volume = %#v", volume)
	}
	if len(roundTripped.MCPServers) != 2 || roundTripped.MCPServers["local-tools"].Command != "npx" || roundTripped.MCPServers["issue-tracker"].Transport != "http" {
		t.Fatalf("round-tripped project MCP servers = %#v", roundTripped.MCPServers)
	}
	if len(roundTripped.Agents) != 1 {
		t.Fatalf("round-tripped agents = %#v", roundTripped.Agents)
	}
	agent := roundTripped.Agents[0]
	if agent.Build == nil || agent.Build.Dockerfile != "Dockerfile.review" || !agent.Build.NoCache || !agent.Build.Pull || len(agent.Build.Args) != 1 {
		t.Fatalf("round-tripped build = %#v", agent.Build)
	}
	if agent.Driver == nil || agent.Driver.Docker == nil || agent.Driver.Docker.Host != "unix:///var/run/docker.sock" {
		t.Fatalf("round-tripped driver = %#v", agent.Driver)
	}
	if len(agent.Skills) != 1 || agent.Skills[0].Name != "release-check" || agent.Skills[0].Token != "${SKILL_TOKEN}" {
		t.Fatalf("round-tripped skills = %#v", agent.Skills)
	}
	if len(agent.MCPServers) != 2 || agent.MCPServers["audit-api"].Transport != "sse" {
		t.Fatalf("round-tripped agent MCP servers = %#v", agent.MCPServers)
	}
	if len(agent.Volumes) != 2 || !agent.Volumes[0].ReadOnly || agent.Volumes[1].Type != "bind" {
		t.Fatalf("round-tripped mounts = %#v", agent.Volumes)
	}
	if agent.Jupyter == nil || !agent.Jupyter.Enabled || agent.Jupyter.GuestPort != 8888 {
		t.Fatalf("round-tripped jupyter = %#v", agent.Jupyter)
	}
	if agent.Scheduler == nil || agent.Scheduler.ConcurrencyPolicy != domain.LoaderConcurrencyPolicyParallel {
		t.Fatalf("round-tripped scheduler = %#v", agent.Scheduler)
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

func TestProjectSpecOctoBusServersRoundTripAndRedaction(t *testing.T) {
	spec := &compose.NormalizedProjectSpec{
		Name: "octobus",
		OctoBusServers: map[string]compose.NormalizedOctoBusServerSpec{
			"public":   {URL: "https://public.example"},
			"internal": {URL: "https://internal.example", Token: "secret-token"},
		},
	}

	wire := ProjectSpecToProto(spec)
	if len(wire.GetOctobusServers()) != 2 || wire.GetOctobusServers()[0].GetName() != "internal" || wire.GetOctobusServers()[0].GetToken() != "secret-token" {
		t.Fatalf("octobus proto = %#v", wire.GetOctobusServers())
	}
	shape, issues := ProjectSpecYAMLShape(wire)
	if len(issues) != 0 {
		t.Fatalf("ProjectSpecYAMLShape issues = %#v", issues)
	}
	servers, ok := shape["octobus_servers"].(map[string]any)
	if !ok || len(servers) != 2 {
		t.Fatalf("octobus yaml shape = %#v", shape["octobus_servers"])
	}
	internal, ok := servers["internal"].(map[string]any)
	if !ok || internal["url"] != "https://internal.example" || internal["token"] != "secret-token" {
		t.Fatalf("internal yaml shape = %#v", servers["internal"])
	}

	redacted := ProjectSpecToProtoRedacted(spec)
	if got := redacted.GetOctobusServers()[0].GetToken(); got == "secret-token" || got == "" {
		t.Fatalf("redacted token = %q", got)
	}
}
