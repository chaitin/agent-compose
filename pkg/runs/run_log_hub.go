package runs

import (
	"sync"
	"time"
)

const defaultRunLogSubscriptionBuffer = 64

type RunLogEvent struct {
	RunID     string
	ProjectID string
	SandboxID string
	Stream    string
	Data      string
	Offset    uint64
	CreatedAt time.Time
	// Seq is the run log entry that produced the event, 0 for a legacy text
	// transcript. Entries with no text (usage, turn ends) still publish, so
	// a structured follower wakes for them.
	Seq uint64
}

type RunLogSubscription struct {
	hub   *RunLogHub
	runID string
	ch    chan RunLogEvent
	once  sync.Once
}

func (s *RunLogSubscription) C() <-chan RunLogEvent {
	if s == nil {
		return nil
	}
	return s.ch
}

func (s *RunLogSubscription) Close() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		s.hub.unsubscribe(s.runID, s)
	})
}

type RunLogHub struct {
	mu          sync.RWMutex
	subscribers map[string]map[*RunLogSubscription]struct{}
	buffer      int
	observer    func(RunLogEvent)
}

func NewRunLogHub() *RunLogHub {
	return &RunLogHub{
		subscribers: make(map[string]map[*RunLogSubscription]struct{}),
		buffer:      defaultRunLogSubscriptionBuffer,
	}
}

// SetObserver installs a process-wide listener for every published chunk. It
// is used by the daemon's event stream and must be set once during
// composition, before runs start.
func (h *RunLogHub) SetObserver(observer func(RunLogEvent)) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.observer = observer
	h.mu.Unlock()
}

func (h *RunLogHub) Subscribe(runID string) *RunLogSubscription {
	if h == nil || runID == "" {
		return nil
	}
	buffer := h.buffer
	if buffer <= 0 {
		buffer = defaultRunLogSubscriptionBuffer
	}
	sub := &RunLogSubscription{
		hub:   h,
		runID: runID,
		ch:    make(chan RunLogEvent, buffer),
	}
	h.mu.Lock()
	if h.subscribers == nil {
		h.subscribers = make(map[string]map[*RunLogSubscription]struct{})
	}
	if h.subscribers[runID] == nil {
		h.subscribers[runID] = make(map[*RunLogSubscription]struct{})
	}
	h.subscribers[runID][sub] = struct{}{}
	h.mu.Unlock()
	return sub
}

func (h *RunLogHub) Publish(event RunLogEvent) bool {
	if h == nil || event.RunID == "" || (event.Data == "" && event.Seq == 0) {
		return false
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	delivered := false
	for sub := range h.subscribers[event.RunID] {
		select {
		case sub.ch <- event:
			delivered = true
		default:
		}
	}
	if h.observer != nil {
		h.observer(event)
	}
	return delivered
}

func (h *RunLogHub) unsubscribe(runID string, sub *RunLogSubscription) {
	if h == nil || sub == nil {
		return
	}
	h.mu.Lock()
	if subs := h.subscribers[runID]; subs != nil {
		delete(subs, sub)
		if len(subs) == 0 {
			delete(h.subscribers, runID)
		}
	}
	close(sub.ch)
	h.mu.Unlock()
}
