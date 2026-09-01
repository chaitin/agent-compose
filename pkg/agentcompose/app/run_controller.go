package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/pkg/agentcompose/adapters"
	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	"github.com/chaitin/agent-compose/pkg/capabilities"
	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/dashboard"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
	"github.com/chaitin/agent-compose/pkg/volumes"
	"github.com/chaitin/agent-compose/pkg/workspaces"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func NewRunController(di do.Injector) (*runs.Controller, error) {
	var dashboardHub runs.DashboardNotifier
	if hub, err := do.Invoke[*dashboard.Hub](di); err == nil {
		dashboardHub = hub
	}
	imageBackends := do.MustInvoke[*adapters.ImageBackends](di)
	runtimeProvider := do.MustInvoke[adapters.RuntimeProvider](di)
	return runs.NewController(runs.ControllerDependencies{
		Config:           do.MustInvoke[*appconfig.Config](di),
		Store:            do.MustInvoke[*sandboxstore.Store](di),
		ConfigDB:         do.MustInvoke[*configstore.ConfigStore](di),
		WorkspaceEnsurer: do.MustInvoke[workspaces.WorkspaceEnsurer](di),
		Driver:           do.MustInvoke[*adapters.SandboxDriver](di),
		Executor:         do.MustInvoke[*adapters.AgentExecutor](di),
		Runtime: func(session *domain.Sandbox) (runs.Runtime, error) {
			return runtimeProvider.ForSession(session)
		},
		Images:          imageBackends.Auto,
		SchedulerEngine: do.MustInvoke[schedulers.SchedulerEngine](di),
		Cap:             do.MustInvoke[capabilities.Provider](di),
		Volumes:         do.MustInvoke[*volumes.Manager](di),
		Streams:         do.MustInvoke[*sandboxes.StreamBroker](di),
		Bus:             do.MustInvoke[*schedulers.Bus](di),
		Dashboard:       dashboardHub,
		CapTokens:       do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
		RunLogs:         do.MustInvoke[*runs.RunLogHub](di),
		LifecycleLocks:  do.MustInvoke[*sandboxes.LifecycleLocks](di),
		Removal:         do.MustInvoke[*sandboxes.RemovalCoordinator](di),
		Completion:      do.MustInvoke[*runs.CompletionManager](di),
	}), nil
}

type runCompletionStopper struct {
	config    *appconfig.Config
	store     *sandboxstore.Store
	driver    *adapters.SandboxDriver
	streams   *sandboxes.StreamBroker
	locks     *sandboxes.LifecycleLocks
	capTokens *adapters.CapabilitySandboxResolver
}

func (s runCompletionStopper) Stop(ctx context.Context, sandbox *domain.Sandbox) error {
	return stopProjectSandbox(ctx, stopProjectSandboxDeps{
		SandboxRoot:   s.config.SandboxRoot,
		Locks:         s.locks,
		Store:         s.store,
		Driver:        s.driver,
		Streams:       s.streams,
		AccessRevoker: s.capTokens,
	}, sandbox)
}

func NewRunCompletionManager(di do.Injector) (*runs.CompletionManager, error) {
	stopper := runCompletionStopper{
		config: do.MustInvoke[*appconfig.Config](di), store: do.MustInvoke[*sandboxstore.Store](di),
		driver: do.MustInvoke[*adapters.SandboxDriver](di), streams: do.MustInvoke[*sandboxes.StreamBroker](di),
		locks: do.MustInvoke[*sandboxes.LifecycleLocks](di), capTokens: do.MustInvoke[*adapters.CapabilitySandboxResolver](di),
	}
	return runs.NewCompletionManager(runs.CompletionManagerDeps{
		Store:     do.MustInvoke[*configstore.ConfigStore](di),
		Sandboxes: do.MustInvoke[*sandboxstore.Store](di),
		Lifecycle: stopper,
		Removal:   do.MustInvoke[*sandboxes.RemovalCoordinator](di),
		Logger:    do.MustInvoke[*slog.Logger](di),
	}), nil
}

type runControllerDelegate struct {
	controller *runs.Controller
	supervisor *RunSupervisor
}

func (d runControllerDelegate) RunProjectCommandAttach(ctx context.Context, receive func() (*agentcomposev2.AttachAgentRunRequest, error), send runs.RunAttachSender) error {
	if d.supervisor == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("run supervisor is required"))
	}
	return runConnectError(d.supervisor.Attach(ctx, receiveRunAttachInput(receive), send))
}

func (d runControllerDelegate) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	if d.supervisor == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run supervisor is required"))
	}
	run, _, err := d.supervisor.Run(ctx, runAgentRequestFromProto(req.Msg), nil)
	if err != nil {
		return nil, runConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.RunAgentResponse{
		Run:      api.ProjectRunDetailToProto(run),
		Warnings: append([]string(nil), run.Warnings...),
	}), nil
}

