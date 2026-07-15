package compose

import (
	"strings"
	"testing"
)

func TestParseMCPConfig(t *testing.T) {
	spec, err := Parse([]byte(`
name: mcp-demo
mcp_servers:
  filesystem:
    type: local
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
    env:
      TOKEN:
        value: ${MCP_TOKEN}
        secret: true
  docs:
    type: remote
    transport: http
    url: https://docs.example.com/mcp
    headers:
      Authorization:
        value: Bearer ${DOCS_TOKEN}
        secret: true
agents:
  reviewer:
    provider: codex
    mcp_servers: [filesystem, docs]
  writer:
    provider: claude
    mcp_servers:
      - filesystem
      - name: notes
        type: local
        command: uvx
        args: [notes-server]
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	if len(spec.MCPServers) != 2 {
		t.Fatalf("mcp servers = %#v", spec.MCPServers)
	}
	if got := spec.MCPServers["filesystem"].Command; got != "npx" {
		t.Fatalf("filesystem command = %q", got)
	}
	if got := spec.Agents["reviewer"].MCPServers; len(got) != 2 || got[0].Ref != "filesystem" || got[1].Ref != "docs" {
		t.Fatalf("reviewer mcp servers = %#v", got)
	}
	if got := spec.Agents["writer"].MCPServers; len(got) != 2 || got[0].Ref != "filesystem" || got[1].Name != "notes" || got[1].Command != "uvx" {
		t.Fatalf("writer mcp servers = %#v", got)
	}
}

func TestParseRejectsLegacyMCPKey(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string
	}{
		{
			name: "project",
			raw: `
name: legacy-project-mcp
mcps:
  filesystem:
    type: local
    command: npx
`,
			wantField: "mcps",
		},
		{
			name: "agent",
			raw: `
name: legacy-agent-mcp
agents:
  reviewer:
    provider: codex
    mcps: [filesystem]
`,
			wantField: "agents.reviewer.mcps",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.raw))
			if err == nil {
				t.Fatalf("expected Parse to reject legacy mcps field")
			}
			if got := err.Error(); !strings.Contains(got, tt.wantField) || !strings.Contains(got, "unknown field") {
				t.Fatalf("error = %q, want unknown field %s", got, tt.wantField)
			}
		})
	}
}

func TestParseRejectsInvalidMCPRefType(t *testing.T) {
	spec, err := Parse([]byte(`
name: invalid-mcp-ref
agents:
  reviewer:
    provider: codex
    mcp_servers:
      name: docs
`))
	if err != nil {
		t.Fatalf("Parse returned error: %v", err)
	}
	_, err = Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, "agents.reviewer.mcp_servers[0].type") {
		t.Fatalf("error = %q, want mcp path", got)
	}
}

func TestNormalizeMCPConfig(t *testing.T) {
	spec := mustParseCompose(t, `
name: mcp-demo
mcp_servers:
  filesystem:
    type: local
    command: npx
    args: ["-y", "@modelcontextprotocol/server-filesystem", "/workspace"]
    env:
      TOKEN:
        value: ${MCP_TOKEN}
        secret: true
  docs:
    type: remote
    transport: http
    url: https://docs.example.com/${DOCS_PATH}
    headers:
      Authorization:
        value: Bearer ${DOCS_TOKEN}
        secret: true
agents:
  reviewer:
    provider: codex
    mcp_servers:
      - filesystem
      - docs
      - filesystem
      - name: notes
        type: local
        command: uvx
        args: [notes-server]
`)

	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{
		"MCP_TOKEN":  "mcp-secret",
		"DOCS_TOKEN": "docs-secret",
		"DOCS_PATH":  "mcp",
	}})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	if got := normalized.MCPServers["filesystem"].Env["TOKEN"]; got.Value != "mcp-secret" || !got.Secret {
		t.Fatalf("filesystem env = %#v", got)
	}
	if got := normalized.MCPServers["docs"].URL; got != "https://docs.example.com/mcp" {
		t.Fatalf("docs url = %q", got)
	}
	if got := normalized.Agents[0].MCPServers; len(got) != 3 || got["filesystem"].Command != "npx" || got["notes"].Command != "uvx" {
		t.Fatalf("agent mcp servers = %#v", got)
	}
}

func TestNormalizeRejectsUndefinedMCPRef(t *testing.T) {
	spec := mustParseCompose(t, `
name: undefined-mcp
mcp_servers:
  filesystem:
    type: local
    command: npx
agents:
  reviewer:
    provider: codex
    mcp_servers: [filesystem, missing]
`)

	_, err := Normalize(spec, NormalizeOptions{})
	if err == nil {
		t.Fatalf("expected Normalize to fail")
	}
	if got := err.Error(); !strings.Contains(got, `mcp "missing" is not defined`) {
		t.Fatalf("error = %q", got)
	}
}

func TestNormalizeRejectsInvalidMCPShape(t *testing.T) {
	tests := []struct {
		name      string
		raw       string
		wantField string
	}{
		{
			name: "local requires command",
			raw: `
name: bad-local
mcp_servers:
  filesystem:
    type: local
agents:
  reviewer:
    provider: codex
`,
			wantField: "mcp_servers.filesystem.command",
		},
		{
			name: "remote requires transport",
			raw: `
name: bad-remote
mcp_servers:
  docs:
    type: remote
    url: https://docs.example.com/mcp
agents:
  reviewer:
    provider: codex
`,
			wantField: "mcp_servers.docs.transport",
		},
		{
			name: "remote forbids env",
			raw: `
name: bad-remote-env
mcp_servers:
  docs:
    type: remote
    transport: http
    url: https://docs.example.com/mcp
    env:
      TOKEN: x
agents:
  reviewer:
    provider: codex
`,
			wantField: "mcp_servers.docs.env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := mustParseCompose(t, tt.raw)
			_, err := Normalize(spec, NormalizeOptions{})
			if err == nil {
				t.Fatalf("expected Normalize to fail")
			}
			if got := err.Error(); !strings.Contains(got, tt.wantField) {
				t.Fatalf("error = %q, want field %s", got, tt.wantField)
			}
		})
	}
}

func TestRedactedOutputDoesNotLeakMCPSecrets(t *testing.T) {
	spec := mustParseCompose(t, `
name: redacted-mcp
mcp_servers:
  docs:
    type: remote
    transport: http
    url: https://docs.example.com/mcp
    headers:
      Authorization:
        value: Bearer ${DOCS_TOKEN}
        secret: true
agents:
  reviewer:
    provider: codex
    mcp_servers: docs
`)
	normalized, err := Normalize(spec, NormalizeOptions{Env: map[string]string{"DOCS_TOKEN": "super-secret"}})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	data, err := normalized.MarshalCanonicalYAML(true)
	if err != nil {
		t.Fatalf("MarshalCanonicalYAML returned error: %v", err)
	}
	text := string(data)
	if strings.Contains(text, "super-secret") {
		t.Fatalf("redacted yaml leaked secret: %s", text)
	}
	if !strings.Contains(text, redactedEnvValue) || !strings.Contains(text, "mcp_servers:") {
		t.Fatalf("redacted yaml = %s", text)
	}
}

func TestCanonicalMCPJSONUsesMCPServersKey(t *testing.T) {
	spec := mustParseCompose(t, `
name: canonical-mcp
mcp_servers:
  filesystem:
    type: local
    command: npx
agents:
  reviewer:
    provider: codex
    mcp_servers: filesystem
`)
	normalized, err := Normalize(spec, NormalizeOptions{})
	if err != nil {
		t.Fatalf("Normalize returned error: %v", err)
	}
	data, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatalf("MarshalCanonicalJSON returned error: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, `"mcp_servers"`) || strings.Contains(text, `"mcps"`) {
		t.Fatalf("canonical json = %s", text)
	}
}
