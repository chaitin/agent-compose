package agentcompose

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/events/webhooks"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
)

func TestWebhookRunQueueMatchesPayloadRules(t *testing.T) {
	testWebhookRunQueueMatchesPayloadRules(t)
}

func TestWebhookQueueIntegrationMatchesPayloadRules(t *testing.T) {
	testWebhookRunQueueMatchesPayloadRules(t)
}

func TestWebhookQueueE2EMatchesPayloadRules(t *testing.T) {
	testWebhookRunQueueMatchesPayloadRules(t)
}

func testWebhookRunQueueMatchesPayloadRules(t *testing.T) {
	t.Helper()
	queue, err := webhooks.NewRunQueueFromConfig(&appconfig.Config{
		WebhookQueueDefaultWorkers: 8,
		WebhookQueueRulesJSON: `[
			{"name":"repo-a","workers":1,"match":{"topic":"webhook.github.push","payload":{"body.repository.full_name":"org/repo-a"}}},
			{"name":"github-default","workers":3,"match":{"topic":"webhook.github.*"}}
		]`,
	})
	if err != nil {
		t.Fatalf("webhooks.NewRunQueueFromConfig returned error: %v", err)
	}

	name, workers := queue.Match(domain.LoaderTopicEvent{
		Topic: "webhook.github.push",
		Payload: map[string]any{
			"body": map[string]any{
				"repository": map[string]any{"full_name": "org/repo-a"},
			},
		},
	})
	if name != "repo-a" || workers != 1 {
		t.Fatalf("repo-a queue = %s/%d", name, workers)
	}

	name, workers = queue.Match(domain.LoaderTopicEvent{
		Topic: "webhook.github.push",
		Payload: map[string]any{
			"body": map[string]any{
				"repository": map[string]any{"full_name": "org/repo-b"},
			},
		},
	})
	if name != "github-default" || workers != 3 {
		t.Fatalf("repo-b queue = %s/%d", name, workers)
	}
}

func TestWebhookRunQueueReserveAndRelease(t *testing.T) {
	testWebhookRunQueueReserveAndRelease(t)
}

func TestWebhookQueueIntegrationReserveAndRelease(t *testing.T) {
	testWebhookRunQueueReserveAndRelease(t)
}

func TestWebhookQueueE2EReserveAndRelease(t *testing.T) {
	testWebhookRunQueueReserveAndRelease(t)
}

func testWebhookRunQueueReserveAndRelease(t *testing.T) {
	t.Helper()
	queue, err := webhooks.NewRunQueueFromConfig(&appconfig.Config{
		WebhookQueueDefaultWorkers: 1,
	})
	if err != nil {
		t.Fatalf("webhooks.NewRunQueueFromConfig returned error: %v", err)
	}
	event := domain.LoaderTopicEvent{Topic: "webhook.github.push", Payload: map[string]any{}}
	reservation, ok := queue.Reserve(event)
	if !ok {
		t.Fatalf("first reserve failed")
	}
	if _, ok := queue.Reserve(event); ok {
		t.Fatalf("second reserve succeeded while queue was full")
	}
	reservation.Release()
	if _, ok := queue.Reserve(event); !ok {
		t.Fatalf("reserve after release failed")
	}
}

func TestLoaderEventLoopRetriesWhenWebhookQueueFull(t *testing.T) {
	testLoaderEventLoopRetriesWhenWebhookQueueFull(t)
}

func TestWebhookQueueIntegrationRetriesWhenWebhookQueueFull(t *testing.T) {
	testLoaderEventLoopRetriesWhenWebhookQueueFull(t)
}

func TestWebhookQueueE2ERetriesWhenWebhookQueueFull(t *testing.T) {
	testLoaderEventLoopRetriesWhenWebhookQueueFull(t)
}

