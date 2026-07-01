package events

import (
	appconfig "agent-compose/pkg/config"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

func newTestWebhookService(t *testing.T) (*echo.Echo, *Service, *ConfigStore) {
	t.Helper()
	store := newTopicEventTestConfigStore(t)
	service := &Service{
		config:   &appconfig.Config{WebhookBodyLimitBytes: 128},
		configDB: store,
	}
	app := echo.New()
	RegisterRoutes(app, service)
	return app, service, store
}

func addTestWebhookSource(t *testing.T, store *ConfigStore, prefix, token string) {
	t.Helper()
	_, err := store.UpsertWebhookSource(context.Background(), WebhookSource{
		ID:          strings.TrimSuffix(strings.TrimPrefix(prefix, "webhook."), ".") + "-source",
		Name:        prefix,
		Enabled:     true,
		Provider:    providerFromWebhookTopic(strings.TrimSuffix(prefix, ".")),
		TopicPrefix: prefix,
		TokenHash:   webhookTokenHash(token),
	})
	if err != nil {
		t.Fatalf("UpsertWebhookSource returned error: %v", err)
	}
}

func TestWebhookHandlerStoresEvent(t *testing.T) {
	testWebhookHandlerStoresEvent(t)
}

func testWebhookHandlerStoresEvent(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	addTestWebhookSource(t, store, "webhook.test.", "secret")
	body := `{"intent":"command","correlation_id":"corr-1","value":1}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.test.created?debug=1", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Idempotency-Key", "delivery-1")
	req.Header.Set("X-GitHub-Delivery", "delivery-1")
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Cookie", "private=true")
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var accepted webhookAcceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if accepted.Topic != "webhook.test.created" || accepted.CorrelationID != "corr-1" || accepted.Sequence <= 0 || accepted.EventID == "" {
		t.Fatalf("accepted response = %#v", accepted)
	}

	event, err := store.GetEvent(context.Background(), accepted.EventID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if event.Provider != "test" || event.Intent != "command" || event.IdempotencyKey != "delivery-1" {
		t.Fatalf("stored event metadata = %#v", event)
	}
	if event.PayloadHash != topicEventPayloadSHA256(event.PayloadJSON) {
		t.Fatalf("payload hash = %q, want hash of stored payload", event.PayloadHash)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["sequence"].(float64) != float64(accepted.Sequence) {
		t.Fatalf("payload sequence = %#v, want %d", payload["sequence"], accepted.Sequence)
	}
	headers := payload["headers"].(map[string]any)
	if _, ok := headers["authorization"]; ok {
		t.Fatalf("authorization header leaked into payload: %#v", headers)
	}
	if _, ok := headers["cookie"]; ok {
		t.Fatalf("cookie header leaked into payload: %#v", headers)
	}
}

func TestWebhookHandlerAuthAndValidation(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	addTestWebhookSource(t, store, "webhook.test.", "secret")
	tests := []struct {
		name        string
		path        string
		token       string
		contentType string
		body        string
		wantStatus  int
	}{
		{name: "missing token", path: "/api/webhooks/webhook.test.created", contentType: "application/json", body: `{}`, wantStatus: http.StatusUnauthorized},
		{name: "bad topic", path: "/api/webhooks/runtime.test", token: "secret", contentType: "application/json", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "bad content type", path: "/api/webhooks/webhook.test.created", token: "secret", contentType: "text/plain", body: `{}`, wantStatus: http.StatusUnsupportedMediaType},
		{name: "bad json", path: "/api/webhooks/webhook.test.created", token: "secret", contentType: "application/json", body: `{`, wantStatus: http.StatusBadRequest},
		{name: "non object", path: "/api/webhooks/webhook.test.created", token: "secret", contentType: "application/json", body: `[]`, wantStatus: http.StatusBadRequest},
		{name: "too large", path: "/api/webhooks/webhook.test.created", token: "secret", contentType: "application/json", body: `{"value":"` + strings.Repeat("x", 200) + `"}`, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), tt.wantStatus)
			}
		})
	}
}

func TestWebhookTokenAuthentication(t *testing.T) {
	tests := []struct {
		name       string
		setHeader  func(*http.Request)
		wantStatus int
	}{
		{
			name: "bearer success",
			setHeader: func(req *http.Request) {
				req.Header.Set("Authorization", "Bearer secret")
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name: "webhook header success",
			setHeader: func(req *http.Request) {
				req.Header.Set("X-WEBHOOK-TOKEN", "secret")
			},
			wantStatus: http.StatusAccepted,
		},
		{
			name: "agent compose header rejected",
			setHeader: func(req *http.Request) {
				req.Header.Set("X-AGENT-COMPOSE-WEBHOOK-TOKEN", "secret")
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "old header rejected",
			setHeader: func(req *http.Request) {
				req.Header.Set("X-"+"A"+"DP"+"-WEBHOOK-TOKEN", "secret")
			},
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, _, store := newTestWebhookService(t)
			addTestWebhookSource(t, store, "webhook.test.", "secret")
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.test.auth", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			tt.setHeader(req)
			rec := httptest.NewRecorder()

			app.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), tt.wantStatus)
			}
		})
	}
}

func TestWebhookPayloadSanitizesWebhookTokenHeader(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	addTestWebhookSource(t, store, "webhook.test.", "secret")
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.test.sanitized", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-WEBHOOK-TOKEN", "secret")
	rec := httptest.NewRecorder()

	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s, want %d", rec.Code, rec.Body.String(), http.StatusAccepted)
	}
	var accepted webhookAcceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	event, err := store.GetEvent(context.Background(), accepted.EventID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	headers := payload["headers"].(map[string]any)
	if _, ok := headers["x-webhook-token"]; ok {
		t.Fatalf("x-webhook-token header leaked into payload: %#v", headers)
	}
}

func TestWebhookHandlerIdempotency(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	addTestWebhookSource(t, store, "webhook.test.", "secret")
	post := func(body string) (int, webhookAcceptedResponse) {
		req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.test.idempotent", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "same-key")
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		var accepted webhookAcceptedResponse
		_ = json.Unmarshal(rec.Body.Bytes(), &accepted)
		return rec.Code, accepted
	}
	status, first := post(`{"value":1}`)
	if status != http.StatusAccepted {
		t.Fatalf("first status = %d", status)
	}
	status, second := post(`{"value":1}`)
	if status != http.StatusAccepted || second.EventID != first.EventID {
		t.Fatalf("second status = %d response = %#v first = %#v", status, second, first)
	}
	status, _ = post(`{"value":2}`)
	if status != http.StatusConflict {
		t.Fatalf("conflict status = %d, want 409", status)
	}
}

func TestWebhookHandlerUsesWebhookSourceToken(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	_, err := store.UpsertWebhookSource(context.Background(), WebhookSource{
		ID:          "github-main",
		Name:        "GitHub Main",
		Enabled:     true,
		Provider:    "github",
		TopicPrefix: "webhook.github.",
		TokenHash:   webhookTokenHash("source-secret"),
	})
	if err != nil {
		t.Fatalf("UpsertWebhookSource returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(`{"value":1}`))
	req.Header.Set("X-WEBHOOK-TOKEN", "source-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var accepted webhookAcceptedResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted response: %v", err)
	}
	event, err := store.GetEvent(context.Background(), accepted.EventID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if event.Provider != "github" {
		t.Fatalf("provider = %q, want github", event.Provider)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(event.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["webhookSourceId"] != "github-main" {
		t.Fatalf("payload webhookSourceId = %#v", payload["webhookSourceId"])
	}

	req = httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(`{"value":1}`))
	req.Header.Set("X-WEBHOOK-TOKEN", "wrong")
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookHandlerMatchesExactSourcePrefixTopic(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	addTestWebhookSource(t, store, "webhook.gitlab.", "source-secret")

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.gitlab", strings.NewReader(`{"value":1}`))
	req.Header.Set("X-WEBHOOK-TOKEN", "source-secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookSourceManagementHandlers(t *testing.T) {
	app, _, _ := newTestWebhookService(t)
	body := `{"name":"GitHub","enabled":true,"provider":"github","topic_prefix":"webhook.github.","token":"source-secret","signature_secret":"signing-secret"}`
	req := httptest.NewRequest(http.MethodPut, "/api/webhook-sources/github-main", strings.NewReader(body))
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("put status = %d body = %s", rec.Code, rec.Body.String())
	}
	var saved webhookSourceResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decode put response: %v", err)
	}
	if saved.Source.ID != "github-main" || !saved.Source.HasToken || !saved.Source.HasSignatureSecret {
		t.Fatalf("saved source = %#v", saved.Source)
	}
	if strings.Contains(rec.Body.String(), "source-secret") || strings.Contains(rec.Body.String(), "signing-secret") || strings.Contains(rec.Body.String(), "sha256:") {
		t.Fatalf("secret leaked in response: %s", rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/webhook-sources", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var list webhookSourceListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "github-main" || !list.Items[0].HasToken {
		t.Fatalf("list response = %#v", list)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/webhook-sources/github-main", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/webhook-sources/github-main", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestWebhookPayloadHelpers(t *testing.T) {
	payloadJSON := `{"body":{"value":1}}`
	want := topicEventPayloadSHA256(`{"value":1}`)
	if got := existingWebhookBodyHash(payloadJSON); got != want {
		t.Fatalf("existingWebhookBodyHash = %q, want %q", got, want)
	}
	for _, raw := range []string{`{`, `{}`, `{"body":func}`} {
		if got := existingWebhookBodyHash(raw); got != "" {
			t.Fatalf("existingWebhookBodyHash(%q) = %q, want empty", raw, got)
		}
	}
	if providerFromWebhookTopic("webhook.gitlab.push") != "gitlab" || providerFromWebhookTopic("runtime.gitlab.push") != "" {
		t.Fatalf("providerFromWebhookTopic returned unexpected values")
	}
}

func TestEventQueryHandlers(t *testing.T) {
	testEventQueryHandlers(t)
}

func testEventQueryHandlers(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	created, err := store.CreateEvent(context.Background(), TopicEventRecord{
		Topic:         "runtime.test.requested",
		Source:        TopicEventSourceLoader,
		CorrelationID: "corr-query",
		PayloadJSON:   `{"eventId":"manual","correlationId":"corr-query"}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events/"+created.ID, nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", rec.Code, rec.Body.String())
	}

	req = httptest.NewRequest(http.MethodGet, "/api/events?topic=runtime.test.requested&after_sequence=0&limit=10", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", rec.Code, rec.Body.String())
	}
	var list topicEventListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].EventID != created.ID || list.NextAfterSequence != created.Sequence {
		t.Fatalf("list response = %#v", list)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/events", nil)
	rec = httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unfiltered list status = %d, want 400", rec.Code)
	}
}

