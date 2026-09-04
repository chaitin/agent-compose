package chat

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSendDoesNotWaitForResponseHeadersBeforeOpening(t *testing.T) {
	// A Connect handler writes response headers only after receiving the
	// client's first message. Waiting for headers before sending the opening
	// frame deadlocks: the daemon never answers and the turn never starts.
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		// Nothing is written until the opening frame has arrived, exactly as
		// the daemon behaves.
		if _, ok := stream.recv(); !ok {
			return
		}
		stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{}})
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := conversation.Send(ctx, "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(ctx); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestSendReportsAnUnreachableDaemonRatherThanBlocking(t *testing.T) {
	client, err := New(Config{BaseURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	conversation := client.Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reply, err := conversation.Send(ctx, "hi")
	if err == nil {
		if _, err = reply.Wait(ctx); err == nil {
			t.Fatal("the turn succeeded against a closed port")
		}
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}

func TestTheSessionOutlivesTheContextOfTheSendThatOpenedIt(t *testing.T) {
	// A server handling an HTTP request sends on the request's context. That
	// context ends with the request, but the conversation does not: the next
	// message must still land on the same session, with the agent's context
	// intact.
	daemon := newFakeDaemon(t)
	frames := make(chan wireAttachRequest, 2)
	daemon.attach = func(stream *fakeStream) {
		for range 2 {
			frame, ok := stream.recv()
			if !ok {
				return
			}
			frames <- frame
			stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{}})
		}
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()

	first, cancelFirst := context.WithCancel(context.Background())
	reply, err := conversation.Send(first, "first")
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := reply.Wait(first); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	cancelFirst()

	second, err := conversation.Send(context.Background(), "second")
	if err != nil {
		t.Fatalf("second Send after the first context ended: %v", err)
	}
	if _, err := second.Wait(context.Background()); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	<-frames
	followUp := <-frames
	if followUp.HumanMessage == nil || followUp.HumanMessage.Text != "second" {
		t.Fatalf("second frame = %#v, want a human message on the same session", followUp)
	}
	if attaches := daemon.count("AttachAgentRun"); attaches != 1 {
		t.Errorf("attach count = %d, want 1: the session must survive the first context", attaches)
	}
}

func TestCloseReturnsEvenWhenTheDaemonStopsAnswering(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		// Accept the opening frame and then go silent, never answering.
		_, _ = stream.recv()
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	if _, err := conversation.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- conversation.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close blocked on a daemon that stopped answering")
	}
}

func TestSendStreamsATurnAndAccumulatesTheAnswer(t *testing.T) {
	daemon := newFakeDaemon(t)
	starts := make(chan *wireAttachStart, 1)
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.Start
		stream.send(wireAttachResponse{Started: &wireStarted{RunID: "run-1", SandboxID: "sandbox-1"}})
		stream.agentEvent("step_start", map[string]any{"step": 0})
		stream.agentEvent("text_delta", map[string]any{"text": "Hello, "})
		stream.agentEvent("tool_call", map[string]any{"id": "t1", "name": "bash", "toolKind": "execute", "status": "completed", "command": "ls"})
		stream.agentEvent("text_delta", map[string]any{"text": "world"})
		stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{RunID: "run-1", ResultJSON: `{"ok":true}`}})
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()

	reply, err := conversation.Send(context.Background(), "check this PR")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	message, err := reply.Wait(context.Background())
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if message.Text != "Hello, world" {
		t.Errorf("answer = %q, want %q", message.Text, "Hello, world")
	}
	if message.Role != RoleAssistant {
		t.Errorf("role = %q, want %q", message.Role, RoleAssistant)
	}
	if string(message.Result) != `{"ok":true}` {
		t.Errorf("result = %s, want {\"ok\":true}", message.Result)
	}
	if got := conversation.RunID(); got != "run-1" {
		t.Errorf("run ID = %q, want run-1", got)
	}

	// The opening message provisions the session, so it travels in the start
	// frame rather than as a separate human message.
	start := <-starts
	if start == nil || start.Request == nil {
		t.Fatalf("start frame = %#v, want one carrying a run request", start)
	}
	if start.Request.Prompt != "check this PR" {
		t.Errorf("start prompt = %q, want the opening message", start.Request.Prompt)
	}
	if start.Request.Labels[conversationLabel] != conversation.ID() {
		t.Errorf("start labels = %v, want one identifying the conversation", start.Request.Labels)
	}
	if start.DisconnectPolicy != attachPolicyDetach {
		t.Errorf("disconnect policy = %q, want %q", start.DisconnectPolicy, attachPolicyDetach)
	}
	if start.Request.CleanupPolicy != cleanupPolicyKeepLive {
		t.Errorf("cleanup policy = %q, want the environment kept alive", start.Request.CleanupPolicy)
	}
}

