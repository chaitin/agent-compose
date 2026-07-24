package runtimefacade

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	"agent-compose/pkg/llms"
	domain "agent-compose/pkg/model"
)

func TestE2EClaudeModelProviderOverrideSurvivesSandboxReload(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := e2eRuntimeFacadeConfig(root)
	configStore, sandboxStore, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}

	const model = "claude-e2e-model"
	providerEnv := []domain.SandboxEnvVar{
		{Name: "ANTHROPIC_API_KEY", Value: "fixture-key", Secret: true},
		{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.test"},
		{Name: "CLAUDE_MODEL", Value: model},
		{Name: "CLAUDE_CODE_PATH", Value: "/opt/claude"},
	}
	sandbox, err := sandboxStore.CreateSandbox(
		ctx,
		"Claude provider override",
		"",
		driverpkg.RuntimeDriverDocker,
		"guest:latest",
		"",
		domain.SandboxTypeManual,
		nil,
		llms.FilterPersistedRuntimeEnv(providerEnv),
		nil,
	)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	llms.SetSandboxProviderEnvItems(sandbox, providerEnv)
	if len(sandbox.ProviderEnvOverrideNames) != 3 ||
		!slices.Contains(sandbox.ProviderEnvOverrideNames, "CLAUDE_MODEL") ||
		slices.Contains(sandbox.ProviderEnvOverrideNames, "CLAUDE_CODE_PATH") {
		t.Fatalf("provider provenance names = %#v", sandbox.ProviderEnvOverrideNames)
	}
	requireClaudeFacadeModel(t, ctx, config, configStore, sandbox, "run-before-reload", model)
	if err := sandboxStore.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("persist sandbox: %v", err)
	}

	reloaded, err := sandboxStore.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("reload sandbox: %v", err)
	}
	if len(reloaded.ProviderEnvItems) != 0 || llms.EnvItemValue(reloaded.EnvItems, "ANTHROPIC_API_KEY") != "" {
		t.Fatal("reloaded sandbox retained transient provider credentials")
	}
	if llms.EnvItemValue(reloaded.EnvItems, "CLAUDE_CODE_PATH") != "/opt/claude" ||
		slices.Contains(reloaded.ProviderEnvOverrideNames, "CLAUDE_CODE_PATH") {
		t.Fatalf("ordinary Claude runtime environment was reclassified: %#v", reloaded.ProviderEnvOverrideNames)
	}
	anthropicEnv, err := llms.SandboxProviderEnvItems(ctx, configStore, reloaded, llms.ProviderFamilyAnthropic)
	if err != nil {
		t.Fatalf("restore Anthropic provider environment: %v", err)
	}
	if llms.EnvItemValue(anthropicEnv, "CLAUDE_MODEL") != model ||
		llms.EnvItemValue(anthropicEnv, "ANTHROPIC_API_KEY") == "" ||
		llms.EnvItemValue(anthropicEnv, "CLAUDE_CODE_PATH") != "" {
		t.Fatal("restored Anthropic provider environment is incomplete or contains ordinary runtime values")
	}
	openAIEnv, err := llms.SandboxProviderEnvItems(ctx, configStore, reloaded, llms.ProviderFamilyOpenAI)
	if err != nil {
		t.Fatalf("restore OpenAI provider environment: %v", err)
	}
	if llms.EnvItemValue(openAIEnv, "CLAUDE_MODEL") != "" {
		t.Fatal("OpenAI provider environment contains CLAUDE_MODEL")
	}

	rawToken := requireClaudeFacadeModel(t, ctx, config, configStore, reloaded, "run-after-reload", model)
	token, err := configStore.GetLLMFacadeToken(ctx, rawToken)
	if err != nil {
		t.Fatalf("load facade token: %v", err)
	}
	if token.Model != model || token.ProviderID != llms.SessionEnvProviderID(sandbox.Summary.ID, llms.ProviderFamilyAnthropic) {
		t.Fatalf("reloaded facade token scope = model %q, provider %q", token.Model, token.ProviderID)
	}
}

