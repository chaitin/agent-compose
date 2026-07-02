package loader

import "strings"

const BusBufferSize = 256

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
