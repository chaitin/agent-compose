package adapters

import (
	"context"
	"errors"
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
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/sandboxes"
	"agent-compose/pkg/schedulers"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sandboxstore"
)

type SchedulerCommandExecutor struct {
	Config   *appconfig.Config
	Store    *sandboxstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
	Streams  *sandboxes.StreamBroker
}

func NewSchedulerCommandExecutor(config *appconfig.Config, store *sandboxstore.Store, configDB *configstore.ConfigStore, runtimes RuntimeProvider, streams *sandboxes.StreamBroker) *SchedulerCommandExecutor {
	return &SchedulerCommandExecutor{Config: config, Store: store, ConfigDB: configDB, Runtimes: runtimes, Streams: streams}
}

func (e *SchedulerCommandExecutor) ExecuteSchedulerCommand(ctx context.Context, session *domain.Sandbox, request domain.SchedulerCommandRequest) (domain.SchedulerCommandResult, error) {
	appconfig.ApplyDefaultGuestPaths(e.Config)
	if session.Summary.VMStatus != domain.VMStatusRunning {
		return domain.SchedulerCommandResult{}, fmt.Errorf("session is not running")
	}
	if err := schedulers.ValidateCommandRequest(request); err != nil {
		return domain.SchedulerCommandResult{}, err
	}
	vmState, err := e.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return domain.SchedulerCommandResult{}, err
	}
	runtime, err := e.Runtimes.ForSession(session)
	if err != nil {
		return domain.SchedulerCommandResult{}, err
	}

	ctx, cancel := schedulers.CommandContext(ctx, request.TimeoutMs)
	defer cancel()
	execCtx, execCancel := context.WithCancel(ctx)
	defer execCancel()

	cellID := uuid.NewString()
	hostCellDir := filepath.Join(execution.HostSandboxDir(session), "state", "cells", cellID)
	if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
		return domain.SchedulerCommandResult{}, fmt.Errorf("create scheduler command cell state dir: %w", err)
	}
	guestCellDir := filepath.Join(e.Config.GuestStateRoot, "cells", cellID)
	source := schedulers.CommandCellSource(request)
	startedAt := time.Now().UTC()
	cell := domain.NotebookCell{
		ID:        cellID,
		Type:      execution.CellTypeShell,
		Source:    source,
		CreatedAt: startedAt,
		Running:   true,
	}
	execSession, managedFacadeEnv, facadeTokenHashes, err := e.prepareSchedulerCommandLLMFacadeEnv(ctx, session, request, cellID)
	if err != nil {
		return domain.SchedulerCommandResult{}, err
	}
	retainFacadeTokens := false
	if e.ConfigDB != nil && len(facadeTokenHashes) > 0 {
		defer func() {
			if !retainFacadeTokens {
				cleanupCtx := context.WithoutCancel(ctx)
				for _, tokenHash := range facadeTokenHashes {
					_ = e.ConfigDB.DeleteLLMFacadeTokenHash(cleanupCtx, tokenHash)
				}
			}
		}()
	}
	if err := e.Store.AddCell(ctx, session, cell); err != nil {
		return domain.SchedulerCommandResult{}, err
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
	buildSchedulerCommandResult := func(result domain.ExecResult) domain.SchedulerCommandResult {
		return domain.SchedulerCommandResult{
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
	persistFailedCell := func(execResult domain.ExecResult, finalErr error) (domain.SchedulerCommandResult, error) {
		recovered := execution.MergeExecResults(execResult, streamed.Result(execution.FirstNonZeroInt(execResult.ExitCode, 1), false))
		recovered = execution.RecoverExecResultFromCellArtifacts(hostCellDir, recovered)
		recovered.ExitCode = execution.FirstNonZeroInt(recovered.ExitCode, execResult.ExitCode, 1)
		recovered.Success = false
		if strings.TrimSpace(recovered.Output) == "" {
			recovered.Output = firstNonEmpty(recovered.Stderr, recovered.Stdout, finalErr.Error())
		}
		if err := execution.WriteCellArtifacts(hostCellDir, source, recovered); err != nil {
			return buildSchedulerCommandResult(recovered), err
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
			return buildSchedulerCommandResult(recovered), err
		}
		e.Streams.PublishCellCompleted(session.Summary.ID, failedCell)
		event := domain.SandboxEvent{
			ID:        uuid.NewString(),
			Type:      "kernel.cell.failed",
			Level:     "error",
			Message:   firstNonEmpty(recovered.Stderr, fmt.Sprintf("scheduler command failed with exit code %d", recovered.ExitCode), finalErr.Error()),
			CreatedAt: time.Now().UTC(),
		}
		_ = e.Store.AddEvent(ctx, session.Summary.ID, event)
		e.Streams.PublishEventAdded(session.Summary.ID, event)
		return buildSchedulerCommandResult(recovered), finalErr
	}

	runtimeCommandRequest := request
	// The guest runtime applies request.Env after the outer command process
	// environment. Carry the same facade values into the request so stale
	// provider variables cannot replace them in the actual workload child.
	runtimeCommandRequest.Env = mergeSchedulerCommandRuntimeEnv(request.Env, managedFacadeEnv)
	runtimeRequest := execution.RuntimeCommandRequestPayload(e.Config, runtimeCommandRequest, guestCellDir)
	hostRequestPath := filepath.Join(hostCellDir, "command-request.json")
	if err := execution.WriteJSONArtifact(hostRequestPath, runtimeRequest); err != nil {
		return domain.SchedulerCommandResult{}, fmt.Errorf("write scheduler command request artifact: %w", err)
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
	execResult, err := runtime.ExecStream(execCtx, execSession, vmState, execution.BuildSchedulerCommandExecSpec(e.Config, execSession, filepath.Join(guestCellDir, "command-request.json"), commandHome), streamWriter)
	retainFacadeTokens = errors.Is(err, domain.ErrExecTerminationUnconfirmed)
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
		return domain.SchedulerCommandResult{}, err
	}
	e.Streams.PublishCellCompleted(session.Summary.ID, cell)

	eventLevel := "info"
	eventType := "kernel.cell.succeeded"
	eventMessage := "executed scheduler command in agent-compose guest"
	if !commandResult.Success {
		eventLevel = "error"
		eventType = "kernel.cell.failed"
		eventMessage = firstNonEmpty(commandResult.Stderr, fmt.Sprintf("scheduler command failed with exit code %d", commandResult.ExitCode))
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

	return domain.SchedulerCommandResult{
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

func (e *SchedulerCommandExecutor) prepareSchedulerCommandLLMFacadeEnv(ctx context.Context, session *domain.Sandbox, request domain.SchedulerCommandRequest, runID string) (*domain.Sandbox, map[string]string, []string, error) {
	if e == nil || e.Config == nil || e.ConfigDB == nil || session == nil {
		return session, nil, nil, nil
	}
	agent, model := llms.SchedulerCommandFacadeAgentModel(request.Env)

	execSession := *session
	execSession.EnvItems = append([]domain.SandboxEnvVar(nil), session.EnvItems...)
	execSession.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), session.RuntimeEnvItems...)
	execSession.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), session.ProviderEnvItems...)
	if len(execSession.ProviderEnvItems) == 0 && session.ProviderEnvOverrideNames == nil {
		// Metadata without provider provenance cannot identify the original source,
		// so its persisted environment snapshot remains the compatible fallback.
		execSession.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), session.EnvItems...)
	}
	execSession.ProviderEnvItems = domain.MergeEnvItems(execSession.ProviderEnvItems, domain.SchedulerCommandSandboxEnv(request))

	managedConfig, err := runtimefacade.EnsureSessionCommandFacadeConfig(ctx, e.Config, commandFacadeStoreFor(e.ConfigDB), &execSession, agent, model, runtimefacade.TokenSourceSchedulerCommand, runID)
	if err != nil {
		return nil, nil, nil, err
	}
	if len(managedConfig.Env) > 0 {
		execSession.RuntimeEnvItems = domain.MergeEnvItems(execSession.RuntimeEnvItems, llms.EnvItemsFromMap(managedConfig.Env, true))
	}
	return &execSession, managedConfig.Env, managedConfig.TokenHashes, nil
}

func commandFacadeStoreFor(configDB *configstore.ConfigStore) runtimefacade.CommandFacadeStore {
	if configDB == nil {
		return nil
	}
	return configDB
}

func mergeSchedulerCommandRuntimeEnv(requestEnv, managedFacadeEnv map[string]string) map[string]string {
	if len(requestEnv) == 0 && len(managedFacadeEnv) == 0 {
		return nil
	}
	merged := make(map[string]string, len(requestEnv)+len(managedFacadeEnv))
	for name, value := range requestEnv {
		merged[name] = value
	}
	for name, value := range managedFacadeEnv {
		merged[name] = value
	}
	return merged
}
