package bus

import (
	"testing"
	"time"
)

func TestLoaderBusPublishReportsFullChannel(t *testing.T) {
	bus := NewLoaderBusWithBuffer(1)
	if !bus.Publish(LoaderTopicEvent{Topic: "webhook.test", Payload: map[string]any{}, CreatedAt: time.Now().UTC()}) {
		t.Fatalf("first Publish returned false, want true")
	}
	if bus.Publish(LoaderTopicEvent{Topic: "webhook.test", Payload: map[string]any{}, CreatedAt: time.Now().UTC()}) {
		t.Fatalf("second Publish returned true for full channel")
	}
	if bus.Publish(LoaderTopicEvent{}) {
		t.Fatalf("Publish with empty topic returned true")
	}
}
