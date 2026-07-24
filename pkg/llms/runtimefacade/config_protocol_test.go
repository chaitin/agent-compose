package runtimefacade

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestEnsureSessionOpenCodeKeepsResponsesIngressForChatUpstream(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:       root,
		DbAddr:         filepath.Join(root, "data.db"),
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	session := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "sandbox-opencode-chat-upstream",
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", "sandbox-opencode-chat-upstream", "workspace"),
	}}
	llms.SetSandboxProviderEnvItems(session, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://chat.example.test/v1"},
		{Name: "LLM_API_PROTOCOL", Value: llms.APIProtocolChatCompletions},
		{Name: "LLM_API_KEY", Value: "chat-key", Secret: true},
		{Name: "LLM_MODEL", Value: "gpt-chat"},
	})

	env, err := EnsureSessionLLMFacadeConfig(ctx, config, store, session, "opencode", "openai/gpt-chat", TokenSourceAgent, "run-chat")
	if err != nil {
		t.Fatalf("EnsureSessionLLMFacadeConfig returned error: %v", err)
	}
	if env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("OpenCode ingress protocol = %q, want responses", env["LLM_API_PROTOCOL"])
	}
	token, err := store.GetLLMFacadeToken(ctx, env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.WireAPI != llms.APIProtocolResponses {
		t.Fatalf("facade token wire API = %q, want responses", token.WireAPI)
	}
	providers, err := store.ListEnabledLLMProviders(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMProviders returned error: %v", err)
	}
	if len(providers) != 1 || providers[0].DefaultWireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("upstream providers = %#v", providers)
	}
}
