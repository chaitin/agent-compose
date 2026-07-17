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
			"provider":"file",
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
	if workspace.GetName() != "repo-root" || workspace.GetWorkspace().GetProvider() != "file" || workspace.GetWorkspace().GetPath() != "workspaces/local-repo" {
		t.Fatalf("workspace = %#v", workspace)
	}
}

func TestDecodeRevisionSpecSupportsCanonicalGitCommitShape(t *testing.T) {
	decoded, err := DecodeRevisionSpec(`{
		"name":"workspace-project",
		"workspaces":[{
			"key":"repo-root",
			"provider":"git",
			"url":"https://example.test/repo.git",
			"ref":"abc123",
			"target":"."
		}],
		"agents":[{"name":"reviewer"}]
	}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	workspace := decoded.GetWorkspaces()[0].GetWorkspace()
	if workspace.GetRef() != "abc123" || workspace.GetTarget() != "." {
		t.Fatalf("canonical git workspace = %#v", workspace)
	}
}

func TestDecodeRevisionSpecPreservesNestedWorkspaceShape(t *testing.T) {
	decoded, err := DecodeRevisionSpec(`{
		"name":"workspace-project",
		"workspaces":[{
			"name":"repo-root",
			"workspace":{"provider":"git","url":"https://example.test/repo.git","ref":"abc123","target":"."}
		}]
	}`)
	if err != nil {
		t.Fatalf("DecodeRevisionSpec returned error: %v", err)
	}
	workspace := decoded.GetWorkspaces()[0]
	if workspace.GetName() != "repo-root" || workspace.GetWorkspace().GetProvider() != "git" || workspace.GetWorkspace().GetUrl() != "https://example.test/repo.git" || workspace.GetWorkspace().GetRef() != "abc123" || workspace.GetWorkspace().GetTarget() != "." {
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

func TestIntegrationProjectRevisionDoesNotSelectOmittedAgentWorkspace(t *testing.T) {
	normalized, err := compose.Normalize(&compose.ProjectSpec{
		Name: "workspace-project",
		Workspaces: map[string]compose.WorkspaceSpec{
			"repo-root": {Provider: "file", Path: "workspaces/local-repo"},
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
	if agent.GetWorkspace() != nil {
		t.Fatalf("decoded agent workspace = %#v, want nil", agent.GetWorkspace())
	}
	projectWorkspace, agentWorkspace, err := ProjectRunWorkspaceSpecsFromV2(decoded.GetWorkspaces(), agent.GetWorkspace())
	if err != nil {
		t.Fatalf("ProjectRunWorkspaceSpecsFromV2 returned error: %v", err)
	}
	if projectWorkspace != nil || agentWorkspace != nil {
		t.Fatalf("resolved workspaces = (%#v, %#v), want nil, nil", projectWorkspace, agentWorkspace)
	}
}

func TestIntegrationProjectRevisionNamedInlineWorkspaceRoundTrip(t *testing.T) {
	normalized, err := compose.Normalize(&compose.ProjectSpec{
		Name: "workspace-project",
		Workspaces: map[string]compose.WorkspaceSpec{
			"docs-repo": {Provider: "file", Path: "workspaces/local-docs"},
		},
		Agents: map[string]compose.AgentSpec{
			"reviewer": {
				Provider: "codex",
				Driver:   &compose.DriverSpec{Docker: &compose.DockerDriverSpec{}},
				Workspace: &compose.WorkspaceSpec{
					Name:     "repo-root",
					Provider: "file",
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
	if agentWorkspace == nil || agentWorkspace.Name != "repo-root" || agentWorkspace.Provider != "file" || agentWorkspace.Path != "workspaces/local-repo" {
		t.Fatalf("agent workspace = %#v", agentWorkspace)
	}
}
