package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

var errRunAgentStreamSend = errors.New("run agent stream send failed")

func (s *Service) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	run, _, err := s.runProjectAgent(ctx, req.Msg, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.RunAgentResponse{
		Run: api.ProjectRunDetailToProto(run),
	}), nil
}

func (s *Service) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	prepareStreamingHeaders(stream.ResponseHeader())
	sink := projectRunStreamSink{
		send: func(resp *agentcomposev2.RunAgentStreamResponse) error {
			if err := stream.Send(resp); err != nil {
				return fmt.Errorf("%w: %w", errRunAgentStreamSend, err)
			}
			return nil
		},
	}
	run, execErr, err := s.runProjectAgent(ctx, req.Msg, &sink)
	if err != nil {
		return err
	}
	if errors.Is(execErr, errRunAgentStreamSend) {
		return connect.NewError(connect.CodeUnknown, execErr)
	}
	if sendErr := sink.send(&agentcomposev2.RunAgentStreamResponse{
		EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
		Run:       api.ProjectRunSummaryToProto(run),
		RunId:     run.RunID,
		CreatedAt: api.FormatProjectTime(time.Now().UTC()),
	}); sendErr != nil {
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	return nil
}

type projectRunStreamSink struct {
	send func(*agentcomposev2.RunAgentStreamResponse) error
}

func (s *Service) runProjectAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest, stream *projectRunStreamSink) (ProjectRunRecord, error, error) {
	if s.configDB == nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	commandText := strings.TrimSpace(msg.GetCommand())
	if commandText != "" && (strings.TrimSpace(msg.GetPrompt()) != "" || strings.TrimSpace(msg.GetTriggerId()) != "") {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run requires only one of command, prompt, or trigger"))
	}
	coordinator := runs.NewCoordinator(s.configDB, domain.StableProjectRunID)
	run, err := coordinator.BeginRun(ctx, runs.StartRequest{
		ProjectID:       msg.GetProjectId(),
		AgentName:       msg.GetAgentName(),
		Source:          api.ProjectRunSourceFromProto(msg.GetSource()),
		SchedulerID:     msg.GetSchedulerId(),
		TriggerID:       msg.GetTriggerId(),
		Prompt:          msg.GetPrompt(),
		ClientRequestID: msg.GetClientRequestId(),
	})
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	transitionCtx := context.WithoutCancel(ctx)
	prepared, err := s.prepareProjectRun(ctx, run, msg.GetEnv())
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, runs.TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("workspace preparation failed: %v", err),
		})
		if markErr != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, markErr)
		}
		return run, err, nil
	}
	sessionResult, err := s.ensureProjectRunSession(ctx, run, prepared, msg.GetSessionId())
	if err != nil {
		transition := runs.TransitionRequest{
			RunID: run.RunID,
			Error: fmt.Sprintf("session start failed: %v", err),
		}
		if sessionResult.Session != nil {
			transition.SessionID = sessionResult.Session.Summary.ID
		}
		run, markErr := coordinator.MarkFailed(transitionCtx, transition)
		if markErr != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, markErr)
		}
		return run, err, nil
	}
	run, err = coordinator.MarkRunning(transitionCtx, run.RunID, sessionResult.Session.Summary.ID)
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
	}
	if commandText != "" {
		transition, execErr := s.executeProjectRunCommand(ctx, run, sessionResult.Session, msg, commandText, stream)
		if execErr != nil || transition.ExitCode != 0 {
			run, err = coordinator.MarkFailed(transitionCtx, transition)
			if err != nil {
				return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
			}
			run = s.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult.Session, msg.GetCleanupPolicy())
			return run, execErr, nil
		}
		run, err = coordinator.MarkSucceeded(transitionCtx, transition)
		if err != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
		}
		run = s.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult.Session, msg.GetCleanupPolicy())
		return run, nil, nil
	}
	agentConfig, err := s.projectRunAgentConfig(ctx, run)
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, runs.TransitionRequest{
			RunID:     run.RunID,
			SessionID: sessionResult.Session.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, markErr)
		}
		return run, err, nil
	}
	if s.executor == nil {
		err = fmt.Errorf("executor is required")
		run, markErr := coordinator.MarkFailed(transitionCtx, runs.TransitionRequest{
			RunID:     run.RunID,
			SessionID: sessionResult.Session.Summary.ID,
			ExitCode:  1,
			Error:     fmt.Sprintf("agent execution failed: %v", err),
		})
		if markErr != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, markErr)
		}
		return run, err, nil
	}
	cell, _, _, execErr := s.executor.ExecuteAgentRequest(ctx, sessionResult.Session, execution.ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: run.ManagedAgentID,
		Model:             agentConfig.Model,
		RunID:             run.RunID,
		Message:           msg.GetPrompt(),
		OutputSchemaJSON:  msg.GetOutputSchemaJson(),
		Stream:            projectRunAgentExecutionStream(run, stream),
	})
	transition := runs.TransitionFromAgentCell(run, sessionResult.Session, cell, execErr)
	if execErr != nil || !cell.Success {
		run, err = coordinator.MarkFailed(transitionCtx, transition)
		if err != nil {
			return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
		}
		run = s.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult.Session, msg.GetCleanupPolicy())
		return run, execErr, nil
	}
	run, err = coordinator.MarkSucceeded(transitionCtx, transition)
	if err != nil {
		return ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, err)
	}
	run = s.cleanupProjectRunSession(transitionCtx, coordinator, run, sessionResult.Session, msg.GetCleanupPolicy())
	return run, nil, nil
}