func TestEventsReplayForEveryCaller(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.agentEvent("text_delta", map[string]any{"text": "one"})
		stream.agentEvent("usage", map[string]any{"scope": "turn", "inputTokens": 10, "outputTokens": 4})
		stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{}})
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()
	reply, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	// Wait consumed nothing, and a second pass sees the same events as the
	// first: the Reply retains them rather than draining a channel.
	for pass := range 2 {
		var kinds []EventKind
		for event, err := range reply.Events(context.Background()) {
			if err != nil {
				t.Fatalf("pass %d events: %v", pass, err)
			}
			kinds = append(kinds, event.Kind())
		}
		if len(kinds) != 2 || kinds[0] != KindTextDelta || kinds[1] != KindUsage {
			t.Fatalf("pass %d kinds = %v, want [text_delta usage]", pass, kinds)
		}
	}
}

func TestSendWhileAReplyIsStreamingReportsBusy(t *testing.T) {
	daemon := newFakeDaemon(t)
	release := make(chan struct{})
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		<-release
		stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{}})
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()
	first, err := conversation.Send(context.Background(), "first")
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := conversation.Send(context.Background(), "second"); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Send error = %v, want ErrBusy", err)
	}
	close(release)
	if _, err := first.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
}

func TestFollowUpTurnsTravelAsMessagesOnTheSameSession(t *testing.T) {
	daemon := newFakeDaemon(t)
	frames := make(chan wireAttachRequest, 2)
	daemon.attach = func(stream *fakeStream) {
		for range 2 {
			frame, ok := stream.recv()
			if !ok {
				return
			}
			frames <- frame
			stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{}})
		}
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()
	for _, text := range []string{"first", "second"} {
		reply, err := conversation.Send(context.Background(), text)
		if err != nil {
			t.Fatalf("Send %q: %v", text, err)
		}
		if _, err := reply.Wait(context.Background()); err != nil {
			t.Fatalf("Wait %q: %v", text, err)
		}
	}
	if opening := <-frames; opening.Start == nil {
		t.Fatalf("first frame = %#v, want a start", opening)
	}
	followUp := <-frames
	if followUp.HumanMessage == nil || followUp.HumanMessage.Text != "second" {
		t.Fatalf("second frame = %#v, want the follow-up as a human message", followUp)
	}
	// One session served both turns, so the environment carried context across
	// them.
	if attaches := daemon.count("AttachAgentRun"); attaches != 1 {
		t.Errorf("attach count = %d, want 1", attaches)
	}
}

func TestOpenResumesALiveConversation(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["ListRuns"] = wireListRunsResponse{Runs: []wireRunSummary{{
		RunID:     "run-7",
		Status:    "RUN_STATUS_RUNNING",
		SandboxID: "sandbox-7",
		Labels:    map[string]string{conversationLabel: "conv-abc"},
		CreatedAt: time.Now().Add(-time.Hour),
	}}, Total: 1}
	starts := make(chan *wireAttachStart, 1)
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.Start
		<-stream.hold
	}

	conversation, err := daemon.client(t).Agent("project-1", "reviewer").Open(context.Background(), "conv-abc")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conversation.Close() }()
	if conversation.Continuity() != Continuous {
		t.Errorf("continuity = %q, want %q", conversation.Continuity(), Continuous)
	}
	start := <-starts
	if start == nil || start.RunID != "run-7" {
		t.Fatalf("start frame = %#v, want a reattach to run-7", start)
	}
	if start.Request != nil {
		t.Errorf("reattach carried a run request %#v, want none", start.Request)
	}
}

func TestOpenReportsRestartedWhenTheEnvironmentIsGone(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["ListRuns"] = wireListRunsResponse{Runs: []wireRunSummary{{
		RunID:  "run-7",
		Status: "RUN_STATUS_SUCCEEDED",
	}}, Total: 1}

	conversation, err := daemon.client(t).Agent("project-1", "reviewer").Open(context.Background(), "conv-abc")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if conversation.Continuity() != Restarted {
		t.Errorf("continuity = %q, want %q", conversation.Continuity(), Restarted)
	}
	if attaches := daemon.count("AttachAgentRun"); attaches != 0 {
		t.Errorf("attach count = %d, want none before the next message", attaches)
	}
}

func TestOpenCanRefuseToRestart(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["ListRuns"] = wireListRunsResponse{}

	_, err := daemon.client(t).Agent("project-1", "reviewer").Open(context.Background(), "conv-abc", WithRestartIfGone(false))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open error = %v, want ErrNotFound", err)
	}
}

