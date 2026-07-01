package executor

import (
	"context"
	"fmt"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	appconfig "agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type TargetResolver func(context.Context, *agentcomposev2.ExecRequest) (*Session, string, error)

type Service struct {
	config        *appconfig.Config
	store         *Store
	runtimes      RuntimeProvider
	resolveTarget TargetResolver
}

func NewService(config *appconfig.Config, store *Store, runtimes RuntimeProvider, resolveTarget TargetResolver) *Service {
	return &Service{config: config, store: store, runtimes: runtimes, resolveTarget: resolveTarget}
}

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
	if s.store == nil || s.runtimes == nil || s.resolveTarget == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec runtime dependencies are required"))
	}
	session, runID, err := s.resolveTarget(ctx, req)
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

func execContext(ctx context.Context, timeoutMs uint32) (context.Context, context.CancelFunc) {
	if timeoutMs == 0 {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, time.Duration(timeoutMs)*time.Millisecond)
}

func execEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
	if len(items) == 0 {
		return nil
	}
	result := make(map[string]string, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			continue
		}
		result[name] = item.GetValue()
	}
	return result
}

func execResultResponse(execID, sessionID, runID string, req *agentcomposev2.ExecRequest, cwd string, result ExecResult, execErr error) *agentcomposev2.ExecResult {
	errorText := ""
	if execErr != nil {
		errorText = execErr.Error()
	}
	return &agentcomposev2.ExecResult{
		ExecId:    execID,
		SessionId: sessionID,
		RunId:     runID,
		Command: &agentcomposev2.ExecCommand{
			Command: req.GetCommand().GetCommand(),
			Args:    append([]string(nil), req.GetCommand().GetArgs()...),
		},
		Cwd:      cwd,
		ExitCode: int32(result.ExitCode),
		Success:  result.Success,
		Stdout:   result.Stdout,
		Stderr:   result.Stderr,
		Output:   result.Output,
		Error:    errorText,
	}
}
