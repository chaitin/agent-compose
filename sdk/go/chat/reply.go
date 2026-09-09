package chat

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync"
	"time"
)

// Reply is one turn of a conversation: the agent's answer to a single message.
//
// Events and Wait may both be used on the same Reply, in either order and any
// number of times; events are retained, so a later call replays them from the
// start rather than seeing only what has not been consumed yet.
type Reply struct {
	conversation *Conversation

	mu     sync.Mutex
	events []Event
	notify chan struct{}
	text   strings.Builder
	result json.RawMessage
	at     time.Time
	done   bool
	err    error
}

func newReply(conversation *Conversation) *Reply {
	return &Reply{conversation: conversation, notify: make(chan struct{})}
}

// Events yields the agent's observations as they arrive, ending when the turn
// completes. A failed turn yields its error as the final pair.
func (r *Reply) Events(ctx context.Context) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		for index := 0; ; {
			r.mu.Lock()
			if index < len(r.events) {
				event := r.events[index]
				r.mu.Unlock()
				index++
				if !yield(event, nil) {
					return
				}
				continue
			}
			if r.done {
				err := r.err
				r.mu.Unlock()
				if err != nil {
					yield(nil, err)
				}
				return
			}
			wait := r.notify
			r.mu.Unlock()
			select {
			case <-wait:
			case <-ctx.Done():
				yield(nil, ctx.Err())
				return
			}
		}
	}
}

// Wait blocks until the turn completes and returns the agent's message. It
// does not require the caller to have consumed Events.
func (r *Reply) Wait(ctx context.Context) (Message, error) {
	for {
		r.mu.Lock()
		if r.done {
			message := Message{Role: RoleAssistant, Text: r.text.String(), Result: r.result, Time: r.at}
			err := r.err
			r.mu.Unlock()
			return message, err
		}
		wait := r.notify
		r.mu.Unlock()
		select {
		case <-wait:
		case <-ctx.Done():
			return Message{}, ctx.Err()
		}
	}
}

// Text reports the answer accumulated so far. It is safe to call while the
// turn is still streaming.
func (r *Reply) Text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.text.String()
}

// Done reports whether the turn has finished.
func (r *Reply) Done() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.done
}

// Err reports why the turn failed, or nil while it is running or once it
// succeeded.
func (r *Reply) Err() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// Interrupt stops the agent mid-turn.
//
// The daemon cancels the whole interactive session rather than the single
// turn, so this Reply ends and the next [Conversation.Send] needs a new run.
// That run asks to resume the same sandbox; [Conversation.Continuity] reports
// [Restarted] only if that does not happen. Either way the agent keeps the
// conversation's durable history.
func (r *Reply) Interrupt(ctx context.Context) error {
	return r.conversation.interrupt(ctx, r)
}

// add records one event and wakes every waiter.
func (r *Reply) add(event Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	if delta, ok := event.(*TextDeltaEvent); ok {
		r.text.WriteString(delta.Text)
	}
	r.events = append(r.events, event)
	r.wake()
}

// finish completes the turn. Only the first call takes effect, so a stream
// error arriving after a turn already completed cannot overwrite its result.
func (r *Reply) finish(result json.RawMessage, at time.Time, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done {
		return
	}
	r.done = true
	r.err = err
	if len(result) > 0 && string(result) != "null" {
		r.result = result
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	r.at = at
	r.wake()
}

// wake must be called with r.mu held.
func (r *Reply) wake() {
	close(r.notify)
	r.notify = make(chan struct{})
}
