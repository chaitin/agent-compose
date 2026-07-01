package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	loaderspkg "agent-compose/pkg/loaders"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"
)

func newTopicEventTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	di := do.New()
	do.ProvideValue(di, &appconfig.Config{DataRoot: t.TempDir()})
	store, err := NewConfigStore(di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	t.Cleanup(func() { _ = store.DB().Close() })
	return store
}

func TestConfigStoreCreateAndListTopicEvents(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	createdAt := time.Now().UTC().Add(-time.Second).Truncate(time.Millisecond)

	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "webhook.test.created",
		Source:         TopicEventSourceWebhook,
		Provider:       "test",
		Intent:         "notification",
		CorrelationID:  "corr-1",
		IdempotencyKey: "delivery-1",
		PayloadJSON:    `{"value":1}`,
		CreatedAt:      createdAt,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	if !strings.HasPrefix(created.ID, "evt_") {
		t.Fatalf("event id = %q, want evt_ prefix", created.ID)
	}
	if created.Sequence <= 0 {
		t.Fatalf("event sequence = %d, want positive", created.Sequence)
	}
	if created.DispatchStatus != TopicEventDispatchPending {
		t.Fatalf("dispatch status = %q, want pending", created.DispatchStatus)
	}
	if created.PayloadHash != topicEventPayloadSHA256(`{"value":1}`) {
		t.Fatalf("payload hash = %q", created.PayloadHash)
	}
	if !created.CreatedAt.Equal(createdAt) {
		t.Fatalf("created at = %s, want %s", created.CreatedAt, createdAt)
	}

	loaded, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if loaded.ID != created.ID || loaded.Sequence != created.Sequence {
		t.Fatalf("loaded event = %#v, want id %s sequence %d", loaded, created.ID, created.Sequence)
	}

	items, err := store.ListEvents(ctx, TopicEventFilter{Topic: "webhook.test.created", AfterSequence: 0, Limit: 10})
	if err != nil {
		t.Fatalf("ListEvents by topic returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("ListEvents by topic returned %#v", items)
	}

	items, err = store.ListEvents(ctx, TopicEventFilter{CorrelationID: "corr-1", Limit: 10})
	if err != nil {
		t.Fatalf("ListEvents by correlation returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("ListEvents by correlation returned %#v", items)
	}

	if _, err := store.ListEvents(ctx, TopicEventFilter{Limit: 10}); err == nil {
		t.Fatalf("ListEvents without topic or correlation id returned nil error")
	}
}

func TestConfigStoreTopicEventIdempotency(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)

	first, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "webhook.test.idempotent",
		Source:         TopicEventSourceWebhook,
		CorrelationID:  "corr-1",
		IdempotencyKey: "same-key",
		PayloadHash:    "sha256:same",
		PayloadJSON:    `{"value":1}`,
	})
	if err != nil {
		t.Fatalf("first CreateEvent returned error: %v", err)
	}
	second, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "webhook.test.idempotent",
		Source:         TopicEventSourceWebhook,
		CorrelationID:  "corr-2",
		IdempotencyKey: "same-key",
		PayloadHash:    "sha256:same",
		PayloadJSON:    `{"value":1}`,
	})
	if err != nil {
		t.Fatalf("second CreateEvent returned error: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second event id = %q, want %q", second.ID, first.ID)
	}
	found, ok, err := store.FindEventByIdempotencyKey(ctx, "webhook.test.idempotent", "same-key")
	if err != nil {
		t.Fatalf("FindEventByIdempotencyKey returned error: %v", err)
	}
	if !ok || found.ID != first.ID {
		t.Fatalf("FindEventByIdempotencyKey = %#v/%t, want first event", found, ok)
	}
	if found, ok, err := store.FindEventByIdempotencyKey(ctx, "webhook.test.idempotent", "missing"); err != nil || ok || found.ID != "" {
		t.Fatalf("FindEventByIdempotencyKey missing = %#v/%t/%v", found, ok, err)
	}
	if found, ok, err := store.FindEventByIdempotencyKey(ctx, "", "same-key"); err != nil || ok || found.ID != "" {
		t.Fatalf("FindEventByIdempotencyKey empty topic = %#v/%t/%v", found, ok, err)
	}

	_, err = store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "webhook.test.idempotent",
		Source:         TopicEventSourceWebhook,
		CorrelationID:  "corr-3",
		IdempotencyKey: "same-key",
		PayloadHash:    "sha256:different",
		PayloadJSON:    `{"value":2}`,
	})
	if err == nil || !strings.Contains(err.Error(), "idempotency conflict") {
		t.Fatalf("conflicting CreateEvent error = %v, want idempotency conflict", err)
	}
}

