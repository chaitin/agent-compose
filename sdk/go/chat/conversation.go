package chat

import (
	"cmp"
	"connectrpc.com/connect"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// historyPageSize bounds one page of a history read. The daemon rejects pages
// above 500.
const historyPageSize = 200

// Conversation is a durable thread of turns with one Agent.
//
// It is not safe for concurrent use and allows one [Reply] in flight at a
// time; a [Conversation.Send] issued while an earlier Reply is still streaming
// returns [ErrBusy].
type Conversation struct {
	agent  *Agent
	id     string
	labels map[string]string

	mu     sync.Mutex
	stream *attachStream
	// cancel tears down the stream's own context. The stream outlives any one
	// Send, so it must not borrow that call's context.
	cancel     context.CancelFunc
	runID      string
	sandboxID  string
	continuity Continuity
	current    *Reply
	closed     bool
	// sentFrameID identifies the message of the turn in flight, and
	// retryFrameID that of a turn dropStream ended without an outcome.
	// Resending the latter verbatim reuses its ID, which is what tells the
	// daemon the second copy is the same message.
	sentFrameID  string
	sentText     string
	retryFrameID string
	retryText    string
	// pendingRun records that this handle asked the daemon to start a run and
	// has not yet been told its ID. The daemon reports that ID asynchronously,
	// in the start frame, so between the request and that frame there is a run
	// — holding a sandbox, and asked to survive a disconnect — that this handle
	// cannot yet name. Close has to end it anyway.
	pendingRun bool
}

// Start returns a new conversation with this Agent.
//
// It performs no I/O: the agent's environment is provisioned by the first
// [Conversation.Send], whose message opens the session. A product can
// therefore create a Conversation per UI thread and pay for it only once
// someone actually writes something.
func (a *Agent) Start(opts ...Option) *Conversation {
	resolved := newOptions(opts)
	return &Conversation{
		agent:      a,
		id:         resolved.id,
		labels:     resolved.labels,
		continuity: Continuous,
	}
}

// Open resumes the conversation identified by id.
//
// When its environment is still alive, Open reattaches and
// [Conversation.Continuity] reports [Continuous]. When the environment is
// gone, Open by default returns a conversation that will rebuild it on the
// next Send and reports [Restarted]; [WithRestartIfGone] set to false makes
// Open return [ErrNotFound] instead. Earlier turns stay readable through
// [Conversation.History] either way.
func (a *Agent) Open(ctx context.Context, id string, opts ...Option) (*Conversation, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, invalidArgument("Open", "conversation ID is required")
	}
	resolved := newOptions(append([]Option{WithID(id)}, opts...))
	conversation := &Conversation{agent: a, id: id, labels: resolved.labels}

	run, err := conversation.latestRun(ctx, "Open")
	if err != nil {
		return nil, err
	}
	if run == nil || !runIsLive(run) {
		if !resolved.restartIfGone {
			return nil, &Error{Op: "Open", Code: "not_found", Message: "conversation " + id + " is no longer running"}
		}
		// A conversation whose last run has ended is the ordinary case, not a
		// lost one: runs end whenever a client closes, and the sandbox they
		// used survives with the workspace and the directories a provider
		// keeps its session in. Carrying that sandbox forward is what lets the
		// next run resume the conversation instead of building a new
		// environment beside it.
		//
		// Whether the resume actually happened is not decided here: the
		// daemon's start frame reports which sandbox the new run got, and
		// handle compares it against the one asked for.
		if run != nil {
			conversation.sandboxID = run.GetSandboxId()
		}
		if conversation.sandboxID == "" {
			conversation.continuity = Restarted
		}
		return conversation, nil
	}
	conversation.runID = run.GetRunId()
	conversation.sandboxID = run.GetSandboxId()
	// attach starts the read loop, and the frames it handles write these same
	// fields. Everything from here on is shared state.
	conversation.mu.Lock()
	defer conversation.mu.Unlock()
	if err := conversation.attach(ctx, "Open", ""); err != nil {
		if !resolved.restartIfGone {
			return nil, err
		}
		// The run could not be attached to, but the sandbox it used is still
		// the one to resume; only the run is given up on.
		conversation.runID = ""
		return conversation, nil
	}
	conversation.continuity = Continuous
	return conversation, nil
}

