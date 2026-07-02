package transport

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"

	projectdomain "agent-compose/internal/agentcompose/project"
	rundomain "agent-compose/internal/agentcompose/run"
	"agent-compose/pkg/agentcompose/domain"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

var ErrRunAgentStreamSend = errors.New("run agent stream send failed")

type ProjectRunStore interface {
	GetProjectRun(context.Context, string) (projectdomain.ProjectRunRecord, error)
	ListProjectRunsByOptions(context.Context, projectdomain.ProjectRunListOptions) ([]projectdomain.ProjectRunRecord, error)
}

type ProjectRunCanceler interface {
	MarkCanceled(context.Context, rundomain.TransitionRequest) (projectdomain.ProjectRunRecord, error)
}

type RunAgentFunc func(context.Context, *agentcomposev2.RunAgentRequest, func(*agentcomposev2.RunAgentStreamResponse) error) (projectdomain.ProjectRunRecord, error, error)

type RunService struct {
	Store       ProjectRunStore
	NewCanceler func() ProjectRunCanceler
	RunAgentFn  RunAgentFunc
	Now         func() time.Time
}

func NewRunService(store ProjectRunStore, newCanceler func() ProjectRunCanceler, runAgent RunAgentFunc) *RunService {
	return &RunService{Store: store, NewCanceler: newCanceler, RunAgentFn: runAgent}
}

func (s *RunService) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	run, _, err := s.runAgent(ctx, req.Msg, nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.RunAgentResponse{
		Run: RunDetailResponse(run),
	}), nil
}

func (s *RunService) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	PrepareStreamingHeaders(stream.ResponseHeader())
	send := func(resp *agentcomposev2.RunAgentStreamResponse) error {
		if err := stream.Send(resp); err != nil {
			return fmt.Errorf("%w: %w", ErrRunAgentStreamSend, err)
		}
		return nil
	}
	run, execErr, err := s.runAgent(ctx, req.Msg, send)
	if err != nil {
		return err
	}
	if errors.Is(execErr, ErrRunAgentStreamSend) {
		return connect.NewError(connect.CodeUnknown, execErr)
	}
	if sendErr := send(&agentcomposev2.RunAgentStreamResponse{
		EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
		Run:       RunSummaryResponse(run),
		RunId:     run.RunID,
		CreatedAt: s.formatNow(),
	}); sendErr != nil {
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	return nil
}

func (s *RunService) GetRun(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	run, err := s.Store.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if projectID := strings.TrimSpace(req.Msg.GetProjectId()); projectID != "" && run.ProjectID != projectID {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project run %s not found in project %s", runID, projectID))
	}
	return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: RunDetailResponse(run)}), nil
}

func (s *RunService) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	if s.Store == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runs, err := s.Store.ListProjectRunsByOptions(ctx, projectdomain.ProjectRunListOptions{
		ProjectID:   req.Msg.GetProjectId(),
		AgentName:   req.Msg.GetAgentName(),
		SessionID:   req.Msg.GetSessionId(),
		SchedulerID: req.Msg.GetSchedulerId(),
		Status:      ProjectRunStatusFromProto(req.Msg.GetStatus()),
		Source:      ProjectRunSourceFilterFromProto(req.Msg.GetSource()),
		Offset:      int(req.Msg.GetOffset()),
		Limit:       int(req.Msg.GetLimit()),
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*agentcomposev2.RunSummary, 0, len(runs))
	for _, run := range runs {
		items = append(items, RunSummaryResponse(run))
	}
	return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: items}), nil
}

func (s *RunService) StopRun(ctx context.Context, req *connect.Request[agentcomposev2.StopRunRequest]) (*connect.Response[agentcomposev2.StopRunResponse], error) {
	if s.Store == nil || s.NewCanceler == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("config store is required"))
	}
	runID := strings.TrimSpace(req.Msg.GetRunId())
	if runID == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("run id is required"))
	}
	current, err := s.Store.GetProjectRun(ctx, runID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, connect.NewError(connect.CodeNotFound, err)
		}
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if domain.ProjectRunStatusIsTerminal(current.Status) {
		return connect.NewResponse(&agentcomposev2.StopRunResponse{
			Run:           RunDetailResponse(current),
			StopRequested: false,
		}), nil
	}
	reason := strings.TrimSpace(req.Msg.GetReason())
	if reason == "" {
		reason = "stop requested"
	}
	run, err := s.NewCanceler().MarkCanceled(ctx, rundomain.TransitionRequest{
		RunID: runID,
		Error: reason,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&agentcomposev2.StopRunResponse{
		Run:           RunDetailResponse(run),
		StopRequested: true,
	}), nil
}

