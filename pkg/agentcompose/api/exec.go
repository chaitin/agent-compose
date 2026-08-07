package api

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/types/known/timestamppb"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/sandboxes"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type ExecSandboxStore interface {
	GetSandbox(context.Context, string) (*domain.Sandbox, error)
	GetVMState(string) (domain.VMState, error)
}

type ExecProjectStore interface {
	GetProject(context.Context, string) (domain.ProjectRecord, error)
	GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error)
	ListProjects(context.Context, domain.ProjectListOptions) (domain.ProjectListResult, error)
	ListProjectSandboxRuns(context.Context, domain.ProjectSandboxRelationFilter) ([]domain.ProjectRunRecord, error)
}

type ExecRuntime interface {
	ExecStream(context.Context, *domain.Sandbox, domain.VMState, domain.ExecSpec, domain.ExecStreamWriter) (domain.ExecResult, error)
}

type ExecInteractionRuntime interface {
	OpenInteraction(context.Context, *domain.Sandbox, domain.VMState, driverpkg.RuntimeStartSpec) (driverpkg.RuntimeInteraction, error)
}

type ExecRuntimeResolver func(*domain.Sandbox) (ExecRuntime, error)

type ExecRunAttachDelegate interface {
	RunProjectCommandAttach(context.Context, func() (*agentcomposev2.AttachAgentRunRequest, error), runs.RunAttachSender) error
}

type ExecHandler struct {
	config    *appconfig.Config
	store     ExecSandboxStore
	projects  ExecProjectStore
	runtime   ExecRuntimeResolver
	runAttach ExecRunAttachDelegate
	locks     *sandboxes.LifecycleLocks
}

func (h *ExecHandler) WithLifecycleLocks(locks *sandboxes.LifecycleLocks) *ExecHandler {
	h.locks = locks
	return h
}

func NewExecHandler(config *appconfig.Config, store ExecSandboxStore, projects ExecProjectStore, runtime ExecRuntimeResolver, runAttach ...ExecRunAttachDelegate) *ExecHandler {
	handler := &ExecHandler{
		config:   config,
		store:    store,
		projects: projects,
		runtime:  runtime,
	}
	if len(runAttach) > 0 {
		handler.runAttach = runAttach[0]
	}
	return handler
}

func (h *ExecHandler) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	result, err := h.executeProjectCommand(ctx, req.Msg, uuid.NewString(), nil)
	if err != nil {
		return nil, err
	}
	return connect.NewResponse(&agentcomposev2.ExecResponse{Result: result}), nil
}

func (h *ExecHandler) StreamExec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.StreamExecResponse]) error {
	PrepareStreamingHeaders(stream.ResponseHeader())
	execID := uuid.NewString()
	result, err := h.executeProjectCommand(ctx, req.Msg, execID, func(resp *agentcomposev2.StreamExecResponse) error {
		return stream.Send(resp)
	})
	if err != nil {
		return err
	}
	return stream.Send(&agentcomposev2.StreamExecResponse{
		EventType: agentcomposev2.StreamExecEventType_STREAM_EXEC_EVENT_TYPE_COMPLETED,
		ExecId:    execID,
		SandboxId: result.GetSandboxId(),
		RunId:     result.GetRunId(),
		Result:    result,
	})
}

func (h *ExecHandler) AttachExec(ctx context.Context, stream *connect.BidiStream[agentcomposev2.AttachExecRequest, agentcomposev2.AttachExecResponse]) error {
	return h.execAttach(ctx, stream.Receive, stream.Send)
}

type execStreamSender func(*agentcomposev2.StreamExecResponse) error
type execAttachReceiver func() (*agentcomposev2.AttachExecRequest, error)
type execAttachSender func(*agentcomposev2.AttachExecResponse) error

func (h *ExecHandler) execAttach(ctx context.Context, receive execAttachReceiver, send execAttachSender) error {
	if h.store == nil || h.projects == nil || h.runtime == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("exec runtime dependencies are required"))
	}
	if receive == nil || send == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("exec attach stream is required"))
	}
	first, err := receive()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec attach start frame is required"))
		}
		return connect.NewError(connect.CodeUnknown, err)
	}
	start := first.GetStart()
	if start == nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("first exec attach frame must be start"))
	}
	mode := start.GetMode()
	if mode == agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT || strings.TrimSpace(start.GetPrompt()) != "" {
		return h.execPromptAttach(ctx, start, receive, send)
	}
	state, err := h.prepareExecAttach(ctx, start)
	if err != nil {
		return err
	}
	unlock := h.locks.Lock(state.sandbox.Summary.ID)
	defer unlock()
	state, err = h.prepareExecAttach(ctx, start)
	if err != nil {
		return err
	}
	runner := execAttachRunner{
		state:   state,
		receive: receive,
		send:    send,
	}
	return runner.run(ctx)
}

