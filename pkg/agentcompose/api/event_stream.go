package api

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/chaitin/agent-compose/pkg/eventstream"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

const (
	maxWatchProjectIDs = 100
	maxWatchEventTypes = 64
)

// EventStreamHandler serves the opt-in observation stream. The broker owns
// lifecycle and filtering; this handler only validates the request, batches
// frames, and maps domain events to proto.
type EventStreamHandler struct {
	broker *eventstream.Broker
}

func NewEventStreamHandler(broker *eventstream.Broker) *EventStreamHandler {
	return &EventStreamHandler{broker: broker}
}

// watchEventsStream is the small transport surface used by the handler. It is
// satisfied by *connect.ServerStream and by test recorders.
type watchEventsStream interface {
	Send(*agentcomposev2.WatchEventsResponse) error
	ResponseHeader() http.Header
}

type followOptions struct {
	maxBatch       int
	heartbeat      time.Duration
	lastSentCursor *string
}

func (h *EventStreamHandler) GetEventStreamCapabilities(_ context.Context, _ *connect.Request[agentcomposev2.GetEventStreamCapabilitiesRequest]) (*connect.Response[agentcomposev2.GetEventStreamCapabilitiesResponse], error) {
	if h == nil || h.broker == nil {
		return connect.NewResponse(&agentcomposev2.GetEventStreamCapabilitiesResponse{Enabled: false}), nil
	}
	cfg := h.broker.Config()
	return connect.NewResponse(&agentcomposev2.GetEventStreamCapabilitiesResponse{
		Enabled:                h.broker.Enabled(),
		EventTypes:             eventstream.EventTypes(),
		MaxSubscribers:         uint32(cfg.MaxSubscribers),
		SubscriberBufferEvents: uint32(cfg.SubscriberBuffer),
		ReplayEvents:           uint32(cfg.ReplayEvents),
		HeartbeatSeconds:       uint32(cfg.Heartbeat / time.Second),
	}), nil
}

func (h *EventStreamHandler) WatchEvents(ctx context.Context, req *connect.Request[agentcomposev2.WatchEventsRequest], stream *connect.ServerStream[agentcomposev2.WatchEventsResponse]) error {
	if h == nil || h.broker == nil || !h.broker.Enabled() {
		return connect.NewError(connect.CodeFailedPrecondition, eventstream.ErrDisabled)
	}
	filter, err := watchFilter(req.Msg)
	if err != nil {
		return err
	}
	if len(req.Msg.GetProjectIds()) > maxWatchProjectIDs {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d project_ids are allowed", maxWatchProjectIDs))
	}
	sub, resume, err := h.broker.Subscribe(filter, req.Msg.GetCursor())
	if err != nil {
		return watchSubscribeError(err)
	}
	defer sub.Close()
	slog.InfoContext(ctx, "event stream subscription started", "subscribers", h.broker.SubscriberCount(), "projects", len(req.Msg.GetProjectIds()), "event_types", len(req.Msg.GetEventTypes()), "include_logs", req.Msg.GetIncludeLogs())
	defer func() { slog.Info("event stream subscription ended", "subscribers", h.broker.SubscriberCount()) }()

	cfg := h.broker.Config()
	maxBatch := int(req.Msg.GetMaxBatchEvents())
	if maxBatch <= 0 || maxBatch > cfg.MaxBatchEvents {
		maxBatch = cfg.MaxBatchEvents
	}
	PrepareStreamingHeaders(stream.ResponseHeader())
	heartbeatSeconds := uint32(cfg.Heartbeat / time.Second)
	// HELLO must not acknowledge replay events that have not been sent yet. If
	// a replay is pending, echo the client's last consumed cursor; otherwise the
	// head is the safe live position.
	helloCursor := resume.HeadCursor
	if !resume.SyncRequired && len(resume.Replay) > 0 && req.Msg.GetCursor() != "" {
		helloCursor = req.Msg.GetCursor()
	}
	lastSentCursor := helloCursor
	if err := stream.Send(&agentcomposev2.WatchEventsResponse{
		NextCursor: helloCursor,
		Control: &agentcomposev2.StreamControl{
			Kind:             agentcomposev2.StreamControl_KIND_HELLO,
			Epoch:            resume.Epoch,
			Cursor:           helloCursor,
			ReplayFloor:      resume.ReplayFloor,
			HeartbeatSeconds: heartbeatSeconds,
		},
	}); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	if resume.SyncRequired {
		slog.InfoContext(ctx, "event stream cursor requires reconciliation", "reason", resume.Reason)
		if err := stream.Send(&agentcomposev2.WatchEventsResponse{
			NextCursor: resume.HeadCursor,
			Control: &agentcomposev2.StreamControl{
				Kind:        agentcomposev2.StreamControl_KIND_SYNC_REQUIRED,
				Epoch:       resume.Epoch,
				Cursor:      resume.HeadCursor,
				ReplayFloor: resume.ReplayFloor,
				Reason:      resume.Reason,
			},
		}); err != nil {
			return connect.NewError(connect.CodeUnknown, err)
		}
	}
	if len(resume.Replay) > 0 {
		if err := h.sendEventBatches(stream, resume.Replay, maxBatch, &lastSentCursor); err != nil {
			return err
		}
	}
	return h.follow(ctx, sub, stream, followOptions{maxBatch: maxBatch, heartbeat: cfg.Heartbeat, lastSentCursor: &lastSentCursor})
}

