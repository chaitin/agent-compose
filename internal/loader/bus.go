package loader

import (
	"strings"

	"github.com/samber/do/v2"
)

type LoaderBus struct {
	ch chan LoaderTopicEvent
}

func NewLoaderBus(do.Injector) (*LoaderBus, error) {
	return &LoaderBus{ch: make(chan LoaderTopicEvent, 256)}, nil
}

func NewLoaderBusForTest(buffer int) *LoaderBus {
	if buffer <= 0 {
		buffer = 256
	}
	return &LoaderBus{ch: make(chan LoaderTopicEvent, buffer)}
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
