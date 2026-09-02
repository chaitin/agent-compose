package webhooks

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"

	"github.com/labstack/echo/v4"
)

func TestWebhookHelpersAndQueueCoverageWorkflows(t *testing.T) {
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/hooks?one=1&many=a&many=b", strings.NewReader(`{"intent":"push","correlationId":"corr-body"}`))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("X-Correlation-ID", "corr-header")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("User-Agent", "tester")
	req.RemoteAddr = "127.0.0.1:1234"
	if PresentedToken(req) != "token" || !ValidTokenHash(req, TokenHash("token")) {
		t.Fatalf("token helpers failed")
	}
	if !RequestContentTypeIsJSON(req) {
		t.Fatalf("expected JSON content type")
	}
	bodyRaw, err := ReadBody(req, 1<<20)
	if err != nil {
		t.Fatalf("ReadBody returned error: %v", err)
	}
	body, compact, err := DecodeJSONObject(bodyRaw)
	if err != nil || compact == "" {
		t.Fatalf("DecodeJSONObject body=%#v compact=%q err=%v", body, compact, err)
	}
	if err := ValidateExternalTopic("webhook.github.push"); err != nil {
		t.Fatalf("ValidateExternalTopic returned error: %v", err)
	}
	if ValidateExternalTopic("runtime.bad") == nil {
		t.Fatalf("expected invalid external topic")
	}
	if ProviderFromTopic("webhook.github.push") != "github" || IntentFromBody(body) != "push" {
		t.Fatalf("provider/intent helpers failed")
	}
	if ExtractCorrelationID(req, body) != "corr-header" || ExtractIdempotencyKey(req) != "delivery-1" || ExtractDeliveryID(req) != "delivery-1" {
		t.Fatalf("request id helpers failed")
	}
	payload := BuildPayload(req, WebhookPayloadRequest{
		EventID:        "event-1",
		Sequence:       7,
		Topic:          "webhook.github.push",
		CorrelationID:  "corr",
		IdempotencyKey: "idem",
		Source:         domain.WebhookSource{ID: "github", Provider: "github"},
		Body:           body,
	})
	if payload["eventId"] != "event-1" || payload["provider"] != "github" || len(QueryValuesToMap(req)) != 2 {
		t.Fatalf("payload = %#v", payload)
	}
	if headers := SanitizeHeaders(req.Header); headers["user-agent"] != "tester" || headers["authorization"] != "" {
		t.Fatalf("headers = %#v", headers)
	}
	if _, _, err := DecodeJSONObject([]byte(`[]`)); err == nil {
		t.Fatalf("expected non-object decode error")
	}
	if _, err := ReadBody(&http.Request{Body: ioNopCloser{strings.NewReader("toolong")}}, 3); !errors.Is(err, domain.ErrBodyTooLarge) {
		t.Fatalf("expected body too large, got %v", err)
	}

	queue, err := NewRunQueueFromConfig(&appconfig.Config{
		WebhookQueueDefaultWorkers: 1,
		WebhookQueueRulesJSON:      `[{"name":"github-main","workers":1,"match":{"topic":"webhook.github.*","provider":"github","payload":{"body.ref":"main","body.count":2,"body.ok":true,"body.none":null}}}]`,
	})
	if err != nil {
		t.Fatalf("NewRunQueueFromConfig returned error: %v", err)
	}
	event := domain.SchedulerTopicEvent{Topic: "webhook.github.push", Provider: "github", Payload: map[string]any{"body": map[string]any{"ref": "main", "count": float64(2), "ok": true, "none": nil}}}
	name, workers := queue.Match(event)
	if name != "github-main" || workers != 1 {
		t.Fatalf("queue match name=%q workers=%d", name, workers)
	}
	reservation, ok := queue.Reserve(event)
	if !ok || reservation == nil {
		t.Fatalf("first reservation failed")
	}
	if _, ok := queue.Reserve(event); ok {
		t.Fatalf("second reservation should be blocked")
	}
	reservation.Release()
	if _, ok := queue.Reserve(event); !ok {
		t.Fatalf("reservation after release failed")
	}
	for _, reservation := range NoopReservations(2) {
		reservation.Release()
	}
	if _, err := NewRunQueueFromConfig(&appconfig.Config{WebhookQueueRulesJSON: `bad`}); err == nil {
		t.Fatalf("expected bad queue json error")
	}
	if _, err := normalizeQueueRule(queueRuleConfig{Name: "bad", Workers: 0, Match: queueMatchConfig{Topic: "webhook.github."}}); err == nil {
		t.Fatalf("expected invalid worker count")
	}
	if value, ok := payloadPathScalar(event.Payload, "body.ref"); !ok || value != "main" {
		t.Fatalf("payloadPathScalar value=%q ok=%v", value, ok)
	}
	if _, ok := payloadPathScalar(event.Payload, "body."); ok {
		t.Fatalf("expected invalid payload path")
	}
}

