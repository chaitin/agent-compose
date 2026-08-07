package webhooks_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"agent-compose/pkg/events/webhooks"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
	"agent-compose/pkg/storage/sqlite"

	"github.com/labstack/echo/v4"
)

func TestIntegrationEventQueryHTTPWorkflow(t *testing.T) {
	testEventQueryHTTPWorkflow(t)
}

func TestE2EEventQueryHTTPWorkflow(t *testing.T) {
	testEventQueryHTTPWorkflow(t)
}

func TestIntegrationWebhookParentHeaderPersistsIntoTraceDescendants(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "data.db"), 0)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := configstore.FromDB(database.DB())
	root, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "webhook-root", Topic: "webhook.devboard.task.created", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "task-123", PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatalf("create root event: %v", err)
	}
	if _, err := store.UpsertWebhookSource(ctx, domain.WebhookSource{
		ID: "agentkit", Name: "AgentKit internal", Enabled: true, Provider: "agentkit",
		TopicPrefix: "webhook.agentkit.", TokenHash: webhooks.TokenHash("internal-token"),
	}); err != nil {
		t.Fatalf("upsert webhook source: %v", err)
	}

	app := echo.New()
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{
		Store: store, QueryStore: store, NewEventID: func() string { return "webhook-child" },
	})
	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.agentkit.devboard.project.demo.task.created", strings.NewReader(`{"kind":"task"}`))
	request.Header.Set("Authorization", "Bearer internal-token")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "task-123:workload")
	request.Header.Set("X-Correlation-ID", root.CorrelationID)
	request.Header.Set("X-Agent-Compose-Parent-Event-ID", root.ID)
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST child webhook status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	child, err := store.GetEvent(ctx, "webhook-child")
	if err != nil {
		t.Fatalf("get child event: %v", err)
	}
	if child.ParentEventID != root.ID || child.CorrelationID != root.CorrelationID {
		t.Fatalf("stored child lineage = parent %q correlation %q", child.ParentEventID, child.CorrelationID)
	}
	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{
		EventID: child.ID, SchedulerID: "workload-scheduler", TriggerID: "workload-trigger",
		Status: domain.EventDeliveryStatusMatched,
	}); err != nil {
		t.Fatalf("upsert child delivery: %v", err)
	}

	var trace webhooks.EventTraceResponse
	if err := json.Unmarshal([]byte(getEventQueryResponse(t, app, "/api/events/"+root.ID+"/trace")), &trace); err != nil {
		t.Fatalf("decode trace response: %v", err)
	}
	if trace.Event.EventID != root.ID || len(trace.Runs) != 1 || trace.Runs[0].Delivery.EventID != child.ID {
		t.Fatalf("trace did not include HTTP-created descendant: %#v", trace)
	}
}

func TestIntegrationWebhookSourceCustomTokenHeaderIsNotPersisted(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "data.db"), 0)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := configstore.FromDB(database.DB())
	app := echo.New()
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{
		Store: store, NewEventID: func() string { return "webhook-custom-header" },
	})

	request := httptest.NewRequest(http.MethodPut, "/api/webhook-sources/generic", strings.NewReader(`{"name":"Generic","enabled":true,"provider":"generic","topic_prefix":"webhook.generic.","token":"custom-token","token_header":"User-Agent"}`))
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("PUT source status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.generic.push", strings.NewReader(`{"intent":"push"}`))
	request.Header.Set("User-Agent", "custom-token")
	request.Header.Set("Content-Type", "application/json")
	recorder = httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST webhook status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	event, err := store.GetEvent(ctx, "webhook-custom-header")
	if err != nil {
		t.Fatalf("get webhook event: %v", err)
	}
	if strings.Contains(event.PayloadJSON, "custom-token") || strings.Contains(event.PayloadJSON, `"user-agent"`) {
		t.Fatalf("payload persisted the configured credential header: %s", event.PayloadJSON)
	}
}

func TestIntegrationLegacyProtocolTokenHeaderIsNotReinterpretedAsMetadata(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "data.db"), 0)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	// Simulate a source persisted by an older version, before protocol metadata
	// headers were rejected as credential names.
	if _, err := database.DB().ExecContext(ctx, `INSERT INTO webhook_source(id, name, enabled, provider, topic_prefix, token_hash, token_header, created_at, updated_at) VALUES(?, ?, 1, ?, ?, ?, ?, 1, 1)`,
		"legacy-correlation", "Legacy correlation", "generic", "webhook.legacy.", webhooks.TokenHash("custom-token"), "X-Correlation-ID"); err != nil {
		t.Fatalf("insert legacy webhook source: %v", err)
	}
	store := configstore.FromDB(database.DB())
	app := echo.New()
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{
		Store: store, NewEventID: func() string { return "webhook-legacy-correlation" },
	})

	request := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.legacy.push", strings.NewReader(`{"intent":"push"}`))
	request.Header.Set("X-Correlation-ID", "custom-token")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("POST legacy webhook status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	event, err := store.GetEvent(ctx, "webhook-legacy-correlation")
	if err != nil {
		t.Fatalf("get legacy webhook event: %v", err)
	}
	if event.CorrelationID != event.ID || event.IdempotencyKey != "" || event.DeliveryID != "" || event.ParentEventID != "" || strings.Contains(event.PayloadJSON, "custom-token") {
		t.Fatalf("legacy credential was reinterpreted as metadata: %#v payload=%s", event, event.PayloadJSON)
	}
}

