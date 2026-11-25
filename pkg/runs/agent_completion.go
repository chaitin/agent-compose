package runs

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// agentRunOutcome is what an agent execution produced, fresh or re-attached.
type agentRunOutcome struct {
	Run       domain.ProjectRunRecord
	Sandbox   *domain.Sandbox
	Cell      domain.NotebookCell
	Assistant domain.SandboxEvent
	ExecErr   error
	Projector *agentRunProjector
}

func (c *Controller) finishProjectRunAgent(transitionCtx, runCtx context.Context, outcome agentRunOutcome) (domain.ProjectRunRecord, error) {
	projector := outcome.Projector
	if projector == nil {
		projector = newAgentRunProjector(transitionCtx, outcome.Run, outcome.Sandbox, outcome.Run.LogsPath, c.runLogs)
		projector.bindStore(c.configDB)
	}
	transition := projector.CompleteCell(outcome.Cell, outcome.Assistant, outcome.ExecErr)
	if outcome.ExecErr != nil || !outcome.Cell.Success || transition.ExitCode != 0 || transition.Error != "" {
		return c.completeProjectRunError(transitionCtx, runCtx, transition, outcome.ExecErr)
	}
	transition.Status = domain.ProjectRunStatusSucceeded
	return c.completeProjectRun(transitionCtx, transition)
}

// CompleteCell supplies the execution-boundary outcome, including old executors
// that only report events and a final cell. It uses the same terminal projection
// as wire frames; the cell contributes compatibility artifacts and process errors.
func (p *agentRunProjector) CompleteCell(cell domain.NotebookCell, assistant domain.SandboxEvent, execErr error) (transition TransitionRequest) {
	transition = TransitionFromAgentCell(p.run, p.sandbox, cell, assistant.Message, execErr)
	defer func() { transition.LogIncomplete = p.health.incomplete }()
	p.mu.Lock()
	terminal := p.terminal
	p.mu.Unlock()
	if terminal == nil {
		frame := execution.AgentFrame{Type: "result", Provider: cell.Agent, StopReason: cell.StopReason,
			FinalText: assistant.Message, FinalTextSource: domain.AgentFinalTextSourceProviderMessage}
		if execErr != nil || !cell.Success {
			frame.Type, frame.Message, frame.Code = "error", firstNonEmpty(transition.ErrorStack, transition.Error), "agent_execution_failed"
			frame.StopReason = firstNonEmpty(cell.StopReason, "error")
		}
		projection, err := p.ProjectFrame(frame)
		// Execution has already finished. As before, failure to finish its log
		// cannot replace the confirmed process outcome.
		if err == nil {
			terminal = projection.Terminal
		}
	}
	if terminal != nil {
		return mergeAgentExecutionOutcome(transition, terminal)
	} else {
		transition.TerminalEvents = p.pendingTurnMessages()
	}
	return transition
}

// CompleteExecution validates the process outcome after projecting its wire
// frames. Both transports finish through mergeAgentExecutionOutcome; adapters
// supply their own artifact and output representations.
func (p *agentRunProjector) CompleteExecution(result domain.ExecResult, execErr error) (TransitionRequest, error) {
	p.mu.Lock()
	terminal := p.terminal
	p.mu.Unlock()
	base := TransitionRequest{RunID: p.run.RunID, LogsPath: p.logsPath, ExitCode: result.ExitCode}
	if p.sandbox != nil {
		base.SandboxID = p.sandbox.Summary.ID
	}
	if terminal != nil {
		base.Output, base.ResultJSON = terminal.Output, terminal.ResultJSON
	}
	if terminal == nil && execErr == nil && result.Success && result.ExitCode == 0 {
		execErr = fmt.Errorf("agent runtime ended without a result frame")
	}
	if execErr != nil || !result.Success || result.ExitCode != 0 {
		base.ExitCode = max(result.ExitCode, 1)
		base.Error = "agent execution failed"
		if execErr != nil {
			base.Error = fmt.Sprintf("agent execution failed: %v", execErr)
		}
		if terminal == nil {
			projection, err := p.ProjectFrame(execution.AgentFrame{Type: "error", Message: base.Error, Code: "agent_execution_failed"})
			// Preserve the confirmed process failure if recording its final log fails.
			if err == nil {
				terminal = projection.Terminal
			}
		}
	}
	transition := mergeAgentExecutionOutcome(base, terminal)
	transition.LogIncomplete = p.health.incomplete
	if transition.Error != "" || transition.ExitCode != 0 {
		return transition, agentTransitionError(transition)
	}
	return transition, nil
}

func mergeAgentExecutionOutcome(base TransitionRequest, terminal *TransitionRequest) TransitionRequest {
	if terminal == nil {
		return base
	}
	base.TerminalEvents = append([]domain.ProjectRunEventRecord(nil), terminal.TerminalEvents...)
	if strings.TrimSpace(base.Error) == "" && terminal.Error != "" {
		base.Error, base.ExitCode = terminal.Error, max(terminal.ExitCode, 1)
	}
	return base
}
