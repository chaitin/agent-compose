package run

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	execdomain "agent-compose/internal/agentcompose/exec"
	projectdomain "agent-compose/internal/agentcompose/project"
)

type AgentCell struct {
	ID             string
	Agent          string
	AgentSessionID string
	StopReason     string
	Success        bool
	ExitCode       int
	Output         string
	Stderr         string
}

func TransitionFromAgentCell(run projectdomain.ProjectRunRecord, sessionID, hostSessionDir string, cell AgentCell, execErr error) TransitionRequest {
	req := TransitionRequest{
		RunID:     run.RunID,
		SessionID: sessionID,
		ExitCode:  cell.ExitCode,
		Output:    cell.Output,
	}
	if cell.ID != "" {
		artifactsDir := filepath.Join(hostSessionDir, "state", "cells", cell.ID)
		req.ArtifactsDir = artifactsDir
		req.LogsPath = filepath.Join(artifactsDir, "output.txt")
	}
	resultJSON, err := json.Marshal(map[string]any{
		"cellId":         cell.ID,
		"agent":          cell.Agent,
		"agentSessionId": cell.AgentSessionID,
		"stopReason":     cell.StopReason,
		"success":        cell.Success,
		"exitCode":       cell.ExitCode,
	})
	if err == nil {
		req.ResultJSON = string(resultJSON)
	}
	if execErr != nil {
		req.ExitCode = execdomain.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = fmt.Sprintf("agent execution failed: %v", execErr)
		return req
	}
	if !cell.Success {
		req.ExitCode = execdomain.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = "agent execution failed"
		if detail := execdomain.FirstNonEmpty(cell.Stderr, cell.Output); strings.TrimSpace(detail) != "" {
			req.Error += ": " + strings.TrimSpace(detail)
		}
	}
	return req
}