func TestIntegrationWebhookSourceRejectsProtocolTokenHeaders(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "data.db"), 0)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := configstore.FromDB(database.DB())
	app := echo.New()
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{Store: store})

	for _, tokenHeader := range []string{"X-Agent-Compose-Parent-Event-ID", "X-Correlation-ID", "Idempotency-Key", "X-GitHub-Delivery", "X-Gitlab-Event-UUID", "X-Request-ID"} {
		request := httptest.NewRequest(http.MethodPut, "/api/webhook-sources/internal", strings.NewReader(`{"name":"Internal","enabled":true,"provider":"generic","topic_prefix":"webhook.internal.","token":"custom-token","token_header":`+strconv.Quote(tokenHeader)+`}`))
		recorder := httptest.NewRecorder()
		app.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("PUT source with reserved header %s status=%d body=%s", tokenHeader, recorder.Code, recorder.Body.String())
		}
	}
	if _, ok, err := store.GetWebhookSource(ctx, "internal"); err != nil {
		t.Fatalf("get webhook source: %v", err)
	} else if ok {
		t.Fatal("reserved token header source was persisted")
	}
}

func testEventQueryHTTPWorkflow(t *testing.T) {
	ctx := context.Background()
	database, err := sqlite.Open(filepath.Join(t.TempDir(), "data.db"), 0)
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() {
		if err := database.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	})
	store := configstore.FromDB(database.DB())
	root, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "webhook-root", Topic: "webhook.github.push", Source: domain.TopicEventSourceWebhook,
		CorrelationID: "corr-http-trace", IdempotencyKey: "private-idempotency-key",
		PayloadJSON: `{"secret":"not-for-collection-views"}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatalf("create root event: %v", err)
	}
	child, err := store.CreateEvent(ctx, domain.TopicEventRecord{
		ID: "scheduler-child", Topic: "runtime.completed", Source: domain.TopicEventSourceScheduler,
		CorrelationID: root.CorrelationID, ParentEventID: root.ID,
		PayloadJSON: `{}`, DispatchStatus: domain.TopicEventDispatchPublishedToBus,
	})
	if err != nil {
		t.Fatalf("create child event: %v", err)
	}
	if err := store.UpsertEventDelivery(ctx, domain.EventDelivery{
		EventID: child.ID, SchedulerID: "scheduler-1", TriggerID: "trigger-1",
		Status: domain.EventDeliveryStatusMatched,
	}); err != nil {
		t.Fatalf("upsert child delivery: %v", err)
	}
	if err := store.AddEventSandboxLink(ctx, domain.EventSandboxLink{
		EventID: child.ID, SandboxID: "sandbox-1", Relation: "scheduler.completed",
		SchedulerID: "scheduler-1", TriggerID: "trigger-1",
	}); err != nil {
		t.Fatalf("add child sandbox link: %v", err)
	}

	app := echo.New()
	webhooks.RegisterRoutes(app, webhooks.RouteOptions{Store: store, QueryStore: store})

	summaryBody := getEventQueryResponse(t, app, "/api/events?source=webhook&view=summary&offset=0&limit=10")
	if strings.Contains(summaryBody, "private-idempotency-key") || strings.Contains(summaryBody, "not-for-collection-views") {
		t.Fatalf("summary response exposed private event fields: %s", summaryBody)
	}
	var summaries webhooks.EventSummaryListResponse
	if err := json.Unmarshal([]byte(summaryBody), &summaries); err != nil {
		t.Fatalf("decode summary response: %v", err)
	}
	if summaries.Total != 1 || len(summaries.Items) != 1 || summaries.Items[0].EventID != root.ID {
		t.Fatalf("summary response = %#v", summaries)
	}

	var topics webhooks.EventTopicListResponse
	if err := json.Unmarshal([]byte(getEventQueryResponse(t, app, "/api/events/topics?source=webhook")), &topics); err != nil {
		t.Fatalf("decode topic response: %v", err)
	}
	if topics.Total != 1 || len(topics.Items) != 1 || topics.Items[0].Topic != root.Topic || topics.Items[0].EventCount != 1 {
		t.Fatalf("topic response = %#v", topics)
	}

	var trace webhooks.EventTraceResponse
	if err := json.Unmarshal([]byte(getEventQueryResponse(t, app, "/api/events/"+root.ID+"/trace")), &trace); err != nil {
		t.Fatalf("decode trace response: %v", err)
	}
	if trace.Event.EventID != root.ID || len(trace.Runs) != 1 || trace.Runs[0].Delivery.EventID != child.ID {
		t.Fatalf("trace runs = %#v", trace)
	}
	if len(trace.Sandboxes) != 1 || trace.Sandboxes[0].EventID != child.ID || trace.Sandboxes[0].SandboxID != "sandbox-1" {
		t.Fatalf("trace sandboxes = %#v", trace.Sandboxes)
	}
}

func getEventQueryResponse(t *testing.T, app *echo.Echo, target string) string {
	t.Helper()
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET %s status=%d body=%s", target, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.String()
}
