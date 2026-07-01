package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/agentcompose/execution"
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
	if s.store == nil || s.configDB == nil || s.runtimes == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec runtime dependencies are required"))
	}
	session, runID, err := s.resolveExecTargetSession(ctx, req)
	if err != nil {
		return nil, err
	}
	command := strings.TrimSpace(req.GetCommand().GetCommand())
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec command is required"))
	}
	if send != nil {
		if err := send(&agentcomposev2.ExecStreamResponse{
			EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_STARTED,
			ExecId:    execID,
			SessionId: session.Summary.ID,
			RunId:     runID,
		}); err != nil {
			return nil, connect.NewError(connect.CodeUnknown, err)
		}
	}
	appconfig.ApplyDefaultGuestPaths(s.config)
	vmState, err := s.store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	runtime, err := s.runtimes.ForSession(session)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	accumulator := execStreamAccumulator{}
	var sendErr error
	writer := func(chunk ExecChunk) {
		if sendErr != nil {
			return
		}
		accumulator.writeChunk(chunk)
		if send != nil {
			sendErr = send(&agentcomposev2.ExecStreamResponse{
				EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT,
				ExecId:    execID,
				SessionId: session.Summary.ID,
				RunId:     runID,
				Chunk:     chunk.Text,
				IsStderr:  chunk.IsStderr,
			})
		}
	}
	cwd := strings.TrimSpace(req.GetCwd())
	if cwd == "" {
		cwd = s.config.GuestWorkspacePath
	}
	execCtx, cancel := execContext(ctx, req.GetTimeoutMs())
	defer cancel()
	result, execErr := runtime.ExecStream(execCtx, session, vmState, ExecSpec{
		Command: command,
		Args:    append([]string(nil), req.GetCommand().GetArgs()...),
		Env:     execEnvMap(req.GetEnv()),
		Cwd:     cwd,
	}, writer)
	if sendErr != nil {
		return nil, connect.NewError(connect.CodeUnknown, sendErr)
	}
	if execErr != nil {
		result = mergeExecResults(result, accumulator.result(firstNonZeroInt(result.ExitCode, 1), false))
		result.ExitCode = firstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = firstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
		return execResultResponse(execID, session.Summary.ID, runID, req, cwd, result, execErr), nil
	}
	result = mergeExecResults(result, accumulator.result(result.ExitCode, result.Success))
	return execResultResponse(execID, session.Summary.ID, runID, req, cwd, result, nil), nil
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
		if errors.Is(err, ErrRequired) || errors.Is(err, ErrAmbiguous) {
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
	return execution.ExecContext(ctx, timeoutMs)
}

func execEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
	return api.ExecEnvMap(items)
}

func execResultResponse(execID, sessionID, runID string, req *agentcomposev2.ExecRequest, cwd string, result ExecResult, execErr error) *agentcomposev2.ExecResult {
	return api.ExecResultToProto(execID, sessionID, runID, req, cwd, result, execErr)
}
