package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"

	"github.com/labstack/echo/v4"
)

func TestEventStopHandler(t *testing.T) {
	defaultEventIDs := []string{"event-1", "child-event"}
	tests := []struct {
		name                string
		event               *domain.TopicEventRecord
		source              *domain.WebhookSource
		token               string
		body                string
		bodyLimit           int64
		eventIDs            []string
		correlatedIDs       []string
		deliveries          []domain.EventDelivery
		stopResults         []bool
		stopErrs            []error
		cancellation        domain.EventDispatchCancellation
		cancelErr           error
		deliveryErr         error
		wantStatus          int
		wantRequested       bool
		wantRuns            int
		wantCanceledEvents  int
		wantPendingEvents   int
		wantStaleEvents     int
		wantFailed          []string
		wantPendingRuns     []string
		wantStopped         []string
		wantSchedulers      []string
		wantDescendantQuery bool
		wantEventIDs        []string
	}{
		{
			name: "valid token stops active run", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopResults: []bool{true},
			wantStatus: http.StatusOK, wantRequested: true, wantRuns: 1, wantStopped: []string{"run-1"},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "invalid token is unauthorized", event: webhookStopEvent("event-1", "github"), token: "wrong",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "signature only source requires a stop token", event: webhookStopEvent("event-1", "github"), token: "token",
			source:     &domain.WebhookSource{ID: "github", Provider: "github", TopicPrefix: "webhook.github.", SignatureType: domain.WebhookSignatureGitHubSHA256, SignatureSecret: "secret"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "disabled source cannot stop runs", event: webhookStopEvent("event-1", "github"), token: "token",
			source:     &domain.WebhookSource{ID: "github", Provider: "github", TopicPrefix: "webhook.github.", TokenHash: TokenHash("token")},
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
			name: "malformed stored payload is an internal error", event: &domain.TopicEventRecord{ID: "event-1", PayloadJSON: "{"}, token: "token",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "malformed request body is rejected", event: webhookStopEvent("event-1", "github"), token: "token", body: "{",
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "oversized request body is rejected", event: webhookStopEvent("event-1", "github"), token: "token",
			body: `{"reason":"request body exceeds the configured limit"}`, bodyLimit: 16,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name: "empty body uses the default stop reason", event: webhookStopEvent("event-1", "github"), token: "token", body: " ",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopResults: []bool{true},
			wantStatus: http.StatusOK, wantRequested: true, wantRuns: 1, wantStopped: []string{"run-1"},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "no active run reports no request", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopResults: []bool{false},
			wantStatus: http.StatusOK, wantStopped: []string{"run-1"},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "event and descendant deliveries are stopped once", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries:  []domain.EventDelivery{{EventID: "event-1", RunID: "run-1"}, {EventID: "child-event", RunID: " "}, {EventID: "child-event", RunID: "run-1"}, {EventID: "child-event", RunID: "run-2"}},
			stopResults: []bool{true, true},
			wantStatus:  http.StatusOK, wantRequested: true, wantRuns: 2, wantStopped: []string{"run-1", "run-2"},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "same run id from different schedulers is stopped separately", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries:    []domain.EventDelivery{{SchedulerID: "scheduler-1", RunID: "run-1"}, {SchedulerID: "scheduler-2", RunID: "run-1"}},
			stopResults:   []bool{true, true},
			wantStatus:    http.StatusOK,
			wantRequested: true,
			wantRuns:      2,
			wantStopped:   []string{"run-1", "run-1"},
			wantSchedulers: []string{
				"scheduler-1", "scheduler-2",
			},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "stop failures are aggregated after other runs", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries:  []domain.EventDelivery{{RunID: "run-1"}, {RunID: "run-2"}},
			stopResults: []bool{false, true}, stopErrs: []error{errors.New("stop failed"), nil},
			wantStatus: http.StatusInternalServerError, wantRequested: true, wantRuns: 1,
			wantFailed: []string{"run-1"}, wantStopped: []string{"run-1", "run-2"},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "waiting event is withdrawn from dispatch", event: webhookStopEvent("event-1", "github"), token: "token",
			cancellation: domain.EventDispatchCancellation{Canceled: 1},
			wantStatus:   http.StatusOK, wantRequested: true, wantCanceledEvents: 1,
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "claimed event is reported as still pending", event: webhookStopEvent("event-1", "github"), token: "token",
			cancellation: domain.EventDispatchCancellation{InFlight: 2},
			wantStatus:   http.StatusOK, wantPendingEvents: 2,
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "expired claim is reported apart from a held one", event: webhookStopEvent("event-1", "github"), token: "token",
			cancellation: domain.EventDispatchCancellation{InFlight: 1, Stale: 2},
			wantStatus:   http.StatusOK, wantPendingEvents: 1, wantStaleEvents: 2,
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "withdrawn event and stopped run are both reported", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopResults: []bool{true},
			cancellation: domain.EventDispatchCancellation{Canceled: 1, InFlight: 1},
			wantStatus:   http.StatusOK, wantRequested: true, wantRuns: 1, wantStopped: []string{"run-1"},
			wantCanceledEvents: 1, wantPendingEvents: 1,
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "dispatch cancellation failure stops no run", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries: []domain.EventDelivery{{RunID: "run-1"}}, stopResults: []bool{true},
			cancelErr:           errors.New("cancel failed"),
			wantStatus:          http.StatusInternalServerError,
			wantDescendantQuery: true,
		},
		{
			name: "delivery lookup failure reports completed cancellation", event: webhookStopEvent("event-1", "github"), token: "token",
			cancellation:        domain.EventDispatchCancellation{Canceled: 1, InFlight: 2},
			deliveryErr:         errors.New("lookup failed"),
			wantStatus:          http.StatusInternalServerError,
			wantRequested:       true,
			wantCanceledEvents:  1,
			wantPendingEvents:   2,
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "run not owned by this process is pending, not failed", event: webhookStopEvent("event-1", "github"), token: "token",
			deliveries:  []domain.EventDelivery{{RunID: "run-1"}, {RunID: "run-2"}},
			stopResults: []bool{false, true},
			stopErrs: []error{domain.ResourceError(domain.ErrFailedPrecondition, "scheduler run", "scheduler-1/run-1",
				"scheduler run scheduler-1/run-1 is not active in this process", nil), nil},
			wantStatus: http.StatusOK, wantRequested: true, wantRuns: 1,
			wantPendingRuns: []string{"run-1"}, wantStopped: []string{"run-1", "run-2"},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
		{
			name: "oversized event tree fails before stopping any run", event: webhookStopEvent("event-1", "github"), token: "token",
			eventIDs: webhookStopEventIDs(maxStopEventCount + 1), deliveries: []domain.EventDelivery{{RunID: "run-1"}},
			wantStatus: http.StatusConflict, wantDescendantQuery: true,
		},
		{
			name: "correlated event outside the parent chain is stopped too", event: webhookStopEvent("event-1", "github"), token: "token",
			correlatedIDs: []string{"event-1", "sibling-event"},
			deliveries:    []domain.EventDelivery{{EventID: "event-1", RunID: "run-1"}, {EventID: "sibling-event", RunID: "run-2"}},
			stopResults:   []bool{true, true},
			wantStatus:    http.StatusOK, wantRequested: true, wantRuns: 2, wantStopped: []string{"run-1", "run-2"},
			wantDescendantQuery: true, wantEventIDs: []string{"event-1", "child-event", "sibling-event"},
		},
		{
			name: "correlated event already in the parent chain is not duplicated", event: webhookStopEvent("event-1", "github"), token: "token",
			correlatedIDs: []string{"event-1", "child-event"},
			deliveries:    []domain.EventDelivery{{EventID: "child-event", RunID: "run-1"}},
			stopResults:   []bool{true},
			wantStatus:    http.StatusOK, wantRequested: true, wantRuns: 1, wantStopped: []string{"run-1"},
			wantDescendantQuery: true, wantEventIDs: defaultEventIDs,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := newWebhookRouteStore()
			if tt.event != nil {
				base.events[tt.event.ID] = *tt.event
			}
			if tt.source != nil {
				base.sources[tt.source.ID] = *tt.source
			}
			deliveries := append([]domain.EventDelivery(nil), tt.deliveries...)
			for index := range deliveries {
				if deliveries[index].SchedulerID == "" {
					deliveries[index].SchedulerID = "scheduler-1"
				}
			}
			store := &webhookStopStore{
				webhookRouteStore: base, descendantIDs: tt.eventIDs, correlatedIDs: tt.correlatedIDs, deliveries: deliveries,
				cancellation: tt.cancellation, cancelErr: tt.cancelErr, deliveryErr: tt.deliveryErr,
			}
			stopper := &recordingRunStopper{results: tt.stopResults, errs: tt.stopErrs, store: store}
			app := echo.New()
			RegisterRoutes(app, RouteOptions{Store: store, RunStopper: stopper, WebhookBodyLimit: tt.bodyLimit})

			body := tt.body
			if body == "" {
				body = `{"reason":"operator request"}`
			}
			req := httptest.NewRequest(http.MethodPost, "/api/webhooks/events/event-1/stop", strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+tt.token)
			rec := httptest.NewRecorder()
			app.ServeHTTP(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status=%d, want %d; body=%s", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantDescendantQuery {
				if store.gotRootEventID != "event-1" || store.gotDescendantLimit != maxStopEventCount+1 {
					t.Fatalf("descendant query root=%q limit=%d, want event-1 and %d", store.gotRootEventID, store.gotDescendantLimit, maxStopEventCount+1)
				}
				if store.gotCorrelatedLimit != maxStopEventCount+1 {
					t.Fatalf("correlated query limit=%d, want %d", store.gotCorrelatedLimit, maxStopEventCount+1)
				}
			}
			if !equalStrings(store.gotEventIDs, tt.wantEventIDs) {
				t.Fatalf("delivery event IDs=%v, want %v", store.gotEventIDs, tt.wantEventIDs)
			}
			// The oversized-tree and rejected-auth cases return before any
			// cancellation, so the ordering contract only applies once the
			// handler actually reached the dispatch queue.
			if store.gotCancelEventIDs != nil {
				if !store.canceledBeforeStops {
					t.Fatal("dispatch cancellation must run before any run is stopped")
				}
				if len(store.gotEventIDs) > 0 && !equalStrings(store.gotCancelEventIDs, store.gotEventIDs) {
					t.Fatalf("cancel event IDs=%v, want the delivery event IDs %v", store.gotCancelEventIDs, store.gotEventIDs)
				}
			}
			if tt.cancelErr != nil && len(stopper.runIDs) != 0 {
				t.Fatalf("stopped runs=%v, want none when dispatch cancellation fails", stopper.runIDs)
			}
			if !equalStrings(stopper.runIDs, tt.wantStopped) {
				t.Fatalf("stopped runs=%v, want %v", stopper.runIDs, tt.wantStopped)
			}
			wantSchedulers := tt.wantSchedulers
			if wantSchedulers == nil {
				wantSchedulers = make([]string, len(tt.wantStopped))
				for index := range wantSchedulers {
					wantSchedulers[index] = "scheduler-1"
				}
			}
			if !equalStrings(stopper.schedulerIDs, wantSchedulers) {
				t.Fatalf("stopped schedulers=%v, want %v", stopper.schedulerIDs, wantSchedulers)
			}
			for i := range tt.wantStopped {
				wantReason := "operator request"
				if tt.body == " " {
					wantReason = ""
				}
				if stopper.reasons[i] != wantReason {
					t.Fatalf("stop reason=%q, want %q", stopper.reasons[i], wantReason)
				}
			}
			if tt.wantStatus == http.StatusOK || len(tt.wantFailed) > 0 || tt.deliveryErr != nil {
				var response stopEventResponse
				if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
					t.Fatalf("decode response: %v", err)
				}
				if response.EventID != "event-1" || response.StopRequested != tt.wantRequested || response.RequestedRuns != tt.wantRuns || !equalStrings(response.FailedRunIDs, tt.wantFailed) {
					t.Fatalf("response=%+v, want requested=%v runs=%d failed=%v", response, tt.wantRequested, tt.wantRuns, tt.wantFailed)
				}
				if response.FailedRuns != len(tt.wantFailed) {
					t.Fatalf("failed_runs=%d, want %d", response.FailedRuns, len(tt.wantFailed))
				}
				if !equalStrings(response.PendingRunIDs, tt.wantPendingRuns) || response.PendingRuns != len(tt.wantPendingRuns) {
					t.Fatalf("pending_runs=%d ids=%v, want %d and %v",
						response.PendingRuns, response.PendingRunIDs, len(tt.wantPendingRuns), tt.wantPendingRuns)
				}
				if response.CanceledEvents != tt.wantCanceledEvents || response.PendingEvents != tt.wantPendingEvents || response.StaleEvents != tt.wantStaleEvents {
					t.Fatalf("canceled_events=%d pending_events=%d stale_events=%d, want %d, %d and %d",
						response.CanceledEvents, response.PendingEvents, response.StaleEvents,
						tt.wantCanceledEvents, tt.wantPendingEvents, tt.wantStaleEvents)
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

func webhookStopEventIDs(count int) []string {
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("event-%d", i+1)
	}
	return ids
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range want {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

type webhookStopStore struct {
	*webhookRouteStore
	descendantIDs       []string
	correlatedIDs       []string
	deliveries          []domain.EventDelivery
	cancellation        domain.EventDispatchCancellation
	cancelErr           error
	deliveryErr         error
	gotRootEventID      string
	gotDescendantLimit  int
	gotCorrelatedLimit  int
	gotEventIDs         []string
	gotCancelEventIDs   []string
	gotCancelReason     string
	canceledBeforeStops bool
	stopsStarted        bool
}

func (s *webhookStopStore) ListDescendantEventIDs(_ context.Context, rootEventID string, limit int) ([]string, error) {
	s.gotRootEventID = rootEventID
	s.gotDescendantLimit = limit
	if s.descendantIDs == nil {
		return []string{"event-1", "child-event"}, nil
	}
	return append([]string(nil), s.descendantIDs...), nil
}

// ListCorrelatedEventIDs defaults to just the root event, matching
// production's default correlation_id (the event's own ID), so existing
// tests that don't set correlatedIDs see no change in the merged set.
func (s *webhookStopStore) ListCorrelatedEventIDs(_ context.Context, rootEventID string, limit int) ([]string, error) {
	s.gotCorrelatedLimit = limit
	if s.correlatedIDs == nil {
		return []string{rootEventID}, nil
	}
	return append([]string(nil), s.correlatedIDs...), nil
}

func (s *webhookStopStore) ListEventDeliveries(_ context.Context, eventIDs []string) ([]domain.EventDelivery, error) {
	s.gotEventIDs = append([]string(nil), eventIDs...)
	if s.deliveryErr != nil {
		return nil, s.deliveryErr
	}
	return s.deliveries, nil
}

func (s *webhookStopStore) CancelEventDispatch(_ context.Context, eventIDs []string, reason string) (domain.EventDispatchCancellation, error) {
	s.gotCancelEventIDs = append([]string(nil), eventIDs...)
	s.gotCancelReason = reason
	s.canceledBeforeStops = !s.stopsStarted
	if s.cancelErr != nil {
		return domain.EventDispatchCancellation{}, s.cancelErr
	}
	return s.cancellation, nil
}

type recordingRunStopper struct {
	results      []bool
	errs         []error
	store        *webhookStopStore
	schedulerIDs []string
	runIDs       []string
	reasons      []string
}

func (s *recordingRunStopper) RequestSchedulerRunStop(_ context.Context, schedulerID, runID, reason string) (bool, error) {
	if s.store != nil {
		s.store.stopsStarted = true
	}
	s.schedulerIDs = append(s.schedulerIDs, schedulerID)
	s.runIDs = append(s.runIDs, runID)
	s.reasons = append(s.reasons, reason)
	index := len(s.runIDs) - 1
	if index < len(s.errs) && s.errs[index] != nil {
		return false, s.errs[index]
	}
	if index < len(s.results) {
		return s.results[index], nil
	}
	return false, nil
}
