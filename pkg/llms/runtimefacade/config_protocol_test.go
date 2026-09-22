package runtimefacade

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

// openFacadeCatalogStore opens a config store with a model catalog applied, the
// way the daemon projects models.json at startup. The agent facade resolves
// against this catalog and nothing else.
func openFacadeCatalogStore(t *testing.T, ctx context.Context, root string, catalog llms.ModelCatalog) (*appconfig.Config, *configstore.ConfigStore) {
	t.Helper()
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
	if err := store.ApplyModelCatalog(ctx, catalog); err != nil {
		t.Fatalf("apply model catalog: %v", err)
	}
	return config, store
}

// TestIntegrationEnsureSessionOpenCodePinsChatIngressIndependentOfUpstream pins
// the split between the two protocols: LLM_API_PROTOCOL names the upstream the
// daemon forwards to, while the facade token (and the ai-sdk package opencode is
// configured with) pins the guest ingress. OpenCode cannot speak the Responses
// API, so a responses upstream is converted and the token stays chat
// completions.
func TestIntegrationEnsureSessionOpenCodePinsChatIngressIndependentOfUpstream(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	const sessionID = "sandbox-opencode-chat-ingress"
	config, store := openFacadeCatalogStore(t, ctx, root, llms.ModelCatalog{
		Default: "gateway/gpt-test",
		Providers: map[string]llms.CatalogProvider{
			"gateway": {
				BaseURL:  catalogStringPointer("https://responses.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("gateway-key"),
				Models:   []llms.CatalogModel{{ID: "gpt-test"}},
			},
		},
	})
	session := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            sessionID,
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", sessionID, "workspace"),
	}}

	runtimeConfig, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "gpt-test", Source: TokenSourceAgent, RunID: "run-chat-ingress"})
	if err != nil {
		t.Fatalf("EnsureSessionAgentRuntimeConfig returned error: %v", err)
	}
	if runtimeConfig.Env["LLM_API_PROTOCOL"] != llms.APIProtocolResponses {
		t.Fatalf("LLM_API_PROTOCOL = %q, want the responses upstream", runtimeConfig.Env["LLM_API_PROTOCOL"])
	}
	if runtimeConfig.Model != "agent-compose/gpt-test" {
		t.Fatalf("OpenCode runtime model = %q, want the facade reference", runtimeConfig.Model)
	}
	token, err := store.GetLLMFacadeToken(ctx, runtimeConfig.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
	if err != nil {
		t.Fatalf("GetLLMFacadeToken returned error: %v", err)
	}
	if token.WireAPI != llms.APIProtocolChatCompletions || token.ProviderID != "gateway" || token.Model != "gpt-test" {
		t.Fatalf("facade token = %#v, want the chat ingress to gateway", token)
	}

	var openCodeConfig struct {
		Provider map[string]struct {
			NPM     string `json:"npm"`
			Options struct {
				BaseURL string `json:"baseURL"`
			} `json:"options"`
		} `json:"provider"`
	}
	data, err := os.ReadFile(filepath.Join(execution.HostSandboxHome(session), ".config", "opencode", "opencode.json"))
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	if err := json.Unmarshal(data, &openCodeConfig); err != nil {
		t.Fatalf("decode opencode config: %v\n%s", err, data)
	}
	entry := openCodeConfig.Provider[llms.GuestProviderAgentCompose]
	if entry.NPM != "@ai-sdk/openai-compatible" {
		t.Fatalf("opencode provider npm = %q, want the chat-completions package", entry.NPM)
	}
	wantBaseURL := "http://agent-compose.test:7410/api/runtime/sandboxes/" + sessionID + "/llm/openai/v1"
	if entry.Options.BaseURL != wantBaseURL {
		t.Fatalf("opencode provider baseURL = %q, want %q", entry.Options.BaseURL, wantBaseURL)
	}
}

// TestIntegrationEnsureSessionOpenCodeResolvesTheServingConnection covers a
// catalog with several connections: a model bound to exactly one of them selects
// that connection, and a model bound to more than one without a default match is
// reported as ambiguous instead of guessed.
func TestIntegrationEnsureSessionOpenCodeResolvesTheServingConnection(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	const sessionID = "sandbox-opencode-serving-connection"
	config, store := openFacadeCatalogStore(t, ctx, root, llms.ModelCatalog{
		Default: "openai/gpt-test",
		Providers: map[string]llms.CatalogProvider{
			"openai": {
				BaseURL:  catalogStringPointer("https://openai.example.test/v1"),
				Protocol: catalogStringPointer(llms.APIProtocolResponses),
				APIKey:   catalogStringPointer("openai-key"),
				Models:   []llms.CatalogModel{{ID: "gpt-test"}, {ID: "shared"}},
			},
			"baizhi": {
				BaseURL:  catalogStringPointer("https://baizhi.example.test/api/openai"),
				Protocol: catalogStringPointer(llms.APIProtocolChatCompletions),
				APIKey:   catalogStringPointer("baizhi-key"),
				Models:   []llms.CatalogModel{{ID: "deepseek-v4-flash"}, {ID: "shared"}},
			},
		},
	})
	session := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            sessionID,
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", sessionID, "workspace"),
	}}

	assertTarget := func(model, protocol, providerID string) {
		t.Helper()
		runtimeConfig, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: model, Source: TokenSourceAgent, RunID: "run-" + providerID})
		if err != nil {
			t.Fatalf("EnsureSessionAgentRuntimeConfig(%q) returned error: %v", model, err)
		}
		if runtimeConfig.Env["LLM_API_PROTOCOL"] != protocol || runtimeConfig.Model != "agent-compose/"+model {
			t.Fatalf("OpenCode runtime config for %q = %#v", model, runtimeConfig)
		}
		token, err := store.GetLLMFacadeToken(ctx, runtimeConfig.Env["AGENT_COMPOSE_SANDBOX_TOKEN"])
		if err != nil {
			t.Fatalf("GetLLMFacadeToken(%q) returned error: %v", model, err)
		}
		// OpenCode always posts chat completions, whatever the upstream serves.
		if token.ProviderID != providerID || token.Model != model || token.WireAPI != llms.APIProtocolChatCompletions {
			t.Fatalf("facade token for %q = %#v, want %s over chat completions", model, token, providerID)
		}
	}

	assertTarget("gpt-test", llms.APIProtocolResponses, "openai")
	assertTarget("deepseek-v4-flash", llms.APIProtocolChatCompletions, "baizhi")

	if _, err := EnsureSessionAgentRuntimeConfig(ctx, SessionFacadeConfigRequest{Config: config, Store: store, Session: session, Agent: "opencode", Model: "shared", Source: TokenSourceAgent, RunID: "run-shared"}); !errors.Is(err, llms.ErrAmbiguousConnection) {
		t.Fatalf("shared model error = %v, want ErrAmbiguousConnection", err)
	}
}
