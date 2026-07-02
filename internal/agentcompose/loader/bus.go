package loader

import "strings"

const BusBufferSize = 256

type Bus struct {
	ch chan TopicEvent
}

func NewBus() *Bus {
	return &Bus{ch: make(chan TopicEvent, BusBufferSize)}
}

func (b *Bus) Events() <-chan TopicEvent {
	if b == nil {
		return nil
	}
	return b.ch
}

func (b *Bus) Publish(event TopicEvent) bool {
	if b == nil {
		return false
	}
	return PublishTopicEvent(b.ch, event)
}

func PublishTopicEvent(ch chan TopicEvent, event TopicEvent) bool {
	if ch == nil || strings.TrimSpace(event.Topic) == "" {
		return false
	}
	select {
	case ch <- event:
		return true
	default:
		return false
	}
}
