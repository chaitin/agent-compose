package model

import "testing"

func TestIntegrationModelNormalizationWorkflow(t *testing.T) {
	testModelNormalizationWorkflow(t)
}

func TestE2EModelNormalizationWorkflow(t *testing.T) {
	testModelNormalizationWorkflow(t)
}

func testModelNormalizationWorkflow(t *testing.T) {
	TestNormalizeSessionEnvItems(t)
	TestMergeSessionEnvItems(t)
	TestNormalizeAgentProvider(t)
	TestValidateTopicEventName(t)
	TestTopicEventPayloadSHA256(t)
	TestNormalizeTopicEventSource(t)
	TestNormalizeTopicEventDispatchStatus(t)
	TestNormalizeEventDeliveryStatus(t)
}