func TestIntegrationWebhookHelpersAndQueueCoverageWorkflows(t *testing.T) {
	TestWebhookHelpersAndQueueCoverageWorkflows(t)
}

func TestE2EWebhookHelpersAndQueueCoverageWorkflows(t *testing.T) {
	TestWebhookHelpersAndQueueCoverageWorkflows(t)
}

func TestWebhookHTTPRoutesCoverageWorkflow(t *testing.T) {
	store := newWebhookRouteStore()
	app := echo.New()
	RegisterRoutes(app, RouteOptions{Store: store, WebhookBodyLimit: 1 << 20, NewEventID: func() string { return "event-1" }})

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(`{"intent":"push","correlationId":"corr-1"}`))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-1")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted || len(store.events) != 1 {
		t.Fatalf("webhook post status=%d body=%s events=%#v", rec.Code, rec.Body.String(), store.events)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(`{"intent":"push","correlationId":"corr-1"}`))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "idem-1")
	store.existingID = "event-1"
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook idempotent status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, target := range []string{
		"/api/events/event-1",
		"/api/events?topic=webhook.github.push&limit=10",
		"/api/events?source=webhook&view=summary&offset=0&limit=10",
		"/api/events/topics?source=webhook&limit=10",
		"/api/events/event-1/sessions",
		"/api/events/event-1/sandboxes",
		"/api/events/event-1/runs",
		"/api/events/event-1/trace",
		"/api/webhook-sources",
	} {
		req = httptest.NewRequest(http.MethodGet, target, nil)
		rec = httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s status=%d body=%s", target, rec.Code, rec.Body.String())
		}
	}
	if store.lastTopicSource != domain.TopicEventSourceWebhook {
		t.Fatalf("event topic source = %q, want webhook", store.lastTopicSource)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/events?topic=webhook.github.push&offset=0", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"payload"`) || !strings.Contains(rec.Body.String(), `"idempotency_key":"idem-1"`) {
		t.Fatalf("full events compatibility status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/events?source=webhook&view=summary&offset=0", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || store.lastFilter.Source != domain.TopicEventSourceWebhook || strings.Contains(rec.Body.String(), `"payload"`) || strings.Contains(rec.Body.String(), `"idempotency_key"`) {
		t.Fatalf("summary events status=%d filter=%#v body=%s", rec.Code, store.lastFilter, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/events?source=webhook&view=summary", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"next_after_sequence":1`) {
		t.Fatalf("summary event cursor status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/events?topic=webhook.github.push&offset=3&limit=7", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || store.lastFilter.Offset != 3 || store.lastFilter.Limit != 7 || !strings.Contains(rec.Body.String(), `"total":1`) {
		t.Fatalf("paginated events status=%d filter=%#v body=%s", rec.Code, store.lastFilter, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodGet, "/api/events?topic=webhook.github.push&offset=1&after_sequence=1", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mixed event pagination status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPut, "/api/webhook-sources/github", strings.NewReader(`{"name":"GitHub","enabled":true,"provider":"github","topic_prefix":"webhook.github.","token":"new-token"}`))
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT source status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/webhook-sources/github", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE source status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/runtime.bad", strings.NewReader(`{}`))
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid topic status=%d", rec.Code)
	}
}

func TestWebhookHTTPRoutesCustomTokenHeader(t *testing.T) {
	store := newWebhookRouteStore()
	app := echo.New()
	RegisterRoutes(app, RouteOptions{Store: store, WebhookBodyLimit: 1 << 20, NewEventID: func() string { return "event-custom" }})

	req := httptest.NewRequest(http.MethodPut, "/api/webhook-sources/github", strings.NewReader(`{"name":"GitHub","enabled":true,"provider":"github","topic_prefix":"webhook.github.","token":"custom-token","token_header":"X-GitHub-Token"}`))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT source status=%d body=%s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(`{"intent":"push"}`))
	req.Header.Set("X-GitHub-Token", "custom-token")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook with custom token header status=%d body=%s", rec.Code, rec.Body.String())
	}

	for _, header := range []string{"Authorization", "X-WEBHOOK-TOKEN"} {
		req = httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(`{"intent":"push"}`))
		req.Header.Set(header, "custom-token")
		if header == "Authorization" {
			req.Header.Set(header, "Bearer custom-token")
		}
		req.Header.Set("Content-Type", "application/json")
		rec = httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("webhook with legacy header %s status=%d body=%s", header, rec.Code, rec.Body.String())
		}
	}
}

