package event

import (
	"context"
	"time"

	eventtypes "agent-compose/internal/eventtypes"
)

type LoaderTopicEvent = eventtypes.LoaderTopicEvent

type ConfigStore interface {
	ListDispatchableEvents(context.Context, time.Time, int) ([]TopicEventRecord, error)
	ClaimEvent(context.Context, string, string, time.Time, time.Time) (bool, error)
	ReleaseEventClaim(context.Context, string, string, string, string, time.Time) error
	MarkEventPublished(context.Context, string, string, time.Time) error
	MarkEventNoSubscriber(context.Context, string, string, time.Time) error
}

type LoaderBus interface {
	Publish(LoaderTopicEvent) bool
}
