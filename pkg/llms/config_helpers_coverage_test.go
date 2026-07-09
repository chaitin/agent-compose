package llms

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"agent-compose/pkg/compose"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

func TestRuntimeConfigAndEnvHelperWorkflows(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: filepath.Join(root, "workspace")}}
	if err := WriteCodexRuntimeConfig(session, "gpt", "http://runtime/openai/v1/", APIProtocolChatCompletions); err != nil {
		t.Fatalf("WriteCodexRuntimeConfig returned error: %v", err)
	}
	codexConfig, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(codexConfig), `wire_api = "chat_completions"`) || !strings.Contains(string(codexConfig), `AGENT_COMPOSE_SANDBOX_TOKEN`) {
		t.Fatalf("codex config=%q err=%v", string(codexConfig), err)
	}
	if err := WriteOpenCodeRuntimeConfig(session, "custom", "gpt-custom", "http://runtime/openai/v1/"); err != nil {
		t.Fatalf("WriteOpenCodeRuntimeConfig returned error: %v", err)
	}
	if err := WriteOpenCodeAnthropicRuntimeConfig(session, "claude", "http://runtime/anthropic/"); err != nil {
		t.Fatalf("WriteOpenCodeAnthropicRuntimeConfig returned error: %v", err)
	}
	openCodeConfig, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json"))
	if err != nil || !strings.Contains(string(openCodeConfig), "@ai-sdk/anthropic") || !strings.Contains(string(openCodeConfig), "AGENT_COMPOSE_SANDBOX_TOKEN") {
		t.Fatalf("opencode config=%q err=%v", string(openCodeConfig), err)
	}
	if err := WriteCodexRuntimeConfig(nil, "gpt", "http://runtime", ""); err != nil {
		t.Fatalf("nil session codex config returned error: %v", err)
	}
	mcps := map[string]compose.NormalizedMCPServerSpec{
		"filesystem": {Type: "local", Command: "npx", Args: []string{"-y", "server"}, Env: map[string]compose.EnvVarSpec{"TOKEN": {Value: "secret"}}},
		"docs":       {Type: "remote", Transport: "http", URL: "https://docs.example/mcp", Headers: map[string]compose.EnvVarSpec{"Authorization": {Value: "Bearer token"}}},
	}
	if err := WriteCodexMCPConfig(session, mcps); err != nil {
		t.Fatalf("WriteCodexMCPConfig returned error: %v", err)
	}
	codexConfig, err = os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(codexConfig), `[mcp_servers.filesystem]`) || !strings.Contains(string(codexConfig), `[mcp_servers.docs.http_headers]`) {
		t.Fatalf("codex mcp config=%q err=%v", string(codexConfig), err)
	}
	if err := WriteOpenCodeMCPConfig(session, mcps); err != nil {
		t.Fatalf("WriteOpenCodeMCPConfig returned error: %v", err)
	}
	openCodeConfig, err = os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json"))
	if err != nil || !strings.Contains(string(openCodeConfig), `"mcp"`) || !strings.Contains(string(openCodeConfig), `"filesystem"`) {
		t.Fatalf("opencode mcp config=%q err=%v", string(openCodeConfig), err)
	}
	if got := GuestOpenCodeConfigPath(&appconfig.Config{GuestHomePath: "/guest"}); got != "/root/.config/opencode/opencode.json" {
		t.Fatalf("GuestOpenCodeConfigPath = %q", got)
	}
	if base := GuestRuntimeBaseURL(&appconfig.Config{RuntimeBaseURL: " http://configured/ "}, session); base != "http://configured" {
		t.Fatalf("configured base = %q", base)
	}
	session.ProviderEnvItems = []domain.SandboxEnvVar{{Name: "AGENT_COMPOSE_RUNTIME_BASE_URL", Value: "http://provider"}}
	if base := GuestRuntimeBaseURL(&appconfig.Config{RuntimeBaseURL: "http://configured"}, session); base != "http://configured" {
		t.Fatalf("configured base with provider override = %q", base)
	}
	if base := GuestRuntimeBaseURL(&appconfig.Config{}, session); base != "http://provider" {
		t.Fatalf("provider fallback base = %q", base)
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
	filtered := FilterPersistedRuntimeEnv([]domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}, {Name: "AGENT_COMPOSE_RUNTIME_BASE_URL", Value: "http://runtime"}, {Name: "VISIBLE", Value: "1"}})
	if len(filtered) != 1 || filtered[0].Name != "VISIBLE" {
		t.Fatalf("filtered = %#v", filtered)
	}
	if env := RuntimeEnvMap([]domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}, {Name: "VISIBLE", Value: "1"}}); env["VISIBLE"] != "1" || env["OPENAI_API_KEY"] != "" {
		t.Fatalf("runtime env = %#v", env)
	}
	if env := ManagedRuntimeEnvMap([]domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}}); env["OPENAI_API_KEY"] != "secret" {
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

func TestE2ERuntimeConfigAndEnvHelperWorkflows(t *testing.T) {
	TestRuntimeConfigAndEnvHelperWorkflows(t)
}

func TestConfigHelperEdgeBranches(t *testing.T) {
	if got := parseStoredTime(nil); !got.IsZero() {
		t.Fatalf("nil stored time = %v, want zero", got)
	}
	if got := parseStoredTime(int64(1_700_000_000)); !got.Equal(time.Unix(1_700_000_000, 0).UTC()) {
		t.Fatalf("int64 stored time = %v", got)
	}
	if got := parseStoredTime(1_700_000_000); !got.Equal(time.Unix(1_700_000_000, 0).UTC()) {
		t.Fatalf("int stored time = %v", got)
	}
	if got := parseStoredTime(float64(1_700_000_000_000)); !got.Equal(time.UnixMilli(1_700_000_000_000).UTC()) {
		t.Fatalf("float stored time = %v", got)
	}
	if got := parseStoredTime([]byte("2026-07-01T02:03:04Z")); got.IsZero() || got.Location() != time.UTC {
		t.Fatalf("bytes stored time = %v", got)
	}
	if got := parseStoredTime("2026-07-01T02:03:04.000Z"); got.IsZero() {
		t.Fatalf("millisecond stored time = %v", got)
	}
	if got := parseStoredTime("not-time"); !got.IsZero() {
		t.Fatalf("invalid stored time = %v, want zero", got)
	}

	if got := NormalizeWireAPI("chat-completion"); got != APIProtocolChatCompletions {
		t.Fatalf("NormalizeWireAPI chat-completion = %q", got)
	}
	if got := NormalizeWireAPI("messages"); got != APIProtocolMessages {
		t.Fatalf("NormalizeWireAPI messages = %q", got)
	}
	if got := NormalizeWireAPI("custom-api"); got != "custom_api" {
		t.Fatalf("NormalizeWireAPI custom = %q", got)
	}
	if got := NormalizeAPIEndpointForProtocol("://bad", APIProtocolResponses); got != "://bad" {
		t.Fatalf("NormalizeAPIEndpointForProtocol invalid = %q", got)
	}
	if got := NormalizeAPIEndpointForProtocol("https://api.example.test/openai", APIProtocolChatCompletions); got != "https://api.example.test/openai/v1/chat/completions" {
		t.Fatalf("NormalizeAPIEndpointForProtocol openai chat = %q", got)
	}
	if got := NormalizeAPIEndpointForProtocol("https://api.example.test/v1", APIProtocolResponses); got != "https://api.example.test/v1/responses" {
		t.Fatalf("NormalizeAPIEndpointForProtocol responses v1 = %q", got)
	}
	if got := NormalizeAPIEndpointForProtocol("https://api.example.test/custom", APIProtocolResponses); got != "https://api.example.test/custom" {
		t.Fatalf("NormalizeAPIEndpointForProtocol custom path = %q", got)
	}

	if got := NormalizeAPIBaseURL("https://api.example.test/v1/responses/", APIProtocolResponses); got != "https://api.example.test/v1" {
		t.Fatalf("NormalizeAPIBaseURL responses = %q", got)
	}
	if got := NormalizeAPIBaseURL("https://api.example.test/v1/chat/completions/", APIProtocolChatCompletions); got != "https://api.example.test/v1" {
		t.Fatalf("NormalizeAPIBaseURL chat = %q", got)
	}
	if got := NormalizeAPIBaseURL("://bad", APIProtocolResponses); got != "://bad" {
		t.Fatalf("NormalizeAPIBaseURL invalid = %q", got)
	}
	if got := NormalizeAnthropicAPIBaseURL("https://api.anthropic.test/v1/messages/"); got != "https://api.anthropic.test/v1" {
		t.Fatalf("NormalizeAnthropicAPIBaseURL messages = %q", got)
	}
	if got := NormalizeAnthropicAPIBaseURL("https://api.anthropic.test"); got != "https://api.anthropic.test/v1" {
		t.Fatalf("NormalizeAnthropicAPIBaseURL root = %q", got)
	}
	if got := NormalizeAnthropicAPIBaseURL("://bad"); got != "://bad" {
		t.Fatalf("NormalizeAnthropicAPIBaseURL invalid = %q", got)
	}

	if got := EndpointForProvider(Provider{ProviderType: ProviderFamilyAnthropic, BaseURL: "://bad"}, APIProtocolMessages); got != "://bad/messages" {
		t.Fatalf("EndpointForProvider anthropic invalid = %q", got)
	}
	if got := EndpointForProvider(Provider{ProviderType: ProviderFamilyOpenAI, BaseURL: "https://api.example.test/openai", Scope: ProviderScopeSystem}, APIProtocolResponses); got != "https://api.example.test/openai/v1/responses" {
		t.Fatalf("EndpointForProvider configured = %q", got)
	}
	if got := EndpointForProvider(Provider{ProviderType: ProviderFamilyOpenAI, BaseURL: "https://api.example.test/openai", Scope: ProviderScopeEnvDefault}, APIProtocolResponses); got != "https://api.example.test/openai/v1/responses" {
		t.Fatalf("EndpointForProvider env default = %q", got)
	}

	headers, err := ProviderForwardHeaders(Provider{
		HeadersJSON: `{"X-Test":"1","Authorization":"bad","Content-Type":"application/json"}`,
		APIKey:      "secret",
		AuthHeader:  "X-Api-Key",
	})
	if err != nil {
		t.Fatalf("ProviderForwardHeaders returned error: %v", err)
	}
	if headers.Get("X-Test") != "1" || headers.Get("Authorization") != "" || headers.Get("Content-Type") != "" || headers.Get("X-Api-Key") != "secret" {
		t.Fatalf("headers = %#v", headers)
	}
	if _, err := ProviderForwardHeaders(Provider{HeadersJSON: "{broken"}); err == nil {
		t.Fatal("ProviderForwardHeaders returned nil for invalid JSON")
	}
	if !ForbiddenProviderHeader(" proxy-authorization ", "Authorization") ||
		!ForbiddenProviderHeader("Host", "Authorization") ||
		!ForbiddenProviderHeader("", "Authorization") ||
		ForbiddenProviderHeader("X-Allowed", "Authorization") {
		t.Fatal("ForbiddenProviderHeader returned unexpected values")
	}

	if got := AppendAPIEndpointToBaseURL("", APIProtocolResponses); got != "" {
		t.Fatalf("AppendAPIEndpointToBaseURL empty = %q", got)
	}
	if got := AppendAPIEndpointToBaseURL("://bad", APIProtocolChatCompletions); got != "://bad/v1/chat/completions" {
		t.Fatalf("AppendAPIEndpointToBaseURL invalid chat = %q", got)
	}
	if got := AppendAPIEndpointToBaseURL("https://api.example.test/v1", APIProtocolChatCompletions); got != "https://api.example.test/v1/chat/completions" {
		t.Fatalf("AppendAPIEndpointToBaseURL chat v1 = %q", got)
	}
	if got := AppendAPIEndpointToBaseURL("https://api.example.test/base", APIProtocolResponses); got != "https://api.example.test/base/v1/responses" {
		t.Fatalf("AppendAPIEndpointToBaseURL base responses = %q", got)
	}
	joinAPIBasePath(nil, "/v1", "responses")
}

func TestClientConfigAndSelectionWorkflows(t *testing.T) {
	ctx := context.Background()
	store := llmCoverageEnvStore{items: []domain.SandboxEnvVar{{Name: "LLM_API_ENDPOINT", Value: "https://example.test"}, {Name: "LLM_API_PROTOCOL", Value: "chat"}}}
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

func TestE2EClientConfigAndSelectionWorkflows(t *testing.T) {
	TestClientConfigAndSelectionWorkflows(t)
}

type llmCoverageEnvStore struct {
	items []domain.SandboxEnvVar
}

func (s llmCoverageEnvStore) ListGlobalEnv(context.Context) ([]domain.SandboxEnvVar, error) {
	return s.items, nil
}

type llmCoverageWireStore struct {
	ok      bool
	wireAPI string
}

func (s llmCoverageWireStore) LLMProviderModelWireAPI(context.Context, string, string) (string, bool, error) {
	return s.wireAPI, s.ok, nil
}
