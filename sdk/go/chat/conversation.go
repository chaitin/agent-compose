package chat

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
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

	mu         sync.Mutex
	stream     *stream
	loopDone   chan struct{}
	runID      string
	sandboxID  string
	continuity Continuity
	current    *Reply
	closed     bool
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
	if run == nil || !run.live() {
		if !resolved.restartIfGone {
			return nil, &Error{Op: "Open", Code: "not_found", Message: "conversation " + id + " is no longer running"}
		}
		conversation.continuity = Restarted
		return conversation, nil
	}
	conversation.runID = run.RunID
	conversation.sandboxID = run.SandboxID
	if err := conversation.attach(ctx, "Open", ""); err != nil {
		if !resolved.restartIfGone {
			return nil, err
		}
		conversation.runID = ""
		conversation.sandboxID = ""
		conversation.continuity = Restarted
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
func (c *Conversation) Send(ctx context.Context, text string) (*Reply, error) {
	if strings.TrimSpace(text) == "" {
		return nil, invalidArgument("Send", "message text is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, ErrClosed
	}
	if c.current != nil && !c.current.Done() {
		return nil, ErrBusy
	}
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
			// A reattach start frame carries no prompt, so the message still
			// has to be sent.
			if err := c.stream.send(wireAttachRequest{HumanMessage: &wireHumanMessage{Text: text}}); err != nil {
				return nil, err
			}
		}
	} else if err := c.stream.send(wireAttachRequest{HumanMessage: &wireHumanMessage{Text: text}}); err != nil {
		return nil, err
	}
	reply := newReply(c)
	c.current = reply
	return reply, nil
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
// turns that ran while no client was attached.
func (c *Conversation) History(ctx context.Context) ([]Message, error) {
	runs, err := c.runs(ctx, "History")
	if err != nil {
		return nil, err
	}
	messages := make([]Message, 0, len(runs)*4)
	for _, run := range runs {
		page, err := c.runMessages(ctx, run.RunID)
		if err != nil {
			return nil, err
		}
		messages = append(messages, page...)
	}
	return messages, nil
}

// Close releases this client's hold on the conversation and leaves it intact
// on the server, so a later [Agent.Open] resumes it. Work already in progress
// keeps running; its events are readable from [Conversation.History] on
// return.
//
// Close is the right way to finish with a conversation. Use
// [Conversation.Delete] only to end one for good.
func (c *Conversation) Close() error {
	c.mu.Lock()
	stream, current := c.stream, c.current
	c.stream, c.current, c.closed = nil, nil, true
	done := c.loopDone
	c.loopDone = nil
	c.mu.Unlock()

	if current != nil {
		current.finish(nil, time.Time{}, ErrClosed)
	}
	if stream == nil {
		return nil
	}
	err := stream.close()
	if done != nil {
		<-done
	}
	if err != nil && !errors.Is(err, io.ErrClosedPipe) {
		return err
	}
	return nil
}

// Delete ends the conversation for good: the agent stops and its environment
// is released. Durable history remains readable through the daemon, but
// [Agent.Open] can no longer resume this conversation.
func (c *Conversation) Delete(ctx context.Context) error {
	c.mu.Lock()
	runID := c.runID
	c.mu.Unlock()
	if runID == "" {
		run, err := c.latestRun(ctx, "Delete")
		if err != nil {
			return err
		}
		if run != nil {
			runID = run.RunID
		}
	}
	closeErr := c.Close()
	if runID == "" {
		return closeErr
	}
	request := wireStopRunRequest{RunID: runID, Reason: "conversation deleted"}
	if err := c.agent.client.transport.unary(ctx, "Delete", "StopRun", request, nil); err != nil {
		return err
	}
	return closeErr
}

// interrupt stops the agent mid-turn.
//
// The daemon's cancel ends the whole interactive session, not just the current
// turn, so the conversation's environment goes with it: the next Send rebuilds
// and Continuity reports Restarted.
func (c *Conversation) interrupt(ctx context.Context) error {
	c.mu.Lock()
	stream := c.stream
	c.mu.Unlock()
	if stream == nil {
		return ErrClosed
	}
	_ = ctx
	return stream.send(wireAttachRequest{Cancel: &struct{}{}})
}

// attach opens the interactive session. prompt is the opening message when
// starting a new one, and empty when reattaching to an existing run.
//
// Callers must hold c.mu, except in Open where the conversation is not yet
// shared.
func (c *Conversation) attach(ctx context.Context, op, prompt string) error {
	stream, err := c.agent.client.transport.openStream(ctx, op, "AttachAgentRun")
	if err != nil {
		return err
	}
	start := &wireAttachStart{
		Mode:             attachModePrompt,
		AttachStdin:      true,
		DisconnectPolicy: attachPolicyDetach,
	}
	if c.runID != "" {
		start.RunID = c.runID
	} else {
		labels := maps.Clone(c.labels)
		if labels == nil {
			labels = map[string]string{}
		}
		labels[conversationLabel] = c.id
		start.Request = &wireRunRequest{
			ProjectID: c.agent.projectID,
			AgentName: c.agent.name,
			Prompt:    prompt,
			// The environment must outlive each turn; it is what carries the
			// agent's context forward. Conversation.Delete releases it.
			CleanupPolicy: cleanupPolicyKeepLive,
			Labels:        labels,
		}
	}
	if err := stream.send(wireAttachRequest{Start: start}); err != nil {
		_ = stream.close()
		return err
	}
	done := make(chan struct{})
	c.stream = stream
	c.loopDone = done
	go c.readLoop(stream, done)
	return nil
}

// readLoop translates server frames until the stream ends.
func (c *Conversation) readLoop(stream *stream, done chan struct{}) {
	defer close(done)
	for {
		var frame wireAttachResponse
		err := stream.recv(&frame)
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = nil
			}
			c.endSession(err)
			return
		}
		c.handle(frame)
	}
}

