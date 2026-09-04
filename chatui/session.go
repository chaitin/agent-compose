package main

import (
	"context"
	"iter"
	"sync"
	"time"

	"github.com/chaitin/agent-compose/sdk/go/chat"
)

// session is one conversation plus the viewers watching it.
//
// Viewers are not fanned out to. Every viewer ranges over the same
// [chat.Reply], which retains its events and replays them for each caller, so
// a browser that connects halfway through a turn still sees that turn from the
// beginning and one that connects between turns simply waits.
type session struct {
	conversation *chat.Conversation

	mu      sync.Mutex
	current *chat.Reply
	// prompt is the message the current turn answers, so a viewer that did not
	// send it still sees what was asked.
	prompt string
	// turn closes and is replaced whenever a turn begins, waking every viewer
	// waiting for one.
	turn    chan struct{}
	viewers int
	idle    time.Time
}

func newSession(conversation *chat.Conversation) *session {
	return &session{conversation: conversation, turn: make(chan struct{}), idle: time.Now()}
}

// send contributes a message and publishes the resulting turn to viewers.
func (s *session) send(ctx context.Context, text string) error {
	reply, err := s.conversation.Send(ctx, text)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.current, s.prompt = reply, text
	close(s.turn)
	s.turn = make(chan struct{})
	return nil
}

// interrupt stops the turn in flight, reporting whether there was one.
func (s *session) interrupt(ctx context.Context) (bool, error) {
	s.mu.Lock()
	reply := s.current
	s.mu.Unlock()
	if reply == nil || reply.Done() {
		return false, nil
	}
	return true, reply.Interrupt(ctx)
}

// turns yields the turn in flight, if any, and then every turn that begins
// while ctx lives — whoever started it. A browser reconnecting therefore picks
// up work it never asked for, which is the normal case once a conversation
// outlives the page that opened it.
func (s *session) turns(ctx context.Context) iter.Seq2[*chat.Reply, string] {
	return func(yield func(*chat.Reply, string) bool) {
		var last *chat.Reply
		for {
			s.mu.Lock()
			reply, prompt, wait := s.current, s.prompt, s.turn
			s.mu.Unlock()
			if reply != nil && reply != last {
				last = reply
				if !yield(reply, prompt) {
					return
				}
				continue
			}
			select {
			case <-wait:
			case <-ctx.Done():
				return
			}
		}
	}
}

// join and leave track viewers so an unattended conversation can be released.
func (s *session) join() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewers++
}

func (s *session) leave() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.viewers--
	s.idle = time.Now()
}

// releasable reports whether nobody is watching and nothing is running, so the
// conversation can be closed. Closing does not end it: a later resume reopens
// the same conversation with its context intact.
func (s *session) releasable(after time.Duration) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.viewers > 0 || (s.current != nil && !s.current.Done()) {
		return false
	}
	return time.Since(s.idle) > after
}
