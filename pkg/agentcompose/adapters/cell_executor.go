package adapters

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type CellExecutor struct {
	config   *appconfig.Config
	store    *sandboxstore.Store
	runtimes RuntimeProvider
	streams  *sandboxes.StreamBroker
}

func NewCellExecutor(config *appconfig.Config, store *sandboxstore.Store, runtimes RuntimeProvider, streams *sandboxes.StreamBroker) *CellExecutor {
	return &CellExecutor{config: config, store: store, runtimes: runtimes, streams: streams}
}

// CellExecutionRequest bundles the session, cell content, and output stream
// executeCell needs.
type CellExecutionRequest struct {
	Session  *domain.Sandbox
	CellType string
	Source   string
	Stream   execution.CellExecutionStream
}

func (e *CellExecutor) ExecuteCell(ctx context.Context, session *domain.Sandbox, cellType, source string) (domain.NotebookCell, error) {
	return e.executeCell(ctx, CellExecutionRequest{Session: session, CellType: cellType, Source: source})
}

func (e *CellExecutor) ExecuteCellStream(ctx context.Context, req CellExecutionRequest) (domain.NotebookCell, error) {
	return e.executeCell(ctx, req)
}

// preparedCellExecution is the resolved runtime/state and the initial,
// already-published cell prepareCellExecution assembles before executeCell
// runs the command.
type preparedCellExecution struct {
	VMState     domain.VMState
	Runtime     SandboxRuntime
	CellID      string
	HostCellDir string
	Command     string
	Args        []string
	StartedCell domain.NotebookCell
}

// prepareCellExecution resolves the sandbox's VM state and runtime, creates
// the host cell state dir, writes the cell script, and publishes the started
// cell (invoking req.Stream.OnStart first, if set).
func (e *CellExecutor) prepareCellExecution(ctx context.Context, req CellExecutionRequest) (preparedCellExecution, error) {
	session, cellType, source, stream := req.Session, req.CellType, req.Source, req.Stream
	vmState, err := e.store.GetVMState(session.Summary.ID)
	if err != nil {
		return preparedCellExecution{}, err
	}
	runtime, err := e.runtimes.ForSession(session)
	if err != nil {
		return preparedCellExecution{}, err
	}

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(filepath.Dir(session.Summary.WorkspacePath), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return preparedCellExecution{}, fmt.Errorf("create cell state dir: %w", err)
	}

	guestCellDir := filepath.Join(e.config.GuestStateRoot, "cells", cellID)
	scriptName, command, args := execution.CellExecSpec(cellType, guestCellDir)
	hostScriptPath := filepath.Join(hostCellDir, scriptName)
	if err := os.WriteFile(hostScriptPath, []byte(source), 0o644); err != nil {
		return preparedCellExecution{}, fmt.Errorf("write cell script: %w", err)
	}
	if writer, ok := runtime.(GuestFileWriter); ok {
		guestScriptPath := filepath.Join(guestCellDir, scriptName)
		writeGuestFile := func(ctx context.Context, guestPath string, content []byte) error {
			return writer.WriteGuestFile(ctx, session, vmState, guestPath, content)
		}
		if err := execution.SyncHostFileToGuest(ctx, hostScriptPath, guestScriptPath, writeGuestFile); err != nil {
			return preparedCellExecution{}, fmt.Errorf("push cell script to guest: %w", err)
		}
	}

	startedCell := domain.NotebookCell{
		ID:        cellID,
		Type:      cellType,
		Source:    source,
		CreatedAt: time.Now().UTC(),
		Running:   true,
	}
	if stream.OnStart != nil {
		if err := stream.OnStart(startedCell); err != nil {
			return preparedCellExecution{}, err
		}
	}
	if e.streams != nil {
		e.streams.PublishCellStarted(session.Summary.ID, startedCell)
	}
	return preparedCellExecution{
		VMState:     vmState,
		Runtime:     runtime,
		CellID:      cellID,
		HostCellDir: hostCellDir,
		Command:     command,
		Args:        args,
		StartedCell: startedCell,
	}, nil
}

func (e *CellExecutor) executeCell(ctx context.Context, req CellExecutionRequest) (domain.NotebookCell, error) {
	session, cellType, source, stream := req.Session, req.CellType, req.Source, req.Stream
	appconfig.ApplyDefaultGuestPaths(e.config)
	source = strings.TrimSpace(source)
	if source == "" {
		return domain.NotebookCell{}, fmt.Errorf("source is required")
	}

	cellType, err := execution.NormalizeCellType(cellType)
	if err != nil {
		return domain.NotebookCell{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.config.SandboxStartTimeout)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	prepared, err := e.prepareCellExecution(ctx, CellExecutionRequest{Session: session, CellType: cellType, Source: source, Stream: stream})
	if err != nil {
		return domain.NotebookCell{}, err
	}
	vmState, runtime, cellID, hostCellDir := prepared.VMState, prepared.Runtime, prepared.CellID, prepared.HostCellDir
	command, args, startedAt := prepared.Command, prepared.Args, prepared.StartedCell.CreatedAt

	var streamErrMu sync.Mutex
	var streamErr error
	streamWriter := func(chunk domain.ExecChunk) {
		if e.streams != nil {
			e.streams.PublishCellOutput(session.Summary.ID, cellID, chunk.Text, chunk.Stream)
		}
		if stream.OnChunk != nil {
			if err := stream.OnChunk(cellID, chunk); err != nil {
				streamErrMu.Lock()
				if streamErr == nil {
					streamErr = err
					execCancel()
				}
				streamErrMu.Unlock()
			}
		}
	}
	result, err := runtime.ExecStream(execCtx, session, vmState, domain.ExecSpec{
		Command: command,
		Args:    args,
		Cwd:     e.config.GuestWorkspacePath,
	}, streamWriter)
	streamErrMu.Lock()
	deferredStreamErr := streamErr
	streamErrMu.Unlock()
	if deferredStreamErr != nil {
		return domain.NotebookCell{}, deferredStreamErr
	}
	if err != nil {
		return domain.NotebookCell{}, err
	}

	if err := execution.WriteCellArtifacts(hostCellDir, result); err != nil {
		return domain.NotebookCell{}, err
	}

	cell := domain.NotebookCell{
		ID:        cellID,
		Type:      cellType,
		Source:    source,
		Stdout:    result.Stdout,
		Stderr:    result.Stderr,
		Output:    result.Output,
		ExitCode:  result.ExitCode,
		Success:   result.Success,
		CreatedAt: startedAt,
	}
	if err := e.store.AddCell(ctx, session, cell); err != nil {
		return domain.NotebookCell{}, err
	}
	if e.streams != nil {
		e.streams.PublishCellCompleted(session.Summary.ID, cell)
	}

	eventLevel := "info"
	eventType := "kernel.cell.succeeded"
	eventMessage := fmt.Sprintf("executed %s cell in agent-compose guest", cellType)
	if !result.Success {
		eventLevel = "error"
		eventType = "kernel.cell.failed"
		eventMessage = firstNonEmpty(result.Stderr, fmt.Sprintf("%s cell failed with exit code %d", cellType, result.ExitCode))
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     eventLevel,
		Message:   eventMessage,
		CreatedAt: time.Now().UTC(),
	}
	_ = e.store.AddEvent(ctx, session.Summary.ID, event)
	if e.streams != nil {
		e.streams.PublishEventAdded(session.Summary.ID, event)
	}
	return cell, nil
}
