package agentcompose

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

const watchRunPollInterval = 250 * time.Millisecond

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

func (s *Service) WatchRun(ctx context.Context, req *connect.Request[agentcomposev2.WatchRunRequest], stream *connect.ServerStream[agentcomposev2.RunStreamResponse]) error {
	if s == nil || s.configDB == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	projectID := strings.TrimSpace(req.Msg.GetProjectId())
	prepareStreamingHeaders(stream.ResponseHeader())

	run, err := s.watchRunLoad(ctx, runID, projectID)
	if err != nil {
		return err
	}
	if err := stream.Send(runStreamResponseFromRun(run, runStreamEventTypeForStatus(run.Status))); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	if projectRunStatusIsTerminal(run.Status) {
		return nil
	}
	last := runStreamSnapshotFromRun(run)
	ticker := time.NewTicker(watchRunPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			run, err := s.watchRunLoad(ctx, runID, projectID)
			if err != nil {
				return err
			}
			next := runStreamSnapshotFromRun(run)
			if next == last {
				continue
			}
			last = next
			if err := stream.Send(runStreamResponseFromRun(run, runStreamEventTypeForStatus(run.Status))); err != nil {
				return connect.NewError(connect.CodeUnknown, err)
			}
			if projectRunStatusIsTerminal(run.Status) {
				return nil
			}
		}
	}
}

func (s *Service) watchRunLoad(ctx context.Context, runID, projectID string) (ProjectRunRecord, error) {
	run, err := s.configDB.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ProjectRunRecord{}, connect.NewError(connect.CodeNotFound, err)
		}
		return ProjectRunRecord{}, connect.NewError(connect.CodeInternal, err)
	}
	if projectID != "" && run.ProjectID != projectID {
		return ProjectRunRecord{}, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found", runID))
	}
	return run, nil
}

func runStreamEventTypeForStatus(status string) agentcomposev2.RunStreamEventType {
	if projectRunStatusIsTerminal(status) {
		return agentcomposev2.RunStreamEventType_RUN_STREAM_EVENT_TYPE_COMPLETED
	}
	return agentcomposev2.RunStreamEventType_RUN_STREAM_EVENT_TYPE_STATUS
}

func runStreamResponseFromRun(run ProjectRunRecord, eventType agentcomposev2.RunStreamEventType) *agentcomposev2.RunStreamResponse {
	chunk := strings.TrimSpace(run.Error)
	isStderr := chunk != ""
	if projectRunStatusIsTerminal(run.Status) {
		artifacts, metrics := projectRunArtifactsAndMetrics(run)
		chunk = projectRunEnvelopeJSON(run, artifacts, metrics)
		isStderr = false
	}
	return &agentcomposev2.RunStreamResponse{
		EventType: eventType,
		Run:       runSummaryResponse(run),
		RunId:     run.RunID,
		Chunk:     chunk,
		IsStderr:  isStderr,
		CreatedAt: formatProjectTime(run.UpdatedAt),
	}
}

type runStreamSnapshot struct {
	status      string
	sessionID   string
	exitCode    int
	err         string
	startedAt   int64
	completedAt int64
	updatedAt   int64
}

func runStreamSnapshotFromRun(run ProjectRunRecord) runStreamSnapshot {
	return runStreamSnapshot{
		status:      normalizeProjectRunStatus(run.Status),
		sessionID:   run.SessionID,
		exitCode:    run.ExitCode,
		err:         run.Error,
		startedAt:   nonZeroTimeUnixMilli(run.StartedAt),
		completedAt: nonZeroTimeUnixMilli(run.CompletedAt),
		updatedAt:   run.UpdatedAt.Unix(),
	}
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
		RuntimeContext:  msg.GetRuntimeContext(),
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
	return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: s.runDetailResponse(ctx, run)}), nil
}

func (s *Service) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	if s.configDB == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runs, err := s.configDB.ListProjectRunsByOptions(ctx, ProjectRunListOptions{
		ProjectID:       req.Msg.GetProjectId(),
		AgentName:       req.Msg.GetAgentName(),
		SessionID:       req.Msg.GetSessionId(),
		SchedulerID:     req.Msg.GetSchedulerId(),
		ClientRequestID: req.Msg.GetClientRequestId(),
		Status:          projectRunStatusFromProto(req.Msg.GetStatus()),
		Source:          projectRunSourceFilterFromProto(req.Msg.GetSource()),
		TargetType:      projectRunTargetTypeFromProto(req.Msg.GetTargetType()),
		TargetName:      req.Msg.GetTargetName(),
		Offset:          int(req.Msg.GetOffset()),
		Limit:           int(req.Msg.GetLimit()),
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
	artifacts, metrics := projectRunArtifactsAndMetrics(run)
	return &agentcomposev2.RunDetail{
		Summary:        runSummaryResponse(run),
		Prompt:         run.Prompt,
		Output:         run.Output,
		ResultJson:     projectRunEnvelopeJSON(run, artifacts, metrics),
		InputJson:      run.InputJSON,
		OutputJson:     run.OutputJSON,
		RuntimeContext: runtimeContextResponse(run.RuntimeContextJSON),
		LogsPath:       run.LogsPath,
		ArtifactsDir:   run.ArtifactsDir,
		CleanupError:   run.CleanupError,
		Driver:         run.Driver,
		ImageRef:       run.ImageRef,
		Artifacts:      artifacts,
		Metrics:        metrics,
	}
}

func projectRunEnvelopeJSON(run ProjectRunRecord, artifacts []*agentcomposev2.Artifact, metrics map[string]string) string {
	payload := map[string]any{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(run.ResultJSON)), &payload); err != nil || payload == nil {
		payload = map[string]any{}
	}
	payload["run_id"] = run.RunID
	payload["runId"] = run.RunID
	payload["status"] = normalizeProjectRunStatus(run.Status)
	payload["target_type"] = strings.TrimSpace(run.TargetType)
	payload["targetType"] = strings.TrimSpace(run.TargetType)
	payload["target_name"] = strings.TrimSpace(run.TargetName)
	payload["targetName"] = strings.TrimSpace(run.TargetName)
	payload["output_json"] = strings.TrimSpace(run.OutputJSON)
	payload["outputJson"] = strings.TrimSpace(run.OutputJSON)
	payload["error"] = strings.TrimSpace(run.Error)
	payload["logs"] = run.Output
	payload["logs_path"] = strings.TrimSpace(run.LogsPath)
	payload["logsPath"] = strings.TrimSpace(run.LogsPath)
	payload["artifacts"] = projectRunEnvelopeArtifacts(payload["artifacts"], artifacts)
	payload["metrics"] = cloneProjectRunStringMap(metrics)
	payload["started_at"] = formatProjectTime(run.StartedAt)
	payload["startedAt"] = formatProjectTime(run.StartedAt)
	payload["completed_at"] = formatProjectTime(run.CompletedAt)
	payload["completedAt"] = formatProjectTime(run.CompletedAt)
	payload["result_json"] = firstNonEmpty(strings.TrimSpace(run.ResultJSON), "{}")
	payload["resultJson"] = firstNonEmpty(strings.TrimSpace(run.ResultJSON), "{}")
	raw, err := json.Marshal(payload)
	if err != nil {
		return firstNonEmpty(strings.TrimSpace(run.ResultJSON), "{}")
	}
	return string(raw)
}