func TestWebhookHTTPRoutesLegacyTokenHeaderFallbacks(t *testing.T) {
	for _, header := range []string{"Authorization", "X-WEBHOOK-TOKEN"} {
		store := newWebhookRouteStore()
		app := echo.New()
		RegisterRoutes(app, RouteOptions{Store: store, WebhookBodyLimit: 1 << 20, NewEventID: func() string { return "event-legacy-" + header }})

		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(`{"intent":"push"}`))
		req.Header.Set(header, "token")
		if header == "Authorization" {
			req.Header.Set(header, "Bearer token")
		}
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("webhook with legacy header %s status=%d body=%s", header, rec.Code, rec.Body.String())
		}
	}
}

func TestWebhookHTTPRoutesRejectInvalidTokenHeader(t *testing.T) {
	store := newWebhookRouteStore()
	app := echo.New()
	RegisterRoutes(app, RouteOptions{Store: store, WebhookBodyLimit: 1 << 20})

	req := httptest.NewRequest(http.MethodPut, "/api/webhook-sources/github", strings.NewReader(`{"name":"GitHub","enabled":true,"provider":"github","topic_prefix":"webhook.github.","token":"custom-token","token_header":"Bad Header"}`))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT source with invalid token header status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestIntegrationWebhookHTTPRoutesCoverageWorkflow(t *testing.T) {
	TestWebhookHTTPRoutesCoverageWorkflow(t)
}

func TestE2EWebhookHTTPRoutesCoverageWorkflow(t *testing.T) {
	TestWebhookHTTPRoutesCoverageWorkflow(t)
}

type ioNopCloser struct {
	*strings.Reader
}

func (c ioNopCloser) Close() error { return nil }

type webhookRouteStore struct {
	events          map[string]domain.TopicEventRecord
	sources         map[string]domain.WebhookSource
	existingID      string
	lastFilter      domain.TopicEventFilter
	lastTopicSource string
}

func newWebhookRouteStore() *webhookRouteStore {
	return &webhookRouteStore{
		events: map[string]domain.TopicEventRecord{},
		sources: map[string]domain.WebhookSource{
			"github": {ID: "github", Name: "GitHub", Enabled: true, Provider: "github", TopicPrefix: "webhook.github.", TokenHash: TokenHash("token")},
		},
	}
}

func (s *webhookRouteStore) FindEventByIdempotencyKey(context.Context, string, string) (domain.TopicEventRecord, bool, error) {
	if s.existingID == "" {
		return domain.TopicEventRecord{}, false, nil
	}
	event, ok := s.events[s.existingID]
	return event, ok, nil
}

func (s *webhookRouteStore) CreateEvent(_ context.Context, event domain.TopicEventRecord) (domain.TopicEventRecord, error) {
	event.Sequence = int64(len(s.events) + 1)
	event.CreatedAt = time.Now().UTC()
	s.events[event.ID] = event
	return event, nil
}

func (s *webhookRouteStore) GetEvent(_ context.Context, eventID string) (domain.TopicEventRecord, error) {
	event, ok := s.events[eventID]
	if !ok {
		return domain.TopicEventRecord{}, domain.ResourceError(domain.ErrNotFound, "event", eventID, "not found", nil)
	}
	return event, nil
}

func (s *webhookRouteStore) ListEvents(_ context.Context, filter domain.TopicEventFilter) ([]domain.TopicEventRecord, int, error) {
	s.lastFilter = filter
	items := make([]domain.TopicEventRecord, 0, len(s.events))
	for _, event := range s.events {
		items = append(items, event)
	}
	return items, len(items), nil
}

func (s *webhookRouteStore) ListEventSummaries(_ context.Context, filter domain.TopicEventFilter) ([]domain.EventSummary, int, error) {
	s.lastFilter = filter
	items := make([]domain.EventSummary, 0, len(s.events))
	for _, event := range s.events {
		items = append(items, domain.EventSummary{
			ID: event.ID, Sequence: event.Sequence, Topic: event.Topic, Source: event.Source,
			Provider: event.Provider, Intent: event.Intent, CorrelationID: event.CorrelationID,
			DeliveryID: event.DeliveryID, DispatchStatus: event.DispatchStatus,
			ParentEventID: event.ParentEventID, PublisherType: event.PublisherType,
			PublisherID: event.PublisherID, PublisherRunID: event.PublisherRunID,
			CreatedAt: event.CreatedAt, DispatchedAt: event.DispatchedAt,
		})
	}
	return items, len(items), nil
}

func (s *webhookRouteStore) ListEventTopics(_ context.Context, source string, _, _ int) ([]domain.EventTopicSummary, int, error) {
	s.lastTopicSource = source
	return []domain.EventTopicSummary{{Topic: "webhook.github.push", EventCount: 1, LatestEventAt: time.Now().UTC()}}, 1, nil
}

func (s *webhookRouteStore) GetEventTrace(_ context.Context, eventID string, _ int) (domain.EventTrace, error) {
	event, err := s.GetEvent(context.Background(), eventID)
	if err != nil {
		return domain.EventTrace{}, err
	}
	return domain.EventTrace{
		Event: domain.EventSummary{
			ID: event.ID, Sequence: event.Sequence, Topic: event.Topic, Source: event.Source,
			Provider: event.Provider, Intent: event.Intent, CorrelationID: event.CorrelationID,
			DeliveryID: event.DeliveryID, DispatchStatus: event.DispatchStatus,
			CreatedAt: event.CreatedAt, DispatchedAt: event.DispatchedAt,
		},
		Runs: []domain.EventRunTrace{{
			Delivery: domain.EventDelivery{EventID: eventID, SchedulerID: "scheduler-1", TriggerID: "trigger-1", RunID: "run-1", Status: domain.EventDeliveryStatusRunSucceeded},
		}},
		SandboxLinks: []domain.EventSandboxTraceItem{{EventID: eventID, SandboxID: "sandbox-1", Relation: "created"}},
	}, nil
}

func (s *webhookRouteStore) ListDescendantEventIDs(context.Context, string, int) ([]string, error) {
	return []string{"event-1"}, nil
}

func (s *webhookRouteStore) ListEventSandboxLinks(context.Context, []string) ([]domain.EventSandboxTraceItem, error) {
	return []domain.EventSandboxTraceItem{{EventID: "event-1", SandboxID: "session-1", Relation: "created"}}, nil
}

func (s *webhookRouteStore) CancelEventDispatch(context.Context, []string, string) (domain.EventDispatchCancellation, error) {
	return domain.EventDispatchCancellation{}, nil
}

func (s *webhookRouteStore) ListEventDeliveries(context.Context, []string) ([]domain.EventDelivery, error) {
	return []domain.EventDelivery{{EventID: "event-1", SchedulerID: "scheduler-1", TriggerID: "trigger-1", RunID: "run-1", Status: domain.EventDeliveryStatusRunSucceeded}}, nil
}

func (s *webhookRouteStore) ListWebhookSources(context.Context) ([]domain.WebhookSource, error) {
	items := make([]domain.WebhookSource, 0, len(s.sources))
	for _, source := range s.sources {
		items = append(items, source)
	}
	return items, nil
}

func (s *webhookRouteStore) GetWebhookSource(_ context.Context, sourceID string) (domain.WebhookSource, bool, error) {
	source, ok := s.sources[sourceID]
	return source, ok, nil
}

func (s *webhookRouteStore) UpsertWebhookSource(_ context.Context, source domain.WebhookSource) (domain.WebhookSource, error) {
	tokenHeader, err := events.NormalizeHTTPHeaderName(source.TokenHeader)
	if err != nil {
		return domain.WebhookSource{}, err
	}
	source.TokenHeader = tokenHeader
	s.sources[source.ID] = source
	return source, nil
}

func (s *webhookRouteStore) DeleteWebhookSource(_ context.Context, sourceID string) error {
	delete(s.sources, sourceID)
	return nil
}

func (s *webhookRouteStore) ListEnabledWebhookSourcesForTopic(context.Context, string) ([]domain.WebhookSource, error) {
	return []domain.WebhookSource{s.sources["github"]}, nil
}
