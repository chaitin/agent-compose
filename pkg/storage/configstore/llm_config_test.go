package configstore

import (
	"context"
	"testing"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestSessionProtocolOverridePreservesSharedModelMetadata(t *testing.T) {
	for _, key := range []string{"LLM_API_ENDPOINT", "LLM_API_PROTOCOL", "LLM_API_KEY", "OPENAI_API_KEY", "LLM_MODEL"} {
		t.Setenv(key, "")
	}
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("initSchema returned error: %v", err)
	}
	if _, err := store.ReplaceGlobalEnv(ctx, []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://global.example/v1"},
		{Name: "LLM_API_PROTOCOL", Value: llms.APIProtocolResponses},
		{Name: "LLM_API_KEY", Value: "global-key", Secret: true},
		{Name: "LLM_MODEL", Value: "shared-model"},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}

	initial, err := llms.ResolveLLMTarget(ctx, &appconfig.Config{}, store, "")
	if err != nil {
		t.Fatalf("ResolveLLMTarget returned error: %v", err)
	}
	if !initial.Model.DefaultModel || initial.Model.Scope != llms.ProviderScopeEnvDefault {
		t.Fatalf("initial shared model = %#v", initial.Model)
	}

	session, err := llms.ResolveRuntimeLLMTargetWithEnv(ctx, &appconfig.Config{}, store, "sandbox-protocol", llms.ProviderFamilyOpenAI, "", "", []domain.SandboxEnvVar{
		{Name: "LLM_API_PROTOCOL", Value: llms.APIProtocolChatCompletions},
	})
	if err != nil {
		t.Fatalf("ResolveRuntimeLLMTargetWithEnv returned error: %v", err)
	}
	if session.Provider.Scope != llms.ProviderScopeSessionEnv || session.WireAPI != llms.APIProtocolChatCompletions {
		t.Fatalf("session target = %#v", session)
	}

	models, err := store.ListEnabledLLMModels(ctx)
	if err != nil {
		t.Fatalf("ListEnabledLLMModels returned error: %v", err)
	}
	if len(models) != 1 || models[0].ID != "shared-model" || !models[0].DefaultModel || models[0].Scope != llms.ProviderScopeEnvDefault {
		t.Fatalf("shared model metadata after session binding = %#v", models)
	}
}
