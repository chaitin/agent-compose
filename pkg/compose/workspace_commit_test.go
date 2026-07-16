package compose

import (
	"strings"
	"testing"
)

func TestNormalizeGitWorkspacePreservesBranchAndCommit(t *testing.T) {
	normalized, err := Normalize(&ProjectSpec{
		Name: "project",
		Agents: map[string]AgentSpec{
			"worker": {
				Provider: "codex",
				Workspace: &WorkspaceSpec{
					Provider: " git ",
					URL:      " https://example.test/repo.git ",
					Branch:   " main ",
					Commit:   " abc123 ",
				},
			},
		},
	}, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	workspace := normalized.Agents[0].Workspace
	if workspace == nil || workspace.Branch != "main" || workspace.Commit != "abc123" {
		t.Fatalf("normalized workspace = %#v", workspace)
	}

	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if !strings.Contains(string(data), `"branch":"main"`) || !strings.Contains(string(data), `"commit":"abc123"`) {
		t.Fatalf("canonical JSON lost git revision fields: %s", data)
	}
}

func TestParseGitWorkspaceAcceptsCommit(t *testing.T) {
	spec, err := Parse([]byte(`
name: project
agents:
  worker:
    provider: codex
    workspace:
      provider: git
      url: https://example.test/repo.git
      branch: main
      commit: abc123
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	workspace := spec.Agents["worker"].Workspace
	if workspace == nil || workspace.Branch != "main" || workspace.Commit != "abc123" {
		t.Fatalf("parsed workspace = %#v", workspace)
	}
}

func TestNormalizeLocalWorkspaceRejectsCommit(t *testing.T) {
	_, err := Normalize(&ProjectSpec{
		Name: "project",
		Agents: map[string]AgentSpec{
			"worker": {Provider: "codex", Workspace: &WorkspaceSpec{Provider: "local", Path: ".", Commit: "abc123"}},
		},
	}, NormalizeOptions{})
	if err == nil || !strings.Contains(err.Error(), "local workspace does not support commit") {
		t.Fatalf("Normalize error = %v", err)
	}
}
