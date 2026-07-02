package loaders

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"os"
	"path/filepath"
	"testing"
)

const agentSystemPromptFileName = "system-prompt.txt"

func TestLoaderRunHostAgentWritesSystemPromptFromBoundAgentDefinition(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "loader-box:latest",
		GuestWorkspacePath:   "/data/workspace",
		GuestStateRoot:       "/data/state",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("MkdirAll(session root) returned error: %v", err)
	}

	configDB := newLoaderEventTestConfigStore(t)
	agent, err := configDB.CreateAgentDefinition(ctx, AgentDefinition{
		ID:           "loader-agent-prompt",
		Name:         "Loader Prompt Agent",
		Provider:     "codex",
		SystemPrompt: "Reply only from loader",
		Enabled:      true,
	})
	if err != nil {
		t.Fatalf("CreateAgentDefinition returned error: %v", err)
	}
	loader, err := configDB.CreateLoader(ctx, Loader{
		Summary: LoaderSummary{
			Name:         "Loader With Agent Prompt",
			Runtime:      LoaderRuntimeScheduler,
			Enabled:      true,
			AgentID:      agent.ID,
			DefaultAgent: "codex",
		},
		Script: "function main() {}",
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}

	store := mustTestStore(t, config)
	runtime := &fakeLoaderAgentRuntime{}
	driver := &fakeSessionDriver{}
	runtimes := fixedRuntimeProvider{runtime: runtime}
	manager := newTestLoaderManager(t, ManagerDeps{
		Config:   config,
		RootCtx:  ctx,
		Store:    store,
		ConfigDB: configDB,
		Driver:   driver,
		Executor: NewExecutor(config, store, configDB, runtimes, nil),
		Engine:   &QJSLoaderEngine{},
	})
	host := &loaderRunHost{
		manager:      manager,
		loader:       loader,
		run:          &LoaderRunSummary{ID: "run-loader-system-prompt", LoaderID: loader.Summary.ID},
		triggerEvent: loaderTriggerEventMetadata{},
	}

	result, err := host.Agent(ctx, "summarize loader state", LoaderAgentRequest{})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if !result.Success || result.SessionID == "" {
		t.Fatalf("loader agent result = %#v", result)
	}

	hostPath := filepath.Join(config.SessionRoot, result.SessionID, "state", "agents", "system-prompts", agentSystemPromptFileName)
	content, err := os.ReadFile(hostPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) returned error: %v", hostPath, err)
	}
	if string(content) != "Reply only from loader" {
		t.Fatalf("system prompt file content = %q", string(content))
	}
}
