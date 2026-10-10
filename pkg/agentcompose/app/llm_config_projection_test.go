package app

import (
	"context"
	"errors"
	"testing"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/llms"
)

// A connection id another source owns must not fail startup: the environment is
// a bootstrap fallback, so the projection reports the conflict and the daemon
// keeps running with the operator's configuration.
func TestLoadLLMConfigToleratesEnvProjectionConflict(t *testing.T) {
	t.Setenv("LLM_API_KEY", "env-key")
	t.Setenv("LLM_MODEL", "env-model")
	store := &llmConfigStoreFake{upsertErr: &llms.DefaultConfigConflict{Kind: "provider", ID: "default", Scope: llms.ProviderScopeCatalog}}
	if err := loadLLMConfig(context.Background(), &appconfig.Config{DataRoot: t.TempDir()}, store); err != nil {
		t.Fatalf("loadLLMConfig error = %v, want the conflict tolerated", err)
	}
	if store.upserts != 1 {
		t.Fatalf("projection upserts = %d, want 1", store.upserts)
	}
}

// An unrelated projection failure is still fatal.
func TestLoadLLMConfigReturnsNonConflictProjectionError(t *testing.T) {
	t.Setenv("LLM_API_KEY", "env-key")
	t.Setenv("LLM_MODEL", "env-model")
	store := &llmConfigStoreFake{upsertErr: errors.New("disk full")}
	if err := loadLLMConfig(context.Background(), &appconfig.Config{DataRoot: t.TempDir()}, store); err == nil {
		t.Fatal("loadLLMConfig returned nil for a non-conflict projection error")
	}
}

func TestDefaultModelMissing(t *testing.T) {
	cases := []struct {
		name        string
		store       *llmConfigStoreFake
		wantMissing bool
	}{
		{
			name:        "no connections at all",
			store:       &llmConfigStoreFake{},
			wantMissing: true,
		},
		{
			name:        "catalog default reference resolves",
			store:       &llmConfigStoreFake{providers: []llms.Provider{{ID: "gateway", Enabled: true}}, hasDefault: true, defaultProvider: "gateway", defaultModel: "model"},
			wantMissing: false,
		},
		{
			name: "model flag with a single serving connection resolves",
			store: &llmConfigStoreFake{
				providers: []llms.Provider{{ID: "gateway", Enabled: true}},
				models:    []llms.Model{{ID: "model", Enabled: true, DefaultModel: true}},
				bindings:  []llms.ProviderModelBinding{{ProviderID: "gateway", ModelID: "model"}},
			},
			wantMissing: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			missing, err := defaultModelMissing(context.Background(), tc.store)
			if err != nil {
				t.Fatalf("defaultModelMissing: %v", err)
			}
			if missing != tc.wantMissing {
				t.Fatalf("missing = %v, want %v", missing, tc.wantMissing)
			}
		})
	}
}

type llmConfigStoreFake struct {
	providers       []llms.Provider
	models          []llms.Model
	bindings        []llms.ProviderModelBinding
	hasDefault      bool
	defaultProvider string
	defaultModel    string
	upsertErr       error
	upserts         int
}

func (s *llmConfigStoreFake) ApplyModelCatalog(context.Context, llms.ModelCatalog) error { return nil }

func (s *llmConfigStoreFake) UpsertDefaultLLMConfig(context.Context, llms.Provider, llms.Model) error {
	s.upserts++
	return s.upsertErr
}

func (s *llmConfigStoreFake) ListEnabledLLMProviders(context.Context) ([]llms.Provider, error) {
	return s.providers, nil
}

func (s *llmConfigStoreFake) ListEnabledLLMModels(context.Context) ([]llms.Model, error) {
	return s.models, nil
}

func (s *llmConfigStoreFake) ListLLMProviderModelConfigs(context.Context) ([]llms.ProviderModelBinding, error) {
	return s.bindings, nil
}

func (s *llmConfigStoreFake) DefaultLLMModelReference(context.Context) (string, string, bool, error) {
	return s.defaultProvider, s.defaultModel, s.hasDefault, nil
}
