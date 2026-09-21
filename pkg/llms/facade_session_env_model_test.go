package llms

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// A sandbox that supplies its own provider environment owns the model. That is
// how a gateway injects LLM_API_ENDPOINT/LLM_API_KEY/LLM_MODEL/LLM_API_PROTOCOL
// while the agent still declares a model, and the injected value is already
// qualified in the namespace the upstream expects (a gateway logical model name
// such as baizhi/deepseek-flash, whose own id contains a slash).
//
// Every facade agent must publish that exact literal. Reading the declared
// <connection>/<model> as a connection plus a literal shortened it to the
// remainder, so the guest addressed baizhi/deepseek-flash as deepseek-flash and
// the upstream rejected a model it never had. The facade token is asserted
// because it is the model the proxy sends upstream.
func TestFacadesKeepSessionEnvModelOverDeclaredReference(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	const injected = "baizhi/deepseek-flash"
	for _, test := range []struct {
		agent     string
		wantGuest string
	}{
		// codex and dsh address the literal; pi and opencode prefix the provider
		// key their own model configuration registers.
		{agent: "codex", wantGuest: injected},
		{agent: "pi", wantGuest: "agent-compose/" + injected},
		{agent: "dsh", wantGuest: injected},
		{agent: "opencode", wantGuest: "agent-compose/" + injected},
	} {
		t.Run(test.agent, func(t *testing.T) {
			root := t.TempDir()
			store := newBareModelFacadeStore()
			sandbox := bareModelSandbox(root, "session-env-"+test.agent)
			SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.test/v1"},
				{Name: "LLM_API_KEY", Value: "facade-token"},
				{Name: "LLM_MODEL", Value: injected},
				{Name: "LLM_API_PROTOCOL", Value: "responses"},
			})

			env, err := ensureFacadeAgentConfig(ctx, test.agent, bareModelConfig(root), store, sandbox, injected)
			if err != nil {
				t.Fatalf("Ensure%sFacadeConfig returned error: %v", test.agent, err)
			}
			if got := env[GuestModelEnvName]; got != test.wantGuest {
				t.Fatalf("%s env[%s] = %q, want %q", test.agent, GuestModelEnvName, got, test.wantGuest)
			}
			if len(store.savedTokens) != 1 {
				t.Fatalf("%s issued %d facade tokens, want 1", test.agent, len(store.savedTokens))
			}
			if got := store.savedTokens[0].Model; got != injected {
				t.Fatalf("%s facade token model = %q, want %q", test.agent, got, injected)
			}
			// The guest CLI receives the guest reference; the catalog it reads
			// must register the same literal as a model id, or the CLI rewrites
			// nothing and the facade token and the wire model disagree.
			switch test.agent {
			case "pi":
				var catalog struct {
					Providers map[string]struct{ Models []struct{ ID string } }
				}
				readFacadeModelConfig(t, filepath.Join(execution.HostSandboxHome(sandbox), ".pi", "agent", "models.json"), &catalog)
				models := catalog.Providers["agent-compose"].Models
				if len(models) != 1 || models[0].ID != injected {
					t.Fatalf("pi catalog models = %#v, want %s", models, injected)
				}
			case "opencode":
				var catalog struct {
					Provider map[string]struct{ Models map[string]json.RawMessage }
				}
				readFacadeModelConfig(t, filepath.Join(execution.HostSandboxHome(sandbox), ".config", "opencode", "opencode.json"), &catalog)
				if catalog.Provider["agent-compose"].Models[injected] == nil {
					t.Fatalf("opencode catalog does not register model %s", injected)
				}
			}
		})
	}
}

