package runs

import (
	"testing"

	"agent-compose/pkg/compose"
)

func TestDecodeRevisionSpecSupportsCanonicalWorkspaceShape(t *testing.T) {
	decoded, err := DecodeRevisionSpec(`{
		"name":"workspace-project",
		"workspaces":[{
			"key":"repo-root",
			"name":"repo-root",
			"provider":"local",
			"path":"workspaces/local-repo"
		}],
		"agents":[{"name":"reviewer"}]
	}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	if len(decoded.GetWorkspaces()) != 1 {
		t.Fatalf("workspaces = %#v", decoded.GetWorkspaces())
	}
	workspace := decoded.GetWorkspaces()[0]
	if workspace.GetName() != "repo-root" || workspace.GetWorkspace().GetProvider() != "local" || workspace.GetWorkspace().GetPath() != "workspaces/local-repo" {
		t.Fatalf("workspace = %#v", workspace)
	}
}

func TestDecodeRevisionSpecPreservesNestedWorkspaceShape(t *testing.T) {
	decoded, err := DecodeRevisionSpec(`{
		"name":"workspace-project",
		"workspaces":[{
			"name":"repo-root",
			"workspace":{"provider":"git","url":"https://example.test/repo.git","branch":"main"}
		}]
	}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	workspace := decoded.GetWorkspaces()[0]
	if workspace.GetName() != "repo-root" || workspace.GetWorkspace().GetProvider() != "git" || workspace.GetWorkspace().GetUrl() != "https://example.test/repo.git" || workspace.GetWorkspace().GetBranch() != "main" {
		t.Fatalf("workspace = %#v", workspace)
	}
}

func TestDecodeRevisionSpecSupportsCanonicalAgentStatus(t *testing.T) {
	decoded, err := DecodeRevisionSpec(`{"name":"status-project","agents":[{"name":"worker","status":"disabled"}]}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	if len(decoded.GetAgents()) != 1 || decoded.GetAgents()[0].GetStatus() != 2 {
		t.Fatalf("agents = %#v", decoded.GetAgents())
	}
	if _, err := DecodeRevisionSpec(`{"agents":[{"name":"worker","status":"paused"}]}`); err == nil {
		t.Fatal("unknown status returned nil error")
	}
}

func TestIntegrationProjectRevisionWorkspaceRoundTrip(t *testing.T) {
	normalized, err := compose.Normalize(&compose.ProjectSpec{
		Name: "workspace-project",
		Workspaces: map[string]compose.WorkspaceSpec{
			"repo-root": {Provider: "local", Path: "workspaces/local-repo"},
		},
		Agents: map[string]compose.AgentSpec{
			"reviewer": {
				Provider: "codex",
				Driver:   &compose.DriverSpec{Docker: &compose.DockerDriverSpec{}},
			},
		},
	}, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	revisionJSON, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	decoded, err := DecodeRevisionSpec(string(revisionJSON))
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	agent, ok := AgentSpecByName(decoded, "reviewer")
	if !ok {
		t.Fatalf("reviewer missing from decoded revision: %#v", decoded.GetAgents())
	}
	projectWorkspace, agentWorkspace, err := ProjectRunWorkspaceSpecsFromV2(decoded.GetWorkspaces(), agent.GetWorkspace())
	if err != nil {
		t.Fatalf("ProjectRunWorkspaceSpecsFromV2 returned error: %v", err)
	}
	resolved := agentWorkspace
	if resolved == nil {
		resolved = projectWorkspace
	}
	if resolved == nil || resolved.Provider != "local" || resolved.Path != "workspaces/local-repo" {
		t.Fatalf("resolved workspace = %#v", resolved)
	}
}

func TestIntegrationProjectRevisionNamedInlineWorkspaceRoundTrip(t *testing.T) {
	normalized, err := compose.Normalize(&compose.ProjectSpec{
		Name: "workspace-project",
		Workspaces: map[string]compose.WorkspaceSpec{
			"docs-repo": {Provider: "local", Path: "workspaces/local-docs"},
		},
		Agents: map[string]compose.AgentSpec{
			"reviewer": {
				Provider: "codex",
				Driver:   &compose.DriverSpec{Docker: &compose.DockerDriverSpec{}},
				Workspace: &compose.WorkspaceSpec{
					Name:     "repo-root",
					Provider: "local",
					Path:     "workspaces/local-repo",
				},
			},
		},
	}, compose.NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	revisionJSON, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	decoded, err := DecodeRevisionSpec(string(revisionJSON))
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	agent, ok := AgentSpecByName(decoded, "reviewer")
	if !ok {
		t.Fatalf("reviewer missing from decoded revision: %#v", decoded.GetAgents())
	}
	projectWorkspace, agentWorkspace, err := ProjectRunWorkspaceSpecsFromV2(decoded.GetWorkspaces(), agent.GetWorkspace())
	if err != nil {
		t.Fatalf("ProjectRunWorkspaceSpecsFromV2 returned error: %v", err)
	}
	if projectWorkspace != nil {
		t.Fatalf("project workspace = %#v, want nil for inline override", projectWorkspace)
	}
	if agentWorkspace == nil || agentWorkspace.Name != "repo-root" || agentWorkspace.Provider != "local" || agentWorkspace.Path != "workspaces/local-repo" {
		t.Fatalf("agent workspace = %#v", agentWorkspace)
	}
}