func TestEventSessionsHandler(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	root, err := store.CreateEvent(context.Background(), TopicEventRecord{
		Topic:         "webhook.test.session",
		Source:        TopicEventSourceWebhook,
		CorrelationID: "corr-session",
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent root returned error: %v", err)
	}
	child, err := store.CreateEvent(context.Background(), TopicEventRecord{
		Topic:         "runtime.test.session",
		Source:        TopicEventSourceLoader,
		CorrelationID: "corr-session",
		ParentEventID: root.ID,
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent child returned error: %v", err)
	}
	if err := store.AddEventSessionLink(context.Background(), EventSessionLink{
		EventID:       child.ID,
		SessionID:     "session-1",
		Relation:      "session_rpc_completed",
		LoaderID:      "loader-1",
		RunID:         "run-1",
		TriggerID:     "trigger-1",
		LoaderEventID: "event-1",
	}); err != nil {
		t.Fatalf("AddEventSessionLink returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events/"+root.ID+"/sessions", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp eventSessionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.EventID != root.ID || resp.CorrelationID != "corr-session" || len(resp.Sessions) != 1 {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Sessions[0].SessionID != "session-1" || resp.Sessions[0].EventID != child.ID {
		t.Fatalf("session trace = %#v", resp.Sessions[0])
	}
}

func TestEventRunsHandler(t *testing.T) {
	app, _, store := newTestWebhookService(t)
	root, err := store.CreateEvent(context.Background(), TopicEventRecord{
		Topic:         "webhook.test.run",
		Source:        TopicEventSourceWebhook,
		CorrelationID: "corr-run",
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent root returned error: %v", err)
	}
	child, err := store.CreateEvent(context.Background(), TopicEventRecord{
		Topic:         "runtime.test.run",
		Source:        TopicEventSourceLoader,
		CorrelationID: "corr-run",
		ParentEventID: root.ID,
		PayloadJSON:   `{}`,
	})
	if err != nil {
		t.Fatalf("CreateEvent child returned error: %v", err)
	}
	if err := store.UpsertEventDelivery(context.Background(), EventDelivery{
		EventID:   child.ID,
		LoaderID:  "loader-1",
		TriggerID: "trigger-1",
		RunID:     "run-1",
		Status:    EventDeliveryStatusRunSucceeded,
	}); err != nil {
		t.Fatalf("UpsertEventDelivery returned error: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/events/"+root.ID+"/runs", nil)
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp eventRunsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.EventID != root.ID || resp.CorrelationID != "corr-run" || len(resp.Runs) != 1 {
		t.Fatalf("response = %#v", resp)
	}
	if resp.Runs[0].EventID != child.ID || resp.Runs[0].RunID != "run-1" || resp.Runs[0].Status != EventDeliveryStatusRunSucceeded {
		t.Fatalf("run trace = %#v", resp.Runs[0])
	}
}

func TestEventHandlersReturnNotFoundForMissingEvent(t *testing.T) {
	app, _, _ := newTestWebhookService(t)
	for _, path := range []string{
		"/api/events/missing",
		"/api/events/missing/sessions",
		"/api/events/missing/runs",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		app.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d body = %s", path, rec.Code, rec.Body.String())
		}
	}
}

func TestWebhookDisabledReturnsNotFound(t *testing.T) {
	app, _, _ := newTestWebhookService(t)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.test", bytes.NewBufferString(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestWebhookHandlerDoesNotFallbackToBearerToken(t *testing.T) {
	app, _, _ := newTestWebhookService(t)
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.test", bytes.NewBufferString(`{}`))
	req.Header.Set("Authorization", "Bearer secret")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s, want 404", rec.Code, rec.Body.String())
	}
}
