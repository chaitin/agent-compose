package runs

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/execution"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func (c *Controller) projectRunAgentConfig(ctx context.Context, run domain.ProjectRunRecord) (execution.AgentConfig, error) {
	agent, err := c.projectRunAgentDefinition(ctx, run)
	if err != nil {
		return execution.AgentConfig{}, err
	}
	config := execution.AgentConfigFromDefinition(agent, domain.DefaultAgentProvider)
	if config.Provider == "" {
		config.Provider = domain.DefaultAgentProvider
	}
	return config, nil
}

func (c *Controller) projectRunAgentSystemPrompt(ctx context.Context, run domain.ProjectRunRecord) (string, error) {
	if c == nil || c.configDB == nil || strings.TrimSpace(run.AgentID) == "" {
		return "", nil
	}
	agent, err := c.projectRunAgentDefinition(ctx, run)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(agent.SystemPrompt), nil
}

func (c *Controller) projectRunAgentDefinition(ctx context.Context, run domain.ProjectRunRecord) (domain.AgentDefinition, error) {
	project, err := c.configDB.GetProject(ctx, run.ProjectID)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("resolve project %s: %w", run.ProjectID, err)
	}
	revision, err := c.configDB.GetProjectRevision(ctx, run.ProjectID, run.ProjectRevision)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("resolve project revision %s/%d: %w", run.ProjectID, run.ProjectRevision, err)
	}
	agent, err := projects.AgentDefinitionFromRevision(project, revision, run.AgentName)
	if err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("resolve revision agent %s: %w", run.AgentID, err)
	}
	return agent, nil
}

// agentExecutionStreamRequest bundles the coordinator, run, target sandbox,
// output sink, and log hub projectRunAgentExecutionStream needs to build the
// callbacks execution.AgentExecutionStream fires as the agent run progresses.
type agentExecutionStreamRequest struct {
	Coordinator *Coordinator
	Run         domain.ProjectRunRecord
	Sandbox     *domain.Sandbox
	Sink        *StreamSink
	Hub         *RunLogHub
	Projector   *agentRunProjector
}

func projectRunAgentExecutionStream(ctx context.Context, req agentExecutionStreamRequest) execution.AgentExecutionStream {
	coordinator := req.Coordinator
	run := req.Run
	sandbox := req.Sandbox
	sink := req.Sink
	hub := req.Hub
	projector := req.Projector
	if projector == nil {
		projector = newAgentRunProjector(ctx, run, sandbox, run.LogsPath, hub)
		if coordinator != nil {
			projector.bindStore(coordinator.store)
		}
	}
	var rawOutput atomic.Bool
	return execution.AgentExecutionStream{
		OnRuntimeEnd: projector.FlushRuntimeOutput,
		OnRuntimeOutput: func(chunk domain.ExecChunk) error {
			rawOutput.Store(true)
			_, err := projector.ProjectRuntimeOutput(chunk)
			return err
		},
		OnStart: func(cell domain.NotebookCell) error {
			if !usesRunLog(run) && projector.logsPath == "" {
				projector.logsPath = projectRunAgentCellOutputPath(sandbox, cell.ID)
			}
			if coordinator != nil {
				logsPath := run.LogsPath
				if !usesRunLog(run) {
					logsPath = projectRunAgentCellOutputPath(sandbox, cell.ID)
				}
				if strings.TrimSpace(logsPath) != "" {
					if _, err := coordinator.TransitionRun(ctx, TransitionRequest{
						RunID:       run.RunID,
						Status:      domain.ProjectRunStatusRunning,
						LogsPath:    logsPath,
						AgentCellID: cell.ID,
					}); err != nil {
						return err
					}
				}
			}
			if sink == nil || sink.SendStarted == nil {
				return nil
			}
			return sink.SendStarted(run, time.Now().UTC())
		},
		OnChunk: func(cellID string, chunk domain.ExecChunk) error {
			if chunk.Transcript || rawOutput.Load() {
				// The shared projector records the original agent events.
				return sendAgentChunk(sink, run, chunk)
			}
			if err := projector.AppendOutput(chunk); err != nil {
				return err
			}
			return sendAgentChunk(sink, run, chunk)
		},
		OnAgentEvent: func(_ string, event []byte) error {
			_, err := projector.ProjectFrame(execution.AgentFrame{Type: "agent_event", Event: event})
			return err
		},
		OnFrame: func(_ string, frame execution.AgentFrame) error {
			_, err := projector.ProjectFrame(frame)
			return err
		},
	}
}

func projectRunAgentCellOutputPath(sandbox *domain.Sandbox, cellID string) string {
	cellID = strings.TrimSpace(cellID)
	if sandbox == nil || cellID == "" {
		return ""
	}
	return filepath.Join(execution.HostSandboxDir(sandbox), "state", "cells", cellID, "output.txt")
}

func sendAgentChunk(sink *StreamSink, run domain.ProjectRunRecord, chunk domain.ExecChunk) error {
	if sink == nil || sink.SendChunk == nil {
		return nil
	}
	return sink.SendChunk(run.RunID, chunk, time.Now().UTC())
}

func agentHistoryMode(run domain.ProjectRunRecord) execution.AgentHistoryMode {
	if usesRunLog(run) {
		return execution.AgentHistoryRunLog
	}
	return execution.AgentHistoryNotebook
}
