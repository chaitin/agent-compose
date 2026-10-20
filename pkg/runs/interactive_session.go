package runs

import (
	"context"
	"errors"
	"fmt"
	"sync"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
)

var (
	ErrInteractiveSessionNotFound = errors.New("interactive session not found")
	ErrInteractiveSessionClosed   = errors.New("interactive session closed")
	ErrInteractiveSessionAttached = errors.New("interactive session input already attached")
)

type InteractiveSessionState string

const (
	InteractiveSessionCreated   InteractiveSessionState = "created"
	InteractiveSessionRunning   InteractiveSessionState = "running"
	InteractiveSessionDetached  InteractiveSessionState = "detached"
	InteractiveSessionCompleted InteractiveSessionState = "completed"
	InteractiveSessionCanceled  InteractiveSessionState = "canceled"
	InteractiveSessionFailed    InteractiveSessionState = "failed"
)

type InteractiveSession struct {
	RunID string

	mu                 sync.Mutex
	state              InteractiveSessionState
	input              chan RunAttachInput
	done               chan struct{}
	attached           bool
	closeOnce          sync.Once
	runtime            driverpkg.RuntimeInteraction
	subscribers        map[uint64]chan RunAttachOutput
	nextSubscriber     uint64
	promptTurns        bool
	promptTurnsPending int
}

type InteractiveSessionAttachment struct {
	session *InteractiveSession
	release func()
	done    chan struct{}
	mu      sync.RWMutex
	close   sync.Once
}

func (a *InteractiveSessionAttachment) Send(ctx context.Context, input RunAttachInput) error {
	a.mu.RLock()
	defer a.mu.RUnlock()
	select {
	case <-a.done:
		return ErrInteractiveSessionClosed
	default:
	}
	return a.session.send(ctx, input, a.done)
}
func (a *InteractiveSessionAttachment) Close() {
	a.close.Do(func() { close(a.done) })
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.release != nil {
		a.release()
		a.release = nil
	}
}

// BindRuntime transfers ownership of the runtime interaction to the session.
// It may only be called once, before the session is closed.
func (s *InteractiveSession) BindRuntime(interaction driverpkg.RuntimeInteraction) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtime != nil {
		return fmt.Errorf("runtime interaction already bound")
	}
	if interactiveSessionTerminal(s.state) {
		return ErrInteractiveSessionClosed
	}
	s.runtime = driverpkg.GuardRuntimeInteractionInput(interaction)
	return nil
}

func (s *InteractiveSession) Runtime() (driverpkg.RuntimeInteraction, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtime == nil {
		return nil, fmt.Errorf("runtime interaction is not bound")
	}
	return s.runtime, nil
}

func NewInteractiveSession(runID string) *InteractiveSession {
	return &InteractiveSession{RunID: runID, state: InteractiveSessionCreated, input: make(chan RunAttachInput, 32), done: make(chan struct{}), subscribers: map[uint64]chan RunAttachOutput{}}
}

func (s *InteractiveSession) Subscribe() (<-chan RunAttachOutput, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if interactiveSessionTerminal(s.state) {
		return nil, nil, ErrInteractiveSessionClosed
	}
	id := s.nextSubscriber
	s.nextSubscriber++
	ch := make(chan RunAttachOutput, 32)
	s.subscribers[id] = ch
	return ch, func() {
		s.mu.Lock()
		if current, ok := s.subscribers[id]; ok {
			delete(s.subscribers, id)
			close(current)
		}
		s.mu.Unlock()
	}, nil
}

func (s *InteractiveSession) Publish(output RunAttachOutput) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, subscriber := range s.subscribers {
		select {
		case subscriber <- output:
		default:
			delete(s.subscribers, id)
			close(subscriber)
		}
	}
}

func (s *InteractiveSession) State() InteractiveSessionState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

func (s *InteractiveSession) Start() error { return s.transition(InteractiveSessionRunning) }

// enablePromptTurns marks this session as a prompt-session-level multi-turn
// interaction. Command sessions never enter this state, so StopRun keeps their
// existing cancellation behavior.
func (s *InteractiveSession) enablePromptTurns() {
	s.mu.Lock()
	s.promptTurns = true
	s.mu.Unlock()
}

// beginInitialPromptTurn arms prompt turn tracking and records the prompt that
// is sent before the session's input pump starts. Doing both under one lock
// means a stop arriving before that prompt falls back to cancellation instead
// of being queued as a normal close for work that never started.
func (s *InteractiveSession) beginInitialPromptTurn() {
	s.mu.Lock()
	if !interactiveSessionTerminal(s.state) {
		s.promptTurns = true
		s.promptTurnsPending++
	}
	s.mu.Unlock()
}

// completePromptTurn records the agent's completed-turn frame. StopRun reads
// the remaining queued/current turns to choose a normal eof close over an
// interrupt cancel.
func (s *InteractiveSession) completePromptTurn() {
	s.mu.Lock()
	if s.promptTurnsPending > 0 {
		s.promptTurnsPending--
	}
	s.mu.Unlock()
}

func (s *InteractiveSession) rollbackPromptTurnReservation() {
	s.mu.Lock()
	if s.promptTurnsPending > 0 {
		s.promptTurnsPending--
	}
	s.mu.Unlock()
}

func (s *InteractiveSession) promptTurnRunning() (enabled, running bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.promptTurns, s.promptTurnsPending > 0
}

func (s *InteractiveSession) transition(next InteractiveSessionState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if interactiveSessionTerminal(s.state) {
		return ErrInteractiveSessionClosed
	}
	s.state = next
	return nil
}

