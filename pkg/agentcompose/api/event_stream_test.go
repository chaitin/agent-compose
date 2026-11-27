package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/chaitin/agent-compose/pkg/eventstream"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func eventStreamTestConfig() eventstream.Config {
	cfg := eventstream.DefaultConfig()
	cfg.Enabled = true
	cfg.Heartbeat = time.Hour
	return cfg
}

func newEventStreamTestClient(t *testing.T, handler *EventStreamHandler) (agentcomposev2connect.EventServiceClient, func()) {
	t.Helper()
	mux := http.NewServeMux()
	path, serviceHandler := agentcomposev2connect.NewEventServiceHandler(handler)
	mux.Handle(path, serviceHandler)
	server := httptest.NewServer(mux)
	return agentcomposev2connect.NewEventServiceClient(server.Client(), server.URL), server.Close
}

func TestEventStreamCapabilitiesReportDisabled(t *testing.T) {
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(eventstream.NewBroker(eventstream.Config{})))
	defer closeServer()
	resp, err := client.GetEventStreamCapabilities(context.Background(), connect.NewRequest(&agentcomposev2.GetEventStreamCapabilitiesRequest{}))
	if err != nil {
		t.Fatalf("GetEventStreamCapabilities: %v", err)
	}
	if resp.Msg.GetEnabled() {
		t.Fatal("capabilities reported enabled")
	}
}

func TestEventStreamCapabilitiesReportLimits(t *testing.T) {
	cfg := eventStreamTestConfig()
	cfg.MaxSubscribers = 3
	cfg.SubscriberBuffer = 5
	cfg.ReplayEvents = 7
	cfg.Heartbeat = 9 * time.Second
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(eventstream.NewBroker(cfg)))
	defer closeServer()
	resp, err := client.GetEventStreamCapabilities(context.Background(), connect.NewRequest(&agentcomposev2.GetEventStreamCapabilitiesRequest{}))
	if err != nil {
		t.Fatalf("GetEventStreamCapabilities: %v", err)
	}
	if !resp.Msg.GetEnabled() || resp.Msg.GetMaxSubscribers() != 3 || resp.Msg.GetSubscriberBufferEvents() != 5 ||
		resp.Msg.GetReplayEvents() != 7 || resp.Msg.GetHeartbeatSeconds() != 9 {
		t.Fatalf("capabilities = %+v", resp.Msg)
	}
	if len(resp.Msg.GetEventTypes()) == 0 {
		t.Fatal("capabilities omitted event types")
	}
}

func TestEventStreamWatchEventsRejectsDisabled(t *testing.T) {
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(eventstream.NewBroker(eventstream.Config{})))
	defer closeServer()
	stream, err := client.WatchEvents(context.Background(), connect.NewRequest(&agentcomposev2.WatchEventsRequest{}))
	if err != nil {
		// Connect may surface the failure when the stream is opened; that is
		// also acceptable.
		if connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("WatchEvents code = %s (err=%v)", connect.CodeOf(err), err)
		}
		return
	}
	for stream.Receive() {
		t.Fatal("disabled stream produced a frame")
	}
	if code := connect.CodeOf(stream.Err()); code != connect.CodeFailedPrecondition {
		t.Fatalf("WatchEvents code = %s, want FailedPrecondition (err=%v)", code, stream.Err())
	}
}

