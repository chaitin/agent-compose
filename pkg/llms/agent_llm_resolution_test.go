package llms

import (
	"context"
	"errors"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestPrepareAgentLLMBareModelUsesSoleConnection(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	sandbox := bareModelSandbox(root, agentLLMSandboxID)
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway-responses", "https://gateway.test")},
	}}

	prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: sandbox,
		AgentKind: "pi", Model: "qwen3-8b", Source: "agent", RunID: "run-bare",
	})
	if err != nil {
		t.Fatalf("PrepareAgentLLM returned error: %v", err)
	}
	if prepared.Model != "qwen3-8b" || prepared.Target.Provider.ID != "gateway-responses" {
		t.Errorf("model/provider = %q/%q, want qwen3-8b on the sole connection", prepared.Model, prepared.Target.Provider.ID)
	}
	if prepared.GuestModel != "agent-compose/qwen3-8b" {
		t.Errorf("GuestModel = %q, want the namespaced bare model", prepared.GuestModel)
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != "gateway-responses" {
		t.Fatalf("saved tokens = %#v, want the sole connection", store.savedTokens)
	}
}

// TestPrepareAgentLLMPrefersAPassthroughConnection pins the affinity rule at
// the entry point: when one model is served over several protocols, the run is
// pinned to the connection the agent speaks natively, so the daemon proxies
// instead of converting.
func TestPrepareAgentLLMPrefersAPassthroughConnection(t *testing.T) {
	providers := []Provider{
		catalogOpenAIConnection("shared-responses", "https://responses.test"),
		catalogChatConnection("shared-chat", "https://chat.test"),
		catalogAnthropicConnection("shared-messages", "https://messages.test"),
	}
	bindings := []ProviderModelBinding{
		{ProviderID: "shared-responses", ModelID: "shared-model"},
		{ProviderID: "shared-chat", ModelID: "shared-model"},
		{ProviderID: "shared-messages", ModelID: "shared-model"},
	}
	cases := []struct {
		agent        string
		wantProvider string
		wantInbound  Protocol
	}{
		{"codex", "shared-responses", ProtocolResponses},
		{"claude", "shared-messages", ProtocolMessages},
		{"opencode", "shared-chat", ProtocolChatCompletions},
		{"pi", "shared-responses", ProtocolResponses},
		{"dsh", "shared-responses", ProtocolResponses},
	}
	for _, tc := range cases {
		t.Run(tc.agent, func(t *testing.T) {
			isolateLLMEnv(t)
			root := t.TempDir()
			store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: providers, bindings: bindings}}
			prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
				AgentKind: tc.agent, Model: "shared-model", Source: "agent", RunID: "run-" + tc.agent,
			})
			if err != nil {
				t.Fatalf("PrepareAgentLLM returned error: %v", err)
			}
			if prepared.Target.Provider.ID != tc.wantProvider {
				t.Fatalf("provider = %q, want %q", prepared.Target.Provider.ID, tc.wantProvider)
			}
			if prepared.Convert || prepared.Upstream != tc.wantInbound || prepared.Inbound != tc.wantInbound {
				t.Fatalf("upstream/inbound/convert = %s/%s/%v, want a %s passthrough",
					prepared.Upstream, prepared.Inbound, prepared.Convert, tc.wantInbound)
			}
			if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != tc.wantProvider {
				t.Fatalf("saved tokens = %#v, want one token for %q", store.savedTokens, tc.wantProvider)
			}
		})
	}
}

// TestPrepareAgentLLMBreaksProtocolTiesWithoutFailing pins that two connections
// serving a model over the same protocol no longer stop the run: one of them is
// chosen, and the ambiguity never reaches the operator as an error.
func TestPrepareAgentLLMBreaksProtocolTiesWithoutFailing(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{
			catalogOpenAIConnection("gateway", "https://gateway.test"),
			catalogOpenAIConnection("backup", "https://backup.test"),
		},
		bindings: []ProviderModelBinding{
			{ProviderID: "gateway", ModelID: "shared-model"},
			{ProviderID: "backup", ModelID: "shared-model"},
		},
	}}

	prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "pi", Model: "shared-model", Source: "agent", RunID: "run-shared",
	})
	if err != nil {
		t.Fatalf("PrepareAgentLLM returned error: %v", err)
	}
	if id := prepared.Target.Provider.ID; id != "gateway" && id != "backup" {
		t.Fatalf("provider = %q, want one of the two equivalent connections", id)
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != prepared.Target.Provider.ID {
		t.Fatalf("saved tokens = %#v, want the chosen connection", store.savedTokens)
	}
}

