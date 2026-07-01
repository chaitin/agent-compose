package agentcompose

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"

	executorpkg "agent-compose/pkg/executor"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func (s *Service) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	return s.execService().Exec(ctx, req)
}

func (s *Service) ExecStream(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
	return s.execService().ExecStream(ctx, req, stream)
}

func (s *Service) execService() *executorpkg.Service {
	return executorpkg.NewService(s.config, s.store, s.runtimes, s.resolveExecTargetSession)
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
