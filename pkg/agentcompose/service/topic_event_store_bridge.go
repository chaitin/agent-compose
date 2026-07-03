package agentcompose

import "agent-compose/pkg/storage/configstore"

func webhookSourceTopicMatches(topic, topicPrefix string) bool {
	return configstore.WebhookSourceTopicMatches(topic, topicPrefix)
}