func TestEventStreamWatchEventsHelloAndEvent(t *testing.T) {
	broker := eventstream.NewBroker(eventStreamTestConfig())
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(broker))
	defer closeServer()
	defer broker.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.WatchEvents(ctx, connect.NewRequest(&agentcomposev2.WatchEventsRequest{
		EventTypes: []string{eventstream.TypeRunStatus},
	}))
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("missing HELLO: %v", stream.Err())
	}
	hello := stream.Msg()
	if hello.GetControl().GetKind() != agentcomposev2.StreamControl_KIND_HELLO || hello.GetControl().GetCursor() == "" {
		t.Fatalf("HELLO = %+v", hello)
	}
	assertEventStreamHeaders(t, stream.ResponseHeader())

	occurredAt := time.Unix(1700000000, 0).UTC()
	broker.Publish(&eventstream.Event{
		Type:       eventstream.TypeRunStatus,
		ProjectID:  "project-1",
		Severity:   eventstream.SeverityError,
		OccurredAt: occurredAt,
		RunStatus: &eventstream.RunStatusEvent{
			Run: domain.ProjectRunRecord{
				RunID: "run-1", ProjectID: "project-1", Status: domain.ProjectRunStatusFailed,
				Error: "boom", UpdatedAt: occurredAt,
			},
			PreviousStatus: domain.ProjectRunStatusRunning,
			ErrorStack:     "stack",
		},
	})
	if !stream.Receive() {
		t.Fatalf("missing event: %v", stream.Err())
	}
	frame := stream.Msg()
	if len(frame.GetEvents()) != 1 || frame.GetNextCursor() == "" {
		t.Fatalf("event frame = %+v", frame)
	}
	event := frame.GetEvents()[0]
	if event.GetType() != eventstream.TypeRunStatus || event.GetProjectId() != "project-1" ||
		event.GetSeverity() != agentcomposev2.EventSeverity_EVENT_SEVERITY_ERROR {
		t.Fatalf("event = %+v", event)
	}
	status := event.GetRunStatus()
	if status.GetRun().GetRunId() != "run-1" || status.GetPreviousStatus() != agentcomposev2.RunStatus_RUN_STATUS_RUNNING ||
		status.GetRun().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED || status.GetErrorStack() != "stack" {
		t.Fatalf("run status = %+v", status)
	}
	if !event.GetOccurredAt().AsTime().Equal(occurredAt) {
		t.Fatalf("occurred_at = %s, want %s", event.GetOccurredAt().AsTime(), occurredAt)
	}
}

func TestEventStreamWatchEventsSignalsSyncRequired(t *testing.T) {
	broker := eventstream.NewBroker(eventStreamTestConfig())
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(broker))
	defer closeServer()
	defer broker.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.WatchEvents(ctx, connect.NewRequest(&agentcomposev2.WatchEventsRequest{
		Cursor: eventstream.EncodeCursor("old-epoch", 7),
	}))
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("missing HELLO: %v", stream.Err())
	}
	if stream.Msg().GetControl().GetKind() != agentcomposev2.StreamControl_KIND_HELLO {
		t.Fatalf("first frame = %+v", stream.Msg())
	}
	if !stream.Receive() {
		t.Fatalf("missing SYNC_REQUIRED: %v", stream.Err())
	}
	control := stream.Msg().GetControl()
	if control.GetKind() != agentcomposev2.StreamControl_KIND_SYNC_REQUIRED || control.GetReason() != "epoch_changed" {
		t.Fatalf("sync control = %+v", control)
	}
}

func TestEventStreamWatchEventsRejectsUnknownType(t *testing.T) {
	broker := eventstream.NewBroker(eventStreamTestConfig())
	defer broker.Close()
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(broker))
	defer closeServer()
	stream, err := client.WatchEvents(context.Background(), connect.NewRequest(&agentcomposev2.WatchEventsRequest{
		EventTypes: []string{"run.unknown"},
	}))
	if err != nil {
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("WatchEvents code = %s (err=%v)", connect.CodeOf(err), err)
		}
		return
	}
	for stream.Receive() {
		t.Fatal("invalid filter produced a frame")
	}
	if code := connect.CodeOf(stream.Err()); code != connect.CodeInvalidArgument {
		t.Fatalf("WatchEvents code = %s, want InvalidArgument (err=%v)", code, stream.Err())
	}
}

func assertEventStreamHeaders(t *testing.T, headers http.Header) {
	t.Helper()
	if headers.Get("Cache-Control") != "no-cache, no-transform" || headers.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("stream headers = %#v", headers)
	}
}