type execAttachRunner struct {
	state   *execAttachState
	receive execAttachReceiver
	send    execAttachSender
}

func (r execAttachRunner) run(ctx context.Context) error {
	interactionRuntime, ok := r.state.runtime.(ExecInteractionRuntime)
	if !ok {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("exec attach is unsupported by this runtime driver"))
	}
	interaction, err := interactionRuntime.OpenInteraction(ctx, r.state.sandbox, r.state.vmState, r.state.spec)
	if err != nil {
		if errors.Is(err, driverpkg.ErrRuntimeInteractionUnsupported) {
			return connect.NewError(connect.CodeUnimplemented, err)
		}
		return connect.NewError(connect.CodeInternal, err)
	}
	interaction = driverpkg.GuardRuntimeInteractionInput(interaction)
	defer func() { _ = interaction.CloseSend() }()
	return r.runInteraction(interaction)
}

func (r execAttachRunner) runInteraction(interaction driverpkg.RuntimeInteraction) error {
	go pumpExecAttachInput(r.receive, interaction)

	projection := newExecAttachProjection(r.state)
	for {
		frame, err := interaction.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return sendExecAttachError(r.send, "runtime_recv_error", err.Error(), true)
		}
		resp := projection.responseFromRuntimeFrame(frame)
		if resp == nil {
			continue
		}
		if err := r.send(resp); err != nil {
			return connect.NewError(connect.CodeUnknown, err)
		}
	}
}

func (h *ExecHandler) execPromptAttach(ctx context.Context, start *agentcomposev2.AttachExecStart, receive execAttachReceiver, send execAttachSender) error {
	if h.runAttach == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("exec prompt attach is unsupported"))
	}
	req := start.GetRequest()
	if req == nil {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec attach request is required"))
	}
	sandbox, runID, err := h.resolveExecTargetSandbox(ctx, req)
	if err != nil {
		return err
	}
	var run domain.ProjectRunRecord
	if strings.TrimSpace(runID) != "" {
		run, err = h.projects.GetProjectRun(ctx, runID)
		if err != nil {
			return connect.NewError(connect.CodeInternal, err)
		}
	} else {
		run, err = h.latestSandboxRun(ctx, sandbox.Summary.ID)
		if err != nil {
			return err
		}
	}
	projectID := strings.TrimSpace(run.ProjectID)
	agentName := strings.TrimSpace(run.AgentName)
	if projectID == "" || agentName == "" {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec prompt target must be associated with a project agent"))
	}
	initial := &agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: &agentcomposev2.AttachAgentRunStart{
			Request: &agentcomposev2.RunAgentRequest{
				ProjectId:     projectID,
				AgentName:     agentName,
				Prompt:        strings.TrimSpace(start.GetPrompt()),
				SandboxId:     sandbox.Summary.ID,
				Source:        agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
				CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING,
			},
			Mode:        agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
			AttachStdin: start.GetAttachStdin(),
			Tty:         false,
		}},
	}
	receiver := newExecPromptRunAttachReceiver(initial, receive)
	return h.runAttach.RunProjectCommandAttach(ctx, receiver.Receive, func(output runs.RunAttachOutput) error {
		return send(execAttachResponseFromRunAttach(RunAttachOutputToProto(output)))
	})
}

type execPromptRunAttachReceiver struct {
	initial   *agentcomposev2.AttachAgentRunRequest
	receive   execAttachReceiver
	sentStart bool
}

func newExecPromptRunAttachReceiver(initial *agentcomposev2.AttachAgentRunRequest, receive execAttachReceiver) *execPromptRunAttachReceiver {
	return &execPromptRunAttachReceiver{initial: initial, receive: receive}
}