func TestPrepareAgentLLMReportsNoConnectionWhenCatalogIsEmpty(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()

	catalog, err := LoadCatalog(context.Background(), fakeCatalogStore{})
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	if _, err := catalog.Resolve("", "qwen3-8b", nil); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("Resolve error = %v, want ErrNoConnection", err)
	}

	store := &prepareAgentLLMStore{}
	_, err = PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "pi", Model: "qwen3-8b",
	})
	if !errors.Is(err, ErrNoConnection) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrNoConnection", err)
	}
	if !IsUnmanagedAgentLLMError(err) {
		t.Fatalf("IsUnmanagedAgentLLMError(%v) = false, want true", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none when no connection exists", store.savedTokens)
	}
}

func TestPrepareAgentLLMReturnsErrNoModel(t *testing.T) {
	isolateLLMEnv(t)
	cases := []struct {
		name      string
		providers []Provider
	}{
		{name: "empty catalog"},
		{name: "connection without a default model", providers: []Provider{catalogOpenAIConnection("gateway", "https://gateway.test")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: tc.providers}}
			_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
				Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
				AgentKind: "codex",
			})
			if !errors.Is(err, ErrNoModel) {
				t.Fatalf("PrepareAgentLLM error = %v, want ErrNoModel", err)
			}
			if len(store.savedTokens) != 0 {
				t.Fatalf("saved tokens = %#v, want none", store.savedTokens)
			}
		})
	}
}

// TestPrepareAgentLLMFallsBackToConversion pins that a model only reachable
// through a protocol the agent cannot speak still runs: the daemon converts
// rather than refusing, and the minted token carries the conversion.
func TestPrepareAgentLLMFallsBackToConversion(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{catalogChatConnection("gateway-chat", "https://gateway.test")},
		bindings:  []ProviderModelBinding{{ProviderID: "gateway-chat", ModelID: "shared-model"}},
	}}

	prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "codex", Model: "shared-model", Source: "agent", RunID: "run-converted",
	})
	if err != nil {
		t.Fatalf("PrepareAgentLLM returned error: %v", err)
	}
	if !prepared.Convert || prepared.Upstream != ProtocolChatCompletions || prepared.Inbound != ProtocolResponses {
		t.Fatalf("upstream/inbound/convert = %s/%s/%v, want chat completions converted to responses",
			prepared.Upstream, prepared.Inbound, prepared.Convert)
	}
	if prepared.Target.Provider.ID != "gateway-chat" {
		t.Fatalf("provider = %q, want gateway-chat", prepared.Target.Provider.ID)
	}
}

func TestPrepareAgentLLMRejectsUnsupportedAgentKind(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gateway.test")},
	}}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "gemini", Model: "gpt-5.5",
	})
	if !errors.Is(err, ErrUnsupportedAgentDialect) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrUnsupportedAgentDialect", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for an unsupported agent", store.savedTokens)
	}
}

// TestPrepareAgentLLMReportsNoModelBeforeMissingDaemonURL pins the order of the
// two "this run has nothing to point at" failures. An agent the daemon does not
// manage keeps its own endpoint and credential, so it must be reported as
// ErrNoModel and allowed to proceed even on a daemon with no sandbox-reachable
// URL; reporting the missing URL instead would fail an unmanaged agent's run.
func TestPrepareAgentLLMReportsNoModelBeforeMissingDaemonURL(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: &appconfig.Config{DataRoot: root, GuestHomePath: "/root"},
		Store:  store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "codex",
	})
	if !errors.Is(err, ErrNoModel) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrNoModel before any daemon URL check", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none", store.savedTokens)
	}
}

// TestPrepareAgentLLMRequiresDaemonURLForAManagedModel is the other side of that
// order: once a model is configured, an unreachable daemon URL is a real
// configuration error because the guest cannot obtain its credential.
func TestPrepareAgentLLMRequiresDaemonURLForAManagedModel(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{
		providers: []Provider{catalogOpenAIConnection("gateway", "https://gateway.test")},
	}}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: &appconfig.Config{DataRoot: root, GuestHomePath: "/root"},
		Store:  store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "codex", Model: "gpt-5.5",
	})
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("PrepareAgentLLM error = %v, want failed precondition for a missing daemon URL", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none when the guest cannot reach the daemon", store.savedTokens)
	}
}

// TestPrepareAgentLLMPropagatesTokenSaveFailure keeps the persistence failure on
// the caller's path: a token that was not saved must not produce a usable
// guest environment.
func TestPrepareAgentLLMPropagatesTokenSaveFailure(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	saveErr := errors.New("save token failed")
	store := &prepareAgentLLMStore{
		fakeCatalogStore: fakeCatalogStore{providers: []Provider{catalogOpenAIConnection("gateway", "https://gateway.test")}},
		saveErr:          saveErr,
	}

	prepared, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "pi", Model: "qwen3-8b",
	})
	if !errors.Is(err, saveErr) {
		t.Fatalf("PrepareAgentLLM error = %v, want the save failure", err)
	}
	if prepared != nil {
		t.Fatalf("prepared = %#v, want nil when the token could not be saved", prepared)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none", store.savedTokens)
	}
}
