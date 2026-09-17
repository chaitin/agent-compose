package llms

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// bareModelFacadeStore serves every facade agent's resolution surface. It
// records issued tokens so a test can assert the resolved connection and the
// literal model that reached the facade.
type bareModelFacadeStore struct {
	*resolverCoverageStore
	savedTokens []FacadeToken
}

func newBareModelFacadeStore() *bareModelFacadeStore {
	return &bareModelFacadeStore{resolverCoverageStore: newResolverCoverageStore()}
}

func (s *bareModelFacadeStore) SaveLLMFacadeToken(_ context.Context, token FacadeToken) error {
	s.savedTokens = append(s.savedTokens, token)
	return nil
}

func bareModelSandbox(root, id string) *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            id,
		Driver:        driverpkg.RuntimeDriverDocker,
		WorkspacePath: filepath.Join(root, "sandboxes", id, "workspace"),
	}}
}

func bareModelConfig(root string) *appconfig.Config {
	return &appconfig.Config{
		DataRoot:       root,
		RuntimeBaseURL: "http://agent-compose.test:7410",
		GuestHomePath:  "/root",
	}
}

// gatewayConnection is the shape a connection created through the provider RPC
// has: an enabled upstream and no model or binding rows.
func gatewayConnection() Provider {
	return Provider{
		ID: "gateway", ProviderType: ProviderFamilyOpenAI,
		DefaultWireAPI: APIProtocolChatCompletions,
		BaseURL:        "https://gateway.test/v1", APIKey: "gateway-key",
		Enabled: true, Scope: ProviderScopeAPI,
	}
}

func TestEnsurePiFacadeConfigAcceptsUnqualifiedModel(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}

	env, err := EnsurePiFacadeConfig(context.Background(), PiFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-pi"),
		Model: "qwen3-8b", Source: "agent", RunID: "run-pi",
	})
	if err != nil {
		t.Fatalf("EnsurePiFacadeConfig returned error: %v", err)
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != "gateway" || store.savedTokens[0].Model != "qwen3-8b" {
		t.Fatalf("pi token = %#v, want gateway/qwen3-8b", store.savedTokens)
	}
	if env["LLM_API_KEY"] == "" || env["AGENT_COMPOSE_SANDBOX_TOKEN"] != env["LLM_API_KEY"] {
		t.Fatalf("pi env = %#v", env)
	}
}

func TestEnsureOpenCodeFacadeConfigAcceptsUnqualifiedModel(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}

	env, err := EnsureOpenCodeFacadeConfig(context.Background(), OpenCodeFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-opencode"),
		Model: "qwen3-8b", Source: "agent", RunID: "run-opencode",
	})
	if err != nil {
		t.Fatalf("EnsureOpenCodeFacadeConfig returned error: %v", err)
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != "gateway" || store.savedTokens[0].Model != "qwen3-8b" {
		t.Fatalf("opencode token = %#v, want gateway/qwen3-8b", store.savedTokens)
	}
	if env["OPENCODE_CONFIG"] == "" || env["LLM_API_KEY"] == "" {
		t.Fatalf("opencode env = %#v", env)
	}
	// The guest provider name is synthesized by the adapter; the user never
	// writes it.
	if !strings.Contains(env["OPENCODE_MODEL"], "qwen3-8b") {
		t.Fatalf("OPENCODE_MODEL = %q", env["OPENCODE_MODEL"])
	}
}

func TestEnsureDshFacadeConfigAcceptsUnqualifiedModel(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}

	env, err := EnsureDshFacadeConfig(context.Background(), DshFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-dsh"),
		Model: "qwen3-8b", Source: "agent", RunID: "run-dsh",
	})
	if err != nil {
		t.Fatalf("EnsureDshFacadeConfig returned error: %v", err)
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].ProviderID != "gateway" || store.savedTokens[0].Model != "qwen3-8b" {
		t.Fatalf("dsh token = %#v, want gateway/qwen3-8b", store.savedTokens)
	}
	if env["DSH_MODEL"] != "qwen3-8b" || env["LLM_API_KEY"] == "" {
		t.Fatalf("dsh env = %#v", env)
	}
}

// A bare "opencode" names OpenCode's own provider without a model. It is a typo
// rather than a literal model name: resolving it would mint a managed facade
// token for a model that cannot exist, and it must not silently disable the
// managed path either.
func TestEnsureOpenCodeFacadeConfigRejectsBareNativeProvider(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}

	if _, err := EnsureOpenCodeFacadeConfig(context.Background(), OpenCodeFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-opencode-native"),
		Model: "opencode", Source: "agent", RunID: "run-opencode",
	}); err == nil {
		t.Fatal("EnsureOpenCodeFacadeConfig accepted a bare native provider name")
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for a rejected reference", store.savedTokens)
	}
}

// A malformed reference is rejected at the facade boundary instead of becoming a
// managed token for a model name that cannot exist upstream.
func TestEnsurePiFacadeConfigRejectsMalformedReference(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}

	if _, err := EnsurePiFacadeConfig(context.Background(), PiFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-pi-malformed"),
		Model: "gateway/", Source: "agent", RunID: "run-pi",
	}); err == nil {
		t.Fatal("EnsurePiFacadeConfig accepted a reference with an empty model side")
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none for a rejected reference", store.savedTokens)
	}
}
