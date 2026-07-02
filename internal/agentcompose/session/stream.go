package session

import (
	"strings"
	"sync"
)

const StreamBufferSize = 256

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
	Session   *Summary
	Cell      *NotebookCell
	Event     *Event
	CellID    string
	Chunk     string
	IsStderr  bool
}

type StreamBroker struct {
	mu          sync.RWMutex
	nextID      int
	subscribers map[string]map[int]chan WatchEvent
}

func NewStreamBroker() *StreamBroker {
	return &StreamBroker{subscribers: map[string]map[int]chan WatchEvent{}}
}

func (b *StreamBroker) Subscribe(sessionID string) (<-chan WatchEvent, func()) {
	sessionID = strings.TrimSpace(sessionID)
	ch := make(chan WatchEvent, StreamBufferSize)
	if b == nil || sessionID == "" {
		close(ch)
		return ch, func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	if b.subscribers == nil {
		b.subscribers = map[string]map[int]chan WatchEvent{}
	}
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

func (b *StreamBroker) PublishSessionUpdated(summary *Summary) {
	if summary == nil {
		return
	}
	b.publish(WatchEvent{
		SessionID: summary.ID,
		EventType: WatchEventTypeSessionUpdated,
		Session:   CloneSummary(summary),
	})
}

func (b *StreamBroker) PublishCellStarted(sessionID string, cell NotebookCell) {
	b.publish(WatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: WatchEventTypeCellStarted,
		Cell:      CloneNotebookCell(&cell),
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

func (b *StreamBroker) PublishCellCompleted(sessionID string, cell NotebookCell) {
	b.publish(WatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: WatchEventTypeCellCompleted,
		Cell:      CloneNotebookCell(&cell),
	})
}

func (b *StreamBroker) PublishEventAdded(sessionID string, event Event) {
	b.publish(WatchEvent{
		SessionID: strings.TrimSpace(sessionID),
		EventType: WatchEventTypeEventAdded,
		Event:     CloneEvent(&event),
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

func CloneSummary(summary *Summary) *Summary {
	if summary == nil {
		return nil
	}
	cloned := *summary
	if len(summary.Tags) > 0 {
		cloned.Tags = append([]Tag(nil), summary.Tags...)
	}
	return &cloned
}

func CloneNotebookCell(cell *NotebookCell) *NotebookCell {
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

func CloneEvent(event *Event) *Event {
	if event == nil {
		return nil
	}
	cloned := *event
	return &cloned
}
