package main

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"github.com/samber/do/v2"

	"github.com/chaitin/agent-compose/pkg/eventstream"
	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestEventStreamEndToEndWithDaemonAuth(t *testing.T) {
	t.Setenv("EVENT_STREAM_ENABLED", "true")
	t.Setenv("AGENT_COMPOSE_AUTH_TOKEN", "secret")
	app, cancel := newTestDaemonApp(t, "127.0.0.1:0", nil)
	defer cancel()
	server := httptest.NewServer(app.Echo)
	defer server.Close()

	client := agentcomposev2connect.NewEventServiceClient(server.Client(), server.URL)
	ctx, cancelStream := context.WithCancel(context.Background())
	defer cancelStream()

	if _, err := client.GetEventStreamCapabilities(ctx, connect.NewRequest(&agentcomposev2.GetEventStreamCapabilitiesRequest{})); connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("unauthenticated capabilities error = %v, want Unauthenticated", err)
	}

	capReq := connect.NewRequest(&agentcomposev2.GetEventStreamCapabilitiesRequest{})
	capReq.Header().Set("Authorization", "Bearer secret")
	capabilities, err := client.GetEventStreamCapabilities(ctx, capReq)
	if err != nil {
		t.Fatalf("GetEventStreamCapabilities: %v", err)
	}
	if !capabilities.Msg.GetEnabled() {
		t.Fatalf("capabilities = %+v, want enabled", capabilities.Msg)
	}

	watchReq := connect.NewRequest(&agentcomposev2.WatchEventsRequest{EventTypes: []string{eventstream.TypeRunStatus}})
	watchReq.Header().Set("Authorization", "Bearer secret")
	stream, err := client.WatchEvents(ctx, watchReq)
	if err != nil {
		t.Fatalf("WatchEvents: %v", err)
	}
	if !stream.Receive() {
		t.Fatalf("missing HELLO: %v", stream.Err())
	}
	if stream.Msg().GetControl().GetKind() != agentcomposev2.StreamControl_KIND_HELLO {
		t.Fatalf("first frame = %+v", stream.Msg())
	}

	broker := do.MustInvoke[*eventstream.Broker](app.DI)
	if !broker.Active() {
		t.Fatal("first WatchEvents did not activate the broker")
	}
	broker.Publish(&eventstream.Event{
		Type:      eventstream.TypeRunStatus,
		ProjectID: "project-1",
		Severity:  eventstream.SeverityInfo,
		RunStatus: &eventstream.RunStatusEvent{Run: domain.ProjectRunRecord{
			RunID: "run-1", ProjectID: "project-1", Status: domain.ProjectRunStatusRunning,
		}},
	})
	if !stream.Receive() {
		t.Fatalf("missing event: %v", stream.Err())
	}
	event := stream.Msg().GetEvents()
	if len(event) != 1 || event[0].GetType() != eventstream.TypeRunStatus || event[0].GetProjectId() != "project-1" {
		t.Fatalf("event frame = %+v", stream.Msg())
	}
}
