package compose

import (
	"bytes"

	"testing"
)

func TestNormalizeResolvesGitWorkspaceCredentials(t *testing.T) {
	spec := mustParseCompose(t, `
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
`)

	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{
		"GIT_USER":     "git-user",
		"GIT_PASSWORD": "git-password",
		"GIT_TOKEN":    "git-token",
	}})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if workspace := normalized.Workspaces["shared"]; workspace.Username != "git-user" || workspace.Password != "git-password" {
		t.Fatalf("shared workspace credentials = %#v", workspace)
	}
	if workspace := normalized.Agents[0].Workspace; workspace == nil || workspace.Token != "git-token" {
		t.Fatalf("agent workspace = %#v", workspace)
	}

	redacted, err := normalized.MarshalCanonicalJSON(true)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	for _, secret := range []string{"git-user", "git-password", "git-token"} {
		if bytes.Contains(redacted, []byte(secret)) {
			t.Fatalf("redacted output leaked workspace credential %q: %s", secret, redacted)
		}
	}
	if got := bytes.Count(redacted, []byte(redactedSourceCredential)); got != 3 {
		t.Fatalf("redacted marker count = %d, want 3: %s", got, redacted)
	}
}

func TestNormalizeGitWorkspaceKeepsUnresolvedCredentialReference(t *testing.T) {
	spec := mustParseCompose(t, `
name: private-workspace
workspaces:
  shared:
    provider: git
    url: https://example.test/shared.git
    token: ${GIT_TOKEN}
agents: {}
`)

	// A missing environment variable must not fail authoring normalization:
	// the reference is kept as literal data for subsequent consumers.
	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{}})
	if err != nil {
		t.Fatalf("Normalize returned error for missing credential env: %v", err)
	}
	if got := normalized.Workspaces["shared"].Token; got != "${GIT_TOKEN}" {
		t.Fatalf("workspace token = %q, want unresolved reference kept", got)
	}
}

func TestNormalizeAcceptsResolvedGitWorkspaceCredentials(t *testing.T) {
	spec := mustParseCompose(t, `
name: private-workspaces
workspaces:
  shared:
    provider: git
    url: https://example.test/shared.git
    username: git-user
    password: git-password
agents:
  worker:
    workspace:
      provider: git
      url: https://example.test/agent.git
      token: git-token
`)

	normalized, err := Normalize(spec, NormalizeOptions{SourceCredentials: SourceCredentialsResolved})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if workspace := normalized.Workspaces["shared"]; workspace.Username != "git-user" || workspace.Password != "git-password" {
		t.Fatalf("shared workspace credentials = %#v", workspace)
	}
	if workspace := normalized.Agents[0].Workspace; workspace == nil || workspace.Token != "git-token" {
		t.Fatalf("agent workspace = %#v", workspace)
	}
}

func TestNormalizeResolvedAcceptsEnvironmentReferenceCredentials(t *testing.T) {
	spec := mustParseCompose(t, `
name: private-workspace
workspaces:
  shared:
    provider: git
    url: https://example.test/shared.git
    token: ${GIT_TOKEN}
agents: {}
`)

	// Resolved mode must accept legacy persisted environment references so
	// unrelated patches keep working for projects created before resolution.
	normalized, err := Normalize(spec, NormalizeOptions{
		Env:               map[string]string{"GIT_TOKEN": "daemon-token"},
		SourceCredentials: SourceCredentialsResolved,
	})
	if err != nil {
		t.Fatalf("Normalize returned error for legacy reference: %v", err)
	}
	if got := normalized.Workspaces["shared"].Token; got != "${GIT_TOKEN}" {
		t.Fatalf("workspace token = %q, want legacy reference preserved", got)
	}
}

func TestNormalizeCLIAndDaemonHashMatchWhenReferenceKept(t *testing.T) {
	raw := `
name: probe
workspaces:
  shared:
    provider: git
    url: https://example.test/shared.git
    token: ${GIT_TOKEN}
agents: {}
`
	parsedCLI, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	cliNormalized, err := Normalize(parsedCLI, NormalizeOptions{Env: map[string]string{}})
	if err != nil {
		t.Fatalf("CLI Normalize returned error: %v", err)
	}
	cliHash, err := cliNormalized.Hash()
	if err != nil {
		t.Fatalf("CLI Hash returned error: %v", err)
	}

	parsedDaemon, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	daemonNormalized, err := Normalize(parsedDaemon, NormalizeOptions{SourceCredentials: SourceCredentialsResolved})
	if err != nil {
		t.Fatalf("daemon Normalize returned error: %v", err)
	}
	daemonHash, err := daemonNormalized.Hash()
	if err != nil {
		t.Fatalf("daemon Hash returned error: %v", err)
	}
	if cliHash != daemonHash {
		t.Fatalf("CLI hash %s != daemon hash %s; kept reference must round-trip", cliHash, daemonHash)
	}
}