// ID reports the conversation's identity. Persist it to resume the
// conversation later through [Agent.Open].
func (c *Conversation) ID() string { return c.id }

// Continuity reports whether this conversation kept the environment its
// earlier turns ran in.
func (c *Conversation) Continuity() Continuity {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.continuity == "" {
		return Continuous
	}
	return c.continuity
}

// RunID reports the daemon-side identifier backing this conversation, for
// correlating with daemon logs. It is empty before the first Send and changes
// whenever the conversation restarts.
func (c *Conversation) RunID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.runID
}

// Send contributes a message and returns the agent's [Reply], which streams
// while the agent works.
//
// A Reply that fails because the stream broke leaves the turn's outcome
// unknown: the daemon was asked to keep the run alive without a viewer, so the
// agent may well have finished the turn. Resending the same text is the way to
// recover, and doing so immediately does not record the message twice — it
// travels under the identity the lost turn already had. The agent can still
// work through it a second time, so a caller that cares should read
// [Conversation.History] before resending.
func (c *Conversation) Send(ctx context.Context, text string) (*Reply, error) {
	if strings.TrimSpace(text) == "" {
		return nil, invalidArgument("Send", "message text is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	if c.current != nil && !c.current.Done() {
		return nil, ErrBusy
	}
	frameID := c.turnFrameID(text)
	if c.stream == nil {
		// No live stream: either this is the conversation's first message, or
		// an earlier one was lost. attach reattaches when a run survived and
		// otherwise provisions a new environment, carrying this message as the
		// opening prompt.
		reattached := c.runID != ""
		if err := c.attach(ctx, "Send", text); err != nil {
			return nil, err
		}
		if reattached {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			// A reattach start frame carries no prompt, so the message still
			// has to be sent.
			if err := c.stream.Send(humanMessage(text, frameID)); err != nil {
				return nil, fromConnect("Send", err)
			}
		} else {
			// The message went out as the new run's opening prompt, which the
			// daemon records under an identity derived from the run itself.
			// There is nothing for a frame ID to key, and no earlier copy for
			// a resend to collide with: a resend of a turn that never reached
			// a run starts a run of its own.
			frameID = ""
		}
	} else if err := c.stream.Send(humanMessage(text, frameID)); err != nil {
		return nil, fromConnect("Send", err)
	}
	c.sentFrameID, c.sentText = frameID, text
	c.retryFrameID, c.retryText = "", ""
	reply := newReply(c)
	c.current = reply
	return reply, nil
}

// turnFrameID returns the client frame ID this turn's message carries.
//
// The daemon keys a human message's persisted identity on this value, so a
// turn that ended without an outcome and is then resent verbatim can reuse the
// ID it already had: the daemon recognises the second copy as the message it
// already recorded instead of writing it down twice.
//
// Only the Send immediately after such a break qualifies, and only for the
// same text. Reuse is what collapses two copies into one message, so widening
// it any further would start swallowing messages a person really did repeat.
//
// Callers must hold c.mu.
func (c *Conversation) turnFrameID(text string) string {
	if c.retryFrameID != "" && c.retryText == text {
		return c.retryFrameID
	}
	return newFrameID()
}

// Ask sends a message and waits for the complete answer.
func (c *Conversation) Ask(ctx context.Context, text string) (Message, error) {
	reply, err := c.Send(ctx, text)
	if err != nil {
		return Message{}, err
	}
	return reply.Wait(ctx)
}

// History returns every message of the conversation, oldest first, including
// turns that ran while no client was attached. Only user and assistant text
// comes back; tool calls, reasoning, usage and every other event kind are
// turn-scoped and not durable messages.
//
// This fetches the whole conversation, at a cost proportional to how many
// runs it has occupied. A caller that wants a bounded, incremental read —
// the common case for a chat UI's initial load — should use
// [Conversation.HistoryPage] instead.
func (c *Conversation) History(ctx context.Context) ([]Message, error) {
	runs, err := c.runs(ctx, "History")
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(runs)*4)
	for _, run := range runs {
		page, err := c.runMessages(ctx, run.GetRunId())
		if err != nil {
			return nil, err
		}
		messages = append(messages, page...)
	}
	return messages, nil
}

