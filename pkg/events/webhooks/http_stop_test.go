package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"

	"github.com/labstack/echo/v4"
)

func TestHandleStopEvent(t *testing.T) {
	tests := []struct {
		name          string
		event         *domain.TopicEventRecord
		token         string
		deliveries    []domain.EventDelivery
		stopResults   []bool
		stopErr       error
		wantStatus    int
		wantRequested bool
		wantRuns      int
		wantStopped   []string
	}{
		{
			name: "valid token stops active run", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopResults: []bool{true},
			wantStatus: http.StatusOK, wantRequested: true, wantRuns: 1, wantStopped: []string{"run-1"},
		},
		{
			name: "invalid token is unauthorized", event: webhookStopEvent("event-1", "github"), token: "wrong",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "missing event is not found", token: "token",
			wantStatus: http.StatusNotFound,
		},
		{
			name: "non webhook event is forbidden", event: webhookStopEvent("event-1", ""), token: "token",
			wantStatus: http.StatusForbidden,
		},
		{
			name: "no active run reports no request", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopResults: []bool{false},
			wantStatus: http.StatusOK, wantStopped: []string{"run-1"},
		},
		{
			name: "multiple deliveries are stopped", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}, {RunID: ""}, {RunID: "run-2"}}, stopResults: []bool{true, true},
			wantStatus: http.StatusOK, wantRequested: true, wantRuns: 2, wantStopped: []string{"run-1", "run-2"},
		},
		{
			name: "stop failure returns internal error", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopErr: errors.New("stop failed"),
			wantStatus: http.StatusInternalServerError, wantStopped: []string{"run-1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newWebhookRouteStore()
			if tt.event != nil {
				base.events[tt.event.ID] = *tt.event
			}
			store := &webhookStopStore{webhookRouteStore: base, deliveries: tt.deliveries}
			stopper := &recordingRunStopper{results: tt.stopResults, err: tt.stopErr}
			app := echo.New()
			RegisterRoutes(app, RouteOptions{Store: store, RunStopper: stopper})

			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/events/event-1/stop", strings.NewReader(`{"reason":"operator request"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if len(stopper.runIDs) != len(tt.wantStopped) {
				t.Fatalf("stopped runs=%v, want %v", stopper.runIDs, tt.wantStopped)
			}
			for i := range tt.wantStopped {
				if stopper.runIDs[i] != tt.wantStopped[i] {
					t.Fatalf("stopped runs=%v, want %v", stopper.runIDs, tt.wantStopped)
				}
				if stopper.reasons[i] != "operator request" {
					t.Fatalf("stop reason=%q, want operator request", stopper.reasons[i])
				}
			}
			if tt.wantStatus == http.StatusOK {
				var response struct {
					EventID       string `json:"event_id"`
					StopRequested bool   `json:"stop_requested"`
					RequestedRuns int    `json:"requested_runs"`
				}
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if response.EventID != "event-1" || response.StopRequested != tt.wantRequested || response.RequestedRuns != tt.wantRuns {
					t.Fatalf("response=%+v, want event_id=event-1 stop_requested=%v requested_runs=%d", response, tt.wantRequested, tt.wantRuns)
				}
			}
		})
	}
}

func webhookStopEvent(id, sourceID string) *domain.TopicEventRecord {
	payload := `{}`
	if sourceID != "" {
		payload = `{"webhookSourceId":"` + sourceID + `"}`
	}
	return &domain.TopicEventRecord{ID: id, PayloadJSON: payload}
}

type webhookStopStore struct {
	*webhookRouteStore
	deliveries []domain.EventDelivery
}

func (s *webhookStopStore) ListDescendantEventIDs(context.Context, string, int) ([]string, error) {
	return []string{"event-1", "child-event"}, nil
}

func (s *webhookStopStore) ListEventDeliveries(context.Context, []string) ([]domain.EventDelivery, error) {
	return s.deliveries, nil
}

type recordingRunStopper struct {
	results []bool
	err     error
	runIDs  []string
	reasons []string
}

func (s *recordingRunStopper) StopActiveRun(_ context.Context, runID, reason string) (bool, error) {
	s.runIDs = append(s.runIDs, runID)
	s.reasons = append(s.reasons, reason)
	if s.err != nil {
		return false, s.err
	}
	result := false
	if len(s.results) >= len(s.runIDs) {
		result = s.results[len(s.runIDs)-1]
	}
	return result, nil
}
