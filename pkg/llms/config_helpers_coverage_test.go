package llms

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRuntimeConfigAndEnvHelperWorkflows(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox-1", WorkspacePath: filepath.Join(root, "workspace")}}
	policy := CodexRuntimePolicyFromConfig(nil)
	if err := WriteCodexRuntimeConfig(session, CodexRuntimeConfig{Model: "gpt", BaseURL: "http://runtime/openai/v1/", WireAPI: APIProtocolChatCompletions, Policy: policy}); err == nil {
		t.Fatal("WriteCodexRuntimeConfig accepted unsupported Chat Completions")
	}
	if err := WriteCodexRuntimeConfig(session, CodexRuntimeConfig{Model: "gpt", BaseURL: "http://runtime/openai/v1/", WireAPI: APIProtocolResponses, Policy: policy}); err != nil {
		t.Fatalf("WriteCodexRuntimeConfig returned error: %v", err)
	}
	codexConfig, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil || !strings.Contains(string(codexConfig), `wire_api = "responses"`) || !strings.Contains(string(codexConfig), `AGENT_COMPOSE_SANDBOX_TOKEN`) {
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
	if err := WriteCodexRuntimeConfig(nil, CodexRuntimeConfig{Model: "gpt", BaseURL: "http://runtime", Policy: CodexRuntimePolicyFromConfig(nil)}); err != nil {
		t.Fatalf("nil session codex config returned error: %v", err)
	}
	mcps := map[string]compose.NormalizedMCPServerSpec{
		"filesystem": {Type: "local", Command: "npx", Args: []string{"-y", "server"}, Env: map[string]compose.EnvVarSpec{"TOKEN": {Value: "secret"}}},
		"docs":       {Type: "remote", Transport: "http", URL: "https://docs.example/mcp", Headers: map[string]compose.EnvVarSpec{"Authorization": {Value: "Bearer token"}}},
	}
	if err := WriteCodexMCPConfig(context.Background(), &appconfig.Config{}, session, mcps, nil); err != nil {
		t.Fatalf("WriteCodexMCPConfig returned error: %v", err)
	}
	if err := WriteCodexMCPConfig(context.Background(), &appconfig.Config{}, session, mcps, nil); err != nil {
		t.Fatalf("second WriteCodexMCPConfig returned error: %v", err)
	}
	configPath := filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")
	if err := os.WriteFile(configPath, []byte("model = \"gpt\"\n\n"+codexManagedMCPStart+"\n[mcp_servers.stale]\ncommand = \"old\"\n"), 0o644); err != nil {
		t.Fatalf("seed malformed codex mcp config: %v", err)
	}
	if err := WriteCodexMCPConfig(context.Background(), &appconfig.Config{}, session, mcps, nil); err != nil {
		t.Fatalf("rewrite malformed WriteCodexMCPConfig returned error: %v", err)
	}
	codexConfig, err = os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil || strings.Count(string(codexConfig), `[mcp_servers.filesystem]`) != 1 || !strings.Contains(string(codexConfig), `[mcp_servers.docs.http_headers]`) || strings.Contains(string(codexConfig), `[mcp_servers.stale]`) {
		t.Fatalf("codex mcp config=%q err=%v", string(codexConfig), err)
	}
	if err := WriteOpenCodeMCPConfig(context.Background(), &appconfig.Config{}, session, mcps, nil); err != nil {
		t.Fatalf("WriteOpenCodeMCPConfig returned error: %v", err)
	}
	openCodeConfig, err = os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json"))
	if err != nil || !strings.Contains(string(openCodeConfig), `"mcp"`) || !strings.Contains(string(openCodeConfig), `"filesystem"`) {
		t.Fatalf("opencode mcp config=%q err=%v", string(openCodeConfig), err)
	}
	if err := WriteCodexMCPConfig(context.Background(), &appconfig.Config{}, session, nil, nil); err != nil {
		t.Fatalf("clear WriteCodexMCPConfig returned error: %v", err)
	}
	codexConfig, err = os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil || strings.Contains(string(codexConfig), `[mcp_servers.`) {
		t.Fatalf("cleared codex mcp config=%q err=%v", string(codexConfig), err)
	}
	if err := WriteOpenCodeMCPConfig(context.Background(), &appconfig.Config{}, session, nil, nil); err != nil {
		t.Fatalf("clear WriteOpenCodeMCPConfig returned error: %v", err)
	}
	openCodeConfig, err = os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json"))
	if err != nil || strings.Contains(string(openCodeConfig), `"mcp"`) == true {
		t.Fatalf("cleared opencode mcp config=%q err=%v", string(openCodeConfig), err)
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

	if provider, model := SchedulerCommandFacadeAgentModel(map[string]string{"AGENT_PROVIDER": "claude", "CLAUDE_MODEL": "sonnet"}); provider != "claude" || model != "sonnet" {
		t.Fatalf("claude model provider=%q model=%q", provider, model)
	}
	if provider, model := SchedulerCommandFacadeAgentModel(map[string]string{"AGENT_PROVIDER": "opencode"}); provider != "" || model != "" {
		t.Fatalf("opencode missing model provider=%q model=%q", provider, model)
	}
	filtered := FilterPersistedRuntimeEnv([]domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}, {Name: "LLM_API_HEADERS", Value: `{"X-Token":"secret"}`}, {Name: "AGENT_COMPOSE_RUNTIME_BASE_URL", Value: "http://runtime"}, {Name: "VISIBLE", Value: "1"}})
	if len(filtered) != 1 || filtered[0].Name != "VISIBLE" {
		t.Fatalf("filtered = %#v", filtered)
	}
	if env := RuntimeEnvMap([]domain.SandboxEnvVar{{Name: "OPENAI_API_KEY", Value: "secret"}, {Name: "VISIBLE", Value: "1"}}); env["VISIBLE"] != "1" || env["OPENAI_API_KEY"] != "" {
		t.Fatalf("runtime env = %#v", env)
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

// No-shared-mount path (k8s - see docs/design/k8s_pod_runtime_driver_design.md
// §2.1): both codex and opencode MCP config still merge against the
// daemon's own local copy of the file, but must also push the resulting
// merged content to the guest path a mount would otherwise have made it
// appear at for free.
func TestWriteCodexAndOpenCodeMCPConfigPushToGuest(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	config := &appconfig.Config{}
	mcps := map[string]compose.NormalizedMCPServerSpec{
		"filesystem": {Type: "local", Command: "npx", Args: []string{"-y", "server"}},
	}

	var codexPushedPath string
	var codexPushedContent []byte
	if err := WriteCodexMCPConfig(context.Background(), config, session, mcps, func(_ context.Context, guestPath string, content []byte) error {
		codexPushedPath = guestPath
		codexPushedContent = content
		return nil
	}); err != nil {
		t.Fatalf("WriteCodexMCPConfig returned error: %v", err)
	}
	appconfig.ApplyDefaultGuestPaths(config)
	wantCodexPath := filepath.Join(config.GuestHomePath, ".codex", "config.toml")
	if codexPushedPath != wantCodexPath {
		t.Fatalf("codex pushed path = %q, want %q", codexPushedPath, wantCodexPath)
	}
	localCodexConfig, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml"))
	if err != nil {
		t.Fatalf("read local codex config: %v", err)
	}
	if string(codexPushedContent) != string(localCodexConfig) {
		t.Fatalf("codex pushed content = %q, want it to match the local merged file %q", codexPushedContent, localCodexConfig)
	}
	if !strings.Contains(string(codexPushedContent), "[mcp_servers.filesystem]") {
		t.Fatalf("codex pushed content missing mcp block: %q", codexPushedContent)
	}

	var openCodePushedPath string
	var openCodePushedContent []byte
	if err := WriteOpenCodeMCPConfig(context.Background(), config, session, mcps, func(_ context.Context, guestPath string, content []byte) error {
		openCodePushedPath = guestPath
		openCodePushedContent = content
		return nil
	}); err != nil {
		t.Fatalf("WriteOpenCodeMCPConfig returned error: %v", err)
	}
	wantOpenCodePath := filepath.Join(config.GuestHomePath, ".config", "opencode", "opencode.json")
	if openCodePushedPath != wantOpenCodePath {
		t.Fatalf("opencode pushed path = %q, want %q", openCodePushedPath, wantOpenCodePath)
	}
	if !strings.Contains(string(openCodePushedContent), `"filesystem"`) {
		t.Fatalf("opencode pushed content missing mcp entry: %q", openCodePushedContent)
	}
}

func TestWriteCodexMCPConfigClearsGuestWhenAllServersRemoved(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	config := &appconfig.Config{}
	mcps := map[string]compose.NormalizedMCPServerSpec{
		"filesystem": {Type: "local", Command: "npx", Args: []string{"-y", "server"}},
	}

	if err := WriteCodexMCPConfig(context.Background(), config, session, mcps, func(context.Context, string, []byte) error { return nil }); err != nil {
		t.Fatalf("WriteCodexMCPConfig (populate) returned error: %v", err)
	}
	localPath := filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("expected local codex config to exist after populating: %v", err)
	}

	var pushCount int
	var lastPushedPath string
	var lastPushedContent []byte
	// Docker/boxlite's guest sees the host file disappear for free through
	// their shared mount; a no-shared-mount guest (k8s) only ever finds out
	// via this callback, so removing every MCP server must still push a
	// clearing write, not just delete the daemon's own copy.
	if err := WriteCodexMCPConfig(context.Background(), config, session, nil, func(_ context.Context, guestPath string, content []byte) error {
		pushCount++
		lastPushedPath, lastPushedContent = guestPath, content
		return nil
	}); err != nil {
		t.Fatalf("WriteCodexMCPConfig (clear) returned error: %v", err)
	}
	if _, err := os.Stat(localPath); !os.IsNotExist(err) {
		t.Fatalf("expected local codex config to be removed after clearing, stat err = %v", err)
	}
	if pushCount != 1 {
		t.Fatalf("guest write callback invoked %d times, want exactly 1 (must still clear the guest)", pushCount)
	}
	appconfig.ApplyDefaultGuestPaths(config)
	wantPath := filepath.Join(config.GuestHomePath, ".codex", "config.toml")
	if lastPushedPath != wantPath {
		t.Fatalf("cleared guest path = %q, want %q", lastPushedPath, wantPath)
	}
	if len(lastPushedContent) != 0 {
		t.Fatalf("cleared guest content = %q, want empty", lastPushedContent)
	}
}

func TestWriteCodexMCPConfigSkipsGuestPushWhenNothingWasEverWritten(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	config := &appconfig.Config{}

	var pushCount int
	// No prior WriteCodexRuntimeConfig/WriteCodexMCPConfig call ever touched
	// this sandbox's .codex/config.toml (e.g. no managed LLM provider - see
	// EnsureCodexFacadeConfig's "let Codex use its own login" no-op path).
	// Calling with zero MCP servers must not push an empty file to a guest
	// that never had anything pushed there in the first place.
	if err := WriteCodexMCPConfig(context.Background(), config, session, nil, func(context.Context, string, []byte) error {
		pushCount++
		return nil
	}); err != nil {
		t.Fatalf("WriteCodexMCPConfig returned error: %v", err)
	}
	if pushCount != 0 {
		t.Fatalf("guest write callback invoked %d times, want 0 (nothing to clear)", pushCount)
	}
}

func TestWriteCodexMCPConfigRetriesGuestClearAfterTransientPushFailure(t *testing.T) {
	root := t.TempDir()
	session := &domain.Sandbox{Summary: domain.SandboxSummary{WorkspacePath: filepath.Join(root, "workspace")}}
	config := &appconfig.Config{}
	mcps := map[string]compose.NormalizedMCPServerSpec{
		"filesystem": {Type: "local", Command: "npx", Args: []string{"-y", "server"}},
	}
	if err := WriteCodexMCPConfig(context.Background(), config, session, mcps, func(context.Context, string, []byte) error { return nil }); err != nil {
		t.Fatalf("WriteCodexMCPConfig (populate) returned error: %v", err)
	}
	localPath := filepath.Join(execution.HostSandboxHome(session), ".codex", "config.toml")

	failing := func(context.Context, string, []byte) error { return fmt.Errorf("transient exec failure") }
	if err := WriteCodexMCPConfig(context.Background(), config, session, nil, failing); err == nil {
		t.Fatal("WriteCodexMCPConfig (clear, guest push fails) returned nil error, want the push failure")
	}
	if _, err := os.Stat(localPath); err != nil {
		t.Fatalf("local codex config after a failed guest push, stat error = %v, want the file to still exist so a retry sees hadExisting=true", err)
	}

	var pushCount int
	retry := func(context.Context, string, []byte) error {
		pushCount++
		return nil
	}
	if err := WriteCodexMCPConfig(context.Background(), config, session, nil, retry); err != nil {
		t.Fatalf("WriteCodexMCPConfig (clear, retry) returned error: %v", err)
	}
	if pushCount != 1 {
		t.Fatalf("push count on retry after a prior transient failure = %d, want 1", pushCount)
	}
}

func TestConfigHelperEdgeBranches(t *testing.T) {
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
	selected, provider, wireAPI, ok, err := SelectModelAndProvider(ctx, llmCoverageWireStore{ok: true, wireAPI: APIProtocolResponses}, ModelProviderSelection{Models: models, Providers: providers, ProviderFamily: ProviderFamilyOpenAI})
	if err != nil || !ok || selected.ID != "m2" || provider.ID != "p2" || wireAPI != APIProtocolResponses {
		t.Fatalf("selected=%#v provider=%#v wire=%q ok=%v err=%v", selected, provider, wireAPI, ok, err)
	}
	if _, _, _, ok, err := SelectModelAndProvider(ctx, llmCoverageWireStore{}, ModelProviderSelection{Models: models, Providers: providers, RequestedModel: "missing"}); err != nil || ok {
		t.Fatalf("expected missing model ok=false err=%v", err)
	}
	if priority := ProviderSelectionPriority(ProviderScopeSessionEnv); priority != 0 {
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
