package llms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Every opencode OpenAI-family path must mint the same ingress protocol,
// because they all hand the guest the same kind of client. Before this was
// unified the three paths disagreed — the configured-provider path followed the
// upstream provider, the env-provider path pinned responses, and only the custom
// path matched what opencode actually sends — so a responses-only provider was
// unusable from opencode: its chat-completions request was rejected with
// "llm facade token wire api mismatch" before the facade could proxy it.
func TestOpenCodeGuestWireAPIMatchesTheClientOpenCodeIsGiven(t *testing.T) {
	if openCodeGuestWireAPI != APIProtocolChatCompletions {
		t.Fatalf("opencode guest wire api = %q, want %q", openCodeGuestWireAPI, APIProtocolChatCompletions)
	}
	// WriteOpenCodeRuntimeConfig picks the ai-sdk package the guest talks
	// through; both of its branches post chat completions, so neither can be
	// served by a responses-scoped token.
	for _, providerID := range []string{"agent-compose", "openai"} {
		root := t.TempDir()
		sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-wire", WorkspacePath: filepath.Join(root, "workspace")}}
		if err := WriteOpenCodeRuntimeConfig(sandbox, providerID, "gpt-test", "http://facade.test/llm/openai/v1"); err != nil {
			t.Fatalf("WriteOpenCodeRuntimeConfig(%q) returned error: %v", providerID, err)
		}
		raw, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(sandbox), ".config", "opencode", "opencode.json"))
		if err != nil {
			t.Fatalf("read opencode config for %q: %v", providerID, err)
		}
		if !strings.Contains(string(raw), "@ai-sdk/openai") {
			t.Fatalf("opencode config for %q does not name an ai-sdk package: %s", providerID, raw)
		}
	}
}