func (r *execPromptRunAttachReceiver) Receive() (*agentcomposev2.AttachAgentRunRequest, error) {
	if !r.sentStart {
		r.sentStart = true
		return r.initial, nil
	}
	req, err := r.receive()
	if err != nil {
		return nil, err
	}
	switch frame := req.GetFrame().(type) {
	case *agentcomposev2.AttachExecRequest_StdinEof:
		return &agentcomposev2.AttachAgentRunRequest{ClientFrameId: req.GetClientFrameId(), Frame: &agentcomposev2.AttachAgentRunRequest_StdinEof{StdinEof: frame.StdinEof}}, nil
	case *agentcomposev2.AttachExecRequest_Resize:
		return &agentcomposev2.AttachAgentRunRequest{ClientFrameId: req.GetClientFrameId(), Frame: &agentcomposev2.AttachAgentRunRequest_Resize{Resize: frame.Resize}}, nil
	case *agentcomposev2.AttachExecRequest_Cancel:
		return &agentcomposev2.AttachAgentRunRequest{ClientFrameId: req.GetClientFrameId(), Frame: &agentcomposev2.AttachAgentRunRequest_Cancel{Cancel: frame.Cancel}}, nil
	case *agentcomposev2.AttachExecRequest_HumanMessage:
		return &agentcomposev2.AttachAgentRunRequest{ClientFrameId: req.GetClientFrameId(), Frame: &agentcomposev2.AttachAgentRunRequest_HumanMessage{HumanMessage: frame.HumanMessage}}, nil
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid exec prompt attach frame"))
	}
}

func execAttachResponseFromRunAttach(resp *agentcomposev2.AttachAgentRunResponse) *agentcomposev2.AttachExecResponse {
	out := &agentcomposev2.AttachExecResponse{
		ServerFrameId: resp.GetServerFrameId(),
		CreatedAt:     resp.GetCreatedAt(),
	}
	switch frame := resp.GetFrame().(type) {
	case *agentcomposev2.AttachAgentRunResponse_Started:
		out.Frame = &agentcomposev2.AttachExecResponse_Started{Started: frame.Started}
	case *agentcomposev2.AttachAgentRunResponse_Output:
		out.Frame = &agentcomposev2.AttachExecResponse_Output{Output: frame.Output}
	case *agentcomposev2.AttachAgentRunResponse_Result:
		out.Frame = &agentcomposev2.AttachExecResponse_Result{Result: frame.Result}
	case *agentcomposev2.AttachAgentRunResponse_Error:
		out.Frame = &agentcomposev2.AttachExecResponse_Error{Error: frame.Error}
	case *agentcomposev2.AttachAgentRunResponse_AgentEvent:
		out.Frame = &agentcomposev2.AttachExecResponse_AgentEvent{AgentEvent: frame.AgentEvent}
	case *agentcomposev2.AttachAgentRunResponse_AgentTurnCompleted:
		out.Frame = &agentcomposev2.AttachExecResponse_AgentTurnCompleted{AgentTurnCompleted: frame.AgentTurnCompleted}
	}
	return out
}

func (h *ExecHandler) latestSandboxRun(ctx context.Context, sandboxID string) (domain.ProjectRunRecord, error) {
	runsForSandbox, err := h.projects.ListProjectSandboxRuns(ctx, domain.ProjectSandboxRelationFilter{SandboxID: sandboxID, Limit: 1})
	if err != nil {
		return domain.ProjectRunRecord{}, connect.NewError(connect.CodeInternal, err)
	}
	if len(runsForSandbox) == 0 {
		return domain.ProjectRunRecord{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec prompt target must be associated with a project run"))
	}
	return runsForSandbox[0], nil
}

type execAttachState struct {
	execID  string
	runID   string
	cwd     string
	request *agentcomposev2.ExecRequest
	sandbox *domain.Sandbox
	vmState domain.VMState
	runtime ExecRuntime
	spec    driverpkg.RuntimeStartSpec
	tty     bool
}

func (h *ExecHandler) prepareExecAttach(ctx context.Context, start *agentcomposev2.AttachExecStart) (*execAttachState, error) {
	req := start.GetRequest()
	if req == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec attach request is required"))
	}
	sandbox, runID, err := h.resolveExecTargetSandbox(ctx, req)
	if err != nil {
		return nil, err
	}
	command := strings.TrimSpace(req.GetCommand().GetCommand())
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec command is required"))
	}
	appconfig.ApplyDefaultGuestPaths(h.config)
	cwd := strings.TrimSpace(req.GetCwd())
	if cwd == "" {
		cwd = h.config.GuestWorkspacePath
	}
	vmState, err := h.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	runtime, err := h.runtime(sandbox)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	env := ExecEnvMap(req.GetEnv())
	execID := uuid.NewString()
	size := start.GetTerminalSize()
	spec := driverpkg.RuntimeStartSpec{
		OperationID: execID,
		Kind:        driverpkg.RuntimeOperationCommand,
		Origin:      "exec_attach",
		Command: &driverpkg.RuntimeCommandSpec{
			Command: command,
			Args:    append([]string(nil), req.GetCommand().GetArgs()...),
			Env:     env,
			Cwd:     cwd,
		},
		Cwd:         cwd,
		Env:         env,
		AttachStdin: start.GetAttachStdin(),
		TTY:         start.GetTty(),
		Rows:        size.GetRows(),
		Cols:        size.GetCols(),
		TimeoutMs:   int64(req.GetTimeoutMs()),
	}
	return &execAttachState{
		execID:  execID,
		runID:   runID,
		cwd:     cwd,
		request: req,
		sandbox: sandbox,
		vmState: vmState,
		runtime: runtime,
		spec:    spec,
		tty:     start.GetTty(),
	}, nil
}

