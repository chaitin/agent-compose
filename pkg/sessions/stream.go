package sessions

import (
	"strings"
	"sync"

	"github.com/samber/do/v2"

	domain "agent-compose/pkg/model"
)

const streamBufferSize = 256

type WatchEventType int

const (
	WatchEventTypeUnspecified WatchEventType = iota
	WatchEventTypeSessionUpdated
	WatchEventTypeCellStarted
	WatchEventTypeCellOutput
	WatchEventTypeCellCompleted
	WatchEventTypeEventAdded
)

type WatchEvent struct {
	SessionID string
	EventType WatchEventType
	Session   *domain.SessionSummary
	Cell      *domain.NotebookCell
	Event     *domain.SessionEvent
	CellID    string
	Chunk     string
	IsStderr  bool
}

type StreamBroker struct {
	mu          sync.RWMutex
	nextID      int
	subscribers map[string]map[int]chan WatchEvent
}

func NewStreamBroker(do.Injector) (*StreamBroker, error) {
	return NewStreamBrokerForTest(), nil
}

func NewStreamBrokerForTest() *StreamBroker {
	return &StreamBroker{subscribers: map[string]map[int]chan WatchEvent{}}
}

func (b *StreamBroker) Subscribe(sessionID string) (<-chan WatchEvent, func()) {
	sessionID = strings.TrimSpace(sessionID)
	ch := make(chan WatchEvent, streamBufferSize)
	if b == nil || sessionID == "" {
		close(ch)
		return ch, func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	if b.subscribers[sessionID] == nil {
		b.subscribers[sessionID] = map[int]chan WatchEvent{}
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

func (b *StreamBroker) PublishSessionUpdated(summary *domain.SessionSummary) {
	if summary == nil {
		return
	}
	b.publish(WatchEvent{
		SessionID: summary.ID,
		EventType: WatchEventTypeSessionUpdated,
		Session:   cloneSessionSummary(summary),
	})
}

func (b *StreamBroker) PublishCellStarted(sessionID string, cell domain.NotebookCell) {
	b.publish(WatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: WatchEventTypeCellStarted,
		Cell:      cloneNotebookCell(&cell),
	})
}

func (b *StreamBroker) PublishCellOutput(sessionID, cellID, chunk string, isStderr bool) {
	b.publish(WatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: WatchEventTypeCellOutput,
		CellID:    strings.TrimSpace(cellID),
		Chunk:     chunk,
		IsStderr:  isStderr,
	})
}

func (b *StreamBroker) PublishCellCompleted(sessionID string, cell domain.NotebookCell) {
	b.publish(WatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: WatchEventTypeCellCompleted,
		Cell:      cloneNotebookCell(&cell),
	})
}

func (b *StreamBroker) PublishEventAdded(sessionID string, event domain.SessionEvent) {
	b.publish(WatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: WatchEventTypeEventAdded,
		Event:     cloneSessionEvent(&event),
	})
}

func (b *StreamBroker) publish(event WatchEvent) {
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

func cloneSessionSummary(summary *domain.SessionSummary) *domain.SessionSummary {
	if summary == nil {
		return nil
	}
	cloned := *summary
	if len(summary.Tags) > 0 {
		cloned.Tags = append([]domain.SessionTag(nil), summary.Tags...)
	}
	return &cloned
}

func cloneNotebookCell(cell *domain.NotebookCell) *domain.NotebookCell {
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

func cloneSessionEvent(event *domain.SessionEvent) *domain.SessionEvent {
	if event == nil {
		return nil
	}
	cloned := *event
	return &cloned
}
