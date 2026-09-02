package runs

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// TransitionFromAgentCell builds the completion transition for a finished
// agent cell. assistantMessage is the provider's final assistant message
// (from ExecuteAgentRequest's assistant event), "" when the provider gave no
// message distinct from the transcript; it rides along in ResultJSON so
// AgentResultFromProjectRun can recover it later without access to the
// project run's event stream.
func TransitionFromAgentCell(run domain.ProjectRunRecord, sandbox *domain.Sandbox, cell domain.NotebookCell, assistantMessage string, execErr error) TransitionRequest {
	req := TransitionRequest{
		RunID:    run.RunID,
		ExitCode: cell.ExitCode,
		Output:   cell.Output,
	}
	if sandbox != nil {
		req.SandboxID = sandbox.Summary.ID
	}
	if sandbox != nil && cell.ID != "" {
		artifactsDir := filepath.Join(execution.HostSandboxDir(sandbox), "state", "cells", cell.ID)
		req.ArtifactsDir = artifactsDir
		req.LogsPath = filepath.Join(artifactsDir, "output.txt")
	}
	resultJSON, err := json.Marshal(map[string]any{
		"sandboxId":     req.SandboxID,
		"cellId":        cell.ID,
		"agent":         cell.Agent,
		"agentThreadId": cell.AgentThreadID,
		"stopReason":    cell.StopReason,
		"success":       cell.Success,
		"exitCode":      cell.ExitCode,
		"finalText":     assistantMessage,
	})
	if err == nil {
		req.ResultJSON = string(resultJSON)
	}
	if execErr != nil {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = "agent execution failed"
		req.ErrorStack = execErr.Error()
		return req
	}
	if !cell.Success {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = "agent execution failed"
		if reason := strings.TrimSpace(cell.StopReason); reason != "" {
			req.Error += ": " + reason
		}
		if detail := strings.TrimSpace(cell.Stderr); detail != "" {
			req.ErrorStack = detail
		}
	}
	return req
}

func CleanupPolicyStopsSandbox(policy agentcomposev2.RunSandboxCleanupPolicy) bool {
	return policy != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING
}

func CleanupPolicyRemovesSandbox(policy agentcomposev2.RunSandboxCleanupPolicy) bool {
	return policy == agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION
}

func CleanupPolicyFromProto(policy agentcomposev2.RunSandboxCleanupPolicy) string {
	switch policy {
	case agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING:
		return domain.ProjectRunCleanupKeepRunning
	case agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION:
		return domain.ProjectRunCleanupRemoveOnCompletion
	default:
		return domain.ProjectRunCleanupStopOnCompletion
	}
}

func NormalizeCleanupPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case domain.ProjectRunCleanupKeepRunning:
		return domain.ProjectRunCleanupKeepRunning
	case domain.ProjectRunCleanupRemoveOnCompletion:
		return domain.ProjectRunCleanupRemoveOnCompletion
	default:
		return domain.ProjectRunCleanupStopOnCompletion
	}
}

func CompletionCleanupAction(policy string, hasSandbox, sandboxCreated bool) string {
	if !hasSandbox || NormalizeCleanupPolicy(policy) == domain.ProjectRunCleanupKeepRunning {
		return domain.ProjectRunCompletionActionNone
	}
	if NormalizeCleanupPolicy(policy) == domain.ProjectRunCleanupRemoveOnCompletion && sandboxCreated {
		return domain.ProjectRunCompletionActionRemove
	}
	return domain.ProjectRunCompletionActionStop
}