func testLoaderEventLoopRetriesWhenWebhookQueueFull(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	manager := &LoaderManager{
		rootCtx: ctx,
		config: &appconfig.Config{
			DataRoot:                   filepath.Join(t.TempDir(), "data"),
			WebhookQueueDefaultWorkers: 1,
		},
		configDB:     store,
		bus:          newTestLoaderBus(8),
		engine:       &loaders.QJSLoaderEngine{},
		eventQueue:   webhooks.NewRunQueue(1),
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}
	if _, err := manager.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{
			ID:                "loader-webhook-queue",
			Name:              "Webhook Queue",
			Runtime:           domain.LoaderRuntimeScheduler,
			Enabled:           true,
			ConcurrencyPolicy: domain.LoaderConcurrencyPolicyParallel,
		},
		Script: `
scheduler.on("webhook.queue.test", "on-webhook", function(event) {
  scheduler.sleep(200);
  return { ok: true };
});
`,
	}); err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}

	first, ok := manager.eventQueue.Reserve(domain.LoaderTopicEvent{Topic: "webhook.queue.test", Payload: map[string]any{}})
	if !ok {
		t.Fatalf("failed to fill queue")
	}
	defer first.Release()

	go manager.eventLoop()
	dispatcher := NewEventDispatcher(ctx, store, manager.bus)
	created, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		Topic:         "webhook.queue.test",
		Source:        domain.TopicEventSourceWebhook,
		CorrelationID: "corr-queue",
		PayloadJSON:   `{"eventId":"evt-queue","topic":"webhook.queue.test","body":{"repository":{"full_name":"org/repo"}}}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	dispatcher.DispatchOnce(ctx, 10)
	deadline := time.Now().Add(2 * time.Second)
	for {
		loaded, err := store.GetEvent(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetEvent returned error: %v", err)
		}
		if loaded.DispatchStatus == domain.TopicEventDispatchRetrying {
			if loaded.LastError == "" || loaded.NextAttemptAt.IsZero() {
				t.Fatalf("retry metadata missing: %#v", loaded)
			}
			runs, err := store.ListLoaderRuns(ctx, "loader-webhook-queue", 10)
			if err != nil {
				t.Fatalf("ListLoaderRuns returned error: %v", err)
			}
			if len(runs) != 0 {
				t.Fatalf("runs were created while queue was full: %#v", runs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for retrying status, event=%#v", loaded)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestLoaderEventLoopRetriesWhenSkipPolicyLoaderBusy(t *testing.T) {
	testLoaderEventLoopRetriesWhenSkipPolicyLoaderBusy(t)
}

func TestWebhookQueueIntegrationRetriesWhenSkipPolicyLoaderBusy(t *testing.T) {
	testLoaderEventLoopRetriesWhenSkipPolicyLoaderBusy(t)
}

func TestWebhookQueueE2ERetriesWhenSkipPolicyLoaderBusy(t *testing.T) {
	testLoaderEventLoopRetriesWhenSkipPolicyLoaderBusy(t)
}

func testLoaderEventLoopRetriesWhenSkipPolicyLoaderBusy(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	manager := &LoaderManager{
		rootCtx: ctx,
		config: &appconfig.Config{
			DataRoot:                   filepath.Join(t.TempDir(), "data"),
			WebhookQueueDefaultWorkers: 8,
		},
		configDB:     store,
		bus:          newTestLoaderBus(8),
		engine:       &loaders.QJSLoaderEngine{},
		eventQueue:   webhooks.NewRunQueue(8),
		loaders:      map[string]Loader{},
		running:      map[string]int{"loader-webhook-busy": 1},
		scheduleWake: make(chan struct{}, 1),
	}
	if _, err := manager.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{
			ID:      "loader-webhook-busy",
			Name:    "Webhook Busy",
			Runtime: domain.LoaderRuntimeScheduler,
			Enabled: true,
		},
		Script: `
