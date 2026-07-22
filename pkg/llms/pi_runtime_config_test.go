package llms

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

func TestSplitPiModel(t *testing.T) {
	provider, model, err := SplitPiModel(" custom/model/variant ")
	if err != nil || provider != "custom" || model != "model/variant" {
		t.Fatalf("SplitPiModel provider=%q model=%q err=%v", provider, model, err)
	}
	for _, invalid := range []string{"", "model", "/model", "provider/"} {
		if _, _, err := SplitPiModel(invalid); err == nil {
			t.Fatalf("SplitPiModel(%q) succeeded", invalid)
		}
	}
}

func TestWritePiRuntimeConfigIsPrivateAndContainsNoToken(t *testing.T) {
	root := t.TempDir()
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID: "pi-config", Driver: driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", "pi-config", "workspace"),
	}}
	if err := WritePiRuntimeConfig(sandbox, "gpt-test", "http://runtime/openai/v1/", "openai-responses"); err != nil {
		t.Fatalf("WritePiRuntimeConfig returned error: %v", err)
	}
	path := filepath.Join(execution.HostSandboxHome(sandbox), ".pi", "agent", "models.json")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat models.json: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("models.json mode = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read models.json: %v", err)
	}
	if strings.Contains(string(data), "ac_llm_") {
		t.Fatalf("models.json contains a facade token: %s", data)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode models.json: %v", err)
	}
	if !strings.Contains(string(data), "$AGENT_COMPOSE_SANDBOX_TOKEN") || !strings.Contains(string(data), "openai-responses") {
		t.Fatalf("models.json = %s", data)
	}
}

func TestPiFacadeProtocol(t *testing.T) {
	tests := []struct {
		name       string
		target     ResolvedTarget
		wantAPI    string
		wantWire   string
		wantSuffix string
	}{
		{name: "responses", target: ResolvedTarget{Provider: Provider{ProviderType: ProviderFamilyOpenAI}, WireAPI: APIProtocolResponses}, wantAPI: "openai-responses", wantWire: APIProtocolResponses, wantSuffix: "/llm/openai/v1"},
		{name: "chat completions", target: ResolvedTarget{Provider: Provider{ProviderType: ProviderFamilyOpenAI}, WireAPI: APIProtocolChatCompletions}, wantAPI: "openai-completions", wantWire: APIProtocolChatCompletions, wantSuffix: "/llm/openai/v1"},
		{name: "messages", target: ResolvedTarget{Provider: Provider{ProviderType: ProviderFamilyAnthropic}, WireAPI: APIProtocolMessages}, wantAPI: "anthropic-messages", wantWire: APIProtocolMessages, wantSuffix: "/llm/anthropic/v1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			api, wire, baseURL, err := piFacadeProtocol(test.target, "http://runtime/", "sandbox")
			if err != nil {
				t.Fatalf("piFacadeProtocol returned error: %v", err)
			}
			if api != test.wantAPI || wire != test.wantWire || !strings.HasSuffix(baseURL, test.wantSuffix) {
				t.Fatalf("piFacadeProtocol = (%q, %q, %q)", api, wire, baseURL)
			}
		})
	}
}
