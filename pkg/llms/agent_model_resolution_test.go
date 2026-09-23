package llms

import (
	"context"
	"errors"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type catalogStoreStub struct {
	providers    []Provider
	models       []Model
	bindings     []ProviderModelBinding
	defaultID    string
	defaultModel string
	hasDefault   bool
	err          error
}

func (s catalogStoreStub) ListEnabledLLMProviders(context.Context) ([]Provider, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]Provider(nil), s.providers...), nil
}

func (s catalogStoreStub) ListEnabledLLMModels(context.Context) ([]Model, error) {
	return append([]Model(nil), s.models...), nil
}

func (s catalogStoreStub) ListLLMProviderModelConfigs(context.Context) ([]ProviderModelBinding, error) {
	return append([]ProviderModelBinding(nil), s.bindings...), nil
}

func (s catalogStoreStub) DefaultLLMModelReference(context.Context) (string, string, bool, error) {
	if s.err != nil {
		return "", "", false, s.err
	}
	return s.defaultID, s.defaultModel, s.hasDefault, nil
}

func catalogStoreWithDefault() catalogStoreStub {
	return catalogStoreStub{
		providers:    []Provider{{ID: "gateway", ProviderType: ProviderFamilyOpenAI, Enabled: true}},
		models:       []Model{{ID: "catalog-model", Name: "catalog-model", Enabled: true}},
		defaultID:    "gateway",
		defaultModel: "catalog-model",
		hasDefault:   true,
	}
}

func TestResolveAgentModelsPrecedence(t *testing.T) {
	store := catalogStoreWithDefault()
	tests := []struct {
		name  string
		agent domain.AgentDefinition
		want  AgentModelResolution
	}{
		{
			name:  "project declaration wins over agent env and catalog default",
			agent: domain.AgentDefinition{Provider: "codex", Model: "  project-model  ", EnvItems: []domain.SandboxEnvVar{{Name: "CODEX_MODEL", Value: "agent-model"}}},
			want:  AgentModelResolution{Model: "project-model", Source: AgentModelSourceProject},
		},
		{
			name:  "model id containing a slash is reported intact",
			agent: domain.AgentDefinition{Provider: "gemini", Model: "dev/gpt-5.5"},
			want:  AgentModelResolution{Model: "dev/gpt-5.5", Source: AgentModelSourceProject},
		},
		{
			name: "a declared upstream's model wins over the catalog default",
			agent: domain.AgentDefinition{Provider: "codex", EnvItems: []domain.SandboxEnvVar{
				{Name: "OPENAI_API_KEY", Value: "sk-openai"},
				{Name: "CODEX_MODEL", Value: "  openai/gpt-5.5  "},
			}},
			want: AgentModelResolution{Model: "openai/gpt-5.5", Source: AgentModelSourceAgentEnv},
		},
		{
			name:  "catalog default fills the gap",
			agent: domain.AgentDefinition{Provider: "codex", Model: "   "},
			want:  AgentModelResolution{Model: "catalog-model", Source: AgentModelSourceDaemonDefault},
		},
		{
			// Regression: a model in the agent's environment is only used when
			// that environment also owns the upstream. Without a credential the
			// run is managed, never reads the environment, and uses the catalog
			// default — so reporting agent_env here described a run that would
			// not happen.
			name:  "a model key without a declared upstream falls through to the catalog default",
			agent: domain.AgentDefinition{Provider: "codex", EnvItems: []domain.SandboxEnvVar{{Name: "CODEX_MODEL", Value: "agent-model"}}},
			want:  AgentModelResolution{Model: "catalog-model", Source: AgentModelSourceDaemonDefault},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ResolveAgentModels(context.Background(), store, []domain.AgentDefinition{test.agent})
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 {
				t.Fatalf("resolutions = %#v, want one per agent", got)
			}
			if got[0] != test.want {
				t.Fatalf("resolution = %#v, want %#v", got[0], test.want)
			}
		})
	}
}

func TestResolveAgentModelsReadsProviderSpecificAgentEnvironmentKeys(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		env      []domain.SandboxEnvVar
		want     string
	}{
		{name: "codex preferred key", provider: "codex", env: []domain.SandboxEnvVar{{Name: "CODEX_MODEL", Value: "codex-model"}, {Name: "LLM_MODEL", Value: "generic-model"}}, want: "codex-model"},
		{name: "codex generic fallback", provider: "codex", env: []domain.SandboxEnvVar{{Name: "LLM_MODEL", Value: "generic-model"}}, want: "generic-model"},
		{name: "claude preferred key", provider: "claude", env: []domain.SandboxEnvVar{{Name: "ANTHROPIC_MODEL", Value: "anthropic-model"}, {Name: "CLAUDE_MODEL", Value: "claude-model"}}, want: "anthropic-model"},
		{name: "claude legacy key", provider: "claude", env: []domain.SandboxEnvVar{{Name: "CLAUDE_MODEL", Value: "claude-model"}}, want: "claude-model"},
		{name: "claude generic fallback", provider: "claude", env: []domain.SandboxEnvVar{{Name: "LLM_MODEL", Value: "generic-model"}}, want: "generic-model"},
		{name: "opencode preferred key", provider: "opencode", env: []domain.SandboxEnvVar{{Name: "OPENCODE_MODEL", Value: "opencode-model"}}, want: "opencode-model"},
		{name: "opencode generic fallback", provider: "opencode", env: []domain.SandboxEnvVar{{Name: "LLM_MODEL", Value: "generic-model"}}, want: "generic-model"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			env := append([]domain.SandboxEnvVar{{Name: "LLM_API_KEY", Value: "sk-declared"}}, test.env...)
			resolutions, err := ResolveAgentModels(context.Background(), catalogStoreStub{}, []domain.AgentDefinition{{Provider: test.provider, EnvItems: env}})
			if err != nil {
				t.Fatal(err)
			}
			resolution := resolutions[0]
			if resolution.Model != test.want {
				t.Fatalf("model = %q, want %q", resolution.Model, test.want)
			}
			wantSource := AgentModelSourceAgentEnv
			if test.want == "" {
				wantSource = AgentModelSourceProviderDefault
			}
			if resolution.Source != wantSource {
				t.Fatalf("source = %q, want %q", resolution.Source, wantSource)
			}
		})
	}
}

func TestResolveAgentModelsReportsProviderDefaultWhenNoModelExists(t *testing.T) {
	// Providers that once reported "unresolved" are treated like every other
	// provider: the daemon contributes no model, so the upstream owns it.
	store := catalogStoreStub{
		providers: []Provider{{ID: "gateway", ProviderType: ProviderFamilyOpenAI, Enabled: true}},
		models:    []Model{{ID: "catalog-model", Name: "catalog-model", Enabled: true}},
	}
	agents := []domain.AgentDefinition{{Provider: "gemini"}, {Provider: "opencode"}, {Provider: "pi"}, {Provider: "dsh"}}
	resolutions, err := ResolveAgentModels(context.Background(), store, agents)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolutions) != len(agents) {
		t.Fatalf("resolutions = %#v, want one per agent", resolutions)
	}
	for i, resolution := range resolutions {
		if resolution != (AgentModelResolution{Source: AgentModelSourceProviderDefault}) {
			t.Fatalf("resolution for %s = %#v, want empty provider default", agents[i].Provider, resolution)
		}
	}
}

func TestResolveAgentModelsReturnsCatalogLoadFailure(t *testing.T) {
	wantErr := errors.New("read failed")
	_, err := ResolveAgentModels(context.Background(), catalogStoreStub{err: wantErr}, []domain.AgentDefinition{{Provider: "codex"}})
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
}
