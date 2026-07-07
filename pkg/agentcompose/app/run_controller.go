package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	"agent-compose/pkg/agentcompose/adapters"
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/capabilities"
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/dashboard"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/runs"
	"agent-compose/pkg/sessions"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sessionstore"
	"agent-compose/pkg/volumes"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func NewRunController(di do.Injector) (*runs.Controller, error) {
	var dashboardHub runs.DashboardNotifier
	if hub, err := do.Invoke[*dashboard.Hub](di); err == nil {
		dashboardHub = hub
	}
	imageBackends := do.MustInvoke[*adapters.ImageBackends](di)
	runtimeProvider := do.MustInvoke[adapters.RuntimeProvider](di)
	return runs.NewController(runs.ControllerDependencies{
		Config:   do.MustInvoke[*appconfig.Config](di),
		Store:    do.MustInvoke[*sessionstore.Store](di),
		ConfigDB: do.MustInvoke[*configstore.ConfigStore](di),
		Driver:   do.MustInvoke[*adapters.SessionDriver](di),
		Executor: do.MustInvoke[*adapters.AgentExecutor](di),
		Runtime: func(session *domain.Session) (runs.Runtime, error) {
			return runtimeProvider.ForSession(session)
		},
		Images:       imageBackends.Auto,
		LoaderEngine: do.MustInvoke[loaders.LoaderEngine](di),
		Cap:          do.MustInvoke[capabilities.Provider](di),
		Volumes:      do.MustInvoke[*volumes.Manager](di),
		Streams:      do.MustInvoke[*sessions.StreamBroker](di),
		Bus:          do.MustInvoke[*loaders.Bus](di),
		Dashboard:    dashboardHub,
	}), nil
}

type runControllerDelegate struct {
	controller *runs.Controller
	supervisor *RunSupervisor
}

func (d runControllerDelegate) RunAgent(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest]) (*connect.Response[agentcomposev2.RunAgentResponse], error) {
	run, _, err := d.controller.RunProjectAgent(ctx, runAgentRequestFromProto(req.Msg), nil)
	if err != nil {
		return nil, runConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.RunAgentResponse{
		Run:      api.ProjectRunDetailToProto(run),
		Warnings: append([]string(nil), run.Warnings...),
	}), nil
}

func (d runControllerDelegate) StartRun(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
	if d.supervisor == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("run supervisor is required"))
	}
	run, err := d.supervisor.StartRun(ctx, runAgentRequestFromProto(req.Msg.GetRun()))
	if err != nil {
		return nil, runConnectError(err)
	}
	return connect.NewResponse(&agentcomposev2.StartRunResponse{
		Run:      api.ProjectRunSummaryToProto(run),
		Warnings: append([]string(nil), run.Warnings...),
		Started:  !runs.StatusIsTerminal(run.Status),
	}), nil
}

func (d runControllerDelegate) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	runs.PrepareStreamingHeaders(stream.ResponseHeader())
	sink := runs.StreamSink{
		SendStarted: func(run domain.ProjectRunRecord, createdAt time.Time) error {
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_STARTED,
				Run:       api.ProjectRunSummaryToProto(run),
				RunId:     run.RunID,
				CreatedAt: api.FormatProjectTime(createdAt),
				Warnings:  append([]string(nil), run.Warnings...),
			}); err != nil {
				return fmt.Errorf("%w: %w", runs.ErrRunAgentStreamSend, err)
			}
			return nil
		},
		SendChunk: func(runID string, chunk domain.ExecChunk, createdAt time.Time) error {
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:      runID,
				Chunk:      chunk.Text,
				Stream:     api.StdioStreamToProto(chunk.Stream),
				CreatedAt:  api.FormatProjectTime(createdAt),
				Transcript: api.TranscriptEventFromExecChunk(chunk, createdAt),
			}); err != nil {
				return fmt.Errorf("%w: %w", runs.ErrRunAgentStreamSend, err)
			}
			return nil
		},
	}
	run, execErr, err := d.controller.RunProjectAgent(ctx, runAgentRequestFromProto(req.Msg), &sink)
	if err != nil {
		return runConnectError(err)
	}
	if errors.Is(execErr, runs.ErrRunAgentStreamSend) {
		return connect.NewError(connect.CodeUnknown, execErr)
	}
	if sendErr := stream.Send(&agentcomposev2.RunAgentStreamResponse{
		EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
		Run:       api.ProjectRunSummaryToProto(run),
		RunId:     run.RunID,
		CreatedAt: api.FormatProjectTime(time.Now().UTC()),
		Warnings:  append([]string(nil), run.Warnings...),
	}); sendErr != nil {
		return connect.NewError(connect.CodeUnknown, sendErr)
	}
	return nil
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
		ClientRequestID:  msg.GetClientRequestId(),
		Env:              msg.GetEnv(),
		SessionID:        msg.GetSessionId(),
		Driver:           msg.GetDriver(),
		OutputSchemaJSON: msg.GetOutputSchemaJson(),
		CleanupPolicy:    msg.GetCleanupPolicy(),
		Jupyter:          msg.GetJupyter(),
		Volumes:          volumeMountSpecsFromProto(msg.GetVolumes()),
	}
}

func volumeMountSpecsFromProto(values []*agentcomposev2.VolumeMountSpec) []domain.VolumeMountSpec {
	out := make([]domain.VolumeMountSpec, 0, len(values))
	for _, value := range values {
		if value == nil {
			continue
		}
		out = append(out, domain.VolumeMountSpec{
			Type:     value.GetType(),
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
