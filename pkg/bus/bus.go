package bus

import (
	"strings"

	"github.com/samber/do/v2"

	domain "agent-compose/pkg/model"
)

type Bus struct {
	ch chan domain.LoaderTopicEvent
}

func NewBus(do.Injector) (*Bus, error) {
	return NewBusWithBuffer(256), nil
}

func NewBusWithBuffer(size int) *Bus {
	if size <= 0 {
		size = 256
	}
	return &Bus{ch: make(chan domain.LoaderTopicEvent, size)}
}

func (b *Bus) Events() <-chan domain.LoaderTopicEvent {
	if b == nil {
		return nil
	}
	return b.ch
}

func (b *Bus) Publish(event domain.LoaderTopicEvent) bool {
	if b == nil || strings.TrimSpace(event.Topic) == "" {
		return false
	}
	select {
	case b.ch <- event:
		return true
	default:
		return false
	}
}
