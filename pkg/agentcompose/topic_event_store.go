package agentcompose

import (
	eventsdomain "agent-compose/internal/agentcompose/events"
	"context"
)

func (s *ConfigStore) ensureEventSchema(ctx context.Context) error {
	s.bindDomainStores()
	return s.EventStore.EnsureSchema(ctx)
}

func normalizeTopicEventRecord(item TopicEventRecord, assignID bool) (TopicEventRecord, error) {
	return eventsdomain.NormalizeTopicEventRecord(item, assignID)
}

func webhookSourceTopicMatches(topic, topicPrefix string) bool {
	return eventsdomain.WebhookSourceTopicMatches(topic, topicPrefix)
}
