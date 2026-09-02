package adapters

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

// TestSchedulerHostAgentExecutorReturnsAssistantMessage guards against
// SchedulerHostAgentExecutor.ExecuteAgent silently discarding the provider's
// final assistant message (as it did before this fix), which forced
// RuntimeHost.Agent to fall back to the raw transcript for both Text and
// FinalText.
func TestSchedulerHostAgentExecutorReturnsAssistantMessage(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/data/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
		AgentTimeout:         2 * time.Second,
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "scheduler host session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	runner := NewAgentRunner(AgentRunnerDeps{
		Config:   config,
		Store:    store,
		ConfigDB: nil,
		Agents:   nil,
		Runtimes: fakeRuntimeProvider{runtime: &fakeAgentRuntime{}},
	})
	executor := SchedulerHostAgentExecutor{Executor: NewAgentExecutor(config, store, nil, runner)}

	cell, assistantMessage, err := executor.ExecuteAgent(ctx, session, schedulers.HostAgentExecutionRequest{
		Provider: "codex",
		Prompt:   "hello",
	})
	if err != nil {
		t.Fatalf("ExecuteAgent returned error: %v", err)
	}
	if assistantMessage != "done" {
		t.Fatalf("assistantMessage = %q, want the provider's final message %q", assistantMessage, "done")
	}
	if assistantMessage == cell.Output {
		t.Fatalf("assistantMessage must not equal the raw transcript cell.Output (%q)", cell.Output)
	}
}