func (c *Conversation) handle(frame wireAttachResponse) {
	switch {
	case frame.Started != nil:
		c.mu.Lock()
		c.runID = frame.Started.RunID
		c.sandboxID = frame.Started.SandboxID
		c.mu.Unlock()

	case frame.AgentEvent != nil:
		event, err := decodeEvent(frame.AgentEvent.Name, []byte(frame.AgentEvent.PayloadJSON), c.eventTime(frame))
		if err != nil || event == nil {
			// An unknown kind, or a payload this build cannot parse. Dropping
			// it is better than failing the turn over an event the caller may
			// not even use.
			return
		}
		if reply := c.reply(); reply != nil {
			reply.add(event)
		}

	case frame.TurnComplete != nil:
		if reply := c.takeReply(); reply != nil {
			reply.finish(json.RawMessage(frame.TurnComplete.ResultJSON), c.eventTime(frame), nil)
		}

	case frame.Result != nil:
		var err error
		if !frame.Result.Success {
			err = &Error{Op: "Send", Message: cmp.Or(strings.TrimSpace(frame.Result.Error), "the agent run failed")}
		}
		c.finishSession(err, frame.Result.ResultJSON, c.eventTime(frame))

	case frame.Error != nil:
		failure := &Error{Op: "Send", Code: frame.Error.Code, Message: frame.Error.Message}
		if frame.Error.Terminal {
			c.endSession(failure)
			return
		}
		if reply := c.reply(); reply != nil {
			reply.add(&ErrorEvent{
				eventAt:  eventAt{Time: c.eventTime(frame)},
				Severity: SeverityError,
				Code:     frame.Error.Code,
				Message:  frame.Error.Message,
			})
		}
	}
}

func (c *Conversation) eventTime(frame wireAttachResponse) time.Time {
	if !frame.CreatedAt.IsZero() {
		return frame.CreatedAt
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
	return reply
}

// finishSession records that the run itself reached a terminal state. The
// environment is gone with it, so the next Send builds a new one.
func (c *Conversation) finishSession(err error, resultJSON string, at time.Time) {
	c.mu.Lock()
	reply := c.current
	c.current, c.stream, c.runID = nil, nil, ""
	c.continuity = Restarted
	c.mu.Unlock()
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
func (c *Conversation) dropStream(err error) {
	c.mu.Lock()
	reply := c.current
	c.current, c.stream = nil, nil
	c.mu.Unlock()
	if reply != nil {
		reply.finish(nil, time.Time{}, err)
	}
}

// endSession handles the stream ending, whether cleanly or in error.
func (c *Conversation) endSession(err error) {
	if err == nil {
		err = &Error{Op: "Send", Code: "unavailable", Message: "the conversation stream ended before the turn completed"}
	}
	c.dropStream(err)
}

// runs returns every run this conversation has occupied, oldest first.
func (c *Conversation) runs(ctx context.Context, op string) ([]wireRunSummary, error) {
	request := wireListRunsRequest{
		ProjectID: c.agent.projectID,
		AgentName: c.agent.name,
		Labels:    map[string]string{conversationLabel: c.id},
		Limit:     historyPageSize,
	}
	var response wireListRunsResponse
	if err := c.agent.client.transport.unary(ctx, op, "ListRuns", request, &response); err != nil {
		return nil, err
	}
	runs := response.Runs
	slices.SortStableFunc(runs, func(a, b wireRunSummary) int { return a.CreatedAt.Compare(b.CreatedAt) })
	return runs, nil
}

// latestRun returns the conversation's most recent run, or nil when it has
// none.
func (c *Conversation) latestRun(ctx context.Context, op string) (*wireRunSummary, error) {
	runs, err := c.runs(ctx, op)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, nil
	}
	return &runs[len(runs)-1], nil
}

// runMessages reads one run's durable messages, oldest first.
func (c *Conversation) runMessages(ctx context.Context, runID string) ([]Message, error) {
	messages := make([]Message, 0, historyPageSize)
	for offset := uint32(0); ; {
		request := wireListEventsRequest{RunID: runID, Offset: offset, Limit: historyPageSize}
		var response wireListEventsResponse
		if err := c.agent.client.transport.unary(ctx, "History", "ListRunEvents", request, &response); err != nil {
			return nil, err
		}
		for _, event := range response.Events {
			if message, ok := messageFromEvent(event); ok {
				messages = append(messages, message)
			}
		}
		offset += uint32(len(response.Events))
		if len(response.Events) == 0 || offset >= response.Total {
			return messages, nil
		}
	}
}
