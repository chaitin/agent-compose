package agentcompose

import (
	loaderdomain "agent-compose/internal/agentcompose/loader"

	"github.com/samber/do/v2"
)

type LoaderBus struct {
	ch chan LoaderTopicEvent
}

func NewLoaderBus(do.Injector) (*LoaderBus, error) {
	return &LoaderBus{ch: make(chan LoaderTopicEvent, loaderdomain.BusBufferSize)}, nil
}

func (b *LoaderBus) Events() <-chan LoaderTopicEvent {
	if b == nil {
		return nil
	}
	return b.ch
}

func (b *LoaderBus) Publish(event LoaderTopicEvent) bool {
	if b == nil {
		return false
	}
	return loaderdomain.PublishTopicEvent(b.ch, event)
}
