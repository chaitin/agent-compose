package agentcompose

import (
	execdomain "agent-compose/internal/agentcompose/exec"
	execservice "agent-compose/internal/agentcompose/exec/service"
	rundomain "agent-compose/internal/agentcompose/run"
	appconfig "agent-compose/internal/config"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	result, err := s.executeProjectCommand(ctx, req.Msg, uuid.NewString(), nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.ExecResponse{Result: result}), nil
}

func (s *Service) ExecStream(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
	execID := uuid.NewString()
	result, err := s.executeProjectCommand(ctx, req.Msg, execID, func(resp *agentcomposev2.ExecStreamResponse) error {
		return stream.Send(resp)
	})
	if err != nil {
		return err
	}
	return stream.Send(&agentcomposev2.ExecStreamResponse{
		EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
		ExecId:    execID,
		SessionId: result.GetSessionId(),
		RunId:     result.GetRunId(),
		Result:    result,
	})
}

type execStreamSender func(*agentcomposev2.ExecStreamResponse) error

func (s *Service) executeProjectCommand(ctx context.Context, req *agentcomposev2.ExecRequest, execID string, send execStreamSender) (*agentcomposev2.ExecResult, error) {
	executor := execservice.ProjectCommandExecutor{
		Config:        s.config,
		Store:         s.store,
		Runtimes:      s.runtimes,
		ResolveTarget: s.resolveExecTargetSession,
	}
	return executor.Execute(ctx, req, execID, execservice.StreamSender(send))
}

func (s *Service) resolveExecTargetSession(ctx context.Context, req *agentcomposev2.ExecRequest) (*Session, string, error) {
	if req == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec request is required"))
	}
	if sessionID := strings.TrimSpace(req.GetSessionId()); sessionID != "" {
		session, err := s.store.GetSession(ctx, sessionID)
		if err != nil {
			return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s not found: %w", sessionID, err))
		}
		if session.Summary.VMStatus != VMStatusRunning {
			return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s is not running", sessionID))
		}
		return session, "", nil
	}
	if runID := strings.TrimSpace(req.GetRunId()); runID != "" {
		run, err := s.configDB.GetProjectRun(ctx, runID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("run %s not found: %w", runID, err))
			}
			return nil, "", connect.NewError(connect.CodeInternal, err)
		}
		session, err := s.sessionForProjectRun(ctx, run)
		if err != nil {
			return nil, "", err
		}
		return session, run.RunID, nil
	}
	selector := req.GetSelector()
	if selector == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec target is required"))
	}
	project, err := s.resolveProjectRef(ctx, &agentcomposev2.ProjectRef{
		ProjectId: selector.GetProjectId(),
		Name:      selector.GetProjectName(),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", connect.NewError(connect.CodeNotFound, err)
		}
		if strings.Contains(err.Error(), "required") || strings.Contains(err.Error(), "ambiguous") {
			return nil, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	statuses, err := ListProjectSessionStatuses(ctx, s.configDB, s.store, ProjectSessionRelationFilter{
		ProjectID: project.ID,
		AgentName: selector.GetAgentName(),
	})
	if err != nil {
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	type candidate struct {
		session *Session
		run     ProjectRunRecord
	}
	var candidates []candidate
	for _, status := range statuses {
		if status.Session == nil || status.Session.Summary.VMStatus != VMStatusRunning {
			continue
		}
		candidates = append(candidates, candidate{session: status.Session, run: status.Run})
	}
	contextParts := []string{fmt.Sprintf("project %s", project.Name)}
	if agentName := strings.TrimSpace(selector.GetAgentName()); agentName != "" {
		contextParts = append(contextParts, fmt.Sprintf("agent %s", agentName))
	}
	contextText := strings.Join(contextParts, " ")
	if len(candidates) == 0 {
		return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no running session found for %s", contextText))
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, item := range candidates {
			ids = append(ids, item.session.Summary.ID)
		}
		slices.Sort(ids)
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("multiple running sessions found for %s: %s", contextText, strings.Join(ids, ", ")))
	}
	return candidates[0].session, candidates[0].run.RunID, nil
}

func (s *Service) sessionForProjectRun(ctx context.Context, run ProjectRunRecord) (*Session, error) {
	sessionID := strings.TrimSpace(run.SessionID)
	if sessionID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %s has no session", run.RunID))
	}
	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("session %s for run %s not found: %w", sessionID, run.RunID, err))
	}
	if session.Summary.VMStatus != VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("session %s for run %s is not running", sessionID, run.RunID))
	}
	return session, nil
}

func execContext(ctx context.Context, timeoutMs uint32) (context.Context, context.CancelFunc) {
	return execdomain.ContextWithOptionalTimeout(ctx, timeoutMs)
}

func execEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
	return rundomain.ExecEnvMap(items)
}

func execResultResponse(execID, sessionID, runID string, req *agentcomposev2.ExecRequest, cwd string, result ExecResult, execErr error) *agentcomposev2.ExecResult {
	return rundomain.ExecResultResponse(execID, sessionID, runID, req, cwd, result, execErr)
}