func pumpExecAttachInput(receive execAttachReceiver, interaction driverpkg.RuntimeInteraction) {
	defer func() { _ = interaction.CloseSend() }()
	for {
		req, err := receive()
		if err != nil {
			return
		}
		frame, ok := runtimeInputFrameFromExecAttach(req)
		if !ok {
			_ = interaction.Send(driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputCancel, Message: "invalid exec attach frame"})
			return
		}
		if err := interaction.Send(frame); err != nil {
			return
		}
	}
}

func runtimeInputFrameFromExecAttach(req *agentcomposev2.AttachExecRequest) (driverpkg.RuntimeInputFrame, bool) {
	switch frame := req.GetFrame().(type) {
	case *agentcomposev2.AttachExecRequest_Stdin:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdin, Data: frame.Stdin.GetData()}, true
	case *agentcomposev2.AttachExecRequest_StdinEof:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputStdinEOF}, true
	case *agentcomposev2.AttachExecRequest_Resize:
		size := frame.Resize.GetTerminalSize()
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputResize, Rows: size.GetRows(), Cols: size.GetCols()}, true
	case *agentcomposev2.AttachExecRequest_Signal:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputSignal, Signal: driverpkg.RuntimeSignal(strings.TrimSpace(frame.Signal.GetSignal()))}, true
	case *agentcomposev2.AttachExecRequest_Cancel:
		return driverpkg.RuntimeInputFrame{Type: driverpkg.RuntimeInputCancel, Message: frame.Cancel.GetReason()}, true
	default:
		return driverpkg.RuntimeInputFrame{}, false
	}
}

type execAttachProjection struct {
	state       *execAttachState
	accumulator execution.ExecStreamAccumulator
}

func newExecAttachProjection(state *execAttachState) *execAttachProjection {
	return &execAttachProjection{state: state}
}

func (p *execAttachProjection) responseFromRuntimeFrame(frame driverpkg.RuntimeOutputFrame) *agentcomposev2.AttachExecResponse {
	return p.state.responseFromRuntimeFrame(frame, &p.accumulator)
}

