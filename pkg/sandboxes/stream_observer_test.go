package sandboxes

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestStreamBrokerNotifiesGlobalObserver(t *testing.T) {
	broker := NewStreamBrokerForTest()
	var observed []WatchEvent
	broker.SetObserver(func(event WatchEvent) { observed = append(observed, event) })
	ch, unsubscribe := broker.Subscribe("sandbox-1")
	defer unsubscribe()

	broker.PublishSandboxUpdated(&domain.SandboxSummary{ID: "sandbox-1", VMStatus: domain.VMStatusRunning})
	select {
	case event := <-ch:
		if event.EventType != WatchEventTypeSandboxUpdated || event.Sandbox == nil || event.Sandbox.ID != "sandbox-1" {
			t.Fatalf("subscriber event = %+v", event)
		}
	default:
		t.Fatal("per-sandbox subscriber received no event")
	}
	if len(observed) != 1 || observed[0].EventType != WatchEventTypeSandboxUpdated || observed[0].Sandbox.ID != "sandbox-1" {
		t.Fatalf("observed = %+v", observed)
	}
}

func TestStreamBrokerPublishesSandboxRemovedTombstone(t *testing.T) {
	broker := NewStreamBrokerForTest()
	var observed []WatchEvent
	broker.SetObserver(func(event WatchEvent) { observed = append(observed, event) })
	broker.PublishSandboxRemoved(&domain.SandboxSummary{ID: "sandbox-removed", VMStatus: domain.VMStatusDeleting})
	if len(observed) != 1 || observed[0].EventType != WatchEventTypeSandboxRemoved || observed[0].Sandbox == nil || observed[0].Sandbox.ID != "sandbox-removed" {
		t.Fatalf("observed = %+v", observed)
	}
}