func TestStreamEventToProtoStructuredRunAndSchedulerUpdates(t *testing.T) {
	runEvent := streamEventToProto(&eventstream.Event{
		Type: eventstream.TypeRunEvent, ProjectID: "p1",
		RunEvent: &eventstream.RunEvent{Event: domain.ProjectRunEventRecord{
			ID: "event-1", RunID: "run-1", Sequence: 4, Kind: domain.ProjectRunEventKindAgentMessage, Text: "done",
		}},
	})
	if runEvent.GetRunEvent().GetId() != "event-1" || runEvent.GetRunEvent().GetSeq() != 4 || runEvent.GetRunEvent().GetText() != "done" {
		t.Fatalf("run event proto = %+v", runEvent)
	}

	schedulerEvent := streamEventToProto(&eventstream.Event{
		Type: eventstream.TypeSchedulerUpdate, ProjectID: "p1",
		SchedulerUpdate: &eventstream.SchedulerUpdateEvent{
			SchedulerID: "scheduler-1", ProjectID: "p1", AgentName: "worker", Enabled: false,
			TriggerID: "trigger-1", TriggerEnabled: true, Reason: "trigger_disabled",
		},
	})
	update := schedulerEvent.GetSchedulerUpdate()
	if update.GetSchedulerId() != "scheduler-1" || update.GetTriggerId() != "trigger-1" ||
		!update.GetTriggerEnabled() || update.GetReason() != "trigger_disabled" {
		t.Fatalf("scheduler update proto = %+v", update)
	}
}

func TestEventStreamWatchEventsHeartbeat(t *testing.T) {
	cfg := eventStreamTestConfig()
	cfg.Heartbeat = time.Second
	broker := eventstream.NewBroker(cfg)
	defer broker.Close()
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(broker))
	defer closeServer()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	stream, err := client.WatchEvents(ctx, connect.NewRequest(&agentcomposev2.WatchEventsRequest{}))
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	if !stream.Receive() || stream.Msg().GetControl().GetKind() != agentcomposev2.StreamControl_KIND_HELLO {
		t.Fatalf("missing HELLO: %v", stream.Err())
	}
	if !stream.Receive() {
		t.Fatalf("missing heartbeat: %v", stream.Err())
	}
	if stream.Msg().GetControl().GetKind() != agentcomposev2.StreamControl_KIND_HEARTBEAT {
		t.Fatalf("heartbeat frame = %+v", stream.Msg())
	}
}

func TestEventStreamWatchEventsSubscriberLimit(t *testing.T) {
	cfg := eventStreamTestConfig()
	cfg.MaxSubscribers = 1
	broker := eventstream.NewBroker(cfg)
	defer broker.Close()
	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(broker))
	defer closeServer()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first, err := client.WatchEvents(ctx, connect.NewRequest(&agentcomposev2.WatchEventsRequest{}))
	if err != nil {
		t.Fatalf("first WatchEvents: %v", err)
	}
	if !first.Receive() {
		t.Fatalf("first HELLO: %v", first.Err())
	}
	second, err := client.WatchEvents(ctx, connect.NewRequest(&agentcomposev2.WatchEventsRequest{}))
	if err == nil {
		for second.Receive() {
		}
		err = second.Err()
	}
	if connect.CodeOf(err) != connect.CodeResourceExhausted {
		t.Fatalf("second stream code = %s, want ResourceExhausted (err=%v)", connect.CodeOf(err), err)
	}
}