func (s *execAttachState) responseFromRuntimeFrame(frame driverpkg.RuntimeOutputFrame, accumulator *execution.ExecStreamAccumulator) *agentcomposev2.AttachExecResponse {
	resp := newAttachExecResponse()
	switch frame.Type {
	case driverpkg.RuntimeOutputStarted:
		resp.Frame = &agentcomposev2.AttachExecResponse_Started{Started: &agentcomposev2.AttachStarted{
			OperationId: s.execID,
			ExecId:      s.execID,
			RunId:       s.runID,
			SandboxId:   s.sandbox.Summary.ID,
		}}
	case driverpkg.RuntimeOutputStdout, driverpkg.RuntimeOutputStderr:
		stream := driverOutputStreamToProto(frame.Type)
		if accumulator != nil {
			accumulator.WriteChunk(domain.ExecChunk{Text: string(frame.Data), Stream: protoStreamToDomain(stream)})
		}
		resp.Frame = &agentcomposev2.AttachExecResponse_Output{Output: &agentcomposev2.AttachOutput{
			Data:   append([]byte(nil), frame.Data...),
			Stream: stream,
			Tty:    s.tty,
			Transcript: &agentcomposev2.TranscriptEvent{
				Stream:    stream,
				Text:      string(frame.Data),
				CreatedAt: resp.CreatedAt,
			},
		}}
	case driverpkg.RuntimeOutputResult:
		result := frame.Result
		if result == nil {
			result = &driverpkg.RuntimeResult{OperationID: s.execID}
		}
		accumulated := domain.ExecResult{}
		if accumulator != nil {
			accumulated = accumulator.Result(result.ExitCode, result.Success)
		}
		if strings.TrimSpace(result.Error) != "" {
			accumulated.Success = false
		}
		execResult := ExecResultToProto(s.execID, s.sandbox.Summary.ID, s.runID, s.request, s.cwd, accumulated, errorFromString(result.Error))
		resp.Frame = &agentcomposev2.AttachExecResponse_Result{Result: &agentcomposev2.AttachResult{
			ExitCode:   int32(result.ExitCode),
			Success:    result.Success,
			ExecResult: execResult,
			Output:     accumulated.Output,
			Error:      result.Error,
		}}
	case driverpkg.RuntimeOutputError:
		code := "runtime_error"
		message := "runtime interaction failed"
		if frame.Error != nil {
			code = firstNonEmpty(frame.Error.Code, code)
			message = firstNonEmpty(frame.Error.Message, message)
		}
		resp.Frame = &agentcomposev2.AttachExecResponse_Error{Error: &agentcomposev2.AttachError{Code: code, Message: message, Terminal: true}}
	default:
		return nil
	}
	return resp
}

func newAttachExecResponse() *agentcomposev2.AttachExecResponse {
	return &agentcomposev2.AttachExecResponse{
		ServerFrameId: uuid.NewString(),
		CreatedAt:     timestamppb.Now(),
	}
}

func driverOutputStreamToProto(frameType driverpkg.RuntimeOutputFrameType) agentcomposev2.StdioStream {
	if frameType == driverpkg.RuntimeOutputStderr {
		return agentcomposev2.StdioStream_STDIO_STREAM_STDERR
	}
	return agentcomposev2.StdioStream_STDIO_STREAM_STDOUT
}

func protoStreamToDomain(stream agentcomposev2.StdioStream) domain.StdioStream {
	if stream == agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		return domain.StdioStderr
	}
	return domain.StdioStdout
}

func sendExecAttachError(send execAttachSender, code, message string, terminal bool) error {
	resp := newAttachExecResponse()
	resp.Frame = &agentcomposev2.AttachExecResponse_Error{Error: &agentcomposev2.AttachError{
		Code:     code,
		Message:  message,
		Terminal: terminal,
	}}
	if err := send(resp); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	return nil
}

func errorFromString(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return errors.New(text)
}

