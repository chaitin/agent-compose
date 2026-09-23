package app

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// newProjectAgentModelResolverFixture saves one project revision from yaml and
// returns a resolver backed by a config store. A non-empty catalogDefaultModel
// seeds the store's model catalog so the preview has a daemon default to find.
func newProjectAgentModelResolverFixture(t *testing.T, yaml, catalogDefaultModel string) (domain.ProjectRecord, []domain.ProjectAgentRecord, *projectAgentModelResolver) {
	t.Helper()
	ctx := context.Background()
	store := newRunSupervisorTestConfigStore(t)
	if catalogDefaultModel != "" {
		baseURL, protocol, apiKey := "https://daemon.example.test/v1", llms.APIProtocolResponses, "daemon-key"
		if err := store.ApplyModelCatalog(ctx, llms.ModelCatalog{
			Default: "gateway/" + catalogDefaultModel,
			Providers: map[string]llms.CatalogProvider{
				"gateway": {BaseURL: &baseURL, Protocol: &protocol, APIKey: &apiKey, Models: []llms.CatalogModel{{ID: catalogDefaultModel}}},
			},
		}); err != nil {
			t.Fatalf("seed model catalog: %v", err)
		}
	}
	raw, err := compose.Parse([]byte(yaml))
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := compose.Normalize(raw, compose.NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-model-preview", Name: normalized.Name, SourcePath: "/tmp/model-preview/agent-compose.yml", SourceJSON: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	specJSON, err := normalized.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	revision, _, err := store.SaveProjectRevision(ctx, domain.ProjectRevisionRecord{ProjectID: project.ID, SpecHash: "sha256:model-preview", SpecJSON: string(specJSON)})
	if err != nil {
		t.Fatal(err)
	}
	project, err = store.GetProject(ctx, project.ID)
	if err != nil {
		t.Fatal(err)
	}
	agents, err := projects.NewAgentRecordsFromSpec(project.ID, revision.Revision, normalized)
	if err != nil {
		t.Fatal(err)
	}
	return project, agents, newProjectAgentModelResolver(store)
}

func TestProjectAgentModelResolverUsesCurrentRevisionAndCatalogDefault(t *testing.T) {
	project, agents, resolver := newProjectAgentModelResolverFixture(t,
		"name: model-preview\nagents:\n  coder:\n    provider: codex\n",
		"dev/gpt-5.5")
	resolutions, err := resolver.ResolveProjectAgentModels(context.Background(), project, agents)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolutions["coder"]
	if resolution.Model != "dev/gpt-5.5" || resolution.Source != llms.AgentModelSourceDaemonDefault {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestProjectAgentModelResolverUsesAgentRecordEnv(t *testing.T) {
	project, agents, resolver := newProjectAgentModelResolverFixture(t,
		"name: model-env\nagents:\n  coder:\n    provider: codex\n    env:\n      OPENAI_API_KEY: sk-declared\n      CODEX_MODEL: gpt-env-model\n",
		"dev/gpt-5.5")
	resolutions, err := resolver.ResolveProjectAgentModels(context.Background(), project, agents)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolutions["coder"]
	if resolution.Model != "gpt-env-model" || resolution.Source != llms.AgentModelSourceAgentEnv {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestProjectAgentModelResolverUsesProjectDeclaredModel(t *testing.T) {
	project, agents, resolver := newProjectAgentModelResolverFixture(t,
		"name: model-declared\nagents:\n  coder:\n    provider: codex\n    model: openai/agent-model\n",
		"dev/gpt-5.5")
	resolutions, err := resolver.ResolveProjectAgentModels(context.Background(), project, agents)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolutions["coder"]
	if resolution.Model != "openai/agent-model" || resolution.Source != llms.AgentModelSourceProject {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestProjectAgentModelResolverReportsProviderDefaultWithoutModel(t *testing.T) {
	project, agents, resolver := newProjectAgentModelResolverFixture(t,
		"name: model-empty\nagents:\n  coder:\n    provider: gemini\n",
		"")
	resolutions, err := resolver.ResolveProjectAgentModels(context.Background(), project, agents)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolutions["coder"]
	if resolution.Model != "" || resolution.Source != llms.AgentModelSourceProviderDefault {
		t.Fatalf("resolution = %#v", resolution)
	}
}