func TestE2EClaudeKeepsGenericResponsesProviderAcrossSandboxReload(t *testing.T) {
	isolateLLMEnv(t)

	ctx := context.Background()
	root := t.TempDir()
	config := e2eRuntimeFacadeConfig(root)
	configStore, sandboxStore, err := testutil.OpenStores(t, config)
	if err != nil {
		t.Fatalf("open stores: %v", err)
	}

	const model = "generic-e2e-model"
	providerEnv := []domain.SandboxEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://openai.example.test/base"},
		{Name: "LLM_API_PROTOCOL", Value: llms.APIProtocolResponses},
		{Name: "LLM_API_KEY", Value: "fixture-key", Secret: true},
		{Name: "LLM_MODEL", Value: model},
	}
	sandbox, err := sandboxStore.CreateSandbox(
		ctx,
		"Claude generic provider compatibility",
		"",
		driverpkg.RuntimeDriverDocker,
		"guest:latest",
		"",
		domain.SandboxTypeManual,
		nil,
		llms.FilterPersistedRuntimeEnv(providerEnv),
		nil,
	)
	if err != nil {
		t.Fatalf("create sandbox: %v", err)
	}
	llms.SetSandboxProviderEnvItems(sandbox, providerEnv)
	providerID := llms.SessionEnvProviderID(sandbox.Summary.ID, llms.ProviderFamilyOpenAI)
	requireTokenScope := func(rawToken string) {
		t.Helper()
		token, err := configStore.GetLLMFacadeToken(ctx, rawToken)
		if err != nil {
			t.Fatalf("load facade token: %v", err)
		}
		if token.Model != model || token.ProviderID != providerID || token.WireAPI != llms.APIProtocolMessages {
			t.Fatalf("Claude compatibility token scope = model %q, provider %q, wire API %q", token.Model, token.ProviderID, token.WireAPI)
		}
		target, err := llms.ResolveRuntimeLLMTarget(ctx, config, configStore, model, token.ProviderID)
		if err != nil {
			t.Fatalf("resolve facade compatibility target: %v", err)
		}
		if target.Provider.ProviderType != llms.ProviderFamilyOpenAI || target.WireAPI != llms.APIProtocolResponses {
			t.Fatalf("facade compatibility target = family %q, wire API %q", target.Provider.ProviderType, target.WireAPI)
		}
		providers, err := configStore.ListEnabledLLMProviders(ctx)
		if err != nil {
			t.Fatalf("list providers: %v", err)
		}
		for _, provider := range providers {
			if provider.ID == providerID && provider.ProviderType == llms.ProviderFamilyOpenAI && provider.APIKey != "" {
				return
			}
		}
		t.Fatal("generic OpenAI session provider or its credential is missing")
	}

	rawToken := requireClaudeFacadeModel(t, ctx, config, configStore, sandbox, "run-generic-before-reload", model)
	requireTokenScope(rawToken)
	if err := sandboxStore.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("persist sandbox: %v", err)
	}
	reloaded, err := sandboxStore.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("reload sandbox: %v", err)
	}
	if len(reloaded.ProviderEnvItems) != 0 || llms.EnvItemValue(reloaded.EnvItems, "LLM_API_KEY") != "" {
		t.Fatal("reloaded sandbox retained transient generic provider credentials")
	}
	rawToken = requireClaudeFacadeModel(t, ctx, config, configStore, reloaded, "run-generic-after-reload", model)
	requireTokenScope(rawToken)
}

func e2eRuntimeFacadeConfig(root string) *appconfig.Config {
	return &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		DbAddr:               filepath.Join(root, "data.db"),
		RuntimeDriver:        driverpkg.RuntimeDriverDocker,
		DefaultImage:         "guest:latest",
		RuntimeBaseURL:       "http://agent-compose.test:7410",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/jupyter",
	}
}

func requireClaudeFacadeModel(t *testing.T, ctx context.Context, config *appconfig.Config, store FacadeStore, sandbox *domain.Sandbox, runID, wantModel string) string {
	t.Helper()
	runtimeConfig, err := EnsureSessionAgentRuntimeConfig(ctx, config, store, sandbox, "claude", "", TokenSourceAgent, runID)
	if err != nil {
		t.Fatalf("configure Claude facade: %v", err)
	}
	if runtimeConfig.Env["CLAUDE_MODEL"] != wantModel || runtimeConfig.Env["ANTHROPIC_MODEL"] != wantModel {
		t.Fatalf("Claude facade model = %q/%q, want %q", runtimeConfig.Env["CLAUDE_MODEL"], runtimeConfig.Env["ANTHROPIC_MODEL"], wantModel)
	}
	rawToken := runtimeConfig.Env["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if rawToken == "" {
		t.Fatal("Claude facade token is empty")
	}
	return rawToken
}
