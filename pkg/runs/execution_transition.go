package runs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// commandOutcome is the result of a one-shot command execution, as reported
// back to transitionFromCommandResult.
type commandOutcome struct {
	Command string
	Result  domain.ExecResult
	Err     error
}

func transitionFromCommandResult(run domain.ProjectRunRecord, sandbox *domain.Sandbox, outcome commandOutcome) TransitionRequest {
	artifactsDir := projectRunCommandArtifactsDir(run, sandbox)
	req := TransitionRequest{
		RunID:        run.RunID,
		SandboxID:    sandbox.Summary.ID,
		ExitCode:     outcome.Result.ExitCode,
		Output:       outcome.Result.Output,
		ArtifactsDir: artifactsDir,
	}
	resultJSON, err := json.Marshal(map[string]any{
		"mode":     "command",
		"command":  outcome.Command,
		"success":  outcome.Result.Success,
		"exitCode": outcome.Result.ExitCode,
	})
	if err == nil {
		req.ResultJSON = string(resultJSON)
	}
	if outcome.Err != nil {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = fmt.Sprintf("command execution failed: %v", outcome.Err)
		return req
	}
	if !outcome.Result.Success {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = "command execution failed"
		if detail := firstNonEmpty(outcome.Result.Stderr, outcome.Result.Output, outcome.Result.Stdout); strings.TrimSpace(detail) != "" {
			req.Error += ": " + strings.TrimSpace(detail)
		}
	}
	return req
}

// runtimeOutcome is the result of a driver-executed runtime command, as
// reported back to transitionFromRuntimeResult.
type runtimeOutcome struct {
	Command     string
	LogsPath    string
	Accumulated domain.ExecResult
	Result      driverpkg.RuntimeResult
	Err         error
}

func transitionFromRuntimeResult(run domain.ProjectRunRecord, sandbox *domain.Sandbox, outcome runtimeOutcome) TransitionRequest {
	accumulated := outcome.Accumulated
	accumulated.ExitCode = outcome.Result.ExitCode
	accumulated.Success = outcome.Result.Success
	if strings.TrimSpace(outcome.Result.Error) != "" {
		accumulated.Success = false
	}
	execErr := outcome.Err
	if execErr == nil && strings.TrimSpace(outcome.Result.Error) != "" {
		execErr = errors.New(outcome.Result.Error)
	}
	transition := transitionFromCommandResult(run, sandbox, commandOutcome{Command: outcome.Command, Result: accumulated, Err: execErr})
	transition.LogsPath = outcome.LogsPath
	return transition
}

// agentFrameOutcome carries a normalized agent execution result or error.
type agentFrameOutcome struct {
	LogsPath   string
	Payload    []byte
	FinalText  string
	StopReason string
	Message    string
}

func transitionFromAgentFrame(run domain.ProjectRunRecord, sandbox *domain.Sandbox, outcome agentFrameOutcome) TransitionRequest {
	transition := TransitionRequest{
		RunID:      run.RunID,
		SandboxID:  run.SandboxID,
		Output:     outcome.FinalText,
		ResultJSON: string(outcome.Payload),
		LogsPath:   outcome.LogsPath,
	}
	if sandbox != nil {
		transition.SandboxID = sandbox.Summary.ID
	}
	if strings.TrimSpace(outcome.Message) != "" {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("agent execution failed: %s", strings.TrimSpace(outcome.Message))
		return transition
	}
	if strings.EqualFold(strings.TrimSpace(outcome.StopReason), "cancelled") {
		transition.ExitCode = 1
		transition.Error = "agent execution cancelled"
	}
	return transition
}

func agentTransitionError(transition TransitionRequest) error {
	if strings.EqualFold(strings.TrimSpace(transition.Error), "agent execution cancelled") {
		return context.Canceled
	}
	return errors.New(firstNonEmpty(transition.Error, "agent execution failed"))
}

func errorFromRuntimeResult(result driverpkg.RuntimeResult) error {
	if strings.TrimSpace(result.Error) == "" {
		return nil
	}
	return errors.New(result.Error)
}