func TestConfigStorePendingAndPublishedTopicEvents(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	first, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "runtime.test.requested",
		Source:        TopicEventSourceLoader,
		CorrelationID: "corr-1",
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent first returned error: %v", err)
	}
	second, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "runtime.test.requested",
		Source:        TopicEventSourceLoader,
		CorrelationID: "corr-2",
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent second returned error: %v", err)
	}

	pending, err := store.ListPendingEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingEvents returned error: %v", err)
	}
	if len(pending) != 2 || pending[0].ID != first.ID || pending[1].ID != second.ID {
		t.Fatalf("pending events = %#v", pending)
	}

	dispatchedAt := time.Now().UTC().Truncate(time.Millisecond)
	if claimed, err := store.ClaimEvent(ctx, first.ID, "publish-claim", dispatchedAt, dispatchedAt.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimEvent returned error: %v", err)
	} else if !claimed {
		t.Fatalf("ClaimEvent returned false")
	}
	if err := store.MarkEventPublished(ctx, first.ID, "publish-claim", dispatchedAt); err != nil {
		t.Fatalf("MarkEventPublished returned error: %v", err)
	}
	loaded, err := store.GetEvent(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if loaded.DispatchStatus != TopicEventDispatchPublishedToBus {
		t.Fatalf("dispatch status = %q, want published", loaded.DispatchStatus)
	}
	if !loaded.DispatchedAt.Equal(dispatchedAt) {
		t.Fatalf("dispatched at = %s, want %s", loaded.DispatchedAt, dispatchedAt)
	}

	pending, err = store.ListPendingEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingEvents after publish returned error: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending after publish = %#v", pending)
	}
}

func TestConfigStoreMarkEventNoSubscriberRequiresActiveClaim(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "runtime.test.no-subscriber",
		Source:        TopicEventSourceLoader,
		CorrelationID: "corr-no-subscriber",
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	if claimed, err := store.ClaimEvent(ctx, created.ID, "claim-active", now, now.Add(time.Minute)); err != nil {
		t.Fatalf("ClaimEvent returned error: %v", err)
	} else if !claimed {
		t.Fatalf("ClaimEvent returned false")
	}
	if err := store.MarkEventNoSubscriber(ctx, created.ID, "stale-claim", now); err != nil {
		t.Fatalf("stale MarkEventNoSubscriber returned error: %v", err)
	}
	loaded, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent after stale no-subscriber returned error: %v", err)
	}
	if loaded.DispatchStatus != TopicEventDispatchPublishing || loaded.ClaimID != "claim-active" {
		t.Fatalf("stale no-subscriber changed claim: %#v", loaded)
	}
	if err := store.MarkEventNoSubscriber(ctx, created.ID, "claim-active", now); err != nil {
		t.Fatalf("MarkEventNoSubscriber returned error: %v", err)
	}
	loaded, err = store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent after no-subscriber returned error: %v", err)
	}
	if loaded.DispatchStatus != TopicEventDispatchNoSubscriber || loaded.ClaimID != "" || !loaded.DispatchedAt.Equal(now) {
		t.Fatalf("no-subscriber event = %#v", loaded)
	}
}