scheduler.on("webhook.busy.test", "on-webhook", function(event) {
  return { ok: true };
});
`,
	}); err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	manager.mu.Lock()
	manager.running["loader-webhook-busy"] = 1
	manager.mu.Unlock()

	go manager.eventLoop()
	dispatcher := NewEventDispatcher(ctx, store, manager.bus)
	created, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		Topic:         "webhook.busy.test",
		Source:        domain.TopicEventSourceWebhook,
		CorrelationID: "corr-busy",
		PayloadJSON:   `{"eventId":"evt-busy","topic":"webhook.busy.test","body":{"repository":{"full_name":"org/repo"}}}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	dispatcher.DispatchOnce(ctx, 10)
	deadline := time.Now().Add(2 * time.Second)
	for {
		loaded, err := store.GetEvent(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetEvent returned error: %v", err)
		}
		if loaded.DispatchStatus == domain.TopicEventDispatchRetrying {
			if loaded.LastError != "loader is already running" || loaded.NextAttemptAt.IsZero() {
				t.Fatalf("retry metadata = %#v", loaded)
			}
			runs, err := store.ListLoaderRuns(ctx, "loader-webhook-busy", 10)
			if err != nil {
				t.Fatalf("ListLoaderRuns returned error: %v", err)
			}
			if len(runs) != 0 {
				t.Fatalf("busy webhook event created run instead of retrying: %#v", runs)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for retrying status, event=%#v", loaded)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestLoaderEventLoopDedupesWebhookTargetsByLoader(t *testing.T) {
	testLoaderEventLoopDedupesWebhookTargetsByLoader(t)
}

func TestWebhookQueueIntegrationDedupesWebhookTargetsByLoader(t *testing.T) {
	testLoaderEventLoopDedupesWebhookTargetsByLoader(t)
}

func TestWebhookQueueE2EDedupesWebhookTargetsByLoader(t *testing.T) {
	testLoaderEventLoopDedupesWebhookTargetsByLoader(t)
}

func testLoaderEventLoopDedupesWebhookTargetsByLoader(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	manager := &LoaderManager{
		rootCtx: ctx,
		config: &appconfig.Config{
			DataRoot:                   filepath.Join(t.TempDir(), "data"),
			WebhookQueueDefaultWorkers: 8,
		},
		configDB:     store,
		bus:          newTestLoaderBus(8),
		engine:       &loaders.QJSLoaderEngine{},
		eventQueue:   webhooks.NewRunQueue(8),
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}
	loader, err := manager.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{
			ID:                "loader-webhook-dedupe",
			Name:              "Webhook Dedupe",
			Runtime:           domain.LoaderRuntimeScheduler,
			Enabled:           true,
			ConcurrencyPolicy: domain.LoaderConcurrencyPolicyParallel,
		},
		Script: `
scheduler.on("webhook.dedupe.test", "exact", function(event) {
  return { trigger: "exact" };
});
scheduler.on("webhook.dedupe.*", "wildcard", function(event) {
  return { trigger: "wildcard" };
});
`,
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}

	go manager.eventLoop()
	dispatcher := NewEventDispatcher(ctx, store, manager.bus)
	created, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		Topic:         "webhook.dedupe.test",
		Source:        domain.TopicEventSourceWebhook,
		CorrelationID: "corr-dedupe",
		PayloadJSON:   `{"eventId":"evt-dedupe","topic":"webhook.dedupe.test","body":{"repository":{"full_name":"org/repo"}}}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	dispatcher.DispatchOnce(ctx, 10)
	deadline := time.Now().Add(3 * time.Second)
	for {
		runs, err := store.ListLoaderRuns(ctx, loader.Summary.ID, 10)
		if err != nil {
			t.Fatalf("ListLoaderRuns returned error: %v", err)
		}
		if len(runs) == 1 && runs[0].Status == domain.LoaderRunStatusSucceeded {
			loaded, err := store.GetEvent(ctx, created.ID)
			if err != nil {
				t.Fatalf("GetEvent returned error: %v", err)
			}
			if loaded.DispatchStatus != domain.TopicEventDispatchPublishedToBus {
				t.Fatalf("dispatch status = %q, want published_to_bus", loaded.DispatchStatus)
			}
			return
		}
		if len(runs) > 1 {
			t.Fatalf("webhook event created duplicate runs for one loader: %#v", runs)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for single run, runs=%#v", runs)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestLoaderEventLoopRunsAllWebhookTargetLoaders(t *testing.T) {
	testLoaderEventLoopRunsAllWebhookTargetLoaders(t)
}

func TestWebhookQueueIntegrationRunsAllWebhookTargetLoaders(t *testing.T) {
	testLoaderEventLoopRunsAllWebhookTargetLoaders(t)
}

func TestWebhookQueueE2ERunsAllWebhookTargetLoaders(t *testing.T) {
	testLoaderEventLoopRunsAllWebhookTargetLoaders(t)
}

func testLoaderEventLoopRunsAllWebhookTargetLoaders(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	manager := &LoaderManager{
		rootCtx: ctx,
		config: &appconfig.Config{
			DataRoot:                   filepath.Join(t.TempDir(), "data"),
			WebhookQueueDefaultWorkers: 8,
		},
		configDB:     store,
		bus:          newTestLoaderBus(8),
		engine:       &loaders.QJSLoaderEngine{},
		eventQueue:   webhooks.NewRunQueue(8),
		loaders:      map[string]Loader{},
		running:      map[string]int{},
		scheduleWake: make(chan struct{}, 1),
	}
	script := `
scheduler.on("webhook.multi.test", "on-webhook", function(event) {
  return { ok: true };
});
`
	first, err := manager.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{
			ID:      "loader-webhook-multi-a",
			Name:    "Webhook Multi A",
			Runtime: domain.LoaderRuntimeScheduler,
			Enabled: true,
		},
		Script: script,
	})
	if err != nil {
		t.Fatalf("CreateLoader first returned error: %v", err)
	}
	second, err := manager.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{
			ID:      "loader-webhook-multi-b",
			Name:    "Webhook Multi B",
			Runtime: domain.LoaderRuntimeScheduler,
			Enabled: true,
		},
		Script: script,
	})
	if err != nil {
		t.Fatalf("CreateLoader second returned error: %v", err)
	}

	go manager.eventLoop()
	dispatcher := NewEventDispatcher(ctx, store, manager.bus)
	created, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		Topic:         "webhook.multi.test",
		Source:        domain.TopicEventSourceWebhook,
		CorrelationID: "corr-multi",
		PayloadJSON:   `{"eventId":"evt-multi","topic":"webhook.multi.test","body":{"repository":{"full_name":"org/repo"}}}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	dispatcher.DispatchOnce(ctx, 10)
	deadline := time.Now().Add(3 * time.Second)
	for {
		firstRuns, err := store.ListLoaderRuns(ctx, first.Summary.ID, 10)
		if err != nil {
			t.Fatalf("ListLoaderRuns first returned error: %v", err)
		}
		secondRuns, err := store.ListLoaderRuns(ctx, second.Summary.ID, 10)
		if err != nil {
			t.Fatalf("ListLoaderRuns second returned error: %v", err)
		}
		if len(firstRuns) == 1 && len(secondRuns) == 1 &&
			firstRuns[0].Status == domain.LoaderRunStatusSucceeded &&
			secondRuns[0].Status == domain.LoaderRunStatusSucceeded {
			loaded, err := store.GetEvent(ctx, created.ID)
			if err != nil {
				t.Fatalf("GetEvent returned error: %v", err)
			}
			if loaded.DispatchStatus != domain.TopicEventDispatchPublishedToBus {
				t.Fatalf("dispatch status = %q, want published_to_bus", loaded.DispatchStatus)
			}
			return
		}
		if len(firstRuns) > 1 || len(secondRuns) > 1 {
			t.Fatalf("duplicate runs: first=%#v second=%#v", firstRuns, secondRuns)
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for both runs, first=%#v second=%#v", firstRuns, secondRuns)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func TestLoaderManagerWebhookQueueBypassesNonWebhookEvents(t *testing.T) {
	testLoaderManagerWebhookQueueBypassesNonWebhookEvents(t)
}

func TestWebhookQueueIntegrationBypassesNonWebhookEvents(t *testing.T) {
	testLoaderManagerWebhookQueueBypassesNonWebhookEvents(t)
}

func TestWebhookQueueE2EBypassesNonWebhookEvents(t *testing.T) {
	testLoaderManagerWebhookQueueBypassesNonWebhookEvents(t)
}

func testLoaderManagerWebhookQueueBypassesNonWebhookEvents(t *testing.T) {
	t.Helper()
	manager := &LoaderManager{
		config:     &appconfig.Config{WebhookQueueDefaultWorkers: 1},
		eventQueue: webhooks.NewRunQueue(1),
	}
	first, ok := manager.eventQueue.Reserve(domain.LoaderTopicEvent{
		Source:  domain.TopicEventSourceWebhook,
		Topic:   "webhook.queue.test",
		Payload: map[string]any{},
	})
	if !ok {
		t.Fatalf("failed to fill queue")
	}
	defer first.Release()

	reservations, ok := manager.reserveEventQueueSlots(domain.LoaderTopicEvent{
		Source:  domain.TopicEventSourceLoader,
		Topic:   "runtime.queue.test",
		Payload: map[string]any{},
	}, 2)
	if !ok {
		t.Fatalf("non-webhook event was limited by webhook queue")
	}
	if len(reservations) != 2 {
		t.Fatalf("reservations = %d, want 2", len(reservations))
	}
	for _, reservation := range reservations {
		reservation.Release()
	}
}