func (s *Service) executeProjectRunCommand(ctx context.Context, run ProjectRunRecord, session *Session, msg *agentcomposev2.RunAgentRequest, commandText string, sink *projectRunStreamSink) (runs.TransitionRequest, error) {
	transition := runs.TransitionRequest{
		RunID:     run.RunID,
		SessionID: session.Summary.ID,
	}
	if s.store == nil || s.runtimes == nil {
		err := fmt.Errorf("command runtime dependencies are required")
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	if sink != nil && sink.send != nil {
		if err := sink.send(&agentcomposev2.RunAgentStreamResponse{
			EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_STARTED,
			Run:       api.ProjectRunSummaryToProto(run),
			RunId:     run.RunID,
			CreatedAt: api.FormatProjectTime(time.Now().UTC()),
		}); err != nil {
			transition.ExitCode = 1
			transition.Error = fmt.Sprintf("command execution failed: %v", err)
			return transition, err
		}
	}
	appconfig.ApplyDefaultGuestPaths(s.config)
	vmState, err := s.store.GetVMState(session.Summary.ID)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	runtime, err := s.runtimes.ForSession(session)
	if err != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
		return transition, err
	}
	accumulator := execStreamAccumulator{}
	var sendErr error
	writer := func(chunk ExecChunk) {
		if sendErr != nil {
			return
		}
		accumulator.writeChunk(chunk)
		if sink != nil && sink.send != nil {
			sendErr = sink.send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:     run.RunID,
				Chunk:     chunk.Text,
				IsStderr:  chunk.IsStderr,
				CreatedAt: api.FormatProjectTime(time.Now().UTC()),
			})
		}
	}
	execCtx, cancel := execution.ExecContext(ctx, 0)
	defer cancel()
	result, execErr := runtime.ExecStream(execCtx, session, vmState, ExecSpec{
		Command: "bash",
		Args:    []string{"-lc", commandText},
		Env:     api.ExecEnvMap(msg.GetEnv()),
		Cwd:     s.config.GuestWorkspacePath,
	}, writer)
	if sendErr != nil {
		transition.ExitCode = 1
		transition.Error = fmt.Sprintf("command execution failed: %v", sendErr)
		return transition, sendErr
	}
	if execErr != nil {
		result = execution.MergeExecResults(result, accumulator.result(execution.FirstNonZeroInt(result.ExitCode, 1), false))
		result.ExitCode = execution.FirstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = firstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
	} else {
		result = execution.MergeExecResults(result, accumulator.result(result.ExitCode, result.Success))
	}
	transition = projectRunTransitionFromCommandResult(run, session, commandText, result, execErr)
	if err := writeProjectRunCommandArtifacts(transition.ArtifactsDir, commandText, result); err != nil {
		if execErr == nil {
			execErr = err
		}
		transition.ExitCode = execution.FirstNonZeroInt(transition.ExitCode, 1)
		transition.Error = fmt.Sprintf("command execution failed: %v", err)
	}
	return transition, execErr
}

func (s *Service) projectRunAgentConfig(ctx context.Context, run ProjectRunRecord) (agentExecutionConfig, error) {
	agent, err := s.configDB.GetAgentDefinition(ctx, run.ManagedAgentID)
	if err != nil {
		return agentExecutionConfig{}, fmt.Errorf("resolve managed agent definition %s: %w", run.ManagedAgentID, err)
	}
	config := agentExecutionConfigFromDefinition(agent, domain.DefaultAgentProvider)
	if config.Provider == "" {
		config.Provider = domain.DefaultAgentProvider
	}
	return config, nil
}

func projectRunAgentExecutionStream(run ProjectRunRecord, sink *projectRunStreamSink) execution.AgentExecutionStream {
	if sink == nil || sink.send == nil {
		return execution.AgentExecutionStream{}
	}
	return execution.AgentExecutionStream{
		OnStart: func(NotebookCell) error {
			return sink.send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_STARTED,
				Run:       api.ProjectRunSummaryToProto(run),
				RunId:     run.RunID,
				CreatedAt: api.FormatProjectTime(time.Now().UTC()),
			})
		},
		OnChunk: func(_ string, chunk ExecChunk) error {
			return sink.send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:     run.RunID,
				Chunk:     chunk.Text,
				IsStderr:  chunk.IsStderr,
				CreatedAt: api.FormatProjectTime(time.Now().UTC()),
			})
		},
	}
}

