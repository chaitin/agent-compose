package bus

import (
	"strings"

	"agent-compose/pkg/model"

	"github.com/samber/do/v2"
)

type LoaderTopicEvent = model.LoaderTopicEvent

type LoaderBus struct {
	ch chan LoaderTopicEvent
}

func NewLoaderBus(do.Injector) (*LoaderBus, error) {
	return &LoaderBus{ch: make(chan LoaderTopicEvent, 256)}, nil
}

func NewLoaderBusWithBuffer(size int) *LoaderBus {
	if size <= 0 {
		size = 1
	}
	return &LoaderBus{ch: make(chan LoaderTopicEvent, size)}
}

func (b *LoaderBus) Events() <-chan LoaderTopicEvent {
	if b == nil {
		return nil
	}
	return b.ch
}

func (b *LoaderBus) Publish(event LoaderTopicEvent) bool {
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
