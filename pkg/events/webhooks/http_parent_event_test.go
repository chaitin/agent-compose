package webhooks

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"

	"github.com/labstack/echo/v4"
)

func TestWebhookParentEventBuildsQueryableLineage(t *testing.T) {
	store := newWebhookRouteStore()
	store.events["event-parent"] = domain.TopicEventRecord{
		ID:            "event-parent",
		Topic:         "webhook.devboard.task.created",
		CorrelationID: "task-123",
		PayloadJSON:   `{}`,
	}
	app := echo.New()
	RegisterRoutes(app, RouteOptions{
		Store:            store,
		WebhookBodyLimit: 1 << 20,
		NewEventID:       func() string { return "event-child" },
	})

	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.agentkit.devboard.project.demo.task.created", strings.NewReader(`{"intent":"task"}`))
	req.Header.Set("Authorization", "Bearer token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(parentEventIDHeader, "event-parent")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("webhook status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusAccepted)
	}
	child := store.events["event-child"]
	if child.ParentEventID != "event-parent" || child.CorrelationID != "task-123" {
		t.Fatalf("child lineage = parent %q correlation %q", child.ParentEventID, child.CorrelationID)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(child.PayloadJSON), &payload); err != nil {
		t.Fatalf("decode child payload: %v", err)
	}
	if payload["parentEventId"] != "event-parent" {
		t.Fatalf("child payload parentEventId = %#v", payload["parentEventId"])
	}
	headers, _ := payload["headers"].(map[string]any)
	if headers["x-agent-compose-parent-event-id"] != "event-parent" {
		t.Fatalf("child payload headers = %#v", headers)
	}
}

func TestWebhookParentEventRejectsMissingOrMismatchedParent(t *testing.T) {
	for _, tc := range []struct {
		name          string
		parentEventID string
		correlationID string
	}{
		{name: "missing", parentEventID: "event-missing", correlationID: "task-123"},
		{name: "correlation mismatch", parentEventID: "event-parent", correlationID: "task-other"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newWebhookRouteStore()
			store.events["event-parent"] = domain.TopicEventRecord{
				ID:            "event-parent",
				Topic:         "webhook.devboard.task.created",
				CorrelationID: "task-123",
				PayloadJSON:   `{}`,
			}
			app := echo.New()
			RegisterRoutes(app, RouteOptions{
				Store:            store,
				WebhookBodyLimit: 1 << 20,
				NewEventID:       func() string { return "event-child" },
			})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.agentkit.devboard.project.demo.task.created", strings.NewReader(`{"intent":"task"}`))
			req.Header.Set("Authorization", "Bearer token")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set(parentEventIDHeader, tc.parentEventID)
			req.Header.Set("X-Correlation-ID", tc.correlationID)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("webhook status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusBadRequest)
			}
			if _, exists := store.events["event-child"]; exists {
				t.Fatal("rejected child event was persisted")
			}
		})
	}
}

func TestWebhookParentEventRejectsUnprotectedSource(t *testing.T) {
	base := newWebhookRouteStore()
	base.events["event-parent"] = domain.TopicEventRecord{
		ID: "event-parent", Topic: "webhook.devboard.task.created", CorrelationID: "task-123", PayloadJSON: `{}`,
	}
	store := &candidateWebhookStore{
		webhookRouteStore: base,
		candidates: []domain.WebhookSource{{
			ID: "github-signed", Name: "Signed GitHub", Enabled: true, Provider: "github", TopicPrefix: "webhook.github.",
			SignatureType: domain.WebhookSignatureGitHubSHA256, SignatureSecret: "github-secret",
		}},
	}
	app := echo.New()
	RegisterRoutes(app, RouteOptions{Store: store, NewEventID: func() string { return "event-child" }})

	body := `{"intent":"push"}`
	req := httptest.NewRequest(http.MethodPost, "/api/webhooks/webhook.github.push", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", signGitHubBody(body, "github-secret"))
	req.Header.Set(parentEventIDHeader, "event-parent")
	rec := httptest.NewRecorder()
	app.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("webhook status = %d body=%s, want %d", rec.Code, rec.Body.String(), http.StatusForbidden)
	}
	if _, exists := store.events["event-child"]; exists {
		t.Fatal("unprotected parent event was persisted")
	}
}
