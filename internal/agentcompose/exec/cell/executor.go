package cell

import (
	execdomain "agent-compose/internal/agentcompose/exec"
	sessiondomain "agent-compose/internal/agentcompose/session"
	appconfig "agent-compose/internal/config"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type ExecutionStream struct {
	OnStart func(sessiondomain.NotebookCell) error
	OnChunk func(string, sessiondomain.ExecChunk) error
}

type Store interface {
	GetVMState(sessionID string) (sessiondomain.VMState, error)
	AddCell(context.Context, *sessiondomain.Session, sessiondomain.NotebookCell) error
	AddEvent(context.Context, string, sessiondomain.Event) error
}

type Runtime interface {
	ExecStream(context.Context, *sessiondomain.Session, sessiondomain.VMState, sessiondomain.ExecSpec, sessiondomain.ExecStreamWriter) (sessiondomain.ExecResult, error)
}

type RuntimeProvider interface {
	ForSession(*sessiondomain.Session) (Runtime, error)
}

type StreamBroker interface {
	PublishCellStarted(sessionID string, cell sessiondomain.NotebookCell)
	PublishCellOutput(sessionID, cellID, chunk string, isStderr bool)
	PublishCellCompleted(sessionID string, cell sessiondomain.NotebookCell)
	PublishEventAdded(sessionID string, event sessiondomain.Event)
}

type Executor struct {
	Config   *appconfig.Config
	Store    Store
	Runtimes RuntimeProvider
	Streams  StreamBroker
}

func (e Executor) Execute(ctx context.Context, session *sessiondomain.Session, cellType, source string, stream ExecutionStream) (sessiondomain.NotebookCell, error) {
	appconfig.ApplyDefaultGuestPaths(e.Config)
	source = strings.TrimSpace(source)
	if source == "" {
		return sessiondomain.NotebookCell{}, fmt.Errorf("source is required")
	}

	cellType, err := execdomain.NormalizeCellType(cellType)
	if err != nil {
		return sessiondomain.NotebookCell{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, e.Config.SessionStartTimeout)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	vmState, err := e.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return sessiondomain.NotebookCell{}, err
	}
	runtime, err := e.Runtimes.ForSession(session)
	if err != nil {
		return sessiondomain.NotebookCell{}, err
	}

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(filepath.Dir(session.Summary.WorkspacePath), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return sessiondomain.NotebookCell{}, fmt.Errorf("create cell state dir: %w", err)
	}

	guestCellDir := filepath.Join(e.Config.GuestStateRoot, "cells", cellID)
	scriptName, command, args := execdomain.CellExecSpec(cellType, guestCellDir)
	hostScriptPath := filepath.Join(hostCellDir, scriptName)
	if err := os.WriteFile(hostScriptPath, []byte(source), 0o644); err != nil {
		return sessiondomain.NotebookCell{}, fmt.Errorf("write cell script: %w", err)
	}

	startedAt := time.Now().UTC()
	startedCell := sessiondomain.NotebookCell{
		ID:        cellID,
		Type:      cellType,
		Source:    source,
		CreatedAt: startedAt,
		Running:   true,
	}
	if stream.OnStart != nil {
		if err := stream.OnStart(startedCell); err != nil {
			return sessiondomain.NotebookCell{}, err
		}
	}
	e.Streams.PublishCellStarted(session.Summary.ID, startedCell)

	var streamErrMu sync.Mutex
	var streamErr error
	streamWriter := func(chunk sessiondomain.ExecChunk) {
		e.Streams.PublishCellOutput(session.Summary.ID, cellID, chunk.Text, chunk.IsStderr)
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
	result, err := runtime.ExecStream(execCtx, session, vmState, sessiondomain.ExecSpec{
		Command: command,
		Args:    args,
		Cwd:     e.Config.GuestWorkspacePath,
	}, streamWriter)
	streamErrMu.Lock()
	deferredStreamErr := streamErr
	streamErrMu.Unlock()
	if deferredStreamErr != nil {
		return sessiondomain.NotebookCell{}, deferredStreamErr
	}
	if err != nil {
		return sessiondomain.NotebookCell{}, err
	}

	if err := execdomain.WriteCellArtifacts(hostCellDir, source, result); err != nil {
		return sessiondomain.NotebookCell{}, err
	}

	cell := sessiondomain.NotebookCell{
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
	if err := e.Store.AddCell(ctx, session, cell); err != nil {
		return sessiondomain.NotebookCell{}, err
	}
	e.Streams.PublishCellCompleted(session.Summary.ID, cell)

	eventLevel := "info"
	eventType := "kernel.cell.succeeded"
	eventMessage := fmt.Sprintf("executed %s cell in agent-compose guest", cellType)
	if !result.Success {
		eventLevel = "error"
		eventType = "kernel.cell.failed"
		eventMessage = execdomain.FirstNonEmpty(result.Stderr, fmt.Sprintf("%s cell failed with exit code %d", cellType, result.ExitCode))
	}
	event := sessiondomain.Event{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     eventLevel,
		Message:   eventMessage,
		CreatedAt: time.Now().UTC(),
	}
	_ = e.Store.AddEvent(ctx, session.Summary.ID, event)
	e.Streams.PublishEventAdded(session.Summary.ID, event)
	return cell, nil
}
