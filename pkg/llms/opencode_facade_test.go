package llms

import (
	"encoding/json"
	"os"
	"path/filepath"
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
//
// The package assertions below are exact rather than substring matches: the
// whole reason chat completions is the right ingress is that these two specific
// ai-sdk packages post chat completions, so a test that cannot tell them apart
// (note "@ai-sdk/openai" is a prefix of "@ai-sdk/openai-compatible") would keep
// passing if a branch were switched to a responses-capable client — readmitting
// exactly the token/guest mismatch this pins down.
func TestOpenCodeGuestWireAPIMatchesTheClientOpenCodeIsGiven(t *testing.T) {
	if openCodeGuestWireAPI != APIProtocolChatCompletions {
		t.Fatalf("opencode guest wire api = %q, want %q", openCodeGuestWireAPI, APIProtocolChatCompletions)
	}
	for _, test := range []struct {
		providerID string
		wantNPM    string
	}{
		{providerID: "agent-compose", wantNPM: "@ai-sdk/openai-compatible"},
		{providerID: "openai", wantNPM: "@ai-sdk/openai"},
	} {
		t.Run(test.providerID, func(t *testing.T) {
			root := t.TempDir()
			sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-wire", WorkspacePath: filepath.Join(root, "workspace")}}
			if err := WriteOpenCodeRuntimeConfig(sandbox, test.providerID, "gpt-test", "http://facade.test/llm/openai/v1"); err != nil {
				t.Fatalf("WriteOpenCodeRuntimeConfig returned error: %v", err)
			}
			raw, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(sandbox), ".config", "opencode", "opencode.json"))
			if err != nil {
				t.Fatalf("read opencode config: %v", err)
			}
			var config struct {
				Provider map[string]struct {
					NPM     string `json:"npm"`
					Options struct {
						BaseURL string `json:"baseURL"`
					} `json:"options"`
				} `json:"provider"`
			}
			if err := json.Unmarshal(raw, &config); err != nil {
				t.Fatalf("decode opencode config: %v\n%s", err, raw)
			}
			entry, ok := config.Provider[test.providerID]
			if !ok {
				t.Fatalf("opencode config has no %q provider entry: %s", test.providerID, raw)
			}
			if entry.NPM != test.wantNPM {
				t.Fatalf("opencode provider npm = %q, want %q", entry.NPM, test.wantNPM)
			}
			// The guest is pointed at the facade's OpenAI base, from which
			// opencode derives /chat/completions — the path the token has to
			// authorise.
			if entry.Options.BaseURL != "http://facade.test/llm/openai/v1" {
				t.Fatalf("opencode provider baseURL = %q", entry.Options.BaseURL)
			}
		})
	}
}