func (s *InteractiveSession) AcquireInput() (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if interactiveSessionTerminal(s.state) {
		return nil, ErrInteractiveSessionClosed
	}
	if s.attached {
		return nil, ErrInteractiveSessionAttached
	}
	s.attached = true
	if s.state == InteractiveSessionDetached {
		s.state = InteractiveSessionRunning
	}
	return func() {
		s.mu.Lock()
		s.attached = false
		if s.state == InteractiveSessionRunning {
			s.state = InteractiveSessionDetached
		}
		s.mu.Unlock()
	}, nil
}

func interactiveSessionTerminal(state InteractiveSessionState) bool {
	return state == InteractiveSessionCompleted || state == InteractiveSessionCanceled || state == InteractiveSessionFailed
}

func (s *InteractiveSession) Send(ctx context.Context, input RunAttachInput) error {
	return s.send(ctx, input, nil)
}

func (s *InteractiveSession) send(ctx context.Context, input RunAttachInput, attachmentDone <-chan struct{}) error {
	reservedTurn := false
	s.mu.Lock()
	if interactiveSessionTerminal(s.state) {
		s.mu.Unlock()
		return ErrInteractiveSessionClosed
	}
	if s.promptTurns && startsPromptTurn(input) {
		s.promptTurnsPending++
		reservedTurn = true
	}
	s.mu.Unlock()
	fail := func(err error) error {
		if reservedTurn {
			s.rollbackPromptTurnReservation()
		}
		return err
	}
	select {
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-attachmentDone:
		return fail(ErrInteractiveSessionClosed)
	case <-s.done:
		return fail(ErrInteractiveSessionClosed)
	default:
	}
	select {
	case s.input <- input:
		return nil
	case <-ctx.Done():
		return fail(ctx.Err())
	case <-attachmentDone:
		return fail(ErrInteractiveSessionClosed)
	case <-s.done:
		return fail(ErrInteractiveSessionClosed)
	}
}

func (s *InteractiveSession) Receive() RunAttachReceiver {
	return func() (RunAttachInput, error) {
		select {
		case <-s.done:
			return RunAttachInput{}, ErrInteractiveSessionClosed
		default:
		}
		select {
		case input := <-s.input:
			return input, nil
		case <-s.done:
			return RunAttachInput{}, ErrInteractiveSessionClosed
		}
	}
}

func startsPromptTurn(input RunAttachInput) bool {
	switch input.Kind {
	case RunAttachInputHumanMessage:
		return true
	case RunAttachInputStdin:
		return true
	default:
		return false
	}
}

func (s *InteractiveSession) Close(state InteractiveSessionState) {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.state = state
		runtime := s.runtime
		for id, subscriber := range s.subscribers {
			delete(s.subscribers, id)
			close(subscriber)
		}
		s.mu.Unlock()
		close(s.done)
		if runtime != nil {
			_ = runtime.CloseSend()
		}
	})
}

type InteractiveSessionManager struct {
	mu       sync.RWMutex
	sessions map[string]*InteractiveSession
}

func NewInteractiveSessionManager() *InteractiveSessionManager {
	return &InteractiveSessionManager{sessions: map[string]*InteractiveSession{}}
}

func (m *InteractiveSessionManager) Create(runID string) (*InteractiveSession, error) {
	if runID == "" {
		return nil, fmt.Errorf("run id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[runID]; ok {
		return nil, fmt.Errorf("session for %s already exists", runID)
	}
	s := NewInteractiveSession(runID)
	m.sessions[runID] = s
	return s, nil
}

// CreateAttached creates a running session with its initial input lease held.
// The session is not visible to concurrent Attach calls until the lease has
// been acquired, so a resuming client cannot preempt the run creator.
func (m *InteractiveSessionManager) CreateAttached(runID string) (*InteractiveSession, func(), error) {
	if runID == "" {
		return nil, nil, fmt.Errorf("run id is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.sessions[runID]; ok {
		return nil, nil, fmt.Errorf("session for %s already exists", runID)
	}
	s := NewInteractiveSession(runID)
	if err := s.Start(); err != nil {
		return nil, nil, err
	}
	release, err := s.AcquireInput()
	if err != nil {
		return nil, nil, err
	}
	m.sessions[runID] = s
	return s, release, nil
}

func (m *InteractiveSessionManager) Get(runID string) (*InteractiveSession, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[runID]
	if !ok {
		return nil, ErrInteractiveSessionNotFound
	}
	return s, nil
}

func (m *InteractiveSessionManager) Attach(runID string) (*InteractiveSessionAttachment, error) {
	s, err := m.Get(runID)
	if err != nil {
		return nil, err
	}
	release, err := s.AcquireInput()
	if err != nil {
		return nil, err
	}
	return &InteractiveSessionAttachment{session: s, release: release, done: make(chan struct{})}, nil
}

// BindRuntime transfers an opened interaction to the named session. It closes
// the interaction when ownership cannot be transferred.
func (m *InteractiveSessionManager) BindRuntime(runID string, interaction driverpkg.RuntimeInteraction) (driverpkg.RuntimeInteraction, error) {
	s, err := m.Get(runID)
	if err != nil {
		_ = interaction.CloseSend()
		return nil, err
	}
	if err := s.BindRuntime(interaction); err != nil {
		_ = interaction.CloseSend()
		return nil, err
	}
	return s.Runtime()
}

func (m *InteractiveSessionManager) Remove(runID string, state InteractiveSessionState) error {
	m.mu.Lock()
	s, ok := m.sessions[runID]
	if ok {
		delete(m.sessions, runID)
	}
	m.mu.Unlock()
	if !ok {
		return ErrInteractiveSessionNotFound
	}
	s.Close(state)
	return nil
}
