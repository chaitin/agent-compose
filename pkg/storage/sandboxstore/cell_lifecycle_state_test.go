package sandboxstore

import (
	"context"
	"testing"
	"time"

	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
)

func TestAddCellDoesNotRestoreStaleRunningLifecycleState(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	staleRunning, err := store.CreateSandbox(ctx, "stale command session", "", driverpkg.RuntimeDriverDocker, "", "", "scheduler", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	staleRunning.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, staleRunning); err != nil {
		t.Fatalf("persist running sandbox: %v", err)
	}

	stopped, err := store.GetSandbox(ctx, staleRunning.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox before stop: %v", err)
	}
	stopped.Summary.VMStatus = domain.VMStatusStopped
	stopped.StoppedRuntimePolicy = domain.StoppedRuntimePolicyRetain
	stopped.StoppedRuntime = &domain.StoppedRuntime{State: domain.StoppedRuntimeStateRetained, RequestedAt: time.Now().UTC()}
	if err := store.UpdateSandbox(ctx, stopped); err != nil {
		t.Fatalf("persist out-of-band stop: %v", err)
	}

	cell := domain.NotebookCell{
		ID:        "command-after-stop",
		Type:      execution.CellTypeShell,
		Source:    "git status",
		Running:   false,
		Success:   false,
		ExitCode:  1,
		CreatedAt: time.Now().UTC(),
	}
	if err := store.AddCell(ctx, staleRunning, cell); err != nil {
		t.Fatalf("AddCell with stale command session: %v", err)
	}

	loaded, err := store.GetSandbox(ctx, staleRunning.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox after AddCell: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("VM status after stale AddCell = %q, want %q", loaded.Summary.VMStatus, domain.VMStatusStopped)
	}
	if domain.EffectiveStoppedRuntimeState(loaded) != domain.StoppedRuntimeStateRetained {
		t.Fatalf("stopped runtime after stale AddCell = %#v, want retained", loaded.StoppedRuntime)
	}
	if loaded.Summary.CellCount != 1 {
		t.Fatalf("cell count after stale AddCell = %d, want 1", loaded.Summary.CellCount)
	}
}

func TestAddAgentRunPreservesStoppedLifecycleState(t *testing.T) {
	ctx := context.Background()
	store := newCoverageStore(t)
	sandbox, err := store.CreateSandbox(ctx, "stopped agent run", "", driverpkg.RuntimeDriverDocker, "", "", "agent", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSandbox returned error: %v", err)
	}
	sandbox.Summary.VMStatus = domain.VMStatusStopped
	sandbox.StoppedRuntimePolicy = domain.StoppedRuntimePolicyRetain
	sandbox.StoppedRuntime = &domain.StoppedRuntime{State: domain.StoppedRuntimeStateRetained, RequestedAt: time.Now().UTC()}
	if err := store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatalf("persist stopped sandbox: %v", err)
	}

	if err := store.AddAgentRun(ctx, sandbox.Summary.ID, domain.AgentRun{
		ID:        "agent-run-after-stop",
		Agent:     "codex",
		Message:   "finish after stop",
		Running:   false,
		Success:   false,
		ExitCode:  1,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AddAgentRun returned error: %v", err)
	}

	loaded, err := store.GetSandbox(ctx, sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox after AddAgentRun: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusStopped || domain.EffectiveStoppedRuntimeState(loaded) != domain.StoppedRuntimeStateRetained {
		t.Fatalf("lifecycle after AddAgentRun = status:%q stopped:%#v", loaded.Summary.VMStatus, loaded.StoppedRuntime)
	}
	if loaded.Summary.CellCount != 1 {
		t.Fatalf("cell count after AddAgentRun = %d, want 1", loaded.Summary.CellCount)
	}
}