func TestStreamEventToProtoCoversEveryPayload(t *testing.T) {
	events := []*eventstream.Event{
		{Type: eventstream.TypeRunStatus, RunStatus: &eventstream.RunStatusEvent{Run: domain.ProjectRunRecord{RunID: "run-1"}}},
		{Type: eventstream.TypeRunEvent, RunEvent: &eventstream.RunEvent{Event: domain.ProjectRunEventRecord{ID: "event-1", RunID: "run-1"}}},
		{Type: eventstream.TypeRunLog, RunLog: &eventstream.RunLogEvent{RunID: "run-1", Data: "line"}},
		{Type: eventstream.TypeSchedulerRunStatus, SchedulerRunStatus: &eventstream.SchedulerRunStatusEvent{Run: domain.SchedulerRunSummary{ID: "srun-1"}}},
		{Type: eventstream.TypeSchedulerEvent, SchedulerEvent: &eventstream.SchedulerEvent{Event: domain.SchedulerEvent{ID: "se-1"}}},
		{Type: eventstream.TypeSchedulerUpdate, SchedulerUpdate: &eventstream.SchedulerUpdateEvent{SchedulerID: "scheduler-1"}},
		{Type: eventstream.TypeSandboxStatus, SandboxStatus: &eventstream.SandboxStatusEvent{Summary: domain.SandboxSummary{ID: "sandbox-1"}}},
		{Type: eventstream.TypeSandboxEvent, SandboxEvent: &eventstream.SandboxEvent{SandboxID: "sandbox-1", Event: domain.SandboxEvent{ID: "be-1"}}},
		{Type: eventstream.TypeSandboxCellOutput, SandboxCellOutput: &eventstream.SandboxCellOutputEvent{SandboxID: "sandbox-1", Data: "out"}},
	}
	for _, event := range events {
		mapped := streamEventToProto(event)
		if mapped == nil || mapped.GetPayload() == nil || mapped.GetType() != event.Type {
			t.Fatalf("map %s = %+v", event.Type, mapped)
		}
	}
}

func TestWatchSeverityAndSubscribeError(t *testing.T) {
	for _, test := range []struct {
		value agentcomposev2.EventSeverity
		want  eventstream.Severity
	}{
		{agentcomposev2.EventSeverity_EVENT_SEVERITY_UNSPECIFIED, eventstream.SeverityInfo},
		{agentcomposev2.EventSeverity_EVENT_SEVERITY_INFO, eventstream.SeverityInfo},
		{agentcomposev2.EventSeverity_EVENT_SEVERITY_WARNING, eventstream.SeverityWarning},
		{agentcomposev2.EventSeverity_EVENT_SEVERITY_ERROR, eventstream.SeverityError},
	} {
		got, err := watchSeverity(test.value)
		if err != nil || got != test.want {
			t.Fatalf("watchSeverity(%s) = %v, %v", test.value, got, err)
		}
	}
	if _, err := watchSeverity(agentcomposev2.EventSeverity(99)); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("unknown severity error = %v", err)
	}
	for _, test := range []struct {
		err  error
		code connect.Code
	}{
		{eventstream.ErrDisabled, connect.CodeFailedPrecondition},
		{eventstream.ErrClosed, connect.CodeUnavailable},
		{eventstream.ErrTooManySubscribers, connect.CodeResourceExhausted},
		{eventstream.ErrInvalidCursor, connect.CodeInvalidArgument},
		{context.Canceled, connect.CodeInternal},
	} {
		if got := connect.CodeOf(watchSubscribeError(test.err)); got != test.code {
			t.Fatalf("watchSubscribeError(%v) = %s, want %s", test.err, got, test.code)
		}
	}
}

type recordingWatchStream struct {
	header http.Header
	frames []*agentcomposev2.WatchEventsResponse
}

func (s *recordingWatchStream) Send(frame *agentcomposev2.WatchEventsResponse) error {
	s.frames = append(s.frames, frame)
	return nil
}

func (s *recordingWatchStream) ResponseHeader() http.Header {
	if s.header == nil {
		s.header = make(http.Header)
	}
	return s.header
}

