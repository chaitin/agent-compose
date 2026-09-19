package app

import (
	"testing"

	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/compose"
)

func TestNormalizeProjectRequestAcceptsCLIResolvedWorkspaceCredentials(t *testing.T) {
	parsed, err := compose.Parse([]byte(`
name: private-workspaces
workspaces:
  shared:
    provider: git
    url: https://example.test/shared.git
    username: ${GIT_USER}
    password: ${GIT_PASSWORD}
agents:
  worker:
    workspace:
      provider: git
      url: https://example.test/agent.git
      token: ${GIT_TOKEN}
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	cliSpec, err := compose.Normalize(parsed, compose.NormalizeOptions{Env: map[string]string{
		"GIT_USER":     "git-user",
		"GIT_PASSWORD": "git-password",
		"GIT_TOKEN":    "git-token",
	}})
	if err != nil {
		t.Fatalf("CLI Normalize returned error: %v", err)
	}
	submittedHash, err := cliSpec.Hash()
	if err != nil {
		t.Fatalf("Hash returned error: %v", err)
	}
	wireSpec, err := api.ProjectSpecToProtoChecked(cliSpec)
	if err != nil {
		t.Fatalf("ProjectSpecToProtoChecked returned error: %v", err)
	}

	normalized, issues, err := normalizeProjectRequest(t.Context(), wireSpec, nil, submittedHash)
	if err != nil {
		t.Fatalf("normalizeProjectRequest returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("normalizeProjectRequest issues = %#v", issues)
	}
	if normalized.SpecHash != submittedHash {
		t.Fatalf("daemon spec hash = %q, want %q", normalized.SpecHash, submittedHash)
	}
	if workspace := normalized.Spec.Workspaces["shared"]; workspace.Username != "git-user" || workspace.Password != "git-password" {
		t.Fatalf("daemon shared workspace credentials = %#v", workspace)
	}
	if workspace := normalized.Spec.Agents[0].Workspace; workspace == nil || workspace.Token != "git-token" {
		t.Fatalf("daemon agent workspace = %#v", workspace)
	}

	redacted := normalizedSpecToProto(normalized.Spec)
	if workspace := redacted.GetWorkspaces()[0].GetWorkspace(); workspace.GetUsername() != "********" || workspace.GetPassword() != "********" {
		t.Fatalf("redacted shared workspace = %#v", workspace)
	}
	if got := redacted.GetAgents()[0].GetWorkspace().GetToken(); got != "********" {
		t.Fatalf("redacted agent workspace token = %q", got)
	}
}

func TestNormalizeProjectRequestAcceptsLegacyWorkspaceCredentialReferences(t *testing.T) {
	parsed, err := compose.Parse([]byte(`
name: private-workspace
workspaces:
  shared:
    provider: git
    url: https://example.test/shared.git
    token: ${GIT_TOKEN}
agents: {}
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	// A legacy persisted reference arriving at the daemon boundary must be
	// accepted (not rejected) so unrelated patches keep working. It is resolved
	// literally at clone time.
	wireSpec, err := api.ProjectSpecToProtoChecked(&compose.NormalizedProjectSpec{
		Name:       parsed.Name,
		Workspaces: parsed.Workspaces,
	})
	if err != nil {
		t.Fatalf("ProjectSpecToProtoChecked returned error: %v", err)
	}

	normalized, issues, err := normalizeProjectRequest(t.Context(), wireSpec, nil, "")
	if err != nil {
		t.Fatalf("normalizeProjectRequest returned error: %v", err)
	}
	if len(issues) != 0 {
		t.Fatalf("normalizeProjectRequest issues = %#v, want none for legacy reference", issues)
	}
	if got := normalized.Spec.Workspaces["shared"].Token; got != "${GIT_TOKEN}" {
		t.Fatalf("daemon workspace token = %q, want legacy reference preserved", got)
	}
}