// Close releases everything this handle is holding: its stream, and the run
// behind it whose environment the daemon then stops. The conversation itself
// survives — its history and identity are untouched, and a later
// [Agent.Open] resumes it with the agent's context intact.
//
// Closing has to end the run. A conversation attaches with a disconnect
// policy that deliberately keeps the run alive when the stream drops, because
// a turn must survive its viewer going away; the cost is that nothing else
// ever ends it. A Close that only dropped the stream therefore left a run —
// and the sandbox it holds — alive for every conversation ever opened.
//
// A run this handle started but was never named is stopped too. The daemon
// reports a run's ID asynchronously, in the start frame, while [Conversation.Send]
// returns as soon as the request is away — so a Close right after a Send can
// arrive with a run already running and no ID to stop it by. Such a run is
// found through the conversation's identity label instead.
//
// A handle that never attached holds no run, so closing it is free — in
// particular it cannot end a run some other handle is holding. Use
// [Client.EndSession] to end a session no handle of yours is attached to.
func (c *Conversation) Close(ctx context.Context) error {
	c.mu.Lock()
	opened, current, cancel, runID := c.stream, c.current, c.cancel, c.runID
	pending := c.pendingRun
	c.stream, c.current, c.cancel = nil, nil, nil
	c.pendingRun = false
	c.closed = true
	c.mu.Unlock()

	if current != nil {
		current.finish(nil, time.Time{}, ErrClosed)
	}
	if cancel != nil {
		cancel()
	}

	var failures []error
	if opened != nil {
		// Deliberately not waiting for the reader: a daemon that has stopped
		// answering must not be able to hold Close up. Cancelling the stream's
		// context is what lets the reader finish.
		if err := opened.CloseRequest(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			failures = append(failures, fromConnect("Close", err))
		}
	}
	// A lost stream is not a stopped run either: dropStream keeps a known run
	// ID precisely because the run outlives its viewer, and a run whose start
	// frame never arrived leaves nothing behind to keep. Both still need
	// stopping, so the run to end is decided here rather than per branch.
	if runID == "" && pending {
		found, err := c.unnamedRun(ctx)
		if err != nil {
			failures = append(failures, err)
		}
		runID = found
	}
	if runID != "" {
		if err := c.stopRun(ctx, runID); err != nil {
			failures = append(failures, err)
		} else {
			c.clearRunID(runID)
		}
	}
	return errors.Join(failures...)
}

// unnamedRun finds the run this handle started before the daemon said what it
// was called, and returns an empty ID when there is none to find.
//
// The run carries this conversation's identity label, so it can be asked for
// by that instead, and the newest run under the label is this one: the only
// way to reach here is to have just asked for a new run, which by construction
// is the most recent.
//
// One sliver stays open. A Close fast enough to beat the daemon's own creation
// of the run finds either nothing, or the previous session's run — already
// terminal, since a new run was being started in its place, and stopping a run
// that has finished changes nothing. Closing that sliver properly means the
// daemon not keeping a run whose client left before the handshake finished,
// which is its call to make, not this package's.
func (c *Conversation) unnamedRun(ctx context.Context) (string, error) {
	run, err := c.agent.client.latestMatchingRun(ctx, "Close", Search{
		Labels: map[string]string{conversationLabel: c.id},
	})
	if err != nil || run == nil {
		return "", err
	}
	return run.GetRunId(), nil
}

func (c *Conversation) clearRunID(runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.runID == runID {
		c.runID = ""
	}
}

