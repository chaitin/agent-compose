package storage

import "testing"

func TestIntegrationTopicEventStoreWorkflow(t *testing.T) {
	TestConfigStoreCreateAndListTopicEvents(t)
	TestConfigStoreTopicEventIdempotency(t)
	TestConfigStorePendingAndPublishedTopicEvents(t)
	TestConfigStoreEventDeliveryDoesNotDowngradeRunOnDuplicateMatch(t)
	TestConfigStoreWebhookSourceCRUDAndTopicMatching(t)
	TestTopicEventModelAndStoreErrorBranches(t)
}

func TestE2ETopicEventStoreWorkflow(t *testing.T) {
	TestConfigStoreCreateAndListTopicEvents(t)
	TestConfigStoreTopicEventIdempotency(t)
	TestConfigStorePendingAndPublishedTopicEvents(t)
	TestConfigStoreEventDeliveryDoesNotDowngradeRunOnDuplicateMatch(t)
	TestConfigStoreWebhookSourceCRUDAndTopicMatching(t)
	TestTopicEventModelAndStoreErrorBranches(t)
}
