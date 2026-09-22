package runs

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// projectRunCommandExecution bundles the run, target sandbox, originating
// request, and resolved command text executeProjectRunCommand needs. Kept as
// a struct rather than trimming commandText (it is not always
// strings.TrimSpace(req.Command) - callers may resolve it separately) so the
// signature stays within the argument limit without losing that
// independence.
type projectRunCommandExecution struct {
	Run         domain.ProjectRunRecord
	Sandbox     *domain.Sandbox
	Request     RunAgentRequest
	CommandText string
	Sink        *StreamSink
}

func (c *Controller) executeProjectRunCommand(ctx context.Context, exec projectRunCommandExecution) (TransitionRequest, error) {
	run, sandbox, req, commandText, sink := exec.Run, exec.Sandbox, exec.Request, exec.CommandText, exec.Sink
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	logsPath := filepath.Join(artifactsDir, "transcript.txt")
	transition := TransitionRequest{RunID: run.RunID, SandboxID: sandbox.Summary.ID, LogsPath: logsPath}
	if c.store == nil || c.runtime == nil {
		err := fmt.Errorf("command runtime dependencies are required")
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if sink != nil && sink.SendStarted != nil {
		if err := sink.SendStarted(run, time.Now().UTC()); err != nil {
			transition.ExitCode = 1
			transition.Error = fmt.Sprintf("command execution failed: %v", err)
			return transition, err
		}
	}
	appconfig.ApplyDefaultGuestPaths(c.config)
	vmState, err := c.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	runtime, err := c.runtime(sandbox)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	guestArtifactsDir := filepath.Join(c.config.GuestStateRoot, "runs", run.RunID)
	runtimeRequest := execution.RuntimeCommandRequestPayloadFromCommand(c.config, execution.RuntimeCommandSpec{
		Mode:   "shell",
		Script: commandText,
		Cwd:    c.config.GuestWorkspacePath,
		Env:    execEnvMap(req.Env),
	}, guestArtifactsDir)
	hostRequestPath := filepath.Join(artifactsDir, "command-request.json")
	guestRequestPath := filepath.Join(guestArtifactsDir, "command-request.json")
	if err := execution.WriteJSONArtifact(hostRequestPath, runtimeRequest); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if err := execution.SyncHostFileToGuest(ctx, hostRequestPath, guestRequestPath, guestFileWriterFor(runtime, sandbox, vmState)); err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: push command request: %v", err)
		return transition, err
	}
	var sendErr error
	writer := func(chunk domain.ExecChunk) {
		if sendErr != nil {
			return
		}
		filtered, visible := execution.FilterCommandStreamChunk(chunk)
		if !visible {
			return
		}
		offset, err := appendProjectRunLogChunk(logsPath, filtered)
		if err != nil {
			sendErr = err
			return
		}
		c.publishRunLogChunk(run.RunID, filtered, offset)
		if sink != nil && sink.SendChunk != nil {
			sendErr = sink.SendChunk(run.RunID, filtered, time.Now().UTC())
		}
	}
	execCtx, cancel := execution.ExecContext(ctx, 0)
	defer cancel()
	spec := execution.BuildRuntimeCommandExecSpec(execCtx, c.config, sandbox, guestRequestPath, c.config.GuestHomePath)
	spec.Env["AGENT_COMPOSE_RUN_ID"] = run.RunID
	spec.Env["AGENT_COMPOSE_PROJECT_ID"] = run.ProjectID
	result, execErr := runtime.ExecStream(execCtx, sandbox, vmState, spec, writer)
	if pullErr := execution.SyncGuestDirToHost(ctx, guestArtifactsDir, artifactsDir, guestDirReaderFor(runtime, sandbox, vmState)); pullErr != nil {
		execErr = errors.Join(execErr, fmt.Errorf("pull command artifacts: %w", pullErr))
	}
	if sendErr != nil {
		sendErr = errors.Join(sendErr, execErr)
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", sendErr)
		return transition, sendErr
	}
	if execErr != nil {
		result.ExitCode = execution.FirstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = firstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
		transition = transitionFromCommandResult(run, sandbox, commandOutcome{Command: commandText, Result: result, Err: execErr})
		transition.LogsPath = logsPath
		return transition, execErr
	}
	commandResult, err := execution.ParseCommandExecResult(result)
	if err != nil {
		transition.ExitCode = execution.FirstNonZeroInt(transition.ExitCode, 1)
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if err := execution.MirrorRuntimeCommandArtifacts(artifactsDir, commandResult); err != nil {
		transition.ExitCode = execution.FirstNonZeroInt(commandResult.ExitCode, 1)
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	transition = transitionFromCommandResult(run, sandbox, commandOutcome{Command: commandText, Result: execution.RuntimeCommandResultToExecResult(commandResult)})
	transition.LogsPath = logsPath
	return transition, nil
}

func execEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
	if len(items) == 0 {
		return nil
	}
	env := make(map[string]string)
	for _, item := range items {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			continue
		}
		env[name] = item.GetValue()
	}
	if len(env) == 0 {
		return nil
	}
	return env
}