// interrupt stops the agent mid-turn.
//
// The daemon's cancel ends the whole interactive session, not just the
// current turn, so the next Send needs a new run. That run asks to resume
// the same sandbox, the same as any other run boundary; Continuity reports
// whether it actually did.
func (c *Conversation) interrupt(ctx context.Context, reply *Reply) error {
	c.mu.Lock()
	stream := c.stream
	current := c.current
	c.mu.Unlock()
	if current != reply || reply.Done() {
		return nil
	}
	if stream == nil {
		return ErrClosed
	}
	_ = ctx
	return fromConnect("Interrupt", stream.Send(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Cancel{Cancel: &agentcomposev2.AttachCancel{}},
	}))
}

// attach opens the interactive session. prompt is the opening message when
// starting a new one, and empty when reattaching to an existing run.
//
// Callers must hold c.mu.
func (c *Conversation) attach(ctx context.Context, op, prompt string) error {
	// The stream belongs to the conversation, not to this call: a Send whose
	// context is a single HTTP request's must not take the session down with
	// it when that request ends. Values are kept, cancellation is not.
	streamCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	opened := c.agent.client.transport.runs.AttachAgentRun(streamCtx)
	start := &agentcomposev2.AttachAgentRunStart{
		Mode:             agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT,
		AttachStdin:      true,
		DisconnectPolicy: agentcomposev2.AttachDisconnectPolicy_ATTACH_DISCONNECT_POLICY_DETACH,
	}
	if c.runID != "" {
		start.RunId = c.runID
	} else {
		labels := maps.Clone(c.labels)
		if labels == nil {
			labels = map[string]string{}
		}
		labels[conversationLabel] = c.id
		start.Request = &agentcomposev2.RunAgentRequest{
			ProjectId: c.agent.projectID,
			AgentName: c.agent.name,
			Prompt:    prompt,
			// One run serves every turn of an open session, so the
			// environment already outlives a turn without being pinned:
			// stopping it when the run itself ends is enough. Keeping it
			// running instead left an environment alive for every
			// conversation that had ever been opened, since a conversation
			// ends far more often than it is archived.
			//
			// The sandbox survives the stop — the daemon keeps its metadata,
			// workspace, and the mounted home directories where a provider
			// stores the session it resumes from — so the next run asks for
			// this same sandbox by ID and picks the conversation back up.
			CleanupPolicy: agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION,
			// A prior run's sandbox may still be alive even though that run
			// ended — ask the daemon to resume it rather than starting from
			// nothing. handle's Started case reports whether this actually
			// happened.
			SandboxId: c.sandboxID,
			Labels:    labels,
		}
	}
	// The opening frame is what makes the daemon answer: a Connect handler
	// writes no response headers until it has received one, so a failure to
	// open the stream surfaces here rather than at AttachAgentRun.
	if err := opened.Send(&agentcomposev2.AttachAgentRunRequest{
		Frame: &agentcomposev2.AttachAgentRunRequest_Start{Start: start},
	}); err != nil {
		cancel()
		_ = opened.CloseRequest()
		_ = opened.CloseResponse()
		return fromConnect(op, err)
	}
	c.stream = opened
	c.cancel = cancel
	// The frame is away, so from the daemon's side a run may now exist. Only a
	// start frame carrying a request creates one; a reattach names a run this
	// handle already knows.
	c.pendingRun = start.Request != nil
	go c.readLoop(opened)
	return nil
}

// readLoop translates server frames until the stream ends.
func (c *Conversation) readLoop(stream *attachStream) {
	for {
		frame, err := stream.Receive()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			} else {
				err = fromConnect("Send", err)
			}
			c.endSession(stream, err)
			return
		}
		c.handle(stream, frame)
	}
}

// humanMessage builds the frame carrying one turn's message.
func humanMessage(text, frameID string) *agentcomposev2.AttachAgentRunRequest {
	return &agentcomposev2.AttachAgentRunRequest{
		ClientFrameId: frameID,
		Frame: &agentcomposev2.AttachAgentRunRequest_HumanMessage{
			HumanMessage: &agentcomposev2.AttachHumanMessage{Text: text},
		},
	}
}

// stopRun ends the run backing this conversation.
func (c *Conversation) stopRun(ctx context.Context, runID string) error {
	_, err := c.agent.client.transport.runs.StopRun(ctx, connect.NewRequest(&agentcomposev2.StopRunRequest{
		RunId:  runID,
		Reason: "conversation closed",
	}))
	return fromConnect("Close", err)
}