func (h *ExecHandler) executeProjectCommand(ctx context.Context, req *agentcomposev2.ExecRequest, execID string, send execStreamSender) (*agentcomposev2.ExecResult, error) {
	if h.store == nil || h.projects == nil || h.runtime == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("exec runtime dependencies are required"))
	}
	sandbox, _, err := h.resolveExecTargetSandbox(ctx, req)
	if err != nil {
		return nil, err
	}
	unlock := h.locks.Lock(sandbox.Summary.ID)
	defer unlock()
	sandbox, runID, err := h.resolveExecTargetSandbox(ctx, req)
	if err != nil {
		return nil, err
	}
	command := strings.TrimSpace(req.GetCommand().GetCommand())
	if command == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec command is required"))
	}
	if send != nil {
		if err := send(&agentcomposev2.StreamExecResponse{
			EventType: agentcomposev2.StreamExecEventType_STREAM_EXEC_EVENT_TYPE_STARTED,
			ExecId:    execID,
			SandboxId: sandbox.Summary.ID,
			RunId:     runID,
		}); err != nil {
			return nil, connect.NewError(connect.CodeUnknown, err)
		}
	}
	appconfig.ApplyDefaultGuestPaths(h.config)
	cwd := strings.TrimSpace(req.GetCwd())
	if cwd == "" {
		cwd = h.config.GuestWorkspacePath
	}
	vmState, err := h.store.GetVMState(sandbox.Summary.ID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	runtime, err := h.runtime(sandbox)
	if err != nil {
		return nil, ConnectErrorForDomain(err)
	}
	hostExecDir := filepath.Join(execution.HostSandboxDir(sandbox), "state", "exec", execID)
	if err := os.MkdirAll(hostExecDir, 0o755); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("create exec artifact dir: %w", err))
	}
	guestExecDir := filepath.Join(h.config.GuestStateRoot, "exec", execID)
	runtimeRequest := execution.RuntimeCommandRequestPayloadFromCommand(
		h.config,
		"exec",
		command,
		req.GetCommand().GetArgs(),
		"",
		cwd,
		ExecEnvMap(req.GetEnv()),
		int64(req.GetTimeoutMs()),
		int64(req.GetMaxOutputBytes()),
		guestExecDir,
	)
	if err := execution.WriteJSONArtifact(filepath.Join(hostExecDir, "command-request.json"), runtimeRequest); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("write exec command request artifact: %w", err))
	}
	transcriptPath := filepath.Join(hostExecDir, "transcript.txt")
	var sendErr error
	writer := func(chunk domain.ExecChunk) {
		if sendErr != nil {
			return
		}
		filtered, visible := execution.FilterCommandStreamChunk(chunk)
		if !visible {
			return
		}
		if err := appendExecTranscriptChunk(transcriptPath, filtered); err != nil {
			sendErr = err
			return
		}
		if send != nil {
			createdAt := time.Now().UTC()
			sendErr = send(&agentcomposev2.StreamExecResponse{
				EventType:  agentcomposev2.StreamExecEventType_STREAM_EXEC_EVENT_TYPE_OUTPUT,
				ExecId:     execID,
				SandboxId:  sandbox.Summary.ID,
				RunId:      runID,
				Chunk:      filtered.Text,
				Stream:     StdioStreamToProto(filtered.Stream),
				Transcript: TranscriptEventFromExecChunk(filtered, createdAt),
			})
		}
	}
	execCtx, cancel := execution.ExecContext(ctx, req.GetTimeoutMs())
	defer cancel()
	result, execErr := runtime.ExecStream(execCtx, sandbox, vmState, execution.BuildRuntimeCommandExecSpec(h.config, sandbox, filepath.Join(guestExecDir, "command-request.json"), h.config.GuestHomePath), writer)
	if sendErr != nil {
		return nil, connect.NewError(connect.CodeUnknown, sendErr)
	}
	if execErr != nil {
		result.ExitCode = execution.FirstNonZeroInt(result.ExitCode, 1)
		result.Success = false
		if strings.TrimSpace(result.Output) == "" {
			result.Output = firstNonEmpty(result.Stderr, result.Stdout, execErr.Error())
		}
		return ExecResultToProto(execID, sandbox.Summary.ID, runID, req, cwd, result, execErr), nil
	}
	commandResult, err := execution.ParseCommandExecResult(result)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if err := execution.MirrorRuntimeCommandArtifacts(hostExecDir, commandResult); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return ExecResultToProto(execID, sandbox.Summary.ID, runID, req, cwd, execution.RuntimeCommandResultToExecResult(commandResult), nil), nil
}

