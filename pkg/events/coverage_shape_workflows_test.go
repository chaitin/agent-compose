package events

import "testing"

func TestIntegrationEventIngressWorkflow(t *testing.T) {
	testEventIngressWorkflow(t)
}

func TestE2EEventIngressWorkflow(t *testing.T) {
	testEventIngressWorkflow(t)
}

func testEventIngressWorkflow(t *testing.T) {
	runEventWorkflowTest(t, "WebhookHandlerStoresEvent", TestWebhookHandlerStoresEvent)
	runEventWorkflowTest(t, "WebhookHandlerAuthAndValidation", TestWebhookHandlerAuthAndValidation)
	runEventWorkflowTest(t, "WebhookTokenAuthentication", TestWebhookTokenAuthentication)
	runEventWorkflowTest(t, "WebhookPayloadSanitizesWebhookTokenHeader", TestWebhookPayloadSanitizesWebhookTokenHeader)
	runEventWorkflowTest(t, "WebhookHandlerIdempotency", TestWebhookHandlerIdempotency)
	runEventWorkflowTest(t, "WebhookHandlerUsesWebhookSourceToken", TestWebhookHandlerUsesWebhookSourceToken)
	runEventWorkflowTest(t, "WebhookHandlerMatchesExactSourcePrefixTopic", TestWebhookHandlerMatchesExactSourcePrefixTopic)
	runEventWorkflowTest(t, "WebhookSourceManagementHandlers", TestWebhookSourceManagementHandlers)
	runEventWorkflowTest(t, "WebhookPayloadHelpers", TestWebhookPayloadHelpers)
	runEventWorkflowTest(t, "EventQueryHandlers", TestEventQueryHandlers)
	runEventWorkflowTest(t, "EventSessionsHandler", TestEventSessionsHandler)
	runEventWorkflowTest(t, "EventRunsHandler", TestEventRunsHandler)
	runEventWorkflowTest(t, "EventHandlersReturnNotFoundForMissingEvent", TestEventHandlersReturnNotFoundForMissingEvent)
	runEventWorkflowTest(t, "WebhookDisabledReturnsNotFound", TestWebhookDisabledReturnsNotFound)
	runEventWorkflowTest(t, "WebhookHandlerDoesNotFallbackToBearerToken", TestWebhookHandlerDoesNotFallbackToBearerToken)
	runEventWorkflowTest(t, "EventDispatcherPublishesPendingEvents", TestEventDispatcherPublishesPendingEvents)
	runEventWorkflowTest(t, "EventDispatcherKeepsPendingWhenBusFull", TestEventDispatcherKeepsPendingWhenBusFull)
	runEventWorkflowTest(t, "EventDispatcherIgnoresStaleClaimAck", TestEventDispatcherIgnoresStaleClaimAck)
}

func runEventWorkflowTest(t *testing.T, name string, fn func(*testing.T)) {
	t.Helper()
	t.Run(name, fn)
}
