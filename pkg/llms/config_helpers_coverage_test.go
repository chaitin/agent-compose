package llms

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

func TestRuntimeConfigAndEnvHelperWorkflows(t *testing.T) {
	root := t.TempDir()
	session := &domain.Session{Summary: domain.SessionSummary{ID: "session-1", WorkspacePath: filepath.Join(root, "workspace")}}
	if err := WriteCodexRuntimeConfig(session, "gpt", "http://runtime/openai/v1/", APIProtocolChatCompletions); err != nil {
		t.Fatalf("WriteCodexRuntimeConfig returned error: %v", err)
	}
	codexConfig, err := os.ReadFile(filepath.Join(execution.HostSessionHome(session), ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(codexConfig), `wire_api = "chat_completions"`) {
		t.Fatalf("codex config=%q err=%v", string(codexConfig), err)
	}
	if err := WriteOpenCodeRuntimeConfig(session, "custom", "gpt-custom", "http://runtime/openai/v1/"); err != nil {
		t.Fatalf("WriteOpenCodeRuntimeConfig returned error: %v", err)
	}
	if err := WriteOpenCodeAnthropicRuntimeConfig(session, "claude", "http://runtime/anthropic/"); err != nil {
		t.Fatalf("WriteOpenCodeAnthropicRuntimeConfig returned error: %v", err)
	}
	openCodeConfig, err := os.ReadFile(filepath.Join(execution.HostSessionHome(session), ".config", "opencode", "opencode.json"))
	if err != nil || !strings.Contains(string(openCodeConfig), "@ai-sdk/anthropic") {
		t.Fatalf("opencode config=%q err=%v", string(openCodeConfig), err)
	}
	if err := WriteCodexRuntimeConfig(nil, "gpt", "http://runtime", ""); err != nil {
		t.Fatalf("nil session codex config returned error: %v", err)
	}
	if got := GuestOpenCodeConfigPath(&appconfig.Config{GuestHomePath: "/guest"}); got != "/root/.config/opencode/opencode.json" {
		t.Fatalf("GuestOpenCodeConfigPath = %q", got)
	}
	if base := GuestRuntimeBaseURL(&appconfig.Config{RuntimeBaseURL: " http://configured/ "}, session); base != "http://configured" {
		t.Fatalf("configured base = %q", base)
	}
	session.ProviderEnvItems = []domain.SessionEnvVar{{Name: "AGENT_COMPOSE_RUNTIME_BASE_URL", Value: "http://provider"}}
	if base := GuestRuntimeBaseURL(&appconfig.Config{RuntimeBaseURL: "http://configured"}, session); base != "http://provider" {
		t.Fatalf("provider base = %q", base)
	}
	session.ProviderEnvItems = nil
	if base := GuestRuntimeBaseURL(&appconfig.Config{HttpListen: "0.0.0.0:7410"}, session); base != "http://127.0.0.1:7410" {
		t.Fatalf("listen base = %q", base)
	}
	session.Summary.Driver = "docker"
	if base := GuestRuntimeBaseURL(&appconfig.Config{HttpListen: "127.0.0.1:7410"}, session); base != "" {
		t.Fatalf("docker localhost base = %q", base)
	}

	if provider, model := LoaderCommandFacadeAgentModel(map[string]string{"AGENT_PROVIDER": "claude", "CLAUDE_MODEL": "sonnet"}); provider != "claude" || model != "sonnet" {
		t.Fatalf("claude model provider=%q model=%q", provider, model)
	}
	if provider, model := LoaderCommandFacadeAgentModel(map[string]string{"AGENT_PROVIDER": "opencode"}); provider != "" || model != "" {
		t.Fatalf("opencode missing model provider=%q model=%q", provider, model)
	}
	filtered := FilterPersistedRuntimeEnv([]domain.SessionEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}, {Name: "VISIBLE", Value: "1"}})
	if len(filtered) != 1 || filtered[0].Name != "VISIBLE" {
		t.Fatalf("filtered = %#v", filtered)
	}
	if env := RuntimeEnvMap([]domain.SessionEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}, {Name: "VISIBLE", Value: "1"}}); env["VISIBLE"] != "1" || env["OPENAI_API_KEY"] != "" {
		t.Fatalf("runtime env = %#v", env)
	}
	if env := ManagedRuntimeEnvMap([]domain.SessionEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}}); env["OPENAI_API_KEY"] != "secret" {
		t.Fatalf("managed env = %#v", env)
	}
	if provider, model, err := SplitOpenCodeModel(" custom/gpt "); err != nil || provider != "custom" || model != "gpt" {
		t.Fatalf("SplitOpenCodeModel provider=%q model=%q err=%v", provider, model, err)
	}
	if _, _, err := SplitOpenCodeModel("bad"); err == nil {
		t.Fatalf("expected invalid opencode model error")
	}
	if got := NormalizeAPIEndpoint("https://api.example.test/openai"); got != "https://api.example.test/openai/v1/responses" {
		t.Fatalf("NormalizeAPIEndpoint = %q", got)
	}
	if got := NormalizeAPIEndpointForProtocol("https://api.example.test/openai/v1", APIProtocolChatCompletions); got != "https://api.example.test/openai/v1/chat/completions" {
		t.Fatalf("NormalizeAPIEndpointForProtocol chat = %q", got)
	}
	if got := NormalizeAPIEndpointForProtocol("https://api.example.test", APIProtocolChatCompletions); got != "https://api.example.test/v1/chat/completions" {
		t.Fatalf("NormalizeAPIEndpointForProtocol root = %q", got)
	}
	merged := MergeManagedExecEnv(map[string]string{"OPENAI_API_KEY": "secret", "A": "1"}, map[string]string{"B": "2"})
	if merged["OPENAI_API_KEY"] != "" || merged["A"] != "1" || merged["B"] != "2" {
		t.Fatalf("merged env = %#v", merged)
	}
	if items := EnvItemsFromMap(map[string]string{"B": "2", "A": "1"}, true); len(items) != 2 || !items[0].Secret || items[0].Name != "A" {
		t.Fatalf("items = %#v", items)
	}
}