func projectRunTransitionFromCommandResult(run ProjectRunRecord, session *Session, commandText string, result domain.ExecResult, execErr error) runs.TransitionRequest {
	artifactsDir := filepath.Join(execution.HostSessionDir(session), "state", "runs", run.RunID)
	req := runs.TransitionRequest{
		RunID:        run.RunID,
		SessionID:    session.Summary.ID,
		ExitCode:     result.ExitCode,
		Output:       result.Output,
		ArtifactsDir: artifactsDir,
		LogsPath:     filepath.Join(artifactsDir, "output.txt"),
	}
	resultJSON, err := json.Marshal(map[string]any{
		"mode":     "command",
		"command":  commandText,
		"success":  result.Success,
		"exitCode": result.ExitCode,
	})
	if err == nil {
		req.ResultJSON = string(resultJSON)
	}
	if execErr != nil {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = fmt.Sprintf("command execution failed: %v", execErr)
		return req
	}
	if !result.Success {
		req.ExitCode = execution.FirstNonZeroInt(req.ExitCode, 1)
		req.Error = "command execution failed"
		if detail := firstNonEmpty(result.Stderr, result.Output, result.Stdout); strings.TrimSpace(detail) != "" {
			req.Error += ": " + strings.TrimSpace(detail)
		}
	}
	return req
}

func writeProjectRunCommandArtifacts(artifactsDir, commandText string, result domain.ExecResult) error {
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		return fmt.Errorf("create command artifacts dir: %w", err)
	}
	return execution.WriteCellArtifacts(artifactsDir, commandText, result)
}

func (s *Service) cleanupProjectRunSession(ctx context.Context, coordinator *runs.Coordinator, run ProjectRunRecord, session *Session, policy agentcomposev2.RunSessionCleanupPolicy) ProjectRunRecord {
	if !runs.CleanupPolicyStopsSession(policy) || session == nil {
		return run
	}
	cleanupErr := s.stopProjectRunSession(ctx, session)
	if cleanupErr == nil {
		return run
	}
	updated, err := coordinator.TransitionRun(ctx, runs.TransitionRequest{
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

func (s *Service) stopProjectRunSession(ctx context.Context, session *Session) error {
	if s.store == nil {
		return fmt.Errorf("session store is required")
	}
	loaded, err := s.store.GetSession(ctx, session.Summary.ID)
	if err != nil {
		return err
	}
	if loaded.Summary.VMStatus != domain.VMStatusRunning {
		return nil
	}
	if s.driver == nil {
		return fmt.Errorf("session driver is required")
	}
	if err := s.driver.StopSessionVM(ctx, loaded); err != nil {
		return err
	}
	loaded.Summary.VMStatus = domain.VMStatusStopped
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
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	run, err := s.configDB.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if projectID := strings.TrimSpace(req.Msg.GetProjectId()); projectID != "" && run.ProjectID != projectID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found in project %s", runID, projectID))
	}
	return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: api.ProjectRunDetailToProto(run)}), nil
}

func (s *Service) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runs, err := s.configDB.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
		ProjectID:   req.Msg.GetProjectId(),
		AgentName:   req.Msg.GetAgentName(),
		SessionID:   req.Msg.GetSessionId(),
		SchedulerID: req.Msg.GetSchedulerId(),
		Status:      api.ProjectRunStatusFromProto(req.Msg.GetStatus()),
		Source:      api.ProjectRunSourceFilterFromProto(req.Msg.GetSource()),
		Offset:      int(req.Msg.GetOffset()),
		Limit:       int(req.Msg.GetLimit()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*agentcomposev2.RunSummary, 0, len(runs))
	for _, run := range runs {
		items = append(items, api.ProjectRunSummaryToProto(run))
	}
	return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: items}), nil
}

func (s *Service) StopRun(ctx context.Context, req *connect.Request[agentcomposev2.StopRunRequest]) (*connect.Response[agentcomposev2.StopRunResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	coordinator := runs.NewCoordinator(s.configDB, domain.StableProjectRunID)
	current, err := s.configDB.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if runs.StatusIsTerminal(current.Status) {
		return connect.NewResponse(&agentcomposev2.StopRunResponse{
			Run:           api.ProjectRunDetailToProto(current),
			StopRequested: false,
		}), nil
	}
	reason := strings.TrimSpace(req.Msg.GetReason())
	if reason == "" {
		reason = "stop requested"
	}
	run, err := coordinator.MarkCanceled(ctx, runs.TransitionRequest{
		RunID: runID,
		Error: reason,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.StopRunResponse{
		Run:           api.ProjectRunDetailToProto(run),
		StopRequested: true,
	}), nil
}
