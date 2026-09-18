package llms

import (
	"context"
	"errors"
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
	// The runner forwards this to `pi --model`; without it pi falls back to its
	// own default model and the call never reaches the facade. Pi addresses the
	// model through the provider key WritePiRuntimeConfig registers, so the
	// published reference carries that namespace.
	if env[GuestModelEnvName] != "agent-compose/qwen3-8b" {
		t.Fatalf("pi env[%s] = %q, want the resolved model", GuestModelEnvName, env[GuestModelEnvName])
	}
}

// Every facade publishes the model the guest must address, because the runner
// passes it to the agent CLI. A facade that omits it makes the CLI use its own
// default (pi with no --model) or the declared reference (codex), and the model
// call then fails or hangs instead of naming the real cause.
func TestFacadesPublishResolvedGuestModel(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	for _, test := range []struct {
		provider string
		want     string
	}{
		{provider: "codex", want: "qwen3-8b"},
		// pi addresses the model through the provider key in its models.json.
		{provider: "pi", want: "agent-compose/qwen3-8b"},
		{provider: "dsh", want: "qwen3-8b"},
		// opencode addresses models through the provider key in its config.
		{provider: "opencode", want: "agent-compose/qwen3-8b"},
	} {
		t.Run(test.provider, func(t *testing.T) {
			root := t.TempDir()
			store := newBareModelFacadeStore()
			store.providers = []Provider{gatewayConnection()}
			config := bareModelConfig(root)
			sandbox := bareModelSandbox(root, "sandbox-"+test.provider)
			var (
				env map[string]string
				err error
			)
			switch test.provider {
			case "codex":
				env, err = EnsureCodexFacadeConfig(ctx, CodexFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: "qwen3-8b", Source: "agent", RunID: "run"})
			case "pi":
				env, err = EnsurePiFacadeConfig(ctx, PiFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: "qwen3-8b", Source: "agent", RunID: "run"})
			case "dsh":
				env, err = EnsureDshFacadeConfig(ctx, DshFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: "qwen3-8b", Source: "agent", RunID: "run"})
			case "opencode":
				env, err = EnsureOpenCodeFacadeConfig(ctx, OpenCodeFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: "qwen3-8b", Source: "agent", RunID: "run"})
			}
			if err != nil {
				t.Fatalf("Ensure%sFacadeConfig returned error: %v", test.provider, err)
			}
			if got := env[GuestModelEnvName]; got != test.want {
				t.Fatalf("%s env[%s] = %q, want %q", test.provider, GuestModelEnvName, got, test.want)
			}
		})
	}
}

