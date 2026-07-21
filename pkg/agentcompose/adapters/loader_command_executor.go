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

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/llms"
	"agent-compose/pkg/llms/runtimefacade"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
)

type LoaderCommandExecutor struct {
	Config   *appconfig.Config
	Store    *sessionstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
	Streams  *sessions.StreamBroker
}

func NewLoaderCommandExecutor(config *appconfig.Config, store *sessionstore.Store, configDB *configstore.ConfigStore, runtimes RuntimeProvider, streams *sessions.StreamBroker) *LoaderCommandExecutor {
	return &LoaderCommandExecutor{Config: config, Store: store, ConfigDB: configDB, Runtimes: runtimes, Streams: streams}
}

func (e *LoaderCommandExecutor) ExecuteLoaderCommand(ctx context.Context, session *domain.Sandbox, request domain.LoaderCommandRequest) (domain.LoaderCommandResult, error) {
	appconfig.ApplyDefaultGuestPaths(e.Config)
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return domain.LoaderCommandResult{}, fmt.Errorf("session is not running")
	}
	if err := loaders.ValidateCommandRequest(request); err != nil {
		return domain.LoaderCommandResult{}, err
	}
	vmState, err := e.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return domain.LoaderCommandResult{}, err
	}
	runtime, err := e.Runtimes.ForSession(session)
	if err != nil {
		return domain.LoaderCommandResult{}, err
	}

	ctx, cancel := loaders.CommandContext(ctx, request.TimeoutMs)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(execution.HostSandboxDir(session), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return domain.LoaderCommandResult{}, fmt.Errorf("create loader command cell state dir: %w", err)
	}
	guestCellDir := filepath.Join(e.Config.GuestStateRoot, "cells", cellID)
	source := loaders.CommandCellSource(request)
	startedAt := time.Now().UTC()
	cell := domain.NotebookCell{
		ID:        cellID,
		Type:      execution.CellTypeShell,
		Source:    source,
		CreatedAt: startedAt,
		Running:   true,
	}
	execSession, managedEnv, err := e.prepareLoaderCommandLLMFacadeEnv(ctx, session, request, cellID)
	if err != nil {
		return domain.LoaderCommandResult{}, err
	}
	facadeToken := managedEnv["AGENT_COMPOSE_SANDBOX_TOKEN"]
	if e.ConfigDB != nil && facadeToken != "" {
		defer func() { _ = e.ConfigDB.DeleteLLMFacadeToken(context.WithoutCancel(ctx), facadeToken) }()
	}
	request.Env = llms.MergeManagedExecEnv(request.Env, managedEnv)
	if err := e.Store.AddCell(ctx, session, cell); err != nil {
		return domain.LoaderCommandResult{}, err
	}
	e.Streams.PublishCellStarted(session.Summary.ID, cell)

	artifacts := map[string]string{
		"cellDir": hostCellDir,
		"stdout":  filepath.Join(hostCellDir, "stdout.txt"),
		"stderr":  filepath.Join(hostCellDir, "stderr.txt"),
		"output":  filepath.Join(hostCellDir, "output.txt"),
		"request": filepath.Join(hostCellDir, "command-request.json"),
		"result":  filepath.Join(hostCellDir, "command-result.json"),
	}
	buildLoaderCommandResult := func(result domain.ExecResult) domain.LoaderCommandResult {
		return domain.LoaderCommandResult{
			Stdout:    result.Stdout,
			Stderr:    result.Stderr,
			Output:    result.Output,
			ExitCode:  result.ExitCode,
			Success:   result.Success,
			SandboxID: session.Summary.ID,
			CellID:    cellID,
			Artifacts: artifacts,
		}
	}

	var cellMu sync.Mutex
	var streamErrMu sync.Mutex
	var streamErr error
	var streamed execution.ExecStreamAccumulator
	setStreamErr := func(err error) {
		if err == nil {
			return
		}
		streamErrMu.Lock()
		if streamErr == nil {
			streamErr = err
			execCancel()
		}
		streamErrMu.Unlock()
	}
	persistFailedCell := func(execResult domain.ExecResult, finalErr error) (domain.LoaderCommandResult, error) {
		recovered := execution.MergeExecResults(execResult, streamed.Result(execution.FirstNonZeroInt(execResult.ExitCode, 1), false))
		recovered = execution.RecoverExecResultFromCellArtifacts(hostCellDir, recovered)
		recovered.ExitCode = execution.FirstNonZeroInt(recovered.ExitCode, execResult.ExitCode, 1)
		recovered.Success = false
		if strings.TrimSpace(recovered.Output) == "" {
			recovered.Output = firstNonEmpty(recovered.Stderr, recovered.Stdout, finalErr.Error())
		}
		if err := execution.WriteCellArtifacts(hostCellDir, source, recovered); err != nil {
			return buildLoaderCommandResult(recovered), err
		}
		cellMu.Lock()
		cell.Stdout = recovered.Stdout
		cell.Stderr = recovered.Stderr
		cell.Output = recovered.Output
		cell.ExitCode = recovered.ExitCode
		cell.Success = false
		cell.Running = false
		failedCell := cell
		cellMu.Unlock()
		if err := e.Store.AddCell(ctx, session, failedCell); err != nil {
			return buildLoaderCommandResult(recovered), err
		}
		e.Streams.PublishCellCompleted(session.Summary.ID, failedCell)
		event := domain.SandboxEvent{
			ID:        uuid.NewString(),
			Type:      "kernel.cell.failed",
			Level:     "error",
			Message:   firstNonEmpty(recovered.Stderr, fmt.Sprintf("loader command failed with exit code %d", recovered.ExitCode), finalErr.Error()),
			CreatedAt: time.Now().UTC(),
		}
		_ = e.Store.AddEvent(ctx, session.Summary.ID, event)
		e.Streams.PublishEventAdded(session.Summary.ID, event)
		return buildLoaderCommandResult(recovered), finalErr
	}

	runtimeRequest := execution.RuntimeCommandRequestPayload(e.Config, request, guestCellDir)
	hostRequestPath := filepath.Join(hostCellDir, "command-request.json")
	if err := execution.WriteJSONArtifact(hostRequestPath, runtimeRequest); err != nil {
		return domain.LoaderCommandResult{}, fmt.Errorf("write loader command request artifact: %w", err)
	}

	streamWriter := func(chunk domain.ExecChunk) {
		filtered, visible := execution.FilterCommandStreamChunk(chunk)
		if !visible {
			return
		}
		cellMu.Lock()
		streamed.WriteChunk(filtered)
		isStderr := domain.NormalizeStdioStream(filtered.Stream) == domain.StdioStderr
		if isStderr {
			cell.Stderr += filtered.Text
		} else {
			cell.Stdout += filtered.Text
		}
		cell.Output += filtered.Text
		snapshot := cell
		cellMu.Unlock()
		if err := e.Store.AddCell(ctx, session, snapshot); err != nil {
			setStreamErr(err)
			return
		}
		e.Streams.PublishCellOutput(session.Summary.ID, snapshot.ID, filtered.Text, filtered.Stream)
	}
	commandHome := e.Config.GuestHomePath
	execResult, err := runtime.ExecStream(execCtx, execSession, vmState, execution.BuildLoaderCommandExecSpec(e.Config, execSession, filepath.Join(guestCellDir, "command-request.json"), commandHome), streamWriter)
	streamErrMu.Lock()
	deferredStreamErr := streamErr
	streamErrMu.Unlock()
	if deferredStreamErr != nil {
		return persistFailedCell(execResult, deferredStreamErr)
	}
	if err != nil {
		return persistFailedCell(execResult, err)
	}
	commandResult, err := execution.ParseCommandExecResult(execResult)
	if err != nil {
		return persistFailedCell(execResult, err)
	}
	if err := execution.MirrorRuntimeCommandArtifacts(hostCellDir, commandResult); err != nil {
		return persistFailedCell(execResult, err)
	}

	cell.Stdout = commandResult.Stdout
	cell.Stderr = commandResult.Stderr
	cell.Output = commandResult.Output
	cell.ExitCode = commandResult.ExitCode
	cell.Success = commandResult.Success
	cell.Running = false
	if err := e.Store.AddCell(ctx, session, cell); err != nil {
		return domain.LoaderCommandResult{}, err
	}
	e.Streams.PublishCellCompleted(session.Summary.ID, cell)

	eventLevel := "info"
	eventType := "kernel.cell.succeeded"
	eventMessage := "executed loader command in agent-compose guest"
	if !commandResult.Success {
		eventLevel = "error"
		eventType = "kernel.cell.failed"
		eventMessage = firstNonEmpty(commandResult.Stderr, fmt.Sprintf("loader command failed with exit code %d", commandResult.ExitCode))
	}
	event := domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     eventLevel,
		Message:   eventMessage,
		CreatedAt: time.Now().UTC(),
	}
	_ = e.Store.AddEvent(ctx, session.Summary.ID, event)
	e.Streams.PublishEventAdded(session.Summary.ID, event)

	return domain.LoaderCommandResult{
		Stdout:          commandResult.Stdout,
		Stderr:          commandResult.Stderr,
		Output:          commandResult.Output,
		ExitCode:        commandResult.ExitCode,
		Success:         commandResult.Success,
		StdoutTruncated: commandResult.StdoutTruncated,
		StderrTruncated: commandResult.StderrTruncated,
		OutputTruncated: commandResult.OutputTruncated,
		SandboxID:       session.Summary.ID,
		CellID:          cellID,
		Artifacts:       artifacts,
	}, nil
}