func watchFilter(msg *agentcomposev2.WatchEventsRequest) (eventstream.Filter, error) {
	if len(msg.GetEventTypes()) > maxWatchEventTypes {
		return eventstream.Filter{}, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("at most %d event_types are allowed", maxWatchEventTypes))
	}
	severity, err := watchSeverity(msg.GetMinSeverity())
	if err != nil {
		return eventstream.Filter{}, err
	}
	filter, err := eventstream.NewFilter(msg.GetProjectIds(), msg.GetEventTypes(), severity, msg.GetIncludeLogs())
	if err != nil {
		return eventstream.Filter{}, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return filter, nil
}

func watchSeverity(value agentcomposev2.EventSeverity) (eventstream.Severity, error) {
	switch value {
	case agentcomposev2.EventSeverity_EVENT_SEVERITY_UNSPECIFIED, agentcomposev2.EventSeverity_EVENT_SEVERITY_INFO:
		return eventstream.SeverityInfo, nil
	case agentcomposev2.EventSeverity_EVENT_SEVERITY_WARNING:
		return eventstream.SeverityWarning, nil
	case agentcomposev2.EventSeverity_EVENT_SEVERITY_ERROR:
		return eventstream.SeverityError, nil
	default:
		return eventstream.SeverityInfo, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unknown event severity %d", value))
	}
}

func watchSubscribeError(err error) error {
	switch {
	case errors.Is(err, eventstream.ErrDisabled):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, eventstream.ErrClosed):
		return connect.NewError(connect.CodeUnavailable, err)
	case errors.Is(err, eventstream.ErrTooManySubscribers):
		return connect.NewError(connect.CodeResourceExhausted, err)
	case errors.Is(err, eventstream.ErrInvalidCursor):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

func (h *EventStreamHandler) follow(ctx context.Context, sub *eventstream.Subscription, stream watchEventsStream, opts followOptions) error {
	events := sub.Events()
	ticker := time.NewTicker(opts.heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case event, ok := <-events:
			if !ok {
				return nil
			}
			batch := make([]*eventstream.Event, 0, opts.maxBatch)
			batch = append(batch, event)
			closed := false
		drain:
			for len(batch) < opts.maxBatch {
				select {
				case next, ok := <-events:
					if !ok {
						closed = true
						break drain
					}
					batch = append(batch, next)
				default:
					break drain
				}
			}
			if err := h.sendGap(stream, sub); err != nil {
				return err
			}
			if err := h.sendEventBatches(stream, batch, opts.maxBatch, opts.lastSentCursor); err != nil {
				return err
			}
			if closed {
				return nil
			}
		case <-ticker.C:
			if err := h.sendGap(stream, sub); err != nil {
				return err
			}
			if err := h.sendHeartbeat(stream, *opts.lastSentCursor); err != nil {
				return err
			}
		}
	}
}