func (h *ExecHandler) resolveExecTargetSandbox(ctx context.Context, req *agentcomposev2.ExecRequest) (*domain.Sandbox, string, error) {
	if req == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec request is required"))
	}
	if sandboxID := strings.TrimSpace(req.GetSandboxId()); sandboxID != "" {
		sandbox, err := h.store.GetSandbox(ctx, sandboxID)
		if err != nil {
			return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("sandbox %s not found: %w", sandboxID, err))
		}
		if sandbox.Summary.VMStatus != domain.VMStatusRunning {
			return nil, "", connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is not running", sandboxID))
		}
		return sandbox, "", nil
	}
	if runID := strings.TrimSpace(req.GetRunId()); runID != "" {
		run, err := h.projects.GetProjectRun(ctx, runID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("run %s not found: %w", runID, err))
			}
			return nil, "", connect.NewError(connect.CodeInternal, err)
		}
		sandbox, err := h.sandboxForProjectRun(ctx, run)
		if err != nil {
			return nil, "", err
		}
		return sandbox, run.RunID, nil
	}
	selector := req.GetSelector()
	if selector == nil {
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("exec target is required"))
	}
	projectRef := &agentcomposev2.ProjectRef{}
	switch project := selector.GetProject().(type) {
	case *agentcomposev2.ExecSandboxSelector_ProjectId:
		projectRef.Selector = &agentcomposev2.ProjectRef_ProjectId{ProjectId: strings.TrimSpace(project.ProjectId)}
	case *agentcomposev2.ExecSandboxSelector_ProjectName:
		projectRef.Selector = &agentcomposev2.ProjectRef_Name{Name: strings.TrimSpace(project.ProjectName)}
	}
	project, err := h.resolveProjectRef(ctx, projectRef)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, "", connect.NewError(connect.CodeNotFound, err)
		}
		if errors.Is(err, domain.ErrRequired) || errors.Is(err, domain.ErrAmbiguous) {
			return nil, "", connect.NewError(connect.CodeInvalidArgument, err)
		}
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	statuses, err := runs.ListProjectSandboxStatuses(ctx, h.projects, h.store, domain.ProjectSandboxRelationFilter{
		ProjectID: project.ID,
		AgentName: selector.GetAgentName(),
	})
	if err != nil {
		return nil, "", connect.NewError(connect.CodeInternal, err)
	}
	type candidate struct {
		sandbox *domain.Sandbox
		run     domain.ProjectRunRecord
	}
	var candidates []candidate
	for _, status := range statuses {
		if status.Sandbox == nil || status.Sandbox.Summary.VMStatus != domain.VMStatusRunning {
			continue
		}
		candidates = append(candidates, candidate{sandbox: status.Sandbox, run: status.Run})
	}
	contextParts := []string{fmt.Sprintf("project %s", project.Name)}
	if agentName := strings.TrimSpace(selector.GetAgentName()); agentName != "" {
		contextParts = append(contextParts, fmt.Sprintf("agent %s", agentName))
	}
	contextText := strings.Join(contextParts, " ")
	if len(candidates) == 0 {
		return nil, "", connect.NewError(connect.CodeNotFound, fmt.Errorf("no running sandbox found for %s", contextText))
	}
	if len(candidates) > 1 {
		ids := make([]string, 0, len(candidates))
		for _, item := range candidates {
			ids = append(ids, item.sandbox.Summary.ID)
		}
		slices.Sort(ids)
		return nil, "", connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("multiple runningsandboxs found for %s: %s", contextText, strings.Join(ids, ", ")))
	}
	return candidates[0].sandbox, candidates[0].run.RunID, nil
}

func (h *ExecHandler) sandboxForProjectRun(ctx context.Context, run domain.ProjectRunRecord) (*domain.Sandbox, error) {
	sandboxID := strings.TrimSpace(run.SandboxID)
	if sandboxID == "" {
		sandboxID = strings.TrimSpace(run.SandboxID)
	}
	if sandboxID == "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("run %s has no sandbox", run.RunID))
	}
	sandbox, err := h.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("sandbox %s for run %s not found: %w", sandboxID, run.RunID, err))
	}
	if sandbox.Summary.VMStatus != domain.VMStatusRunning {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s for run %s is not running", sandboxID, run.RunID))
	}
	return sandbox, nil
}

func appendExecTranscriptChunk(path string, chunk domain.ExecChunk) error {
	path = strings.TrimSpace(path)
	if path == "" || chunk.Text == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create exec transcript dir: %w", err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open exec transcript %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	if _, err := file.WriteString(chunk.Text); err != nil {
		return fmt.Errorf("append exec transcript %s: %w", path, err)
	}
	return nil
}

func (h *ExecHandler) resolveProjectRef(ctx context.Context, ref *agentcomposev2.ProjectRef) (domain.ProjectRecord, error) {
	return resolveProjectReference(ctx, h.projects, ref)
}

func ExecEnvMap(items []*agentcomposev2.EnvVarSpec) map[string]string {
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

func ExecResultToProto(execID, sandboxID, runID string, req *agentcomposev2.ExecRequest, cwd string, result domain.ExecResult, execErr error) *agentcomposev2.ExecResult {
	errorText := ""
	if execErr != nil {
		errorText = execErr.Error()
	}
	return &agentcomposev2.ExecResult{
		ExecId:    execID,
		SandboxId: sandboxID,
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