// A resolved model id may itself contain slashes. The facade has to publish the
// complete guest-facing reference so the runner can forward it untouched: pi
// addresses the model under the facade provider, dsh as the bare literal. Both
// guests pass the value straight to their CLI, so a truncated reference would
// silently select a different model.
func TestFacadesPublishGuestModelWithSlashesInModelID(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}
	config := bareModelConfig(root)
	sandbox := bareModelSandbox(root, "sandbox-slash")
	// "gateway" is the connection prefix; the literal remainder keeps its slash.
	const declared = "gateway/anthropic/claude-3.5-sonnet"
	const remainder = "anthropic/claude-3.5-sonnet"

	piEnv, err := EnsurePiFacadeConfig(ctx, PiFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: declared, Source: "agent", RunID: "run-pi"})
	if err != nil {
		t.Fatalf("EnsurePiFacadeConfig returned error: %v", err)
	}
	if got := piEnv[GuestModelEnvName]; got != "agent-compose/"+remainder {
		t.Fatalf("pi env[%s] = %q, want %q", GuestModelEnvName, got, "agent-compose/"+remainder)
	}

	dshEnv, err := EnsureDshFacadeConfig(ctx, DshFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: declared, Source: "agent", RunID: "run-dsh"})
	if err != nil {
		t.Fatalf("EnsureDshFacadeConfig returned error: %v", err)
	}
	if got := dshEnv[GuestModelEnvName]; got != remainder {
		t.Fatalf("dsh env[%s] = %q, want %q", GuestModelEnvName, got, remainder)
	}
	if got := dshEnv["DSH_MODEL"]; got != remainder {
		t.Fatalf("DSH_MODEL = %q, want %q", got, remainder)
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

// secondGatewayConnection is a second enabled OpenAI-family connection, which
// makes a bare model ambiguous.
func secondGatewayConnection() Provider {
	provider := gatewayConnection()
	provider.ID = "gateway-2"
	return provider
}

// An ambiguous default connection is a configuration error the operator has to
// fix. It must reach the caller, and it must keep the failed-precondition
// classification the transport maps.
func TestAmbiguousDefaultConnectionIsReported(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection(), secondGatewayConnection()}

	_, err := ResolveRuntimeLLMTargetWithEnv(context.Background(), store, RuntimeLLMTargetQuery{
		Config: bareModelConfig(root), SessionID: "sandbox-ambiguous",
		PreferredProviderFamily: ProviderFamilyOpenAI, RequestedModel: "qwen3-8b",
	})
	if err == nil {
		t.Fatal("ResolveRuntimeLLMTarget resolved an ambiguous bare model")
	}
	if !errors.Is(err, ErrAmbiguousDefaultConnection) {
		t.Fatalf("err = %v, want ErrAmbiguousDefaultConnection", err)
	}
	if !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("err = %v, want it classified as a failed precondition", err)
	}
	for _, id := range []string{"gateway", "gateway-2"} {
		if !strings.Contains(err.Error(), id) {
			t.Fatalf("err = %v, want it to name %q", err, id)
		}
	}
}

// Codex may fall back to credentials it carries itself only when the daemon has
// no managed configuration. Swallowing the ambiguity instead let a run start and
// fail later as a connectivity error, hiding the real cause.
func TestEnsureCodexFacadeConfigRejectsAmbiguousDefaultConnection(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection(), secondGatewayConnection()}

	env, err := EnsureCodexFacadeConfig(context.Background(), CodexFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-codex-ambiguous"),
		Model: "qwen3-8b", Source: "agent", RunID: "run-codex-ambiguous",
	})
	if err == nil {
		t.Fatalf("ambiguity was swallowed, env = %#v", env)
	}
	if !errors.Is(err, ErrAmbiguousDefaultConnection) {
		t.Fatalf("err = %v, want ErrAmbiguousDefaultConnection", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none", store.savedTokens)
	}
}

func TestOptionalFacadeConfigError(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "missing model", err: domain.ClassifyError(domain.ErrRequired, "llm model is required", nil), want: true},
		{name: "unconfigured provider", err: domain.ClassifyError(domain.ErrFailedPrecondition, "llm provider is not configured", nil), want: true},
		{name: "ambiguous connection", err: ambiguousDefaultConnectionError("llm", []Provider{gatewayConnection(), secondGatewayConnection()}), want: false},
		{name: "unrelated failure", err: domain.ClassifyError(domain.ErrInvalidArgument, "bad model reference", nil), want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := OptionalFacadeConfigError(test.err); got != test.want {
				t.Fatalf("OptionalFacadeConfigError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

// A reference that leaves a side empty is a typo in every agent. codex parses
// the value itself rather than through the facade agents' shared splitter, so
// without the resolver check it forwarded `gateway/` upstream and failed later
// with an unrelated upstream error.
func TestEnsureCodexFacadeConfigRejectsMalformedModelReference(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}

	env, err := EnsureCodexFacadeConfig(context.Background(), CodexFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: bareModelSandbox(root, "sandbox-codex-malformed"),
		Model: "gateway/", Source: "agent", RunID: "run-codex-malformed",
	})
	if err == nil {
		t.Fatalf("malformed reference was accepted, env = %#v", env)
	}
	if !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("err = %v, want an invalid-argument error", err)
	}
	if !strings.Contains(err.Error(), `"gateway/"`) {
		t.Fatalf("err = %v, want it to quote the reference", err)
	}
	if len(store.savedTokens) != 0 {
		t.Fatalf("saved tokens = %#v, want none", store.savedTokens)
	}
}
