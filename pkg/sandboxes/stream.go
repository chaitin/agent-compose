package sandboxes

import (
	"strings"
	"sync"

	"github.com/samber/do/v2"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const streamBufferSize = 256

type WatchEventType int

const (
	WatchEventTypeUnspecified WatchEventType = iota
	WatchEventTypeSandboxUpdated
	WatchEventTypeCellStarted
	WatchEventTypeCellOutput
	WatchEventTypeCellCompleted
	WatchEventTypeEventAdded
)

type WatchEvent struct {
	SandboxID string
	EventType WatchEventType
	Sandbox   *domain.SandboxSummary
	Cell      *domain.NotebookCell
	Event     *domain.SandboxEvent
	CellID    string
	Chunk     string
	Stream    domain.StdioStream
}

type StreamBroker struct {
	mu          sync.RWMutex
	nextID      int
	subscribers map[string]map[int]chan WatchEvent
	observer    func(WatchEvent)
}

func NewStreamBroker(do.Injector) (*StreamBroker, error) {
	return NewStreamBrokerForTest(), nil
}

func NewStreamBrokerForTest() *StreamBroker {
	return &StreamBroker{subscribers: map[string]map[int]chan WatchEvent{}}
}

// SetObserver installs a process-wide listener for every published watch
// event. It is used by the daemon's event stream and must be set once during
// composition. The observer is called after the per-sandbox fan-out and must
// not block.
func (b *StreamBroker) SetObserver(observer func(WatchEvent)) {
	if b == nil {
		return
	}
	b.mu.Lock()
	b.observer = observer
	b.mu.Unlock()
}

func (b *StreamBroker) Subscribe(sandboxID string) (<-chan WatchEvent, func()) {
	sandboxID = strings.TrimSpace(sandboxID)
	ch := make(chan WatchEvent, streamBufferSize)
	if b == nil || sandboxID == "" {
		close(ch)
		return ch, func() {}
	}
	b.mu.Lock()
	b.nextID++
	id := b.nextID
	if b.subscribers[sandboxID] == nil {
		b.subscribers[sandboxID] = map[int]chan WatchEvent{}
	}
	b.subscribers[sandboxID][id] = ch
	b.mu.Unlock()
	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		items := b.subscribers[sandboxID]
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
			delete(b.subscribers, sandboxID)
		}
	}
}

func (b *StreamBroker) PublishSandboxUpdated(summary *domain.SandboxSummary) {
	if summary == nil {
		return
	}
	b.publish(WatchEvent{
		SandboxID: summary.ID,
		EventType: WatchEventTypeSandboxUpdated,
		Sandbox:   cloneSandboxSummary(summary),
	})
}

func (b *StreamBroker) PublishCellStarted(sandboxID string, cell domain.NotebookCell) {
	b.publish(WatchEvent{
		SandboxID: strings.TrimSpace(sandboxID),
		EventType: WatchEventTypeCellStarted,
		Cell:      cloneNotebookCell(&cell),
	})
}

func (b *StreamBroker) PublishCellOutput(sandboxID, cellID, chunk string, stream domain.StdioStream) {
	b.publish(WatchEvent{
		SandboxID: strings.TrimSpace(sandboxID),
		EventType: WatchEventTypeCellOutput,
		CellID:    strings.TrimSpace(cellID),
		Chunk:     chunk,
		Stream:    domain.NormalizeStdioStream(stream),
	})
}

func (b *StreamBroker) PublishCellCompleted(sandboxID string, cell domain.NotebookCell) {
	b.publish(WatchEvent{
		SandboxID: strings.TrimSpace(sandboxID),
		EventType: WatchEventTypeCellCompleted,
		Cell:      cloneNotebookCell(&cell),
	})
}

func (b *StreamBroker) PublishEventAdded(sandboxID string, event domain.SandboxEvent) {
	b.publish(WatchEvent{
		SandboxID: strings.TrimSpace(sandboxID),
		EventType: WatchEventTypeEventAdded,
		Event:     cloneSandboxEvent(&event),
	})
}

func (b *StreamBroker) publish(event WatchEvent) {
	if b == nil || strings.TrimSpace(event.SandboxID) == "" {
		return
	}
	b.mu.RLock()
	for _, ch := range b.subscribers[event.SandboxID] {
		select {
		case ch <- event:
		default:
		}
	}
	observer := b.observer
	b.mu.RUnlock()
	if observer != nil {
		observer(event)
	}
}

func cloneSandboxSummary(summary *domain.SandboxSummary) *domain.SandboxSummary {
	if summary == nil {
		return nil
	}
	cloned := *summary
	if len(summary.Tags) > 0 {
		cloned.Tags = append([]domain.SandboxTag(nil), summary.Tags...)
	}
	return &cloned
}

func cloneNotebookCell(cell *domain.NotebookCell) *domain.NotebookCell {
	if cell == nil {
		return nil
	}
	cloned := *cell
	return &cloned
}

func cloneSandboxEvent(event *domain.SandboxEvent) *domain.SandboxEvent {
	if event == nil {
		return nil
	}
	cloned := *event
	return &cloned
}