func projectRunEnvelopeArtifacts(existing any, artifacts []*agentcomposev2.Artifact) any {
	if len(artifacts) == 0 {
		if existing != nil {
			return existing
		}
		return []map[string]any{}
	}
	items := make([]map[string]any, 0, len(artifacts))
	for _, artifact := range artifacts {
		if artifact == nil {
			continue
		}
		items = append(items, map[string]any{
			"artifact_id":  artifact.GetArtifactId(),
			"artifactId":   artifact.GetArtifactId(),
			"run_id":       artifact.GetRunId(),
			"runId":        artifact.GetRunId(),
			"project_id":   artifact.GetProjectId(),
			"projectId":    artifact.GetProjectId(),
			"name":         artifact.GetName(),
			"path":         artifact.GetPath(),
			"content_type": artifact.GetContentType(),
			"contentType":  artifact.GetContentType(),
			"size_bytes":   artifact.GetSizeBytes(),
			"sizeBytes":    artifact.GetSizeBytes(),
			"digest":       artifact.GetDigest(),
			"created_at":   artifact.GetCreatedAt(),
			"createdAt":    artifact.GetCreatedAt(),
			"metadata":     cloneProjectRunStringMap(artifact.GetMetadata()),
		})
	}
	return items
}

func cloneProjectRunStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func runtimeContextResponse(raw string) *agentcomposev2.RuntimeContext {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	raw = redactRuntimeContextJSON(raw)
	if raw == "{}" {
		return nil
	}
	var context agentcomposev2.RuntimeContext
	if err := json.Unmarshal([]byte(raw), &context); err != nil {
		return nil
	}
	return &context
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
		ClientRequestId: run.ClientRequestID,
		Status:          projectRunStatusResponse(run.Status),
		SessionId:       run.SessionID,
		ExitCode:        int32(run.ExitCode),
		Error:           run.Error,
		StartedAt:       formatProjectTime(run.StartedAt),
		CompletedAt:     formatProjectTime(run.CompletedAt),
		DurationMs:      run.DurationMs,
		CreatedAt:       formatProjectTime(run.CreatedAt),
		UpdatedAt:       formatProjectTime(run.UpdatedAt),
		TargetType:      projectRunTargetTypeResponse(run.TargetType),
		TargetName:      run.TargetName,
	}
}

func projectRunTargetTypeResponse(targetType string) agentcomposev2.RunTargetType {
	switch strings.ToLower(strings.TrimSpace(targetType)) {
	case "agent":
		return agentcomposev2.RunTargetType_RUN_TARGET_TYPE_AGENT
	case "service":
		return agentcomposev2.RunTargetType_RUN_TARGET_TYPE_SERVICE
	case "exec":
		return agentcomposev2.RunTargetType_RUN_TARGET_TYPE_EXEC
	case "trigger":
		return agentcomposev2.RunTargetType_RUN_TARGET_TYPE_TRIGGER
	case "webhook":
		return agentcomposev2.RunTargetType_RUN_TARGET_TYPE_WEBHOOK
	default:
		return agentcomposev2.RunTargetType_RUN_TARGET_TYPE_UNSPECIFIED
	}
}

func projectRunTargetTypeFromProto(targetType agentcomposev2.RunTargetType) string {
	switch targetType {
	case agentcomposev2.RunTargetType_RUN_TARGET_TYPE_AGENT:
		return "agent"
	case agentcomposev2.RunTargetType_RUN_TARGET_TYPE_SERVICE:
		return "service"
	case agentcomposev2.RunTargetType_RUN_TARGET_TYPE_EXEC:
		return "exec"
	case agentcomposev2.RunTargetType_RUN_TARGET_TYPE_TRIGGER:
		return "trigger"
	case agentcomposev2.RunTargetType_RUN_TARGET_TYPE_WEBHOOK:
		return "webhook"
	default:
		return ""
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