func (e *LoaderCommandExecutor) prepareLoaderCommandLLMFacadeEnv(ctx context.Context, session *domain.Sandbox, request domain.LoaderCommandRequest, runID string) (*domain.Sandbox, map[string]string, error) {
	if e == nil || e.Config == nil || e.ConfigDB == nil || session == nil {
		return session, nil, nil
	}
	agent, model := llms.LoaderCommandFacadeAgentModel(request.Env)
	if agent == "" {
		return session, nil, nil
	}

	execSession := *session
	execSession.EnvItems = append([]domain.SandboxEnvVar(nil), session.EnvItems...)
	execSession.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), session.RuntimeEnvItems...)
	execSession.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), session.ProviderEnvItems...)
	execSession.ExecutionProviderEnvItems = append([]domain.SandboxEnvVar(nil), session.ExecutionProviderEnvItems...)
	execSession.ExecutionProviderEnvItems = domain.MergeEnvItems(execSession.ExecutionProviderEnvItems, domain.LoaderCommandSandboxEnv(request))

	managedEnv, err := runtimefacade.EnsureSessionLLMFacadeConfig(ctx, e.Config, facadeStoreFor(e.ConfigDB), &execSession, agent, model, runtimefacade.TokenSourceLoaderCommand, runID)
	if err != nil {
		return nil, nil, err
	}
	if len(managedEnv) > 0 {
		execSession.RuntimeEnvItems = domain.MergeEnvItems(execSession.RuntimeEnvItems, llms.EnvItemsFromMap(managedEnv, false))
	}
	return &execSession, managedEnv, nil
}
