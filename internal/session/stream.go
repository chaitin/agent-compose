package session

import (
	"strings"
	"sync"

	"github.com/samber/do/v2"
)

const sessionStreamBufferSize = 256

type sessionWatchEventType int

const (
	sessionWatchEventTypeUnspecified sessionWatchEventType = iota
	sessionWatchEventTypeSessionUpdated
	sessionWatchEventTypeCellStarted
	sessionWatchEventTypeCellOutput
	sessionWatchEventTypeCellCompleted
	sessionWatchEventTypeEventAdded
)

type sessionWatchEvent struct {
	SessionID string
	EventType sessionWatchEventType
	Session   *SessionSummary
	Cell      *NotebookCell
	Event     *SessionEvent
	CellID    string
	Chunk     string
	IsStderr  bool
}

type SessionWatchEvent = sessionWatchEvent
type SessionWatchEventType = sessionWatchEventType

const (
	SessionWatchEventTypeUnspecified    = sessionWatchEventTypeUnspecified
	SessionWatchEventTypeSessionUpdated = sessionWatchEventTypeSessionUpdated
	SessionWatchEventTypeCellStarted    = sessionWatchEventTypeCellStarted
	SessionWatchEventTypeCellOutput     = sessionWatchEventTypeCellOutput
	SessionWatchEventTypeCellCompleted  = sessionWatchEventTypeCellCompleted
	SessionWatchEventTypeEventAdded     = sessionWatchEventTypeEventAdded
)

type SessionStreamBroker struct {
	mu          sync.RWMutex
	nextID      int
	subscribers map[string]map[int]chan sessionWatchEvent
}

func NewSessionStreamBroker(do.Injector) (*SessionStreamBroker, error) {
	return &SessionStreamBroker{subscribers: map[string]map[int]chan sessionWatchEvent{}}, nil
}

func NewSessionStreamBrokerForTest() *SessionStreamBroker {
	return &SessionStreamBroker{subscribers: map[string]map[int]chan sessionWatchEvent{}}
}

func (b *SessionStreamBroker) Subscribe(sessionID string) (<-chan sessionWatchEvent, func()) {
	sessionID = strings.TrimSpace(sessionID)
	ch := make(chan sessionWatchEvent, sessionStreamBufferSize)
	if b == nil || sessionID == "" {
		close(ch)
		return ch, func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	if b.subscribers[sessionID] == nil {
		b.subscribers[sessionID] = map[int]chan sessionWatchEvent{}
	}
	b.subscribers[sessionID][id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		items := b.subscribers[sessionID]
		if items == nil {
			return
		}
		item, ok := items[id]
		if !ok {
			return
		}
		delete(items, id)
		close(item)
		if len(items) == 0 {
			delete(b.subscribers, sessionID)
		}
	}
}

func (b *SessionStreamBroker) PublishSessionUpdated(summary *SessionSummary) {
	if summary == nil {
		return
	}
	b.publish(sessionWatchEvent{
		SessionID: summary.ID,
		EventType: sessionWatchEventTypeSessionUpdated,
		Session:   cloneSessionSummary(summary),
	})
}

func (b *SessionStreamBroker) PublishCellStarted(sessionID string, cell NotebookCell) {
	b.publish(sessionWatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: sessionWatchEventTypeCellStarted,
		Cell:      cloneNotebookCell(&cell),
	})
}

func (b *SessionStreamBroker) PublishCellOutput(sessionID, cellID, chunk string, isStderr bool) {
	b.publish(sessionWatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: sessionWatchEventTypeCellOutput,
		CellID:    strings.TrimSpace(cellID),
		Chunk:     chunk,
		IsStderr:  isStderr,
	})
}

func (b *SessionStreamBroker) PublishCellCompleted(sessionID string, cell NotebookCell) {
	b.publish(sessionWatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: sessionWatchEventTypeCellCompleted,
		Cell:      cloneNotebookCell(&cell),
	})
}

func (b *SessionStreamBroker) PublishEventAdded(sessionID string, event SessionEvent) {
	b.publish(sessionWatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: sessionWatchEventTypeEventAdded,
		Event:     cloneSessionEvent(&event),
	})
}

func (b *SessionStreamBroker) publish(event sessionWatchEvent) {
	if b == nil || strings.TrimSpace(event.SessionID) == "" {
		return
	}
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, ch := range b.subscribers[event.SessionID] {
		select {
		case ch <- event:
		default:
		}
	}
}

func cloneSessionSummary(summary *SessionSummary) *SessionSummary {
	if summary == nil {
		return nil
	}
	cloned := *summary
	if len(summary.Tags) > 0 {
		cloned.Tags = append([]SessionTag(nil), summary.Tags...)
	}
	return &cloned
}

func cloneNotebookCell(cell *NotebookCell) *NotebookCell {
	if cell == nil {
		return nil
	}
	cloned := *cell
	if cell.AgentResume != nil {
		resume := *cell.AgentResume
		if len(cell.AgentResume.SessionJSONLPaths) > 0 {
			resume.SessionJSONLPaths = append([]string(nil), cell.AgentResume.SessionJSONLPaths...)
		}
		cloned.AgentResume = &resume
	}
	return &cloned
}

func cloneSessionEvent(event *SessionEvent) *SessionEvent {
	if event == nil {
		return nil
	}
	cloned := *event
	return &cloned
}