func (h *EventStreamHandler) sendGap(stream watchEventsStream, sub *eventstream.Subscription) error {
	count, first, last := sub.TakeDropped()
	if count == 0 {
		return nil
	}
	if err := stream.Send(&agentcomposev2.WatchEventsResponse{
		Control: &agentcomposev2.StreamControl{
			Kind:               agentcomposev2.StreamControl_KIND_GAP,
			DroppedEvents:      count,
			FirstDroppedCursor: first,
			LastDroppedCursor:  last,
		},
	}); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	return nil
}

func (h *EventStreamHandler) sendHeartbeat(stream watchEventsStream, cursor string) error {
	if cursor == "" {
		return nil
	}
	heartbeatSeconds := uint32(h.broker.Config().Heartbeat / time.Second)
	if err := stream.Send(&agentcomposev2.WatchEventsResponse{
		NextCursor: cursor,
		Control: &agentcomposev2.StreamControl{
			Kind:             agentcomposev2.StreamControl_KIND_HEARTBEAT,
			Cursor:           cursor,
			HeartbeatSeconds: heartbeatSeconds,
		},
	}); err != nil {
		return connect.NewError(connect.CodeUnknown, err)
	}
	return nil
}

func (h *EventStreamHandler) sendEventBatches(stream watchEventsStream, events []*eventstream.Event, maxBatch int, lastSentCursor *string) error {
	if maxBatch <= 0 {
		maxBatch = 1
	}
	for start := 0; start < len(events); start += maxBatch {
		end := start + maxBatch
		if end > len(events) {
			end = len(events)
		}
		items := make([]*agentcomposev2.StreamEvent, 0, end-start)
		nextCursor := ""
		for _, event := range events[start:end] {
			mapped := streamEventToProto(event)
			if mapped == nil {
				continue
			}
			items = append(items, mapped)
			nextCursor = event.Cursor
		}
		if len(items) == 0 {
			continue
		}
		if err := stream.Send(&agentcomposev2.WatchEventsResponse{Events: items, NextCursor: nextCursor}); err != nil {
			return connect.NewError(connect.CodeUnknown, err)
		}
		if lastSentCursor != nil {
			*lastSentCursor = nextCursor
		}
	}
	return nil
}