func (s *RunService) runAgent(ctx context.Context, msg *agentcomposev2.RunAgentRequest, send func(*agentcomposev2.RunAgentStreamResponse) error) (projectdomain.ProjectRunRecord, error, error) {
	if s.RunAgentFn == nil {
		return projectdomain.ProjectRunRecord{}, nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run agent handler is required"))
	}
	return s.RunAgentFn(ctx, msg, send)
}

func (s *RunService) formatNow() string {
	now := time.Now().UTC
	if s != nil && s.Now != nil {
		now = s.Now
	}
	return FormatMaybeTime(now())
}

func RunDetailResponse(run projectdomain.ProjectRunRecord) *agentcomposev2.RunDetail {
	return &agentcomposev2.RunDetail{
		Summary:      RunSummaryResponse(run),
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

func RunSummaryResponse(run projectdomain.ProjectRunRecord) *agentcomposev2.RunSummary {
	return &agentcomposev2.RunSummary{
		RunId:           run.RunID,
		ProjectId:       run.ProjectID,
		ProjectName:     run.ProjectName,
		ProjectRevision: uint64(run.ProjectRevision),
		AgentId:         run.ManagedAgentID,
		AgentName:       run.AgentName,
		Source:          ProjectRunSourceResponse(run.Source),
		SchedulerId:     run.SchedulerID,
		TriggerId:       run.TriggerID,
		Status:          ProjectRunStatusResponse(run.Status),
		SessionId:       run.SessionID,
		ExitCode:        int32(run.ExitCode),
		Error:           run.Error,
		StartedAt:       FormatMaybeTime(run.StartedAt),
		CompletedAt:     FormatMaybeTime(run.CompletedAt),
		DurationMs:      run.DurationMs,
		CreatedAt:       FormatMaybeTime(run.CreatedAt),
		UpdatedAt:       FormatMaybeTime(run.UpdatedAt),
	}
}

func ProjectRunStatusResponse(status string) agentcomposev2.RunStatus {
	switch domain.NormalizeProjectRunStatus(status) {
	case projectdomain.ProjectRunStatusPending:
		return agentcomposev2.RunStatus_RUN_STATUS_PENDING
	case projectdomain.ProjectRunStatusRunning:
		return agentcomposev2.RunStatus_RUN_STATUS_RUNNING
	case projectdomain.ProjectRunStatusSucceeded:
		return agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED
	case projectdomain.ProjectRunStatusFailed:
		return agentcomposev2.RunStatus_RUN_STATUS_FAILED
	case projectdomain.ProjectRunStatusCanceled:
		return agentcomposev2.RunStatus_RUN_STATUS_CANCELED
	default:
		return agentcomposev2.RunStatus_RUN_STATUS_UNSPECIFIED
	}
}

func ProjectRunStatusFromProto(status agentcomposev2.RunStatus) string {
	switch status {
	case agentcomposev2.RunStatus_RUN_STATUS_PENDING:
		return projectdomain.ProjectRunStatusPending
	case agentcomposev2.RunStatus_RUN_STATUS_RUNNING:
		return projectdomain.ProjectRunStatusRunning
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED:
		return projectdomain.ProjectRunStatusSucceeded
	case agentcomposev2.RunStatus_RUN_STATUS_FAILED:
		return projectdomain.ProjectRunStatusFailed
	case agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return projectdomain.ProjectRunStatusCanceled
	default:
		return ""
	}
}

func ProjectRunSourceResponse(source string) agentcomposev2.RunSource {
	switch domain.NormalizeProjectRunSource(source) {
	case projectdomain.ProjectRunSourceScheduler:
		return agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER
	case projectdomain.ProjectRunSourceAPI:
		return agentcomposev2.RunSource_RUN_SOURCE_API
	case projectdomain.ProjectRunSourceManual:
		return agentcomposev2.RunSource_RUN_SOURCE_MANUAL
	default:
		return agentcomposev2.RunSource_RUN_SOURCE_UNSPECIFIED
	}
}

func ProjectRunSourceFromProto(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return projectdomain.ProjectRunSourceScheduler
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return projectdomain.ProjectRunSourceAPI
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return projectdomain.ProjectRunSourceManual
	default:
		return projectdomain.ProjectRunSourceManual
	}
}

func ProjectRunSourceFilterFromProto(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return projectdomain.ProjectRunSourceScheduler
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return projectdomain.ProjectRunSourceAPI
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return projectdomain.ProjectRunSourceManual
	default:
		return ""
	}
}
