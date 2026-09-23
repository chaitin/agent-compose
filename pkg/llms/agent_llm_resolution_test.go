package llms

import (
	"context"
	"errors"
	"strings"
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

// TestPrepareAgentLLMRejectsAmbiguousBareModel pins the operator-visible
// failure: several connections and nothing that identifies one of them is not
// guessed at, and no facade token is minted.
func TestPrepareAgentLLMRejectsAmbiguousBareModel(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: []Provider{
		catalogOpenAIConnection("gateway", "https://gateway.test"),
		catalogOpenAIConnection("backup", "https://backup.test"),
	}}}

	_, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "pi", Model: "qwen3-8b",
	})
	if !errors.Is(err, ErrAmbiguousConnection) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrAmbiguousConnection", err)
	}
	for _, id := range []string{"gateway", "backup"} {
		if !strings.Contains(err.Error(), id) {
			t.Errorf("error %q does not name candidate %q", err, id)
		}
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for an ambiguous model", store.savedTokens)
	}
}

// TestPrepareAgentLLMReportsNoConnectionWhenCatalogIsEmpty pins the "nothing is
// configured" failure: a declared model against a daemon with zero connections
// is unmanaged (ErrNoConnection), not an ambiguity between zero candidates. The
// facade entry points treat that sentinel as a no-op, so it must not surface as
// the fatal ErrAmbiguousConnection.
func TestPrepareAgentLLMReportsNoConnectionWhenCatalogIsEmpty(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()

	catalog, err := LoadCatalog(context.Background(), fakeCatalogStore{})
	if err != nil {
		t.Fatalf("LoadCatalog returned error: %v", err)
	}
	if _, err := catalog.Resolve("", "qwen3-8b"); !errors.Is(err, ErrNoConnection) {
		t.Fatalf("Resolve error = %v, want ErrNoConnection", err)
	} else if errors.Is(err, ErrAmbiguousConnection) {
		t.Fatalf("Resolve error = %v, must not be reported as ambiguous", err)
	}

	store := &prepareAgentLLMStore{}
	_, err = PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "pi", Model: "qwen3-8b",
	})
	if !errors.Is(err, ErrNoConnection) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrNoConnection", err)
	}
	if errors.Is(err, ErrAmbiguousConnection) {
		t.Fatalf("PrepareAgentLLM error = %v, must not be reported as ambiguous", err)
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

// TestPrepareAgentLLMReportsAnAmbiguousModel pins that an agent declaring only
// an opaque model the daemon cannot attribute to one connection fails loudly at
// the entry point, instead of the daemon guessing an upstream.
func TestPrepareAgentLLMReportsAnAmbiguousModel(t *testing.T) {
	isolateLLMEnv(t)
	providers := []Provider{
		catalogOpenAIConnection("gateway", "https://gateway.test"),
		catalogOpenAIConnection("backup", "https://backup.test"),
	}
	bindings := []ProviderModelBinding{
		{ProviderID: "gateway", ModelID: "shared-model"},
		{ProviderID: "backup", ModelID: "shared-model"},
	}

	root := t.TempDir()
	store := &prepareAgentLLMStore{fakeCatalogStore: fakeCatalogStore{providers: providers, bindings: bindings}}
	if _, err := PrepareAgentLLM(context.Background(), AgentLLMRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, agentLLMSandboxID),
		AgentKind: "pi", Model: "shared-model",
	}); !errors.Is(err, ErrAmbiguousConnection) {
		t.Fatalf("PrepareAgentLLM error = %v, want ErrAmbiguousConnection", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for an ambiguous model", store.savedTokens)
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
