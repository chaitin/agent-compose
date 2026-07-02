package agentcompose

import (
	"context"
	"errors"
	"fmt"
	"time"

	rundomain "agent-compose/internal/agentcompose/run"
	transport "agent-compose/internal/agentcompose/transport"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	return s.runTransport().RunAgent(ctx, req)
}

func (s *Service) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	return s.runTransport().RunAgentStream(ctx, req, stream)
}

func (s *Service) runTransport() *transport.RunService {
	return transport.NewRunService(
		s.configDB,
		func() transport.ProjectRunCanceler { return NewRunCoordinator(s.configDB) },
		func(ctx context.Context, msg *agentcomposev2.RunAgentRequest, send func(*agentcomposev2.RunAgentStreamResponse) error) (ProjectRunRecord, error, error) {
			var sink *projectRunStreamSink
			if send != nil {
				sink = &projectRunStreamSink{send: send}
			}
			return s.runProjectAgent(ctx, msg, sink)
		},
	)
}

type projectRunStreamSink struct {
	send func(*agentcomposev2.RunAgentStreamResponse) error
}

func (s *Service) runProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest, stream *projectRunStreamSink) (ProjectRunRecord, error, error) {
	if s.configDB == nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	if msg == nil {
		msg = &agentcomposev2.RunAgentRequest{}
	}
	coordinator := NewRunCoordinator(s.configDB)
	transitionCtx := context.WithoutCancel(ctx)
	var agentConfig agentExecutionConfig
	orchestrator := rundomain.AgentOrchestrator{
		Coordinator: coordinator,
		Prepare: func(ctx context.Context, run ProjectRunRecord) (any, error) {
			return s.prepareProjectRun(ctx, run, msg.GetEnv())
		},
		Ensure: func(ctx context.Context, run ProjectRunRecord, prepared any) (rundomain.SessionRef, error) {
			result, err := s.ensureProjectRunSession(ctx, run, prepared.(ProjectRunPreparation), msg.GetSessionId())
			return projectRunSessionRef(result.Session), err
		},
		BeforeExec: func(ctx context.Context, run ProjectRunRecord, session rundomain.SessionRef) error {
			config, err := s.projectRunAgentConfig(ctx, run)
			if err != nil {
				return err
			}
			if s.executor == nil {
				return fmt.Errorf("executor is required")
			}
			agentConfig = config
			return nil
		},
		Execute: func(ctx context.Context, run ProjectRunRecord, ref rundomain.SessionRef) (rundomain.AgentCell, error) {
			session := ref.Value.(*Session)
			cell, _, _, execErr := s.executor.ExecuteAgentRequest(ctx, session, ExecuteAgentRequest{
				Agent:             agentConfig.Provider,
				AgentDefinitionID: run.ManagedAgentID,
				Model:             agentConfig.Model,
				RunID:             run.RunID,
				Message:           msg.GetPrompt(),
				OutputSchemaJSON:  msg.GetOutputSchemaJson(),
				Stream:            projectRunAgentExecutionStream(run, stream),
			})
			return projectRunAgentCell(cell), execErr
		},
		Cleanup: func(ctx context.Context, coordinator rundomain.ProjectRunCoordinator, run ProjectRunRecord, ref rundomain.SessionRef) ProjectRunRecord {
			return s.cleanupProjectRunSession(ctx, coordinator.(*RunCoordinator), run, ref.Value.(*Session), msg.GetCleanupPolicy())
		},
	}
	run, execErr, err := orchestrator.Run(ctx, transitionCtx, rundomain.AgentRunRequest{
		ProjectID:       msg.GetProjectId(),
		AgentName:       msg.GetAgentName(),
		Source:          transport.ProjectRunSourceFromProto(msg.GetSource()),
		SchedulerID:     msg.GetSchedulerId(),
		TriggerID:       msg.GetTriggerId(),
		Prompt:          msg.GetPrompt(),
		ClientRequestID: msg.GetClientRequestId(),
	})
	if err != nil {
		var stageErr *rundomain.StageError
		if errors.As(err, &stageErr) && stageErr.Stage == "begin" {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
	}
	return run, execErr, nil
}

func projectRunSessionRef(session *Session) rundomain.SessionRef {
	if session == nil {
		return rundomain.SessionRef{}
	}
	return rundomain.SessionRef{
		ID:      session.Summary.ID,
		HostDir: hostSessionDir(session),
		Value:   session,
	}
}

func projectRunAgentCell(cell NotebookCell) rundomain.AgentCell {
	return rundomain.AgentCell{
		ID:             cell.ID,
		Agent:          cell.Agent,
		AgentSessionID: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Success:        cell.Success,
		ExitCode:       cell.ExitCode,
		Output:         cell.Output,
		Stderr:         cell.Stderr,
	}
}

func (s *Service) projectRunAgentConfig(ctx context.Context, run ProjectRunRecord) (agentExecutionConfig, error) {
	agent, err := s.configDB.GetAgentDefinition(ctx, run.ManagedAgentID)
	if err != nil {
		return agentExecutionConfig{}, fmt.Errorf("resolve managed agent definition %s: %w", run.ManagedAgentID, err)
	}
	config := agentExecutionConfigFromDefinition(agent, defaultAgentProvider)
	if config.Provider == "" {
		config.Provider = defaultAgentProvider
	}
	return config, nil
}

func projectRunAgentExecutionStream(run ProjectRunRecord, sink *projectRunStreamSink) AgentExecutionStream {
	if sink == nil || sink.send == nil {
		return AgentExecutionStream{}
	}
	return AgentExecutionStream{
		OnStart: func(NotebookCell) error {
			return sink.send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_STARTED,
				Run:       transport.RunSummaryResponse(run),
				RunId:     run.RunID,
				CreatedAt: transport.FormatMaybeTime(time.Now().UTC()),
			})
		},
		OnChunk: func(_ string, chunk ExecChunk) error {
			return sink.send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:     run.RunID,
				Chunk:     chunk.Text,
				IsStderr:  chunk.IsStderr,
				CreatedAt: transport.FormatMaybeTime(time.Now().UTC()),
			})
		},
	}
}

