package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

var errRunAgentStreamSend = errors.New("run agent stream send failed")

func (s *Service) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	run, _, err := s.runProjectAgent(ctx, req.Msg, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.RunAgentResponse{
		Run: runDetailResponse(run),
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
		Run:       runSummaryResponse(run),
		RunId:     run.RunID,
		CreatedAt: formatProjectTime(time.Now().UTC()),
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
	coordinator := NewRunCoordinator(s.configDB)
	run, err := coordinator.BeginRun(ctx, ProjectRunStartRequest{
		ProjectID:       msg.GetProjectId(),
		AgentName:       msg.GetAgentName(),
		Source:          projectRunSourceFromProto(msg.GetSource()),
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
		run, markErr := coordinator.MarkFailed(transitionCtx, ProjectRunTransitionRequest{
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
		transition := ProjectRunTransitionRequest{
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
	agentConfig, err := s.projectRunAgentConfig(ctx, run)
	if err != nil {
		run, markErr := coordinator.MarkFailed(transitionCtx, ProjectRunTransitionRequest{
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
		run, markErr := coordinator.MarkFailed(transitionCtx, ProjectRunTransitionRequest{
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
	cell, _, _, execErr := s.executor.ExecuteAgentRequest(ctx, sessionResult.Session, ExecuteAgentRequest{
		Agent:             agentConfig.Provider,
		AgentDefinitionID: run.ManagedAgentID,
		Model:             agentConfig.Model,
		RunID:             run.RunID,
		Message:           msg.GetPrompt(),
		OutputSchemaJSON:  msg.GetOutputSchemaJson(),
		Stream:            projectRunAgentExecutionStream(run, stream),
	})
	transition := projectRunTransitionFromAgentCell(run, sessionResult.Session, cell, execErr)
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
				Run:       runSummaryResponse(run),
				RunId:     run.RunID,
				CreatedAt: formatProjectTime(time.Now().UTC()),
			})
		},
		OnChunk: func(_ string, chunk ExecChunk) error {
			return sink.send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:     run.RunID,
				Chunk:     chunk.Text,
				IsStderr:  chunk.IsStderr,
				CreatedAt: formatProjectTime(time.Now().UTC()),
			})
		},
	}
}

func projectRunTransitionFromAgentCell(run ProjectRunRecord, session *Session, cell NotebookCell, execErr error) ProjectRunTransitionRequest {
	req := ProjectRunTransitionRequest{
		RunID:     run.RunID,
		SessionID: session.Summary.ID,
		ExitCode:  cell.ExitCode,
		Output:    cell.Output,
	}
	if cell.ID != "" {
		artifactsDir := filepath.Join(hostSessionDir(session), "state", "cells", cell.ID)
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
		req.ExitCode = firstNonZeroInt(req.ExitCode, 1)
		req.Error = fmt.Sprintf("agent execution failed: %v", execErr)
		return req
	}
	if !cell.Success {
		req.ExitCode = firstNonZeroInt(req.ExitCode, 1)
		req.Error = "agent execution failed"
		if detail := firstNonEmpty(cell.Stderr, cell.Output); strings.TrimSpace(detail) != "" {
			req.Error += ": " + strings.TrimSpace(detail)
		}
	}
	return req
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
	return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: runDetailResponse(run)}), nil
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
		Status:      projectRunStatusFromProto(req.Msg.GetStatus()),
		Source:      projectRunSourceFilterFromProto(req.Msg.GetSource()),
		Offset:      int(req.Msg.GetOffset()),
		Limit:       int(req.Msg.GetLimit()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*agentcomposev2.RunSummary, 0, len(runs))
	for _, run := range runs {
		items = append(items, runSummaryResponse(run))
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
	coordinator := NewRunCoordinator(s.configDB)
	current, err := s.configDB.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if projectRunStatusIsTerminal(current.Status) {
		return connect.NewResponse(&agentcomposev2.StopRunResponse{
			Run:           runDetailResponse(current),
			StopRequested: false,
		}), nil
	}
	reason := strings.TrimSpace(req.Msg.GetReason())
	if reason == "" {
		reason = "stop requested"
	}
	run, err := coordinator.MarkCanceled(ctx, ProjectRunTransitionRequest{
		RunID: runID,
		Error: reason,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.StopRunResponse{
		Run:           runDetailResponse(run),
		StopRequested: true,
	}), nil
}

func runDetailResponse(run ProjectRunRecord) *agentcomposev2.RunDetail {
	return &agentcomposev2.RunDetail{
		Summary:      runSummaryResponse(run),
		Prompt:       run.Prompt,
		Output:       run.Output,
		ResultJson:   run.ResultJSON,
		LogsPath:     run.LogsPath,
		ArtifactsDir: run.ArtifactsDir,
		CleanupError: run.CleanupError,
		Driver:       run.Driver,
		ImageRef:     run.ImageRef,
	}
}

func runSummaryResponse(run ProjectRunRecord) *agentcomposev2.RunSummary {
	return &agentcomposev2.RunSummary{
		RunId:           run.RunID,
		ProjectId:       run.ProjectID,
		ProjectName:     run.ProjectName,
		ProjectRevision: uint64(run.ProjectRevision),
		AgentId:         run.ManagedAgentID,
		AgentName:       run.AgentName,
		Source:          projectRunSourceResponse(run.Source),
		SchedulerId:     run.SchedulerID,
		TriggerId:       run.TriggerID,
		Status:          projectRunStatusResponse(run.Status),
		SessionId:       run.SessionID,
		ExitCode:        int32(run.ExitCode),
		Error:           run.Error,
		StartedAt:       formatProjectTime(run.StartedAt),
		CompletedAt:     formatProjectTime(run.CompletedAt),
		DurationMs:      run.DurationMs,
		CreatedAt:       formatProjectTime(run.CreatedAt),
		UpdatedAt:       formatProjectTime(run.UpdatedAt),
	}
}

func projectRunStatusResponse(status string) agentcomposev2.RunStatus {
	switch normalizeProjectRunStatus(status) {
	case ProjectRunStatusPending:
		return agentcomposev2.RunStatus_RUN_STATUS_PENDING
	case ProjectRunStatusRunning:
		return agentcomposev2.RunStatus_RUN_STATUS_RUNNING
	case ProjectRunStatusSucceeded:
		return agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED
	case ProjectRunStatusFailed:
		return agentcomposev2.RunStatus_RUN_STATUS_FAILED
	case ProjectRunStatusCanceled:
		return agentcomposev2.RunStatus_RUN_STATUS_CANCELED
	default:
		return agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED
	}
}

func projectRunStatusFromProto(status agentcomposev2.RunStatus) string {
	switch status {
	case agentcomposev2.RunStatus_RUN_STATUS_PENDING:
		return ProjectRunStatusPending
	case agentcomposev2.RunStatus_RUN_STATUS_RUNNING:
		return ProjectRunStatusRunning
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED:
		return ProjectRunStatusSucceeded
	case agentcomposev2.RunStatus_RUN_STATUS_FAILED:
		return ProjectRunStatusFailed
	case agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return ProjectRunStatusCanceled
	default:
		return ""
	}
}

func projectRunSourceResponse(source string) agentcomposev2.RunSource {
	switch normalizeProjectRunSource(source) {
	case ProjectRunSourceScheduler:
		return agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER
	case ProjectRunSourceAPI:
		return agentcomposev2.RunSource_RUN_SOURCE_API
	case ProjectRunSourceManual:
		return agentcomposev2.RunSource_RUN_SOURCE_MANUAL
	default:
		return agentcomposev2.RunSource_RUN_SOURCE_UNSPECIFIED
	}
}

func projectRunSourceFromProto(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return ProjectRunSourceScheduler
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return ProjectRunSourceAPI
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return ProjectRunSourceManual
	default:
		return ProjectRunSourceManual
	}
}

func projectRunSourceFilterFromProto(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return ProjectRunSourceScheduler
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return ProjectRunSourceAPI
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return ProjectRunSourceManual
	default:
		return ""
	}
}
