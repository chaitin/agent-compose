package adapters

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"
)

type fakeCellRuntime struct {
	result domain.ExecResult
}

func (r fakeCellRuntime) EnsureSandbox(context.Context, *domain.Sandbox, domain.VMState, domain.ProxyState) (domain.SandboxVMInfo, error) {
	return domain.SandboxVMInfo{}, nil
}

func (r fakeCellRuntime) StopSandbox(context.Context, *domain.Sandbox, domain.VMState) (bool, error) {
	return false, nil
}

func (r fakeCellRuntime) RemoveSandbox(context.Context, *domain.Sandbox, domain.VMState) error {
	return nil
}

func (r fakeCellRuntime) Exec(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec) (domain.ExecResult, error) {
	return r.result, nil
}

func (r fakeCellRuntime) ExecStream(_ context.Context, _ *domain.Sandbox, _ domain.VMState, _ domain.ExecSpec, stream domain.ExecStreamWriter) (domain.ExecResult, error) {
	if stream != nil {
		stream(domain.ExecChunk{Text: r.result.Stdout})
	}
	return r.result, nil
}

func TestCellExecutorExecuteCellPersistsCellAndEvent(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SandboxRoot:          filepath.Join(root, "sandboxes"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "guest:latest",
		GuestWorkspacePath:   "/workspace",
		GuestStateRoot:       "/state",
		JupyterProxyBasePath: "/agent-compose/session",
		SandboxStartTimeout:  2 * time.Second,
	}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "cell session", "", driverpkg.RuntimeDriverBoxlite, "guest:latest", "", domain.SandboxTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusRunning
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	executor := NewCellExecutor(config, store, fakeRuntimeProvider{runtime: fakeCellRuntime{result: domain.ExecResult{
		Stdout:   "hello\n",
		Output:   "hello\n",
		ExitCode: 0,
		Success:  true,
	}}}, nil)

	cell, err := executor.ExecuteCell(ctx, session, execution.CellTypeShell, "echo hello")
	if err != nil {
		t.Fatalf("ExecuteCell returned error: %v", err)
	}
	if !cell.Success || cell.Stdout != "hello\n" {
		t.Fatalf("cell = %#v", cell)
	}
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) != 1 || cells[0].ID != cell.ID {
		t.Fatalf("stored cells = %#v", cells)
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	if len(events) != 1 || events[0].Type != "kernel.cell.succeeded" {
		t.Fatalf("events = %#v", events)
	}

	var started bool
	var chunks []domain.ExecChunk
	streamed, err := executor.ExecuteCellStream(ctx, session, execution.CellTypePython, "print('hello')", execution.CellExecutionStream{
		OnStart: func(cell domain.NotebookCell) error {
			started = cell.Running && cell.Type == execution.CellTypePython
			return nil
		},
		OnChunk: func(_ string, chunk domain.ExecChunk) error {
			chunks = append(chunks, chunk)
			return nil
		},
	})
	if err != nil || !streamed.Success || !started || len(chunks) != 1 {
		t.Fatalf("ExecuteCellStream cell=%#v started=%v chunks=%#v err=%v", streamed, started, chunks, err)
	}
	cells, err = store.ListCells(ctx, session.Summary.ID)
	if err != nil || len(cells) != 2 {
		t.Fatalf("streamed cells=%#v err=%v", cells, err)
	}

	if _, err := executor.ExecuteCellStream(ctx, session, execution.CellTypeShell, "echo hello", execution.CellExecutionStream{
		OnStart: func(domain.NotebookCell) error {
			return errors.New("start callback failed")
		},
	}); err == nil {
		t.Fatalf("ExecuteCellStream start callback returned nil error")
	}
	if _, err := executor.ExecuteCellStream(ctx, session, execution.CellTypeShell, "echo hello", execution.CellExecutionStream{
		OnChunk: func(string, domain.ExecChunk) error {
			return errors.New("chunk callback failed")
		},
	}); err == nil {
		t.Fatalf("ExecuteCellStream chunk callback returned nil error")
	}

	failingExecutor := NewCellExecutor(config, store, fakeRuntimeProvider{runtime: fakeCellRuntime{result: domain.ExecResult{
		Stderr:   "boom",
		Output:   "boom",
		ExitCode: 9,
		Success:  false,
	}}}, nil)
	failedCell, err := failingExecutor.ExecuteCell(ctx, session, execution.CellTypeShell, "exit 9")
	if err != nil || failedCell.Success || failedCell.ExitCode != 9 {
		t.Fatalf("failed ExecuteCell cell=%#v err=%v", failedCell, err)
	}
	events, err = store.ListEvents(ctx, session.Summary.ID)
	if err != nil || events[len(events)-1].Type != "kernel.cell.failed" {
		t.Fatalf("failed events=%#v err=%v", events, err)
	}
}