func projectRunTransitionFromAgentCell(run ProjectRunRecord, session *Session, cell NotebookCell, execErr error) ProjectRunTransitionRequest {
	return rundomain.TransitionFromAgentCell(run, session.Summary.ID, hostSessionDir(session), rundomain.AgentCell{
		ID:             cell.ID,
		Agent:          cell.Agent,
		AgentSessionID: cell.AgentSessionID,
		StopReason:     cell.StopReason,
		Success:        cell.Success,
		ExitCode:       cell.ExitCode,
		Output:         cell.Output,
		Stderr:         cell.Stderr,
	}, execErr)
}

func (s *Service) cleanupProjectRunSession(ctx context.Context, coordinator *RunCoordinator, run ProjectRunRecord, session *Session, policy agentcomposev2.RunSessionCleanupPolicy) ProjectRunRecord {
	if !projectRunCleanupPolicyStopsSession(policy) || session == nil {
		return run
	}
	cleanupErr := s.stopProjectRunSession(ctx, session)
	if cleanupErr == nil {
		return run
	}
	updated, err := coordinator.TransitionRun(ctx, ProjectRunTransitionRequest{
		RunID:        run.RunID,
		Status:       run.Status,
		SessionID:    run.SessionID,
		CleanupError: cleanupErr.Error(),
	})
	if err != nil {
		return run
	}
	return updated
}

func projectRunCleanupPolicyStopsSession(policy agentcomposev2.RunSessionCleanupPolicy) bool {
	return policy != agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING
}

func (s *Service) stopProjectRunSession(ctx context.Context, session *Session) error {
	if s.store == nil {
		return fmt.Errorf("session store is required")
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return err
	}
	if loaded.Summary.VMStatus != VMStatusRunning {
		return nil
	}
	if s.driver == nil {
		return fmt.Errorf("session driver is required")
	}
	if err := s.driver.StopSessionVM(ctx, loaded); err != nil {
		return err
	}
	loaded.Summary.VMStatus = VMStatusStopped
	if err := s.store.UpdateSession(ctx, loaded); err != nil {
		return err
	}
	event := SessionEvent{ID: uuid.NewString(), Type: "session.stopped", Level: "info", Message: "session stopped", CreatedAt: time.Now().UTC()}
	_ = s.store.AddEvent(ctx, loaded.Summary.ID, event)
	if s.streams != nil {
		s.streams.PublishSessionUpdated(&loaded.Summary)
		s.streams.PublishEventAdded(loaded.Summary.ID, event)
	}
	return nil
}

func (s *Service) GetRun(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	return s.runTransport().GetRun(ctx, req)
}

func (s *Service) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	return s.runTransport().ListRuns(ctx, req)
}

func (s *Service) StopRun(ctx context.Context, req *connect.Request[agentcomposev2.StopRunRequest]) (*connect.Response[agentcomposev2.StopRunResponse], error) {
	return s.runTransport().StopRun(ctx, req)
}