func streamEventToProto(event *eventstream.Event) *agentcomposev2.StreamEvent {
	if event == nil {
		return nil
	}
	out := &agentcomposev2.StreamEvent{
		Id:         event.ID,
		Cursor:     event.Cursor,
		Sequence:   event.Sequence,
		OccurredAt: timestamppb.New(event.OccurredAt),
		Type:       event.Type,
		ProjectId:  event.ProjectID,
		Severity:   eventSeverityToProto(event.Severity),
	}
	switch {
	case event.RunStatus != nil:
		out.Payload = &agentcomposev2.StreamEvent_RunStatus{RunStatus: &agentcomposev2.StreamRunStatus{
			Run:            ProjectRunSummaryToProto(event.RunStatus.Run),
			PreviousStatus: ProjectRunStatusToProto(event.RunStatus.PreviousStatus),
			CleanupError:   event.RunStatus.CleanupError,
			ErrorStack:     event.RunStatus.ErrorStack,
		}}
	case event.RunEvent != nil:
		out.Payload = &agentcomposev2.StreamEvent_RunEvent{RunEvent: runEventToProto(event.RunEvent.Event)}
	case event.RunLog != nil:
		out.Payload = &agentcomposev2.StreamEvent_RunLog{RunLog: &agentcomposev2.StreamRunLog{
			RunId:     event.RunLog.RunID,
			ProjectId: event.RunLog.ProjectID,
			SandboxId: event.RunLog.SandboxID,
			Offset:    event.RunLog.Offset,
			Stream:    event.RunLog.Stream,
			Data:      event.RunLog.Data,
			Format:    event.RunLog.Format,
			EntryType: event.RunLog.EntryType,
		}}
	case event.SchedulerRunStatus != nil:
		scheduler := domain.ProjectSchedulerRecord{
			ProjectID:   event.SchedulerRunStatus.ProjectID,
			AgentName:   event.SchedulerRunStatus.AgentName,
			SchedulerID: event.SchedulerRunStatus.Run.SchedulerID,
		}
		out.Payload = &agentcomposev2.StreamEvent_SchedulerRunStatus{SchedulerRunStatus: &agentcomposev2.StreamSchedulerRunStatus{
			Run:            schedulerRunToProto(event.SchedulerRunStatus.Run, scheduler),
			PreviousStatus: schedulerRunStatusToProto(event.SchedulerRunStatus.PreviousStatus),
		}}
	case event.SchedulerUpdate != nil:
		out.Payload = &agentcomposev2.StreamEvent_SchedulerUpdate{SchedulerUpdate: &agentcomposev2.StreamSchedulerUpdate{
			SchedulerId:    event.SchedulerUpdate.SchedulerID,
			ProjectId:      event.SchedulerUpdate.ProjectID,
			AgentName:      event.SchedulerUpdate.AgentName,
			Enabled:        event.SchedulerUpdate.Enabled,
			TriggerId:      event.SchedulerUpdate.TriggerID,
			TriggerEnabled: event.SchedulerUpdate.TriggerEnabled,
			Reason:         event.SchedulerUpdate.Reason,
		}}
	case event.SchedulerEvent != nil:
		scheduler := domain.ProjectSchedulerRecord{
			ProjectID:   event.SchedulerEvent.ProjectID,
			AgentName:   event.SchedulerEvent.AgentName,
			SchedulerID: event.SchedulerEvent.Event.SchedulerID,
		}
		out.Payload = &agentcomposev2.StreamEvent_SchedulerEvent{SchedulerEvent: schedulerEventToProto(event.SchedulerEvent.Event, scheduler)}
	case event.SandboxStatus != nil:
		out.Payload = &agentcomposev2.StreamEvent_SandboxStatus{SandboxStatus: &agentcomposev2.StreamSandboxStatus{
			SandboxId:        event.SandboxStatus.Summary.ID,
			ProjectId:        event.ProjectID,
			Title:            event.SandboxStatus.Summary.Title,
			Driver:           event.SandboxStatus.Summary.Driver,
			VmStatus:         event.SandboxStatus.Summary.VMStatus,
			PreviousVmStatus: event.SandboxStatus.PreviousVMStatus,
			Error:            event.SandboxStatus.Error,
		}}
	case event.SandboxEvent != nil:
		out.Payload = &agentcomposev2.StreamEvent_SandboxEvent{SandboxEvent: &agentcomposev2.StreamSandboxEvent{
			SandboxId: event.SandboxEvent.SandboxID,
			ProjectId: event.SandboxEvent.ProjectID,
			EventId:   event.SandboxEvent.Event.ID,
			Type:      event.SandboxEvent.Event.Type,
			Level:     event.SandboxEvent.Event.Level,
			Message:   event.SandboxEvent.Event.Message,
		}}
	case event.SandboxCellOutput != nil:
		out.Payload = &agentcomposev2.StreamEvent_SandboxCellOutput{SandboxCellOutput: &agentcomposev2.StreamSandboxCellOutput{
			SandboxId: event.SandboxCellOutput.SandboxID,
			ProjectId: event.SandboxCellOutput.ProjectID,
			CellId:    event.SandboxCellOutput.CellID,
			Stream:    event.SandboxCellOutput.Stream,
			Data:      event.SandboxCellOutput.Data,
		}}
	default:
		return nil
	}
	return out
}

func eventSeverityToProto(severity eventstream.Severity) agentcomposev2.EventSeverity {
	switch severity {
	case eventstream.SeverityWarning:
		return agentcomposev2.EventSeverity_EVENT_SEVERITY_WARNING
	case eventstream.SeverityError:
		return agentcomposev2.EventSeverity_EVENT_SEVERITY_ERROR
	default:
		return agentcomposev2.EventSeverity_EVENT_SEVERITY_INFO
	}
}