// The family aliases keep their documented precedence: a declaration that names
// a route the daemon does serve still selects its model, and the injected
// LLM_MODEL stays the fallback. Only an unrecognised prefix is not a route.
func TestFacadesHonorFamilyAliasOverSessionEnvModel(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	for _, test := range []struct {
		agent     string
		wantModel string
	}{
		// pi and dsh keep the declared model for a family alias.
		{agent: "pi", wantModel: "second"},
		{agent: "dsh", wantModel: "second"},
		// opencode routes the alias through its configured family facade, which
		// resolves with no explicit provider and therefore takes the session
		// env model, exactly as codex does. Assert the existing behaviour so the
		// asymmetry is visible rather than accidental.
		{agent: "opencode", wantModel: "first"},
	} {
		t.Run(test.agent, func(t *testing.T) {
			root := t.TempDir()
			store := newBareModelFacadeStore()
			sandbox := bareModelSandbox(root, "alias-"+test.agent)
			SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.test/v1"},
				{Name: "LLM_API_KEY", Value: "facade-token"},
				{Name: "LLM_MODEL", Value: "first"},
				{Name: "LLM_API_PROTOCOL", Value: "responses"},
			})

			if _, err := ensureFacadeAgentConfig(ctx, test.agent, bareModelConfig(root), store, sandbox, "openai/second"); err != nil {
				t.Fatalf("Ensure%sFacadeConfig returned error: %v", test.agent, err)
			}
			if len(store.savedTokens) != 1 || store.savedTokens[0].Model != test.wantModel {
				t.Fatalf("%s token = %#v, want %s", test.agent, store.savedTokens, test.wantModel)
			}
		})
	}
}

// A declaration that does not name the model the environment publishes keeps
// its established meaning: the prefix is a route (here an unknown one, which
// opencode reads as a custom endpoint key and pi/dsh resolve against the
// session env) and the remainder is the literal model id.
func TestFacadesKeepDeclaredModelWhenItDiffersFromSessionEnvModel(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	for _, agent := range []string{"pi", "dsh", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			store := newBareModelFacadeStore()
			sandbox := bareModelSandbox(root, "declared-"+agent)
			SetSandboxProviderEnvItems(sandbox, []domain.SandboxEnvVar{
				{Name: "LLM_API_ENDPOINT", Value: "https://gateway.test/v1"},
				{Name: "LLM_API_KEY", Value: "facade-token"},
				{Name: "LLM_MODEL", Value: "baizhi/deepseek-flash"},
				{Name: "LLM_API_PROTOCOL", Value: "responses"},
			})

			if _, err := ensureFacadeAgentConfig(ctx, agent, bareModelConfig(root), store, sandbox, "custom/gpt-custom"); err != nil {
				t.Fatalf("Ensure%sFacadeConfig returned error: %v", agent, err)
			}
			if len(store.savedTokens) != 1 || store.savedTokens[0].Model != "gpt-custom" {
				t.Fatalf("%s token = %#v, want custom/gpt-custom", agent, store.savedTokens)
			}
		})
	}
}

// The declared <connection>/<model> keeps its established meaning when the
// sandbox supplies no provider environment: the prefix names a real connection
// and the whole remainder is the literal model id.
func TestFacadesHonorDeclaredConnectionWithoutSessionEnv(t *testing.T) {
	isolateLLMEnv(t)
	ctx := context.Background()
	for _, agent := range []string{"pi", "dsh", "opencode"} {
		t.Run(agent, func(t *testing.T) {
			root := t.TempDir()
			store := newBareModelFacadeStore()
			store.providers = []Provider{gatewayConnection()}
			sandbox := bareModelSandbox(root, "configured-"+agent)

			if _, err := ensureFacadeAgentConfig(ctx, agent, bareModelConfig(root), store, sandbox, "gateway/qwen3-8b"); err != nil {
				t.Fatalf("Ensure%sFacadeConfig returned error: %v", agent, err)
			}
			if len(store.savedTokens) != 1 || store.savedTokens[0].Model != "qwen3-8b" {
				t.Fatalf("%s token = %#v, want gateway/qwen3-8b", agent, store.savedTokens)
			}
		})
	}
}

func ensureFacadeAgentConfig(ctx context.Context, agent string, config *appconfig.Config, store *bareModelFacadeStore, sandbox *domain.Sandbox, model string) (map[string]string, error) {
	switch agent {
	case "codex":
		return EnsureCodexFacadeConfig(ctx, CodexFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: model})
	case "pi":
		return EnsurePiFacadeConfig(ctx, PiFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: model})
	case "dsh":
		return EnsureDshFacadeConfig(ctx, DshFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: model})
	case "opencode":
		return EnsureOpenCodeFacadeConfig(ctx, OpenCodeFacadeConfigRequest{Config: config, Store: store, Sandbox: sandbox, Model: model})
	default:
		return nil, nil
	}
}
