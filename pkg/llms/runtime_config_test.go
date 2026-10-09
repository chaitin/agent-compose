package llms

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestWriteCodexRuntimeConfigRendersRetryPolicy(t *testing.T) {
	root := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: filepath.Join(root, "workspace")}}
	policy := CodexRuntimePolicy{RequestMaxRetries: 0, StreamMaxRetries: 2, StreamIdleTimeout: 1500 * time.Millisecond}
	if err := WriteCodexRuntimeConfig(sandbox, CodexRuntimeConfig{Model: "gpt-test", BaseURL: "http://runtime/openai/v1/", WireAPI: APIProtocolResponses, Policy: policy}); err != nil {
		t.Fatalf("WriteCodexRuntimeConfig returned error: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(sandbox), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex runtime config: %v", err)
	}
	config := string(data)
	for _, want := range []string{
		`request_max_retries = 0`,
		`stream_max_retries = 2`,
		`stream_idle_timeout_ms = 1500`,
	} {
		if !strings.Contains(config, want) {
			t.Fatalf("Codex runtime config %q does not contain %q", config, want)
		}
	}
}

func TestCodexRuntimePolicyFromConfigUsesDefaultsAndBoundsValues(t *testing.T) {
	defaults := CodexRuntimePolicyFromConfig(nil)
	if defaults.RequestMaxRetries != 1 || defaults.StreamMaxRetries != 1 || defaults.StreamIdleTimeout != time.Minute {
		t.Fatalf("default Codex runtime policy = %#v", defaults)
	}
	bounded := CodexRuntimePolicyFromConfig(&appconfig.Config{
		CodexRequestMaxRetries: 101,
		CodexStreamMaxRetries:  200,
		LLMTimeout:             3 * time.Second,
	})
	if bounded.RequestMaxRetries != 100 || bounded.StreamMaxRetries != 100 || bounded.StreamIdleTimeout != 3*time.Second {
		t.Fatalf("bounded Codex runtime policy = %#v", bounded)
	}
}

// Starting a sandbox writes the project's managed MCP block into the codex
// configuration, and an interactive prompt attach refreshes the codex
// configuration of that same file on every turn. The refresh replaces the whole
// file, so it has to carry the block across; otherwise the guest starts codex
// without the MCP servers the project declared.
func TestWriteCodexRuntimeConfigKeepsManagedMCPBlock(t *testing.T) {
	root := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: filepath.Join(root, "workspace")}}
	seed := CodexRuntimeConfig{Model: "gpt-first", BaseURL: "http://runtime/openai/v1", WireAPI: APIProtocolResponses, Policy: CodexRuntimePolicyFromConfig(nil)}
	if err := WriteCodexRuntimeConfig(sandbox, seed); err != nil {
		t.Fatalf("seed codex runtime config: %v", err)
	}
	if err := WriteCodexMCPConfig(context.Background(), &appconfig.Config{}, sandbox, map[string]compose.NormalizedMCPServerSpec{
		"docs": {Type: "remote", Transport: "http", URL: "https://docs.example/mcp"},
	}, nil); err != nil {
		t.Fatalf("seed codex mcp config: %v", err)
	}
	refresh := seed
	refresh.Model = "gpt-second"
	if err := WriteCodexRuntimeConfig(sandbox, refresh); err != nil {
		t.Fatalf("refresh codex runtime config: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(sandbox), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read Codex runtime config: %v", err)
	}
	config := string(data)
	if !strings.Contains(config, `model = "gpt-second"`) {
		t.Fatalf("Codex runtime config did not refresh the model: %s", config)
	}
	if !strings.Contains(config, "[mcp_servers.docs]") || !strings.Contains(config, `url = "https://docs.example/mcp"`) {
		t.Fatalf("Codex runtime config dropped the managed mcp block: %s", config)
	}
	if strings.Count(config, codexManagedMCPStart) != 1 || strings.Count(config, codexManagedMCPEnd) != 1 {
		t.Fatalf("Codex runtime config did not keep exactly one managed mcp block: %s", config)
	}
}

// The same order exists for opencode, which keeps both its facade provider and
// its MCP servers in one JSON document.
func TestWriteOpenCodeRuntimeConfigKeepsManagedMCP(t *testing.T) {
	root := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: filepath.Join(root, "workspace")}}
	if err := WriteOpenCodeRuntimeConfig(sandbox, ProtocolChatCompletions, "first-model", "http://runtime/openai/v1", guestFacadeTokenEnvName); err != nil {
		t.Fatalf("seed opencode runtime config: %v", err)
	}
	if err := WriteOpenCodeMCPConfig(context.Background(), &appconfig.Config{}, sandbox, map[string]compose.NormalizedMCPServerSpec{
		"docs": {Type: "remote", Transport: "http", URL: "https://docs.example/mcp"},
	}, nil); err != nil {
		t.Fatalf("seed opencode mcp config: %v", err)
	}
	if err := WriteOpenCodeRuntimeConfig(sandbox, ProtocolChatCompletions, "second-model", "http://runtime/openai/v1", guestFacadeTokenEnvName); err != nil {
		t.Fatalf("refresh opencode runtime config: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(sandbox), ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode runtime config: %v", err)
	}
	var config map[string]any
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("decode opencode runtime config %s: %v", data, err)
	}
	provider, _ := config["provider"].(map[string]any)
	if _, ok := provider[GuestProviderAgentCompose].(map[string]any); !ok {
		t.Fatalf("opencode runtime config did not refresh the facade provider: %s", data)
	}
	mcp, _ := config["mcp"].(map[string]any)
	docs, _ := mcp["docs"].(map[string]any)
	if docs["url"] != "https://docs.example/mcp" {
		t.Fatalf("opencode runtime config dropped the managed mcp section: %s", data)
	}
}
