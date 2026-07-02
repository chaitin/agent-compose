package loaders

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWebhookIntegrationEventDispatchRunsMatchingLoader(t *testing.T) {
	testWebhookIntegrationEventDispatchRunsMatchingLoader(t)
}

func testWebhookIntegrationEventDispatchRunsMatchingLoader(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newLoaderEventTestConfigStore(t)
	manager := newTestLoaderManager(t, ManagerDeps{
		RootCtx:  ctx,
		Config:   &appconfig.Config{DataRoot: filepath.Join(t.TempDir(), "data")},
		ConfigDB: store,
		Bus:      NewLoaderBusWithBuffer(8),
		Engine:   &QJSLoaderEngine{},
	})
	loader, err := manager.CreateLoader(ctx, Loader{
		Summary: LoaderSummary{
			ID:      "loader-webhook-integration",
			Name:    "Webhook Integration",
			Runtime: LoaderRuntimeScheduler,
			Enabled: true,
		},
		Script: `
scheduler.on("webhook.integration.test", "on-webhook", function(event) {
  scheduler.log("received webhook", { correlationId: event.payload.correlationId });
  scheduler.event.publish("runtime.integration.requested", {
    value: event.payload.body.value
  });
  return { ok: true };
});
`,
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	go manager.RunEventLoop()
	dispatcher := NewEventDispatcher(ctx, store, manager.Bus())

	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "webhook.integration.test",
		Source:        TopicEventSourceWebhook,
		Provider:      "integration",
		CorrelationID: "corr-integration",
		PayloadJSON:   `{"eventId":"evt-integration","sequence":1,"source":"webhook","topic":"webhook.integration.test","correlationId":"corr-integration","body":{"value":42}}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	dispatcher.DispatchOnce(ctx, 10)

	deadline := time.Now().Add(5 * time.Second)
	for {
		runs, err := store.ListLoaderRuns(ctx, loader.Summary.ID, 10)
		if err != nil {
			t.Fatalf("ListLoaderRuns returned error: %v", err)
		}
		if len(runs) > 0 && runs[0].Status == LoaderRunStatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for loader run, runs=%#v", runs)
		}
		time.Sleep(50 * time.Millisecond)
	}

	loaded, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if loaded.DispatchStatus != TopicEventDispatchPublishedToBus {
		t.Fatalf("webhook event status = %q", loaded.DispatchStatus)
	}
	derived, err := store.ListEvents(ctx, model.TopicEventFilter{Topic: "runtime.integration.requested", Limit: 10})
	if err != nil {
		t.Fatalf("ListEvents derived returned error: %v", err)
	}
	if len(derived) != 1 {
		t.Fatalf("derived events = %#v", derived)
	}
	if derived[0].CorrelationID != "corr-integration" || derived[0].ParentEventID != "evt-integration" {
		t.Fatalf("derived event metadata = %#v", derived[0])
	}
	events, err := store.ListLoaderEvents(ctx, loader.Summary.ID, 20)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	foundPublishLog := false
	for _, event := range events {
		if event.Type == "loader.event.published" && strings.Contains(event.PayloadJSON, "runtime.integration.requested") {
			foundPublishLog = true
		}
	}
	if !foundPublishLog {
		t.Fatalf("expected loader.event.published in loader events: %#v", events)
	}
}
