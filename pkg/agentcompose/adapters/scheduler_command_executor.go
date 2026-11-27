package adapters

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	"github.com/chaitin/agent-compose/pkg/llms"
	"github.com/chaitin/agent-compose/pkg/llms/runtimefacade"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

type SchedulerCommandExecutor struct {
	Config   *appconfig.Config
	Store    *sandboxstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
	Streams  *sandboxes.StreamBroker
}

// SchedulerCommandExecutorDeps bundles NewSchedulerCommandExecutor's
// dependencies.
type SchedulerCommandExecutorDeps struct {
	Config   *appconfig.Config
	Store    *sandboxstore.Store
	ConfigDB *configstore.ConfigStore
	Runtimes RuntimeProvider
	Streams  *sandboxes.StreamBroker
}

func NewSchedulerCommandExecutor(deps SchedulerCommandExecutorDeps) *SchedulerCommandExecutor {
	return &SchedulerCommandExecutor{Config: deps.Config, Store: deps.Store, ConfigDB: deps.ConfigDB, Runtimes: deps.Runtimes, Streams: deps.Streams}
}

//nolint:funlen // closures share mutable state across the whole command lifecycle, same as ExecuteAgentRequest; splitting would relocate rather than reduce the complexity.
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

	cellID := strings.TrimSpace(request.CellID)
	if cellID == "" {
		cellID = uuid.NewString()
	}
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
	execSession, facadeTokenHashes, err := e.prepareSchedulerCommandLLMFacadeEnv(ctx, session, request, cellID)
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
	// A step's history is its step log; the sandbox keeps no notebook cell or
	// event for it. Watchers of the sandbox still see the step live, and
	// ListSandboxHistory rebuilds it from the step log.
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
	var streamed execution.ExecStreamAccumulator
	persistFailedCell := func(execResult domain.ExecResult, finalErr error) (domain.SchedulerCommandResult, error) {
		recovered := execution.MergeExecResults(execResult, streamed.Result(execution.FirstNonZeroInt(execResult.ExitCode, 1), false))
		recovered = execution.RecoverExecResultFromCellArtifacts(hostCellDir, recovered)
		recovered.ExitCode = execution.FirstNonZeroInt(recovered.ExitCode, execResult.ExitCode, 1)
		recovered.Success = false
		if strings.TrimSpace(recovered.Output) == "" {
			recovered.Output = firstNonEmpty(recovered.Stderr, recovered.Stdout, finalErr.Error())
		}
		if err := execution.WriteCellArtifacts(hostCellDir, recovered); err != nil {
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
		e.Streams.PublishCellCompleted(session.Summary.ID, failedCell)
		e.Streams.PublishEventAdded(session.Summary.ID, domain.SandboxEvent{
			ID:        uuid.NewString(),
			Type:      "kernel.cell.failed",
			Level:     "error",
			Message:   firstNonEmpty(recovered.Stderr, fmt.Sprintf("scheduler command failed with exit code %d", recovered.ExitCode), finalErr.Error()),
			CreatedAt: time.Now().UTC(),
		})
		return buildSchedulerCommandResult(recovered), finalErr
	}

	// request.Env is an explicit command option and the guest runtime applies it
	// last to the workload child process. Preserve it unchanged; the freshly
	// rebuilt managed facade values live only in execSession.RuntimeEnvItems.
	runtimeRequest := execution.RuntimeCommandRequestPayload(e.Config, request, guestCellDir)
	hostRequestPath := filepath.Join(hostCellDir, "command-request.json")
	if err := execution.WriteJSONArtifact(hostRequestPath, runtimeRequest); err != nil {
		return domain.SchedulerCommandResult{}, fmt.Errorf("write scheduler command request artifact: %w", err)
	}
	if writer, ok := runtime.(GuestFileWriter); ok {
		writeGuestFile := func(ctx context.Context, guestPath string, content []byte) error {
			return writer.WriteGuestFile(ctx, execSession, vmState, guestPath, content)
		}
		if err := execution.SyncHostFileToGuest(ctx, hostRequestPath, filepath.Join(guestCellDir, "command-request.json"), writeGuestFile); err != nil {
			return domain.SchedulerCommandResult{}, fmt.Errorf("push scheduler command request: %w", err)
		}
	}

	stepLog := e.openStepLog(request, cellID)
	if stepLog != nil {
		defer func() { _ = stepLog.Close() }()
	}
	streamWriter := func(chunk domain.ExecChunk) {
		filtered, visible := execution.FilterCommandStreamChunk(chunk)
		if !visible {
			return
		}
		if stepLog != nil {
			stream := "stdout"
			if domain.NormalizeStdioStream(filtered.Stream) == domain.StdioStderr {
				stream = "stderr"
			}
			// The step log is history; failing to write it must not fail
			// the command the script is waiting for.
			_ = stepLog.AppendOutput(stream, filtered.Text)
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
		cellMu.Unlock()
		e.Streams.PublishCellOutput(session.Summary.ID, cellID, filtered.Text, filtered.Stream)
	}
	commandHome := e.Config.GuestHomePath
	execResult, err := runtime.ExecStream(execCtx, execSession, vmState, execution.BuildSchedulerCommandExecSpec(execCtx, e.Config, execSession, filepath.Join(guestCellDir, "command-request.json"), commandHome), streamWriter)
	if reader, ok := runtime.(GuestDirReader); ok {
		readGuestDir := func(ctx context.Context, guestDir, hostDestDir string) error {
			return reader.ReadGuestDir(ctx, execSession, vmState, guestDir, hostDestDir)
		}
		if pullErr := execution.SyncGuestDirToHost(ctx, guestCellDir, hostCellDir, readGuestDir); pullErr != nil {
			err = errors.Join(err, fmt.Errorf("pull scheduler command artifacts: %w", pullErr))
		}
	}
	retainFacadeTokens = errors.Is(err, domain.ErrExecTerminationUnconfirmed)
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
	e.Streams.PublishCellCompleted(session.Summary.ID, cell)

	eventLevel := "info"
	eventType := "kernel.cell.succeeded"
	eventMessage := "executed scheduler command in agent-compose guest"
	if !commandResult.Success {
		eventLevel = "error"
		eventType = "kernel.cell.failed"
		eventMessage = firstNonEmpty(commandResult.Stderr, fmt.Sprintf("scheduler command failed with exit code %d", commandResult.ExitCode))
	}
	e.Streams.PublishEventAdded(session.Summary.ID, domain.SandboxEvent{
		ID:        uuid.NewString(),
		Type:      eventType,
		Level:     eventLevel,
		Message:   eventMessage,
		CreatedAt: time.Now().UTC(),
	})

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

func (e *SchedulerCommandExecutor) prepareSchedulerCommandLLMFacadeEnv(ctx context.Context, session *domain.Sandbox, request domain.SchedulerCommandRequest, runID string) (*domain.Sandbox, []string, error) {
	if e == nil || e.Config == nil || e.ConfigDB == nil || session == nil {
		return session, nil, nil
	}
	agent, model := llms.SchedulerCommandFacadeAgentModel(request.Env)
	if agent == "" {
		return session, nil, nil
	}

	execSession := *session
	execSession.EnvItems = append([]domain.SandboxEnvVar(nil), session.EnvItems...)
	execSession.RuntimeEnvItems = append([]domain.SandboxEnvVar(nil), session.RuntimeEnvItems...)
	// One declaration, derived exactly as every other entry point derives it:
	// the sandbox's own provider environment, recovered from legacy metadata
	// when it predates provenance, plus what this command declares. It decides
	// direct versus managed and it is the provider environment the guest runs
	// with.
	execSession.ProviderEnvItems = session.DeclaredProviderEnv(schedulers.CommandSandboxEnv(request))

	// This path selects its agent and model from the scheduler command's own
	// environment, not from a project agent definition, so the catalog infers
	// the connection from the model. A command that declares its own upstream
	// keeps it instead of being routed through the catalog.
	managedConfig, err := runtimefacade.EnsureSessionCommandFacadeConfig(ctx, runtimefacade.CommandFacadeConfigRequest{
		Config: e.Config, Store: commandFacadeStoreFor(e.ConfigDB), Session: &execSession, Agent: agent, Model: model, AgentEnv: execSession.ProviderEnvItems, Source: runtimefacade.TokenSourceSchedulerCommand, RunID: runID,
	})
	if err != nil {
		return nil, nil, err
	}
	if len(managedConfig.Env) > 0 {
		execSession.RuntimeEnvItems = domain.MergeEnvItems(execSession.RuntimeEnvItems, llms.EnvItemsFromMap(managedConfig.Env, true))
	}
	return &execSession, managedConfig.TokenHashes, nil
}

func commandFacadeStoreFor(configDB *configstore.ConfigStore) runtimefacade.CommandFacadeStore {
	if configDB == nil {
		return nil
	}
	return configDB
}

// openStepLog opens the log of one shell/exec step of a persisted scheduler
// run, inside the directory that run owns, so the step's output outlives the
// sandbox it ran in. It returns nil when the command is not a scheduler run
// step or the log cannot be opened.
func (e *SchedulerCommandExecutor) openStepLog(request domain.SchedulerCommandRequest, cellID string) *runlog.Writer {
	if e == nil || e.Config == nil || strings.TrimSpace(e.Config.DataRoot) == "" ||
		strings.TrimSpace(request.SchedulerID) == "" || strings.TrimSpace(request.SchedulerRunID) == "" {
		return nil
	}
	maxBytes := request.MaxOutputBytes
	if maxBytes <= 0 {
		maxBytes = execution.DefaultSchedulerCommandMaxOutputBytes
	}
	path, err := runlog.SchedulerCellPath(e.Config.DataRoot, request.SchedulerID, request.SchedulerRunID, cellID)
	if err == nil {
		// Leave room for the entry framing around the output itself.
		var w *runlog.Writer
		if w, err = runlog.Open(path, runlog.Options{MaxBytes: 2*maxBytes + 64<<10}); err == nil {
			return w
		}
	}
	// The step log is the step's only history, but failing to keep it must
	// not fail the command the script is waiting for.
	slog.Warn("scheduler step log unavailable", "scheduler_id", request.SchedulerID, "scheduler_run_id", request.SchedulerRunID, "cell_id", cellID, "error", err)
	return nil
}