func TestSendHeartbeatUsesSafeCursorNotGlobalHead(t *testing.T) {
	cfg := eventStreamTestConfig()
	broker := eventstream.NewBroker(cfg)
	defer broker.Close()
	filter, err := eventstream.NewFilter(nil, nil, eventstream.SeverityInfo, false)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	sub, _, err := broker.Subscribe(filter, "")
	if err != nil {
		t.Fatalf("activate broker: %v", err)
	}
	for index := 0; index < 5; index++ {
		broker.Publish(&eventstream.Event{Type: eventstream.TypeRunStatus, ProjectID: "p1", RunStatus: &eventstream.RunStatusEvent{}})
	}
	epoch, head, _ := broker.Head()
	safe := eventstream.EncodeCursor(epoch, 1)
	if head == safe {
		t.Fatal("test needs head to differ from the safe cursor")
	}
	stream := &recordingWatchStream{}
	if err := NewEventStreamHandler(broker).sendHeartbeat(stream, safe); err != nil {
		t.Fatalf("sendHeartbeat: %v", err)
	}
	if len(stream.frames) != 1 || stream.frames[0].GetNextCursor() != safe || stream.frames[0].GetControl().GetCursor() != safe {
		t.Fatalf("heartbeat frames = %+v, want safe cursor %q (head %q)", stream.frames, safe, head)
	}
	sub.Close()
}

func TestWatchEventsHelloDoesNotAcknowledgeUnsentReplay(t *testing.T) {
	cfg := eventStreamTestConfig()
	cfg.ReplayEvents = 16
	broker := eventstream.NewBroker(cfg)
	defer broker.Close()
	filter, err := eventstream.NewFilter(nil, nil, eventstream.SeverityInfo, false)
	if err != nil {
		t.Fatalf("filter: %v", err)
	}
	dummy, _, err := broker.Subscribe(filter, "")
	if err != nil {
		t.Fatalf("activate broker: %v", err)
	}
	for index := 0; index < 5; index++ {
		broker.Publish(&eventstream.Event{Type: eventstream.TypeRunStatus, ProjectID: "p1", RunStatus: &eventstream.RunStatusEvent{}})
	}
	replay := make([]*eventstream.Event, 0, 5)
	for len(replay) < 5 {
		select {
		case event := <-dummy.Events():
			replay = append(replay, event)
		case <-time.After(time.Second):
			t.Fatal("timed out collecting replay events")
		}
	}
	dummy.Close()
	if len(replay) != 5 {
		t.Fatalf("replay events = %d", len(replay))
	}

	client, closeServer := newEventStreamTestClient(t, NewEventStreamHandler(broker))
	defer closeServer()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := client.WatchEvents(ctx, connect.NewRequest(&agentcomposev2.WatchEventsRequest{Cursor: replay[0].Cursor}))
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("missing HELLO: %v", stream.Err())
	}
	if got := stream.Msg().GetNextCursor(); got != replay[0].Cursor {
		t.Fatalf("HELLO next_cursor = %q, want last consumed %q (head %q)", got, replay[0].Cursor, replay[len(replay)-1].Cursor)
	}
}

func TestSendEventBatchesAdvancesSafeCursorOnlyAfterSend(t *testing.T) {
	broker := eventstream.NewBroker(eventStreamTestConfig())
	defer broker.Close()
	handler := NewEventStreamHandler(broker)
	stream := &recordingWatchStream{}
	events := []*eventstream.Event{
		{Type: eventstream.TypeRunStatus, Cursor: "cursor-1", RunStatus: &eventstream.RunStatusEvent{}},
		{Type: eventstream.TypeRunStatus, Cursor: "cursor-2", RunStatus: &eventstream.RunStatusEvent{}},
	}
	cursor := ""
	if err := handler.sendEventBatches(stream, events, 1, &cursor); err != nil {
		t.Fatalf("sendEventBatches: %v", err)
	}
	if cursor != "cursor-2" {
		t.Fatalf("last sent cursor = %q, want cursor-2", cursor)
	}
	if len(stream.frames) != 2 || stream.frames[1].GetNextCursor() != "cursor-2" {
		t.Fatalf("frames = %+v", stream.frames)
	}
}