func TestClientConfigAndSelectionWorkflows(t *testing.T) {
	ctx := context.Background()
	store := llmCoverageEnvStore{items: []domain.SessionEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://example.test"}, {Name: "LLM_API_PROTOCOL", Value: "chat"}}}
	if got := ResolveProtocol(ctx, store, ClientConfig{}); got != APIProtocolChatCompletions {
		t.Fatalf("ResolveProtocol = %q", got)
	}
	if got := ResolveEndpoint(ctx, store, ClientConfig{}); !strings.Contains(got, "chat/completions") {
		t.Fatalf("ResolveEndpoint = %q", got)
	}
	t.Setenv("LLM_API_ENDPOINT", "https://env.test")
	if got := ResolveEndpoint(ctx, nil, ClientConfig{Protocol: APIProtocolResponses}); got != "https://env.test/v1/responses" {
		t.Fatalf("env endpoint = %q", got)
	}
	if got := ResolveSetting(ctx, nil, "fallback", "MISSING_SETTING"); got != "fallback" {
		t.Fatalf("ResolveSetting fallback = %q", got)
	}

	models := []Model{{ID: "m1", Name: "gpt-1"}, {ID: "m2", Name: "gpt-2", DefaultModel: true}}
	providers := []Provider{{ID: "p2", ProviderType: ProviderFamilyOpenAI, Scope: ProviderScopeEnvDefault, Weight: 10}, {ID: "p1", ProviderType: ProviderFamilyOpenAI, Weight: 1}}
	selected, provider, wireAPI, ok, err := SelectModelAndProvider(ctx, llmCoverageWireStore{ok: true, wireAPI: APIProtocolResponses}, models, providers, "", ProviderFamilyOpenAI, "")
	if err != nil || !ok || selected.ID != "m2" || provider.ID != "p1" || wireAPI != APIProtocolResponses {
		t.Fatalf("selected=%#v provider=%#v wire=%q ok=%v err=%v", selected, provider, wireAPI, ok, err)
	}
	if _, _, _, ok, err := SelectModelAndProvider(ctx, llmCoverageWireStore{}, models, providers, "missing", "", ""); err != nil || ok {
		t.Fatalf("expected missing model ok=false err=%v", err)
	}
	if priority := ProviderSelectionPriority(ProviderScopeSessionEnv); priority != 2 {
		t.Fatalf("session env priority = %d", priority)
	}
}

type llmCoverageEnvStore struct {
	items []domain.SessionEnvVar
}

func (s llmCoverageEnvStore) ListGlobalEnv(context.Context) ([]domain.SessionEnvVar, error) {
	return s.items, nil
}

type llmCoverageWireStore struct {
	ok      bool
	wireAPI string
}

func (s llmCoverageWireStore) LLMProviderModelWireAPI(context.Context, string, string) (string, bool, error) {
	return s.wireAPI, s.ok, nil
}
