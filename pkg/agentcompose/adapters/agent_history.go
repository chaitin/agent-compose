package adapters

import (
	"context"
	"fmt"
	"path/filepath"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

// New run-log executions keep their step identity without a second notebook
// history. Older detached runs still load their existing cell at this boundary.
func (e *AgentExecutor) resumeAgentCell(ctx context.Context, session *domain.Sandbox, request execution.ResumeAgentRequest) (domain.NotebookCell, error) {
	if request.History == execution.AgentHistoryRunLog {
		return domain.NotebookCell{ID: request.CellID, Type: execution.CellTypeAgent, Agent: request.Agent, Source: request.Message, Running: true}, nil
	}
	cells, err := e.store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		return domain.NotebookCell{}, err
	}
	for _, cell := range cells {
		if cell.ID == request.CellID {
			return cell, nil
		}
	}
	return domain.NotebookCell{}, fmt.Errorf("agent cell %s not found", request.CellID)
}

func agentExecutionCursorPath(config *appconfig.Config, runID, cellDir string, history execution.AgentHistoryMode) string {
	if history == execution.AgentHistoryRunLog {
		logPath, err := runlog.RunPath(config.DataRoot, runID)
		if err != nil {
			return ""
		}
		return filepath.Join(filepath.Dir(logPath), "journal-cursor")
	}
	return agentCellCursorPath(cellDir)
}

func (r *agentCellRun) completeRunStep(result domain.ExecResult, agent domain.AgentRunResult, execErr error, assistant domain.SandboxEvent) (domain.NotebookCell, domain.SandboxEvent, domain.SandboxEvent, error) {
	r.cellMu.Lock()
	cell := r.cell
	r.cellMu.Unlock()
	cell.Stdout, cell.Stderr, cell.Output = result.Stdout, result.Stderr, result.Output
	cell.ExitCode, cell.Success, cell.Running = result.ExitCode, agent.Success && execErr == nil, false
	cell.Agent = firstNonEmpty(agent.Agent, cell.Agent, r.agent)
	cell.AgentThreadID = r.threadID(cell.Agent, agent.ThreadID)
	cell.StopReason = agent.StopReason
	if !cell.Success {
		assistant.Type, assistant.Level = "agent.assistant.failed", "error"
	}
	return cell, r.userEvent, assistant, execErr
}
