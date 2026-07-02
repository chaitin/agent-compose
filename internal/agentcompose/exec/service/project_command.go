package service

import (
	execdomain "agent-compose/internal/agentcompose/exec"
	rundomain "agent-compose/internal/agentcompose/run"
	sessiondomain "agent-compose/internal/agentcompose/session"
	appconfig "agent-compose/internal/config"
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type Store interface {
	GetVMState(sessionID string) (sessiondomain.VMState, error)
}

type Runtime interface {
	ExecStream(context.Context, *sessiondomain.Session, sessiondomain.VMState, sessiondomain.ExecSpec, sessiondomain.ExecStreamWriter) (sessiondomain.ExecResult, error)
}

type RuntimeProvider interface {
	ForSession(*sessiondomain.Session) (Runtime, error)
}

type TargetResolver func(context.Context, *agentcomposev2.ExecRequest) (*sessiondomain.Session, string, error)

type StreamSender func(*agentcomposev2.ExecStreamResponse) error

type ProjectCommandExecutor struct {
	Config        *appconfig.Config
	Store         Store
	Runtimes      RuntimeProvider
	ResolveTarget TargetResolver
}

func (e ProjectCommandExecutor) Execute(ctx context.Context, req *agentcomposev2.ExecRequest, execID string, send StreamSender) (*agentcomposev2.ExecResult, error) {
	if e.Store == nil || e.Runtimes == nil || e.ResolveTarget == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec runtime dependencies are required"))
	}
	session, runID, err := e.ResolveTarget(ctx, req)
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
	appconfig.ApplyDefaultGuestPaths(e.Config)
	vmState, err := e.Store.GetVMState(session.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	runtime, err := e.Runtimes.ForSession(session)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	accumulator := execdomain.StreamAccumulator{}
	var sendErr error
	writer := func(chunk sessiondomain.ExecChunk) {
		if sendErr != nil {
			return
		}
		accumulator.WriteChunk(chunk)
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
		cwd = e.Config.GuestWorkspacePath
	}
	execCtx, cancel := execdomain.ContextWithOptionalTimeout(ctx, req.GetTimeoutMs())
	defer cancel()
	result, execErr := runtime.ExecStream(execCtx, session, vmState, sessiondomain.ExecSpec{
		Command: command,
		Args:    append([]string(nil), req.GetCommand().GetArgs()...),
		Env:     rundomain.ExecEnvMap(req.GetEnv()),
		Cwd:     cwd,
	}, writer)
	if sendErr != nil {
		return nil, connect.NewError(connect.CodeUnknown, sendErr)
	}
	if execErr != nil {
		result = execdomain.MergeResults(result, accumulator.Result(execdomain.FirstNonZeroInt(result.ExitCode, 1), false))
		result.ExitCode = execdomain.FirstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = execdomain.FirstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
		return rundomain.ExecResultResponse(execID, session.Summary.ID, runID, req, cwd, result, execErr), nil
	}
	result = execdomain.MergeResults(result, accumulator.Result(result.ExitCode, result.Success))
	return rundomain.ExecResultResponse(execID, session.Summary.ID, runID, req, cwd, result, nil), nil
}
