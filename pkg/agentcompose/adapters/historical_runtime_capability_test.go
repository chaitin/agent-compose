package adapters

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

func TestHistoricalUncompiledRuntimeOperationsPreserveState(t *testing.T) {
	ctx := context.Background()
	config, store, session, provider := newHistoricalUncompiledRuntimeFixture(t)
	initialSummary := session.Summary
	initialVM, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState before operations returned error: %v", err)
	}
	initialProxy, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState before operations returned error: %v", err)
	}

	sandboxDriver := newTestSandboxDriver(config, store, nil, provider)
	operations := []struct {
		name string
		call func() error
	}{
		{name: "start", call: func() error { return sandboxDriver.StartSandboxVM(ctx, session) }},
		{name: "stop", call: func() error { return sandboxDriver.StopSandboxVM(ctx, session) }},
		{name: "remove", call: func() error { return sandboxDriver.RemoveSandboxVM(ctx, session) }},
		{name: "exec", call: func() error {
			_, err := NewCellExecutor(config, store, provider, nil).ExecuteCell(ctx, session, execution.CellTypeShell, "echo history")
			return err
		}},
	}
	for _, operation := range operations {
		t.Run(operation.name, func(t *testing.T) {
			assertRuntimeNotCompiledError(t, operation.call())
			assertHistoricalRuntimeState(t, ctx, store, session.Summary.ID, initialSummary, initialVM, initialProxy)
		})
	}

	loaded, err := store.GetSandbox(ctx, session.Summary.ID)
	if err != nil || loaded.Summary.Driver != initialSummary.Driver || loaded.Summary.RuntimeRef != initialSummary.RuntimeRef {
		t.Fatalf("historical sandbox inspect = %#v, %v", loaded, err)
	}
	listed, err := store.ListSandboxes(ctx, domain.SandboxListOptions{})
	if err != nil || len(listed.Sandboxes) != 1 || listed.Sandboxes[0].Summary.ID != session.Summary.ID {
		t.Fatalf("historical sandbox list = %#v, %v", listed, err)
	}
}

func TestHistoricalUncompiledExecPreflightHasNoArtifactsOrRecords(t *testing.T) {
	ctx := context.Background()
	config, store, session, provider := newHistoricalUncompiledRuntimeFixture(t)
	runner := NewAgentRunner(AgentRunnerDeps{
		Config:   config,
		Store:    store,
		ConfigDB: nil,
		Agents:   nil,
		Runtimes: provider,
	})
	executor := NewAgentExecutor(config, store, nil, runner)

	_, _, _, err := executor.ExecuteAgentRequest(ctx, session, execution.ExecuteAgentRequest{Agent: "codex", Message: "hello"})
	assertRuntimeNotCompiledError(t, err)
	if _, _, err := runner.ExecuteAgentRun(ctx, AgentRunRequest{
		Session:           session,
		Agent:             "codex",
		AgentDefinitionID: "",
		Model:             "",
		RunID:             "",
		Message:           "hello",
		OutputSchemaJSON:  "",
	}, nil); err == nil {
		t.Fatal("ExecuteAgentRun returned nil error for uncompiled historical runtime")
	} else {
		assertRuntimeNotCompiledError(t, err)
	}
	scheduler := NewSchedulerCommandExecutor(SchedulerCommandExecutorDeps{Config: config, Store: store, Runtimes: provider})
	_, err = scheduler.ExecuteSchedulerCommand(ctx, session, domain.SchedulerCommandRequest{Mode: "shell", Script: "echo history"})
	assertRuntimeNotCompiledError(t, err)

	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil || len(cells) != 0 {
		t.Fatalf("cells after unsupported execs = %#v, %v", cells, err)
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil || len(events) != 0 {
		t.Fatalf("events after unsupported execs = %#v, %v", events, err)
	}
	for _, path := range []string{
		filepath.Join(execution.HostSandboxDir(session), "state", "cells"),
		filepath.Join(execution.HostSandboxDir(session), "state", "agents", "prompts"),
		execution.HostAgentSystemPromptPath(session),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("unsupported exec created %s or stat failed: %v", path, err)
		}
	}
}

func newHistoricalUncompiledRuntimeFixture(t *testing.T) (*appconfig.Config, *sandboxstore.Store, *domain.Sandbox, RuntimeProvider) {
	t.Helper()
	uncompiledDriver := firstUncompiledRuntimeDriver()
	if uncompiledDriver == "" {
		t.Skip("all recognized runtime drivers are compiled")
	}
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverDocker,
		DefaultImage:         "guest:latest",
		DockerDefaultImage:   "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/state",
		GuestHomePath:        "/root",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  time.Second,
		SandboxStopTimeout:   time.Second,
		AgentTimeout:         time.Second,
	}
	store, err := sandboxstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(context.Background(), "historical", "", uncompiledDriver, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	session.Summary.RuntimeRef = "original-runtime-ref"
	if err := store.UpdateSandbox(context.Background(), session); err != nil {
		t.Fatalf("UpdateSandbox returned error: %v", err)
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{
		Driver:      uncompiledDriver,
		Mode:        uncompiledDriver,
		BoxID:       "original-box",
		BoxName:     "original-box-name",
		RuntimeHome: "/original/runtime",
		LastError:   "original-error",
	}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	if err := store.SaveProxyState(session.Summary.ID, domain.ProxyState{Enabled: true, HostPort: 12345, GuestPort: 8888, ProxyPath: "/original"}); err != nil {
		t.Fatalf("SaveProxyState returned error: %v", err)
	}
	provider, err := NewRuntimeProvider(config, store)
	if err != nil {
		t.Fatalf("NewRuntimeProvider(Docker default) returned error: %v", err)
	}
	return config, store, session, provider
}

func assertRuntimeNotCompiledError(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, driverpkg.ErrRuntimeDriverNotCompiled) || !errors.Is(err, domain.ErrUnsupported) {
		t.Fatalf("error = %v, want ErrRuntimeDriverNotCompiled and domain.ErrUnsupported", err)
	}
}

func assertHistoricalRuntimeState(t *testing.T, ctx context.Context, store *sandboxstore.Store, sandboxID string, summary domain.SandboxSummary, vmState domain.VMState, proxyState domain.ProxyState) {
	t.Helper()
	loaded, err := store.GetSandbox(ctx, sandboxID)
	if err != nil {
		t.Fatalf("GetSandbox after operation returned error: %v", err)
	}
	if loaded.Summary.Driver != summary.Driver || loaded.Summary.VMStatus != summary.VMStatus || loaded.Summary.RuntimeRef != summary.RuntimeRef {
		t.Fatalf("summary changed: got %#v, want driver/status/runtime ref from %#v", loaded.Summary, summary)
	}
	gotVM, err := store.GetVMState(sandboxID)
	if err != nil || !reflect.DeepEqual(gotVM, vmState) {
		t.Fatalf("VM state after operation = %#v, %v; want %#v", gotVM, err, vmState)
	}
	gotProxy, err := store.GetProxyState(sandboxID)
	if err != nil || !reflect.DeepEqual(gotProxy, proxyState) {
		t.Fatalf("proxy state after operation = %#v, %v; want %#v", gotProxy, err, proxyState)
	}
}