func (d runControllerDelegate) StartAgentRun(ctx context.Context, req *connect.Request[agentcomposev2.StartAgentRunRequest]) (*connect.Response[agentcomposev2.StartAgentRunResponse], error) {
	if d.supervisor == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run supervisor is required"))
	}
	runRequest := runAgentRequestFromProto(req.Msg.GetRun())
	runRequest.Interactive = req.Msg.GetInteractive()
	run, err := d.supervisor.StartRun(ctx, runRequest)
	if err != nil {
		return nil, runConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.StartAgentRunResponse{
		Run:      api.ProjectRunSummaryToProto(run),
		Warnings: append([]string(nil), run.Warnings...),
		Started:  !runs.StatusIsTerminal(run.Status),
	}), nil
}

func (d runControllerDelegate) StreamAgentRun(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.StreamAgentRunResponse]) error {
	api.PrepareStreamingHeaders(stream.ResponseHeader())
	sink := runs.StreamSink{
		SendStarted: func(run domain.ProjectRunRecord, createdAt time.Time) error {
			return sendRunAgentStreamStarted(stream, run, createdAt)
		},
		SendChunk: func(runID string, chunk domain.ExecChunk, createdAt time.Time) error {
			return sendRunAgentStreamChunk(stream, runID, chunk, createdAt)
		},
	}
	if d.supervisor == nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("run supervisor is required"))
	}
	run, execErr, err := d.supervisor.Run(ctx, runAgentRequestFromProto(req.Msg), &sink)
	if err != nil {
		return runConnectError(err)
	}
	if errors.Is(execErr, runs.ErrRunAgentStreamSend) {
		return connect.NewError(connect.CodeUnknown, execErr)
	}
	if sendErr := stream.Send(runAgentStreamCompletedProjection(run, time.Now().UTC())); sendErr != nil {
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	return nil
}

func sendRunAgentStreamStarted(stream *connect.ServerStream[agentcomposev2.StreamAgentRunResponse], run domain.ProjectRunRecord, createdAt time.Time) error {
	if err := stream.Send(runAgentStreamStartedProjection(run, createdAt)); err != nil {
		return fmt.Errorf("%w: %w", runs.ErrRunAgentStreamSend, err)
	}
	return nil
}

func sendRunAgentStreamChunk(stream *connect.ServerStream[agentcomposev2.StreamAgentRunResponse], runID string, chunk domain.ExecChunk, createdAt time.Time) error {
	if err := stream.Send(runAgentStreamChunkProjection(runID, chunk, createdAt)); err != nil {
		return fmt.Errorf("%w: %w", runs.ErrRunAgentStreamSend, err)
	}
	return nil
}

func runAgentStreamStartedProjection(run domain.ProjectRunRecord, createdAt time.Time) *agentcomposev2.StreamAgentRunResponse {
	return &agentcomposev2.StreamAgentRunResponse{
		EventType: agentcomposev2.StreamAgentRunEventType_STREAM_AGENT_RUN_EVENT_TYPE_STARTED,
		Run:       api.ProjectRunSummaryToProto(run),
		RunId:     run.RunID,
		CreatedAt: api.FormatProjectTime(createdAt),
		Warnings:  append([]string(nil), run.Warnings...),
	}
}

func runAgentStreamChunkProjection(runID string, chunk domain.ExecChunk, createdAt time.Time) *agentcomposev2.StreamAgentRunResponse {
	return &agentcomposev2.StreamAgentRunResponse{
		EventType:  agentcomposev2.StreamAgentRunEventType_STREAM_AGENT_RUN_EVENT_TYPE_OUTPUT,
		RunId:      runID,
		Chunk:      chunk.Text,
		Stream:     api.StdioStreamToProto(chunk.Stream),
		CreatedAt:  api.FormatProjectTime(createdAt),
		Transcript: api.TranscriptEventFromExecChunk(chunk, createdAt),
	}
}

func runAgentStreamCompletedProjection(run domain.ProjectRunRecord, createdAt time.Time) *agentcomposev2.StreamAgentRunResponse {
	return &agentcomposev2.StreamAgentRunResponse{
		EventType: agentcomposev2.StreamAgentRunEventType_STREAM_AGENT_RUN_EVENT_TYPE_COMPLETED,
		Run:       api.ProjectRunSummaryToProto(run),
		RunId:     run.RunID,
		CreatedAt: api.FormatProjectTime(createdAt),
		Warnings:  append([]string(nil), run.Warnings...),
	}
}

func (d runControllerDelegate) AttachAgentRun(ctx context.Context, stream *connect.BidiStream[agentcomposev2.AttachAgentRunRequest, agentcomposev2.AttachAgentRunResponse]) error {
	return d.RunProjectCommandAttach(ctx, stream.Receive, func(output runs.RunAttachOutput) error {
		return stream.Send(api.RunAttachOutputToProto(output))
	})
}

func receiveRunAttachInput(receive func() (*agentcomposev2.AttachAgentRunRequest, error)) runs.RunAttachReceiver {
	return func() (runs.RunAttachInput, error) {
		request, err := receive()
		if err != nil {
			return runs.RunAttachInput{}, err
		}
		return runAttachInputFromProto(request), nil
	}
}