func (c *Conversation) handle(stream *attachStream, frame *agentcomposev2.AttachAgentRunResponse) {
	switch body := frame.GetFrame().(type) {
	case *agentcomposev2.AttachAgentRunResponse_Started:
		started := body.Started
		c.mu.Lock()
		requested := c.sandboxID
		c.runID = started.GetRunId()
		c.sandboxID = started.GetSandboxId()
		c.pendingRun = false
		// A run starting is not by itself a restart: what matters is whether
		// the sandbox this run got is the one the prior run used. requested
		// is empty for a conversation's very first run, which is not a
		// restart either — there is nothing yet for it to have lost.
		if requested != "" && started.GetSandboxId() != "" {
			if started.GetSandboxId() == requested {
				c.continuity = Continuous
			} else {
				c.continuity = Restarted
			}
		}
		c.mu.Unlock()

	case *agentcomposev2.AttachAgentRunResponse_AgentEvent:
		at := c.eventTime(frame)
		agentEvent := body.AgentEvent
		event, err := decodeEvent(agentEvent.GetName(), []byte(agentEvent.GetPayloadJson()), at)
		if err != nil || event == nil {
			// A malformed typed payload is still a valid Attach event. Preserve
			// its wire fields instead of losing it or failing the whole turn.
			event = &RawEvent{eventAt: eventAt{Time: at}, Name: agentEvent.GetName()}
		}
		if raw, ok := event.(*RawEvent); ok {
			raw.Text = agentEvent.GetText()
			raw.PayloadJSON = agentEvent.GetPayloadJson()
		}
		if reply := c.reply(); reply != nil {
			reply.add(event)
		}

	case *agentcomposev2.AttachAgentRunResponse_AgentTurnCompleted:
		if reply := c.takeReply(); reply != nil {
			reply.finish(json.RawMessage(body.AgentTurnCompleted.GetResultJson()), c.eventTime(frame), nil)
		}

	case *agentcomposev2.AttachAgentRunResponse_Result:
		var err error
		if !body.Result.GetSuccess() {
			err = &Error{Op: "Send", Message: cmp.Or(strings.TrimSpace(body.Result.GetError()), "the agent run failed")}
		}
		c.finishSession(stream, err, body.Result.GetResultJson(), c.eventTime(frame))

	case *agentcomposev2.AttachAgentRunResponse_Error:
		failure := &Error{Op: "Send", Code: body.Error.GetCode(), Message: body.Error.GetMessage()}
		if body.Error.GetTerminal() {
			c.endSession(stream, failure)
			return
		}
		if reply := c.reply(); reply != nil {
			reply.add(&ErrorEvent{
				eventAt:  eventAt{Time: c.eventTime(frame)},
				Severity: SeverityError,
				Code:     body.Error.GetCode(),
				Message:  body.Error.GetMessage(),
			})
		}
	}
}

func (c *Conversation) eventTime(frame *agentcomposev2.AttachAgentRunResponse) time.Time {
	if at := frame.GetCreatedAt(); at.IsValid() {
		return at.AsTime()
	}
	return time.Now().UTC()
}

func (c *Conversation) reply() *Reply {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.current
}

func (c *Conversation) takeReply() *Reply {
	c.mu.Lock()
	defer c.mu.Unlock()
	reply := c.current
	c.current = nil
	// The turn reached its outcome, so there is nothing left to resend: the
	// same text arriving later is a person saying it again, and must be
	// recorded as its own message.
	c.sentFrameID, c.sentText = "", ""
	return reply
}

