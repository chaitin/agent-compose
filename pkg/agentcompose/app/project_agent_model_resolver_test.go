package app

import (
	"context"
	"testing"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/compose"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func newProjectAgentModelResolverFixture(t *testing.T, yaml string, config appconfig.Config) (domain.ProjectRecord, []domain.ProjectAgentRecord, *projectAgentModelResolver) {
	t.Helper()
	ctx := context.Background()
	// Model resolution consults os.Getenv as a fallback; clear ambient LLM
	// environment so the assertions depend only on the injected config.
	for _, key := range []string{"LLM_MODEL", "LLM_API_KEY", "LLM_API_HEADERS", "LLM_API_ENDPOINT", "LLM_API_PROTOCOL", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"} {
		t.Setenv(key, "")
	}
	store := newRunSupervisorTestConfigStore(t)
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
	return project, agents, newProjectAgentModelResolver(&config, store)
}

func TestProjectAgentModelResolverUsesCurrentRevisionAndDaemonDefault(t *testing.T) {
	ctx := context.Background()
	project, agents, resolver := newProjectAgentModelResolverFixture(t,
		"name: model-preview\nagents:\n  coder:\n    provider: codex\n",
		appconfig.Config{
			LLMAPIEndpoint: "https://daemon.example.test/v1",
			LLMAPIProtocol: "responses",
			LLMAPIKey:      "daemon-key",
			LLMModel:       "dev/gpt-5.5",
		})
	resolutions, err := resolver.ResolveProjectAgentModels(ctx, project, agents)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolutions["coder"]
	if resolution.Model != "dev/gpt-5.5" || resolution.Source != "daemon_default" {
		t.Fatalf("resolution = %#v", resolution)
	}
}

func TestProjectAgentModelResolverUsesAgentRecordEnv(t *testing.T) {
	ctx := context.Background()
	project, agents, resolver := newProjectAgentModelResolverFixture(t,
		"name: model-env\nagents:\n  coder:\n    provider: codex\n    env:\n      CODEX_MODEL: gpt-env-model\n",
		appconfig.Config{
			LLMAPIEndpoint: "https://daemon.example.test/v1",
			LLMAPIProtocol: "responses",
			LLMAPIKey:      "daemon-key",
			LLMModel:       "dev/gpt-5.5",
		})
	resolutions, err := resolver.ResolveProjectAgentModels(ctx, project, agents)
	if err != nil {
		t.Fatal(err)
	}
	resolution := resolutions["coder"]
	if resolution.Model != "gpt-env-model" || resolution.Source != "agent_env" {
		t.Fatalf("resolution = %#v", resolution)
	}
}