func TestConfigStoreDispatchableEventsIncludeExpiredPublishingClaims(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "runtime.test.expired-claim",
		Source:         TopicEventSourceLoader,
		CorrelationID:  "corr-expired-claim",
		PayloadJSON:    `{}`,
		DispatchStatus: TopicEventDispatchPublishing,
		ClaimID:        "stale-claim",
		ClaimUntil:     now.Add(-time.Second),
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	items, err := store.ListDispatchableEvents(ctx, now, 10)
	if err != nil {
		t.Fatalf("ListDispatchableEvents returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != created.ID {
		t.Fatalf("dispatchable events = %#v", items)
	}

	claimed, err := store.ClaimEvent(ctx, created.ID, "fresh-claim", now, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("ClaimEvent returned error: %v", err)
	}
	if !claimed {
		t.Fatalf("ClaimEvent returned false for expired publishing claim")
	}
	loaded, err := store.GetEvent(ctx, created.ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if loaded.ClaimID != "fresh-claim" || loaded.AttemptCount != 1 {
		t.Fatalf("reclaimed event = %#v", loaded)
	}
}

func TestConfigStoreEventDeliveryDoesNotDowngradeRunOnDuplicateMatch(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	created, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:         "webhook.delivery.duplicate",
		Source:        TopicEventSourceWebhook,
		CorrelationID: "corr-delivery-duplicate",
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	if err := store.UpsertEventDelivery(ctx, EventDelivery{
		EventID:   created.ID,
		LoaderID:  "loader-1",
		TriggerID: "trigger-1",
		RunID:     "run-1",
		Status:    EventDeliveryStatusRunSucceeded,
	}); err != nil {
		t.Fatalf("UpsertEventDelivery run returned error: %v", err)
	}
	if err := store.UpsertEventDelivery(ctx, EventDelivery{
		EventID:   created.ID,
		LoaderID:  "loader-1",
		TriggerID: "trigger-1",
		Status:    EventDeliveryStatusMatched,
	}); err != nil {
		t.Fatalf("UpsertEventDelivery matched returned error: %v", err)
	}

	items, err := store.ListEventDeliveries(ctx, []string{created.ID})
	if err != nil {
		t.Fatalf("ListEventDeliveries returned error: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("deliveries = %#v", items)
	}
	if items[0].RunID != "run-1" || items[0].Status != EventDeliveryStatusRunSucceeded {
		t.Fatalf("delivery was downgraded: %#v", items[0])
	}
}

func TestConfigStoreWebhookSourceCRUDAndTopicMatching(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	if _, err := store.ListEnabledWebhookSourcesForTopic(ctx, " "); err == nil {
		t.Fatalf("ListEnabledWebhookSourcesForTopic blank topic returned nil error")
	}
	source, err := store.UpsertWebhookSource(ctx, WebhookSource{
		ID:             "gitlab-main",
		Enabled:        true,
		Provider:       "gitlab",
		TopicPrefix:    "webhook.gitlab.",
		TokenHash:      "sha256:token",
		SignatureType:  "hmac-sha256",
		BodyLimitBytes: 512,
	})
	if err != nil {
		t.Fatalf("UpsertWebhookSource returned error: %v", err)
	}
	if source.Name != "gitlab-main" {
		t.Fatalf("source name default = %q", source.Name)
	}
	for _, topic := range []string{"webhook.gitlab", "webhook.gitlab.push"} {
		matches, err := store.ListEnabledWebhookSourcesForTopic(ctx, topic)
		if err != nil {
			t.Fatalf("ListEnabledWebhookSourcesForTopic(%q) returned error: %v", topic, err)
		}
		if len(matches) != 1 || matches[0].ID != source.ID {
			t.Fatalf("matches for %q = %#v", topic, matches)
		}
	}
	if webhookSourceTopicMatches("", "webhook.gitlab.") || webhookSourceTopicMatches("webhook.github", "") {
		t.Fatalf("blank webhook source topic match returned true")
	}
	got, ok, err := store.GetWebhookSource(ctx, "gitlab-main")
	if err != nil || !ok || got.Provider != "gitlab" {
		t.Fatalf("GetWebhookSource = %#v/%t/%v", got, ok, err)
	}
	items, err := store.ListWebhookSources(ctx)
	if err != nil {
		t.Fatalf("ListWebhookSources returned error: %v", err)
	}
	if len(items) != 1 || items[0].ID != "gitlab-main" {
		t.Fatalf("webhook sources = %#v", items)
	}
	if err := store.DeleteWebhookSource(ctx, "gitlab-main"); err != nil {
		t.Fatalf("DeleteWebhookSource returned error: %v", err)
	}
	if _, ok, err := store.GetWebhookSource(ctx, "gitlab-main"); err != nil || ok {
		t.Fatalf("deleted GetWebhookSource ok=%t err=%v", ok, err)
	}
	if err := store.DeleteWebhookSource(ctx, "gitlab-main"); err == nil {
		t.Fatalf("DeleteWebhookSource missing returned nil error")
	}
}

func TestTopicEventModelAndStoreErrorBranches(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)

	for _, topic := range []string{"", strings.Repeat("a", 129), "bad topic"} {
		if err := validateTopicEventName(topic); err == nil {
			t.Fatalf("validateTopicEventName(%q) returned nil error", topic)
		}
	}
	if err := validateTopicEventName("runtime.good-topic_1"); err != nil {
		t.Fatalf("validateTopicEventName valid returned error: %v", err)
	}
	if normalizeTopicEventSource(" WEBHOOK ") != TopicEventSourceWebhook ||
		normalizeTopicEventSource("LOADER") != TopicEventSourceLoader ||
		normalizeTopicEventSource("system") != TopicEventSourceSystem ||
		normalizeTopicEventSource("bad") != "" {
		t.Fatalf("normalizeTopicEventSource returned unexpected values")
	}
	if normalizeTopicEventDispatchStatus("") != TopicEventDispatchPending ||
		normalizeTopicEventDispatchStatus("PUBLISHED_TO_BUS") != TopicEventDispatchPublishedToBus ||
		normalizeTopicEventDispatchStatus("bad") != "" {
		t.Fatalf("normalizeTopicEventDispatchStatus returned unexpected values")
	}

	normalized, err := normalizeTopicEventRecord(TopicEventRecord{
		ID:             " evt-custom ",
		Topic:          " runtime.custom ",
		Source:         " system ",
		Provider:       " provider ",
		Intent:         " intent ",
		IdempotencyKey: " idem ",
		DeliveryID:     " delivery ",
		PayloadJSON:    "",
		ParentEventID:  " parent ",
		PublisherType:  " loader ",
		PublisherID:    " loader-1 ",
		PublisherRunID: " run-1 ",
		CreatedAt:      time.Now().Add(-time.Hour),
		DispatchedAt:   time.Now(),
	}, false)
	if err != nil {
		t.Fatalf("normalizeTopicEventRecord returned error: %v", err)
	}
	if normalized.ID != "evt-custom" || normalized.CorrelationID != "evt-custom" || normalized.PayloadJSON != "{}" || normalized.PayloadHash == "" {
		t.Fatalf("normalized event = %#v", normalized)
	}
	for _, item := range []TopicEventRecord{
		{Topic: "runtime.missing.id", Source: TopicEventSourceSystem},
		{ID: "evt", Topic: "", Source: TopicEventSourceSystem},
		{ID: "evt", Topic: "runtime.bad.source", Source: "bad"},
		{ID: "evt", Topic: "runtime.bad.status", Source: TopicEventSourceSystem, DispatchStatus: "bad"},
		{ID: "evt", Topic: "runtime.bad.payload", Source: TopicEventSourceSystem, PayloadJSON: `[`},
	} {
		if _, err := normalizeTopicEventRecord(item, false); err == nil {
			t.Fatalf("normalizeTopicEventRecord(%#v) returned nil error", item)
		}
	}

	first, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "runtime.branch",
		Source:         TopicEventSourceSystem,
		CorrelationID:  "corr-branch",
		IdempotencyKey: "branch-key",
		PayloadJSON:    `{"value":1}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}
	second, err := store.CreateEvent(ctx, TopicEventRecord{
		Topic:          "runtime.branch",
		Source:         TopicEventSourceSystem,
		CorrelationID:  "corr-branch",
		DispatchStatus: TopicEventDispatchPublishedToBus,
		PayloadJSON:    `{"value":2}`,
		DispatchedAt:   time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("CreateEvent second returned error: %v", err)
	}
	filtered, err := store.ListEvents(ctx, TopicEventFilter{
		Topic:          "runtime.branch",
		CorrelationID:  "corr-branch",
		AfterSequence:  first.Sequence,
		DispatchStatus: TopicEventDispatchPublishedToBus,
		Limit:          999,
	})
	if err != nil {
		t.Fatalf("ListEvents filtered returned error: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ID != second.ID {
		t.Fatalf("filtered events = %#v", filtered)
	}
	if _, err := store.ListEvents(ctx, TopicEventFilter{Topic: "bad topic"}); err == nil {
		t.Fatalf("ListEvents bad topic returned nil error")
	}
	if pending, err := store.ListPendingEvents(ctx, -1); err != nil || len(pending) != 1 || pending[0].ID != first.ID {
		t.Fatalf("ListPendingEvents default limit = %#v/%v", pending, err)
	}
	if _, err := store.GetEvent(ctx, " "); err == nil {
		t.Fatalf("GetEvent blank returned nil error")
	}
	if _, err := store.GetEvent(ctx, "missing"); err == nil {
		t.Fatalf("GetEvent missing returned nil error")
	}
	if _, ok, err := store.FindEventByIdempotencyKey(ctx, "runtime.branch", " "); err != nil || ok {
		t.Fatalf("FindEventByIdempotencyKey blank key = %t/%v", ok, err)
	}
	if err := store.MarkEventPublished(ctx, " ", "claim", time.Time{}); err == nil {
		t.Fatalf("MarkEventPublished blank returned nil error")
	}
	if err := store.MarkEventPublished(ctx, "missing", "claim", time.Time{}); err == nil {
		t.Fatalf("MarkEventPublished missing returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, " ", `{}`); err == nil {
		t.Fatalf("UpdateEventPayload blank id returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, first.ID, " "); err == nil {
		t.Fatalf("UpdateEventPayload blank payload returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, first.ID, `[`); err == nil {
		t.Fatalf("UpdateEventPayload bad payload returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, "missing", `{}`); err == nil {
		t.Fatalf("UpdateEventPayload missing returned nil error")
	}
	if err := store.UpdateEventPayload(ctx, first.ID, `{"updated":true}`); err != nil {
		t.Fatalf("UpdateEventPayload returned error: %v", err)
	}
	updated, err := store.GetEvent(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetEvent updated returned error: %v", err)
	}
	if updated.PayloadJSON != `{"updated":true}` {
		t.Fatalf("updated payload = %q", updated.PayloadJSON)
	}
	if updated.PayloadHash != topicEventPayloadSHA256(updated.PayloadJSON) {
		t.Fatalf("updated payload hash = %q, want hash of %s", updated.PayloadHash, updated.PayloadJSON)
	}
}

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

func TestLoaderRunHostPublishEventStoresDerivedEvent(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	manager := newTestLoaderManager(t, loaderspkg.ManagerDeps{ConfigDB: store})
	host := newLoaderRunHost(manager, Loader{Summary: LoaderSummary{ID: "loader-1"}}, &LoaderRunSummary{ID: "run-1", LoaderID: "loader-1", TriggerID: "trigger-1"}, loaderTriggerEventMetadata{
		EventID:       "evt-parent",
		CorrelationID: "corr-parent",
		Sequence:      10,
	})

	created, err := host.PublishEvent(ctx, "runtime.test.requested", `{"provider":"test-runtime","value":1}`)
	if err != nil {
		t.Fatalf("PublishEvent returned error: %v", err)
	}
	if created.CorrelationID != "corr-parent" || created.ParentEventID != "evt-parent" {
		t.Fatalf("created event inheritance = %#v", created)
	}
	if created.PublisherID != "loader-1" || created.PublisherRunID != "run-1" {
		t.Fatalf("publisher metadata = %#v", created)
	}
	if created.Provider != "test-runtime" {
		t.Fatalf("provider = %q, want test-runtime", created.Provider)
	}
	var envelope map[string]any
	if err := json.Unmarshal([]byte(created.PayloadJSON), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if envelope["sequence"].(float64) != float64(created.Sequence) {
		t.Fatalf("sequence in envelope = %#v, want %d", envelope["sequence"], created.Sequence)
	}

	if _, err := host.PublishEvent(ctx, "webhook.not.allowed", `{}`); err == nil {
		t.Fatalf("PublishEvent with webhook topic returned nil error")
	}
}

func TestLoaderRunHostLinkedLoaderEventStoresEventSessionLink(t *testing.T) {
	ctx := context.Background()
	store := newTopicEventTestConfigStore(t)
	manager := newTestLoaderManager(t, loaderspkg.ManagerDeps{ConfigDB: store})
	loader := createTestLoader(t, ctx, store)
	run := LoaderRunSummary{
		ID:            "run-link",
		LoaderID:      loader.Summary.ID,
		TriggerID:     "trigger-link",
		TriggerKind:   LoaderTriggerKindEvent,
		TriggerSource: "event",
		Status:        LoaderRunStatusRunning,
		StartedAt:     time.Now().UTC(),
	}
	if err := store.CreateLoaderRun(ctx, run); err != nil {
		t.Fatalf("CreateLoaderRun returned error: %v", err)
	}
	host := newLoaderRunHost(manager, loader, &run, loaderTriggerEventMetadata{EventID: "evt-link"})

	if err := host.AddLinkedLoaderEvent(ctx, "loader.command.completed", "info", "command completed", map[string]any{"sessionId": "session-link"}, "session-link", "cell-link", ""); err != nil {
		t.Fatalf("addLinkedLoaderEvent returned error: %v", err)
	}
	links, err := store.ListEventSessionLinks(ctx, []string{"evt-link"})
	if err != nil {
		t.Fatalf("ListEventSessionLinks returned error: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("event session links = %#v, want one link", links)
	}
	link := links[0]
	if link.EventID != "evt-link" || link.SessionID != "session-link" || link.Relation != "loader.command.completed" {
		t.Fatalf("event session link identity = %#v", link)
	}
	if link.LoaderID != loader.Summary.ID || link.RunID != "run-link" || link.TriggerID != "trigger-link" || link.LoaderEventID == "" {
		t.Fatalf("event session link metadata = %#v", link)
	}

	noEventRun := LoaderRunSummary{
		ID:            "run-no-event",
		LoaderID:      loader.Summary.ID,
		TriggerID:     "trigger-link",
		TriggerKind:   LoaderTriggerKindEvent,
		TriggerSource: "event",
		Status:        LoaderRunStatusRunning,
		StartedAt:     time.Now().UTC(),
	}
	if err := store.CreateLoaderRun(ctx, noEventRun); err != nil {
		t.Fatalf("CreateLoaderRun without trigger event returned error: %v", err)
	}
	noEventHost := newLoaderRunHost(manager, loader, &noEventRun, loaderTriggerEventMetadata{})
	if err := noEventHost.AddLinkedLoaderEvent(ctx, "loader.command.completed", "info", "command completed", nil, "session-no-event", "", ""); err != nil {
		t.Fatalf("addLinkedLoaderEvent without trigger event returned error: %v", err)
	}
	links, err = store.ListEventSessionLinks(ctx, []string{"evt-link"})
	if err != nil {
		t.Fatalf("ListEventSessionLinks after no-event run returned error: %v", err)
	}
	if len(links) != 1 {
		t.Fatalf("event session links after no-event run = %#v, want original link only", links)
	}
}

func TestConfigStoreEventSchemaCreated(t *testing.T) {
	store := newTopicEventTestConfigStore(t)
	var name string
	if err := store.DB().QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'event'`).Scan(&name); err != nil {
		t.Fatalf("query event schema returned error: %v", err)
	}
	if name != "event" {
		t.Fatalf("schema table = %q, want event", name)
	}
}

func TestConfigStoreEventSchemaMigratesLegacyDispatchColumnsBeforeIndexes(t *testing.T) {
	ctx := context.Background()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.ExecContext(ctx, `CREATE TABLE event (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		id TEXT NOT NULL UNIQUE,
		topic TEXT NOT NULL,
		source TEXT NOT NULL,
		provider TEXT NOT NULL DEFAULT '',
		intent TEXT NOT NULL DEFAULT '',
		correlation_id TEXT NOT NULL,
		idempotency_key TEXT NOT NULL DEFAULT '',
		delivery_id TEXT NOT NULL DEFAULT '',
		payload_hash TEXT NOT NULL,
		payload_json TEXT NOT NULL,
		dispatch_status TEXT NOT NULL,
		parent_event_id TEXT NOT NULL DEFAULT '',
		publisher_type TEXT NOT NULL DEFAULT '',
		publisher_id TEXT NOT NULL DEFAULT '',
		publisher_run_id TEXT NOT NULL DEFAULT '',
		created_at INTEGER NOT NULL,
		dispatched_at INTEGER NOT NULL DEFAULT 0
	)`); err != nil {
		t.Fatalf("create legacy event: %v", err)
	}
	store := NewConfigStoreFromDB(db)
	if err := store.EnsureEventSchema(ctx); err != nil {
		t.Fatalf("ensureEventSchema returned error: %v", err)
	}
	for _, column := range []string{"next_attempt_at", "claim_id", "claim_until", "attempt_count", "last_error", "dead_letter_at"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('event') WHERE name = ?`, column).Scan(&count); err != nil {
			t.Fatalf("query migrated column %s: %v", column, err)
		}
		if count != 1 {
			t.Fatalf("column %s count = %d, want 1", column, count)
		}
	}
	var indexName string
	if err := db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'index' AND name = 'idx_event_dispatch_attempt'`).Scan(&indexName); err != nil {
		t.Fatalf("query dispatch attempt index: %v", err)
	}
}