// finishSession records that the run itself reached a terminal state, so the
// next Send must start a new one. The sandbox this run used is not
// necessarily gone — CleanupPolicy asked the daemon to keep it running, and
// the next attach asks to resume it — so Continuity is left as it was here;
// handle's Started case sets it once that next attach reports whether the
// resume actually happened.
func (c *Conversation) finishSession(stream *attachStream, err error, resultJSON string, at time.Time) {
	c.mu.Lock()
	if c.stream != stream {
		// A later attach already replaced this one. Its turn is not this
		// loop's to end.
		c.mu.Unlock()
		return
	}
	reply, cancel := c.current, c.cancel
	c.current, c.stream, c.cancel, c.runID = nil, nil, nil, ""
	c.pendingRun = false
	c.sentFrameID, c.sentText = "", ""
	c.retryFrameID, c.retryText = "", ""
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if reply != nil {
		reply.finish(json.RawMessage(resultJSON), at, err)
	}
}

// dropStream records that this client lost the stream while the run may well
// still be alive, which is what a network blip looks like. The run ID is kept
// so the next Send reattaches to the same session rather than abandoning an
// environment that still holds the conversation's context.
//
// A turn in flight still fails: its outcome is genuinely unknown here.
func (c *Conversation) dropStream(stream *attachStream, err error) {
	c.mu.Lock()
	if c.stream != stream {
		// This loop's stream is already gone — finished, closed, or replaced
		// by a later attach. Tearing down what is there now would end a turn
		// this loop has nothing to do with.
		c.mu.Unlock()
		return
	}
	reply, cancel := c.current, c.cancel
	c.current, c.stream, c.cancel = nil, nil, nil
	// The turn's message may or may not have been recorded, so keep its frame
	// ID: a resend of the same text is the same message, and reusing the ID is
	// what stops the daemon from writing it down a second time.
	c.retryFrameID, c.retryText = c.sentFrameID, c.sentText
	c.sentFrameID, c.sentText = "", ""
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if reply != nil {
		reply.finish(nil, time.Time{}, err)
	}
}

// endSession handles the stream ending, whether cleanly or in error.
func (c *Conversation) endSession(stream *attachStream, err error) {
	if err == nil {
		err = &Error{Op: "Send", Code: "unavailable", Message: "the conversation stream ended before the turn completed"}
	}
	c.dropStream(stream, err)
}

// runs returns every run this conversation has occupied, oldest first.
func (c *Conversation) runs(ctx context.Context, op string) ([]*agentcomposev2.RunSummary, error) {
	var runs []*agentcomposev2.RunSummary
	for offset := uint32(0); ; {
		response, err := c.agent.client.transport.runs.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
			ProjectId: c.agent.projectID, AgentName: c.agent.name,
			Labels: map[string]string{conversationLabel: c.id},
			Offset: offset, Limit: historyPageSize,
		}))
		if err != nil {
			return nil, fromConnect(op, err)
		}
		page := response.Msg.GetRuns()
		runs = append(runs, page...)
		if len(page) == 0 || offset+uint32(len(page)) >= response.Msg.GetTotal() {
			break
		}
		offset += uint32(len(page))
	}
	slices.SortStableFunc(runs, func(a, b *agentcomposev2.RunSummary) int {
		return a.GetCreatedAt().AsTime().Compare(b.GetCreatedAt().AsTime())
	})
	return runs, nil
}

// latestRun returns the conversation's most recent run, or nil when it has
// none.
func (c *Conversation) latestRun(ctx context.Context, op string) (*agentcomposev2.RunSummary, error) {
	runs, err := c.runs(ctx, op)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return runs[len(runs)-1], nil
}

// runMessages reads one run's durable messages, oldest first.
func (c *Conversation) runMessages(ctx context.Context, runID string) ([]Message, error) {
	messages := make([]Message, 0, historyPageSize)
	for offset := uint32(0); ; {
		response, err := c.agent.client.transport.runs.ListRunEvents(ctx, connect.NewRequest(&agentcomposev2.ListRunEventsRequest{
			RunId: runID, Offset: offset, Limit: historyPageSize,
		}))
		if err != nil {
			return nil, fromConnect("History", err)
		}
		events := response.Msg.GetEvents()
		for _, event := range events {
			if message, ok := messageFromEvent(event); ok {
				messages = append(messages, message)
			}
		}
		offset += uint32(len(events))
		if len(events) == 0 || offset >= response.Msg.GetTotal() {
			return messages, nil
		}
	}
}