func TestCloseLeavesTheConversationResumableAndDeleteEndsIt(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["StopRun"] = map[string]any{}
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.send(wireAttachResponse{Started: &wireStarted{RunID: "run-1"}})
		stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{}})
		// Hold the session open the way a detached daemon would.
		<-stream.hold
	}

	agent := daemon.client(t).Agent("project-1", "reviewer")
	conversation := agent.Start()
	reply, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if err := conversation.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if stops := daemon.count("StopRun"); stops != 0 {
		t.Fatalf("Close issued %d stops, want none: it must leave the conversation resumable", stops)
	}
	if _, err := conversation.Send(context.Background(), "again"); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close = %v, want ErrClosed", err)
	}

	second := agent.Start()
	second.runID = "run-1"
	if err := second.Delete(context.Background()); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if stops := daemon.count("StopRun"); stops != 1 {
		t.Errorf("Delete issued %d stops, want 1", stops)
	}
}

func TestALostStreamFailsTheTurnAndReattachesToTheSameSession(t *testing.T) {
	daemon := newFakeDaemon(t)
	starts := make(chan *wireAttachStart, 2)
	attaches := 0
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.Start
		attaches++
		if attaches == 1 {
			stream.send(wireAttachResponse{Started: &wireStarted{RunID: "run-1"}})
			stream.agentEvent("text_delta", map[string]any{"text": "partial"})
			// Drop the stream mid-turn, the way a network blip does. The run
			// itself is untouched.
			return
		}
		stream.send(wireAttachResponse{TurnComplete: &wireTurnCompleted{}})
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()
	reply, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(context.Background()); err == nil {
		t.Fatal("Wait succeeded, want the turn to fail when its stream is lost")
	}
	<-starts

	// The run outlived the stream, so nothing was lost and the caller must not
	// be told otherwise.
	if conversation.Continuity() != Continuous {
		t.Errorf("continuity = %q, want %q: losing a stream is not losing the environment", conversation.Continuity(), Continuous)
	}

	second, err := conversation.Send(context.Background(), "again")
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if _, err := second.Wait(context.Background()); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	reattach := <-starts
	if reattach == nil || reattach.RunID != "run-1" {
		t.Fatalf("second start frame = %#v, want a reattach to run-1", reattach)
	}
	if reattach.Request != nil {
		t.Errorf("reattach carried a run request %#v, want none", reattach.Request)
	}
}

func TestATerminalRunReportsRestarted(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.send(wireAttachResponse{Started: &wireStarted{RunID: "run-1"}})
		// The run reached a terminal state, so its environment is gone too.
		stream.send(wireAttachResponse{Result: &wireAttachResult{Success: true}})
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()
	reply, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if conversation.Continuity() != Restarted {
		t.Errorf("continuity = %q, want %q", conversation.Continuity(), Restarted)
	}
	if conversation.RunID() != "" {
		t.Errorf("run ID = %q, want it cleared once the run is terminal", conversation.RunID())
	}
}

func TestAFailedRunSurfacesItsError(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.send(wireAttachResponse{Result: &wireAttachResult{Success: false, Error: "image pull failed"}})
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()
	if _, err := conversation.Ask(context.Background(), "hi"); err == nil || !strings.Contains(err.Error(), "image pull failed") {
		t.Fatalf("Ask error = %v, want the daemon's reason", err)
	}
}

func TestWaitHonorsContextCancellation(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close() }()
	reply, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := reply.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Wait error = %v, want DeadlineExceeded", err)
	}
}

func TestHistoryReadsBothRolesAcrossRestarts(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.unary["ListRuns"] = wireListRunsResponse{Runs: []wireRunSummary{
		{RunID: "run-2", CreatedAt: time.Unix(200, 0)},
		{RunID: "run-1", CreatedAt: time.Unix(100, 0)},
	}, Total: 2}
	daemon.unary["ListRunEvents"] = wireListEventsResponse{Events: []wireRunEvent{
		{ID: "e1", Kind: "RUN_EVENT_KIND_USER_MESSAGE", Text: "question"},
		{ID: "e2", Kind: "RUN_EVENT_KIND_STATUS", Text: "running"},
		{ID: "e3", Kind: "RUN_EVENT_KIND_AGENT_MESSAGE", Text: "answer"},
	}, Total: 3}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start(WithID("conv-abc"))
	messages, err := conversation.History(context.Background())
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	// Both runs are read, oldest first, and lifecycle events are not messages.
	if len(messages) != 4 {
		t.Fatalf("history = %d messages, want 4", len(messages))
	}
	if messages[0].Role != RoleUser || messages[0].Text != "question" {
		t.Errorf("first message = %#v", messages[0])
	}
	if messages[1].Role != RoleAssistant || messages[1].Text != "answer" {
		t.Errorf("second message = %#v", messages[1])
	}
}
