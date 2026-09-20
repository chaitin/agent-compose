package llms

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/execution"
)

func TestIntegrationOpenCodeGuestReferenceMatchesConfiguredLiteral(t *testing.T) {
	isolateLLMEnv(t)
	for _, family := range []string{ProviderFamilyAnthropic, ProviderFamilyOpenAI} {
		t.Run(family, func(t *testing.T) {
			root := t.TempDir()
			store := newBareModelFacadeStore()
			provider := gatewayConnection()
			guestProvider := "agent-compose"
			if family == ProviderFamilyAnthropic {
				provider.ProviderType = family
				provider.DefaultWireAPI = APIProtocolMessages
				guestProvider = "anthropic"
			}
			store.providers = []Provider{provider}
			literal := guestProvider + "/model"
			sandbox := bareModelSandbox(root, "namespace")
			env, err := EnsureOpenCodeFacadeConfig(context.Background(), OpenCodeFacadeConfigRequest{
				Config: bareModelConfig(root), Store: store, Sandbox: sandbox, Model: "gateway/" + literal,
			})
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				Provider map[string]struct{ Models map[string]json.RawMessage }
			}
			readFacadeModelConfig(t, filepath.Join(execution.HostSandboxHome(sandbox), ".config", "opencode", "opencode.json"), &config)
			guestKey, guestModel, _ := strings.Cut(env[GuestModelEnvName], "/")
			if guestKey != guestProvider || guestModel != literal || config.Provider[guestKey].Models[guestModel] == nil {
				t.Fatalf("guest reference %q does not address configured literal %q", env[GuestModelEnvName], literal)
			}
			if len(store.savedTokens) != 1 || store.savedTokens[0].Model != guestModel {
				t.Fatal("guest and facade token address different models")
			}
		})
	}
}

func TestIntegrationPiGuestReferenceMatchesConfiguredLiteral(t *testing.T) {
	isolateLLMEnv(t)
	root := t.TempDir()
	store := newBareModelFacadeStore()
	store.providers = []Provider{gatewayConnection()}
	sandbox := bareModelSandbox(root, "namespace")
	env, err := EnsurePiFacadeConfig(context.Background(), PiFacadeConfigRequest{
		Config: bareModelConfig(root), Store: store, Sandbox: sandbox, Model: "gateway/agent-compose/model",
	})
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Providers map[string]struct{ Models []struct{ ID string } }
	}
	readFacadeModelConfig(t, filepath.Join(execution.HostSandboxHome(sandbox), ".pi", "agent", "models.json"), &config)
	guestKey, guestModel, _ := strings.Cut(env[GuestModelEnvName], "/")
	models := config.Providers[guestKey].Models
	if guestKey != "agent-compose" || guestModel != "agent-compose/model" || len(models) != 1 || models[0].ID != guestModel {
		t.Fatalf("guest reference %q does not address the configured literal", env[GuestModelEnvName])
	}
	if len(store.savedTokens) != 1 || store.savedTokens[0].Model != guestModel {
		t.Fatal("guest and facade token address different models")
	}
}

func readFacadeModelConfig(t *testing.T, path string, dest any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		t.Fatal(err)
	}
}
