package chat

import (
	"context"
	"errors"
	"google.golang.org/protobuf/types/known/timestamppb"
	"strings"
	"testing"
	"time"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
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
		stream.send(turnCompleted(""))
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()

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
	defer func() { _ = conversation.Close(context.Background()) }()

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
	frames := make(chan *agentcomposev2.AttachAgentRunRequest, 2)
	daemon.attach = func(stream *fakeStream) {
		for range 2 {
			frame, ok := stream.recv()
			if !ok {
				return
			}
			frames <- frame
			stream.send(turnCompleted(""))
		}
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()

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
	if followUp.GetHumanMessage() == nil || followUp.GetHumanMessage().GetText() != "second" {
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
	go func() { closed <- conversation.Close(context.Background()) }()
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
	starts := make(chan *agentcomposev2.AttachAgentRunStart, 1)
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.GetStart()
		stream.send(started("run-1", "sandbox-1"))
		stream.agentEvent("step_start", map[string]any{"step": 0})
		stream.agentEvent("text_delta", map[string]any{"text": "Hello, "})
		stream.agentEvent("tool_call", map[string]any{"id": "t1", "name": "bash", "toolKind": "execute", "status": "completed", "command": "ls"})
		stream.agentEvent("text_delta", map[string]any{"text": "world"})
		stream.send(turnCompleted(`{"ok":true}`))
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()

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
	if start == nil || start.GetRequest() == nil {
		t.Fatalf("start frame = %#v, want one carrying a run request", start)
	}
	if start.GetRequest().GetPrompt() != "check this PR" {
		t.Errorf("start prompt = %q, want the opening message", start.GetRequest().GetPrompt())
	}
	if start.GetRequest().GetLabels()[conversationLabel] != conversation.ID() {
		t.Errorf("start labels = %v, want one identifying the conversation", start.GetRequest().GetLabels())
	}
	if start.GetDisconnectPolicy() != agentcomposev2.AttachDisconnectPolicy_ATTACH_DISCONNECT_POLICY_DETACH {
		t.Errorf("disconnect policy = %v, want DETACH", start.GetDisconnectPolicy())
	}
	// A conversation's environment must not outlive the run that serves it:
	// one run covers every turn of a session, and pinning the sandbox past
	// that leaves one running for every conversation ever opened.
	if start.GetRequest().GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION {
		t.Errorf("cleanup policy = %v, want the environment stopped when the run ends", start.GetRequest().GetCleanupPolicy())
	}
}

func TestEventsReplayForEveryCaller(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.agentEvent("text_delta", map[string]any{"text": "one"})
		stream.agentEvent("usage", map[string]any{"scope": "turn", "inputTokens": 10, "outputTokens": 4})
		stream.send(turnCompleted(""))
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
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

func TestEventsPreserveProviderExtensions(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.send(&agentcomposev2.AttachAgentRunResponse{
			Frame: &agentcomposev2.AttachAgentRunResponse_AgentEvent{
				AgentEvent: &agentcomposev2.AttachAgentEvent{
					Name: "item.completed", Text: "provider text",
					PayloadJson: `{"item":{"type":"agent_message","text":"provider text"}}`,
				},
			},
		})
		stream.send(turnCompleted(""))
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
	reply, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	var event Event
	for got, err := range reply.Events(context.Background()) {
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		event = got
	}
	raw, ok := event.(*RawEvent)
	if !ok || raw.Name != "item.completed" || raw.Text != "provider text" || raw.PayloadJSON == "" {
		t.Fatalf("event = %#v, want preserved provider event", event)
	}
}

func TestSendWhileAReplyIsStreamingReportsBusy(t *testing.T) {
	daemon := newFakeDaemon(t)
	release := make(chan struct{})
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		<-release
		stream.send(turnCompleted(""))
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
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
	frames := make(chan *agentcomposev2.AttachAgentRunRequest, 2)
	daemon.attach = func(stream *fakeStream) {
		for range 2 {
			frame, ok := stream.recv()
			if !ok {
				return
			}
			frames <- frame
			stream.send(turnCompleted(""))
		}
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
	for _, text := range []string{"first", "second"} {
		reply, err := conversation.Send(context.Background(), text)
		if err != nil {
			t.Fatalf("Send %q: %v", text, err)
		}
		if _, err := reply.Wait(context.Background()); err != nil {
			t.Fatalf("Wait %q: %v", text, err)
		}
	}
	if opening := <-frames; opening.GetStart() == nil {
		t.Fatalf("first frame = %#v, want a start", opening)
	}
	followUp := <-frames
	if followUp.GetHumanMessage() == nil || followUp.GetHumanMessage().GetText() != "second" {
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
	daemon.listRuns = listRunsReturning(runSummary("run-7", "RUN_STATUS_RUNNING", "sandbox-7",
		timestamppb.New(time.Now().Add(-time.Hour))))
	starts := make(chan *agentcomposev2.AttachAgentRunStart, 1)
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.GetStart()
		<-stream.hold
	}

	conversation, err := daemon.client(t).Agent("project-1", "reviewer").Open(context.Background(), "conv-abc")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = conversation.Close(context.Background()) }()
	if conversation.Continuity() != Continuous {
		t.Errorf("continuity = %q, want %q", conversation.Continuity(), Continuous)
	}
	start := <-starts
	if start == nil || start.GetRunId() != "run-7" {
		t.Fatalf("start frame = %#v, want a reattach to run-7", start)
	}
	if start.GetRequest() != nil {
		t.Errorf("reattach carried a run request %#v, want none", start.GetRequest())
	}
}

func TestOpenReportsRestartedWhenTheEnvironmentIsGone(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning(runSummary("run-7", "RUN_STATUS_SUCCEEDED", ""))

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
	daemon.listRuns = listRunsReturning()

	_, err := daemon.client(t).Agent("project-1", "reviewer").Open(context.Background(), "conv-abc", WithRestartIfGone(false))
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Open error = %v, want ErrNotFound", err)
	}
}

func TestClosingEndsTheRunAndEndSessionReachesItByID(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.send(started("run-1", ""))
		stream.send(turnCompleted(""))
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
	if err := conversation.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Closing ends the run this handle held. The disconnect policy keeps a run
	// alive when a stream merely drops, so if Close did not end it nothing
	// would, and every conversation ever opened would keep an environment.
	if stops := daemon.count("StopRun"); stops != 1 {
		t.Fatalf("Close issued %d stops, want 1: closing must end the run it holds", stops)
	}
	if _, err := conversation.Send(context.Background(), "again"); !errors.Is(err, ErrClosed) {
		t.Errorf("Send after Close = %v, want ErrClosed", err)
	}

	// Ending the session again by ID, the way a product retires a conversation
	// it holds no handle for, reaches the same run through its label.
	daemon.listRuns = listRunsReturning(runSummary("run-1", "RUN_STATUS_RUNNING", "sandbox-1"))
	if err := daemon.client(t).EndSession(context.Background(), conversation.ID()); err != nil {
		t.Fatalf("EndSession: %v", err)
	}
	if stops := daemon.count("StopRun"); stops != 2 {
		t.Errorf("stops = %d, want 2 (one per session ended)", stops)
	}
}

func TestALostStreamFailsTheTurnAndReattachesToTheSameSession(t *testing.T) {
	daemon := newFakeDaemon(t)
	starts := make(chan *agentcomposev2.AttachAgentRunStart, 2)
	attaches := 0
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.GetStart()
		attaches++
		if attaches == 1 {
			stream.send(started("run-1", ""))
			stream.agentEvent("text_delta", map[string]any{"text": "partial"})
			// Drop the stream mid-turn, the way a network blip does. The run
			// itself is untouched.
			return
		}
		stream.send(turnCompleted(""))
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
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
	if reattach == nil || reattach.GetRunId() != "run-1" {
		t.Fatalf("second start frame = %#v, want a reattach to run-1", reattach)
	}
	if reattach.Request != nil {
		t.Errorf("reattach carried a run request %#v, want none", reattach.Request)
	}
}

// TestATerminalRunClearsTheRunButNotContinuity checks that a terminal run by
// itself does not declare a restart: the sandbox it used may still be
// resumable, and Continuity is decided by whether the next attach actually
// gets it back, not by the mere fact that a run ended.
func TestATerminalRunClearsTheRunButNotContinuity(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.send(started("run-1", "sandbox-1"))
		stream.send(runResult(true, ""))
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
	reply, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if conversation.Continuity() != Continuous {
		t.Errorf("continuity = %q, want %q: nothing has attempted a restart yet", conversation.Continuity(), Continuous)
	}
	if conversation.RunID() != "" {
		t.Errorf("run ID = %q, want it cleared once the run is terminal", conversation.RunID())
	}
}

// TestNextRunAsksToReuseTheSandboxAndReportsContinuous checks that once a
// run ends, the next one asks the daemon to resume the same sandbox rather
// than starting from nothing — and that a successful resume reports
// Continuous even though it took a new run to get there.
func TestNextRunAsksToReuseTheSandboxAndReportsContinuous(t *testing.T) {
	daemon := newFakeDaemon(t)
	starts := make(chan *agentcomposev2.AttachAgentRunStart, 2)
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.GetStart()
		stream.send(started("run", "sandbox-1"))
		stream.send(runResult(true, ""))
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()

	first, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := first.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait: %v", err)
	}
	opening := <-starts
	if opening.Request == nil || opening.Request.GetSandboxId() != "" {
		t.Fatalf("opening run asked to reuse a sandbox it never had: %#v", opening.Request)
	}

	second, err := conversation.Send(context.Background(), "again")
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if _, err := second.Wait(context.Background()); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	reopened := <-starts
	if reopened.Request == nil || reopened.Request.GetSandboxId() != "sandbox-1" {
		t.Fatalf("second run did not ask to reuse the prior sandbox: %#v", reopened.Request)
	}
	if conversation.Continuity() != Continuous {
		t.Errorf("continuity = %q, want %q: the daemon reused the same sandbox", conversation.Continuity(), Continuous)
	}
}

// TestSandboxReuseFailingReportsRestarted checks the other side: when the
// daemon does not honor the reuse request and hands back a different
// sandbox, that is a genuine restart and must be reported as one.
func TestSandboxReuseFailingReportsRestarted(t *testing.T) {
	daemon := newFakeDaemon(t)
	attempt := 0
	daemon.attach = func(stream *fakeStream) {
		attempt++
		_, _ = stream.recv()
		sandboxID := "sandbox-1"
		if attempt > 1 {
			sandboxID = "sandbox-2" // a different sandbox: the reuse request was not honored
		}
		stream.send(started("run", sandboxID))
		stream.send(runResult(true, ""))
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()

	first, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("first Send: %v", err)
	}
	if _, err := first.Wait(context.Background()); err != nil {
		t.Fatalf("first Wait: %v", err)
	}

	second, err := conversation.Send(context.Background(), "again")
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if _, err := second.Wait(context.Background()); err != nil {
		t.Fatalf("second Wait: %v", err)
	}
	if conversation.Continuity() != Restarted {
		t.Errorf("continuity = %q, want %q: the daemon did not reuse the sandbox it was asked to", conversation.Continuity(), Restarted)
	}
}

func TestAFailedRunSurfacesItsError(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.attach = func(stream *fakeStream) {
		_, _ = stream.recv()
		stream.send(runResult(false, "image pull failed"))
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
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
	defer func() { _ = conversation.Close(context.Background()) }()
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
	daemon.listRuns = listRunsReturning(
		&agentcomposev2.RunSummary{RunId: "run-2", CreatedAt: timestamppb.New(time.Unix(200, 0))},
		&agentcomposev2.RunSummary{RunId: "run-1", CreatedAt: timestamppb.New(time.Unix(100, 0))},
	)
	daemon.listRunEvents = listEventsReturning(
		runEvent("e1", agentcomposev2.RunEventKind_RUN_EVENT_KIND_USER_MESSAGE, "question"),
		runEvent("e2", agentcomposev2.RunEventKind_RUN_EVENT_KIND_STATUS, "running"),
		runEvent("e3", agentcomposev2.RunEventKind_RUN_EVENT_KIND_AGENT_MESSAGE, "answer"),
	)

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

// A handle that never attached holds no run, so closing it must not go looking
// for one. Two handles can name the same conversation — a server that races to
// attach discards the loser — and a discarded handle that ended the winner's
// run would kill a live session.
func TestClosingAHandleThatNeverAttachedEndsNothing(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning(runSummary("run-live", "RUN_STATUS_RUNNING", "sandbox-a"))

	spare := daemon.client(t).Agent("project-1", "reviewer").Start(WithID("conv-1"))
	if err := spare.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if stops := daemon.count("StopRun"); stops != 0 {
		t.Fatalf("closing an unattached handle issued %d stops, want none", stops)
	}
	if lists := daemon.count("ListRuns"); lists != 0 {
		t.Errorf("closing an unattached handle read the run list %d times, want none", lists)
	}
}

// Reopening a conversation whose last run has ended must still ask for that
// run's sandbox. Runs end every time a client closes, so this is the ordinary
// path back into a conversation — and dropping the sandbox here builds a new
// environment beside the old one, losing the context the agent had persisted.
func TestOpeningAfterTheLastRunEndedStillResumesItsSandbox(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.listRuns = listRunsReturning(runSummary("run-done", "RUN_STATUS_FAILED", "sandbox-a"))
	starts := make(chan *agentcomposev2.AttachAgentRunStart, 1)
	daemon.attach = func(stream *fakeStream) {
		frame, _ := stream.recv()
		starts <- frame.GetStart()
		stream.send(started("run-2", "sandbox-a"))
		stream.send(turnCompleted(""))
		<-stream.hold
	}

	agent := daemon.client(t).Agent("project-1", "reviewer")
	conversation, err := agent.Open(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	reply, err := conversation.Send(context.Background(), "still there?")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := reply.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	start := <-starts
	if start.Request.GetSandboxId() != "sandbox-a" {
		t.Fatalf("resumed sandbox %q, want the one the ended run used", start.Request.GetSandboxId())
	}
	// The daemon gave back the sandbox that was asked for, so nothing was lost.
	if got := conversation.Continuity(); got != Continuous {
		t.Errorf("Continuity = %q, want %q", got, Continuous)
	}
}

// A turn whose stream broke leaves its outcome unknown, so a caller recovers by
// resending. The daemon must be able to tell that resend apart from a person
// typing the same thing twice, and the only thing that can tell them apart is
// the identity the message travels under.
func TestResendingALostTurnCarriesTheIdentityItAlreadyHad(t *testing.T) {
	daemon := newFakeDaemon(t)
	messages := make(chan *agentcomposev2.AttachAgentRunRequest, 3)
	attaches := 0
	daemon.attach = func(stream *fakeStream) {
		attaches++
		if attaches == 1 {
			// The opening turn travels as the run's prompt, not as a message.
			if _, ok := stream.recv(); !ok {
				return
			}
			stream.send(started("run-1", ""))
			stream.send(turnCompleted(""))
			frame, ok := stream.recv()
			if !ok {
				return
			}
			messages <- frame
			// Drop the stream while that turn is in flight. The run survives,
			// and so does the message it already took.
			return
		}
		if _, ok := stream.recv(); !ok {
			return
		}
		for range 2 {
			frame, ok := stream.recv()
			if !ok {
				return
			}
			messages <- frame
			stream.send(turnCompleted(""))
		}
		<-stream.hold
	}

	conversation := daemon.client(t).Agent("project-1", "reviewer").Start()
	defer func() { _ = conversation.Close(context.Background()) }()
	opening, err := conversation.Send(context.Background(), "hi")
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if _, err := opening.Wait(context.Background()); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	lost, err := conversation.Send(context.Background(), "again")
	if err != nil {
		t.Fatalf("second Send: %v", err)
	}
	if _, err := lost.Wait(context.Background()); err == nil {
		t.Fatal("the turn succeeded, want it to fail when its stream is lost")
	}
	for _, step := range []string{"resend", "repeat"} {
		reply, err := conversation.Send(context.Background(), "again")
		if err != nil {
			t.Fatalf("%s Send: %v", step, err)
		}
		if _, err := reply.Wait(context.Background()); err != nil {
			t.Fatalf("%s Wait: %v", step, err)
		}
	}

	original, resent, repeated := <-messages, <-messages, <-messages
	for _, frame := range []*agentcomposev2.AttachAgentRunRequest{original, resent, repeated} {
		if text := frame.GetHumanMessage().GetText(); text != "again" {
			t.Fatalf("frame carried %q, want the message under test", text)
		}
	}
	if original.GetClientFrameId() == "" {
		t.Fatal("the message carried no identity, so a resend of it cannot be recognised")
	}
	if resent.GetClientFrameId() != original.GetClientFrameId() {
		t.Errorf("the resend travelled as %q, want the lost turn's %q: it is the same message",
			resent.GetClientFrameId(), original.GetClientFrameId())
	}
	// The turn before it completed, so this one is a person saying the same
	// thing again and has to be recorded on its own.
	if repeated.GetClientFrameId() == original.GetClientFrameId() {
		t.Errorf("a repeat after a completed turn reused %q, and would be swallowed as a duplicate",
			repeated.GetClientFrameId())
	}
}
