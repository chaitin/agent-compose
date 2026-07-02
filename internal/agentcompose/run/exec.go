package run

import (
	"strings"

	execdomain "agent-compose/internal/agentcompose/exec"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func ExecEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
	if len(items) == 0 {
		return nil
	}
	result := make(map[string]string, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			continue
		}
		result[name] = item.GetValue()
	}
	return result
}

func ExecResultResponse(execID, sessionID, runID string, req *agentcomposev2.ExecRequest, cwd string, result execdomain.Result, execErr error) *agentcomposev2.ExecResult {
	errorText := ""
	if execErr != nil {
		errorText = execErr.Error()
	}
	return &agentcomposev2.ExecResult{
		ExecId:    execID,
		SessionId: sessionID,
		RunId:     runID,
		Command: &agentcomposev2.ExecCommand{
			Command: req.GetCommand().GetCommand(),
			Args:    append([]string(nil), req.GetCommand().GetArgs()...),
		},
		Cwd:      cwd,
		ExitCode: int32(result.ExitCode),
		Success:  result.Success,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Output:   result.Output,
		Error:    errorText,
	}
}
