package compose

import (
	"strings"
	"testing"
)

func TestNormalizeGitWorkspaceUsesRef(t *testing.T) {
	normalized, err := Normalize(&ProjectSpec{
		Name: "project",
		Agents: map[string]AgentSpec{
			"worker": {
				Provider: "codex",
				Workspace: &WorkspaceSpec{
					Provider: " git ",
					URL:      " https://example.test/repo.git ",
					Ref:      " main ",
					Target:   " source ",
				},
			},
		},
	}, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	workspace := normalized.Agents[0].Workspace
	if workspace == nil || workspace.Ref != "main" || workspace.Target != "source" {
		t.Fatalf("normalized workspace = %#v", workspace)
	}

	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	if !strings.Contains(string(data), `"ref":"main"`) || !strings.Contains(string(data), `"target":"source"`) {
		t.Fatalf("canonical JSON lost git source fields: %s", data)
	}
}

func TestNormalizeFileWorkspaceRejectsRef(t *testing.T) {
	_, err := Normalize(&ProjectSpec{
		Name: "project",
		Agents: map[string]AgentSpec{
			"worker": {Provider: "codex", Workspace: &WorkspaceSpec{Provider: "file", Path: ".", Ref: "abc123"}},
		},
	}, NormalizeOptions{})
	if err == nil || !strings.Contains(err.Error(), "file workspace does not support ref") {
		t.Fatalf("Normalize error = %v", err)
	}
}