func runAttachInputFromProto(request *agentcomposev2.AttachAgentRunRequest) runs.RunAttachInput {
	input := runs.RunAttachInput{ClientFrameID: request.GetClientFrameId()}
	switch frame := request.GetFrame().(type) {
	case *agentcomposev2.AttachAgentRunRequest_Start:
		start := frame.Start
		input.Kind = runs.RunAttachInputStart
		input.RunID = strings.TrimSpace(start.GetRunId())
		switch start.GetDisconnectPolicy() {
		case agentcomposev2.AttachDisconnectPolicy_ATTACH_DISCONNECT_POLICY_DETACH:
			input.DisconnectPolicy = runs.AttachDisconnectDetach
		default:
			input.DisconnectPolicy = runs.AttachDisconnectCancel
		}
		input.Request = runAgentRequestFromProto(start.GetRequest())
		// AttachAgentRun historically ignored request-scoped volume mounts. Keep
		// that compatibility behavior while other run transports map volumes.
		input.Request.Volumes = nil
		input.AttachStdin = start.GetAttachStdin()
		input.TTY = start.GetTty()
		input.Rows = start.GetTerminalSize().GetRows()
		input.Cols = start.GetTerminalSize().GetCols()
		switch start.GetMode() {
		case agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND:
			input.Mode = runs.RunAttachModeCommand
		case agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT:
			input.Mode = runs.RunAttachModePrompt
		case agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_UNSPECIFIED:
			input.Mode = runs.RunAttachModeUnspecified
		default:
			input.Mode = runs.RunAttachModeInvalid
		}
	case *agentcomposev2.AttachAgentRunRequest_Stdin:
		input.Kind = runs.RunAttachInputStdin
		input.Data = append([]byte(nil), frame.Stdin.GetData()...)
	case *agentcomposev2.AttachAgentRunRequest_StdinEof:
		input.Kind = runs.RunAttachInputStdinEOF
	case *agentcomposev2.AttachAgentRunRequest_Resize:
		input.Kind = runs.RunAttachInputResize
		input.Rows = frame.Resize.GetTerminalSize().GetRows()
		input.Cols = frame.Resize.GetTerminalSize().GetCols()
	case *agentcomposev2.AttachAgentRunRequest_Signal:
		input.Kind = runs.RunAttachInputSignal
		input.Signal = frame.Signal.GetSignal()
	case *agentcomposev2.AttachAgentRunRequest_Cancel:
		input.Kind = runs.RunAttachInputCancel
		input.Reason = frame.Cancel.GetReason()
	case *agentcomposev2.AttachAgentRunRequest_HumanMessage:
		input.Kind = runs.RunAttachInputHumanMessage
		input.Text = frame.HumanMessage.GetText()
	}
	return input
}

func runAgentRequestFromProto(msg *agentcomposev2.RunAgentRequest) runs.RunAgentRequest {
	return runs.RunAgentRequest{
		ProjectID:        msg.GetProjectId(),
		AgentName:        msg.GetAgentName(),
		Prompt:           msg.GetPrompt(),
		Command:          msg.GetCommand(),
		Source:           api.ProjectRunSourceFromProto(msg.GetSource()),
		SchedulerID:      msg.GetSchedulerId(),
		TriggerID:        msg.GetTriggerId(),
		PayloadJSON:      msg.GetPayloadJson(),
		ClientRequestID:  msg.GetClientRequestId(),
		Env:              msg.GetEnv(),
		SandboxID:        msg.GetSandboxId(),
		Driver:           msg.GetDriver(),
		OutputSchemaJSON: msg.GetOutputSchemaJson(),
		CleanupPolicy:    msg.GetCleanupPolicy(),
		Jupyter:          msg.GetJupyter(),
		Volumes:          volumeMountSpecsFromProto(msg.GetVolumes()),
		Labels:           msg.GetLabels(),
	}
}

func volumeMountSpecsFromProto(values []*agentcomposev2.VolumeMountSpec) []domain.VolumeMountSpec {
	out := make([]domain.VolumeMountSpec, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		out = append(out, domain.VolumeMountSpec{
			Type:     api.VolumeMountTypeText(value.GetType()),
			Source:   value.GetSource(),
			Target:   value.GetTarget(),
			ReadOnly: value.GetReadOnly(),
		})
	}
	return out
}

func runConnectError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, runs.ErrInvalidRequest) {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	if errors.Is(err, domain.ErrUnsupported) ||
		errors.Is(err, domain.ErrNotFound) ||
		errors.Is(err, domain.ErrInvalidArgument) ||
		errors.Is(err, domain.ErrRequired) ||
		errors.Is(err, domain.ErrAmbiguous) ||
		errors.Is(err, domain.ErrFailedPrecondition) ||
		errors.Is(err, domain.ErrConflict) ||
		errors.Is(err, domain.ErrReferenced) ||
		errors.Is(err, domain.ErrAlreadyExists) {
		return api.ConnectErrorForDomain(err)
	}
	return connect.NewError(connect.CodeInternal, fmt.Errorf("%w", err))
}
