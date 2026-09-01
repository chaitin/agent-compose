package webhooks

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"

	"github.com/labstack/echo/v4"
)

const maxStopEventCount = 1000

type RunStopper interface {
	RequestSchedulerRunStop(context.Context, string, string, string) (bool, error)
}

type stopEventRequest struct {
	Reason string `json:"reason"`
}

type stopEventResponse struct {
	EventID       string   `json:"event_id"`
	StopRequested bool     `json:"stop_requested"`
	RequestedRuns int      `json:"requested_runs"`
	FailedRuns    int      `json:"failed_runs"`
	FailedRunIDs  []string `json:"failed_run_ids,omitempty"`
	Error         string   `json:"error,omitempty"`
}

func (h routeHandler) handleStopEvent(c echo.Context) error {
	if h.store() == nil || h.opts.RunStopper == nil {
		return c.JSON(http.StatusNotImplemented, map[string]string{"error": "event stopping is not configured"})
	}
	eventID := strings.TrimSpace(c.Param("event_id"))
	if eventID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "event_id is required"})
	}
	ctx := c.Request().Context()
	event, err := h.store().GetEvent(ctx, eventID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	sourceID, err := stopEventSourceID(event.PayloadJSON)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to inspect event source"})
	}
	if sourceID == "" {
		return c.JSON(http.StatusForbidden, map[string]string{"error": "event is not associated with a webhook source"})
	}
	source, ok, err := h.store().GetWebhookSource(ctx, sourceID)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook source"})
	}
	if !ok || !source.Enabled || source.TokenHash == "" || !ValidTokenHash(c.Request(), source.TokenHash, source.TokenHeader) {
		return c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid webhook token"})
	}
	req, err := decodeStopEventRequest(c.Request(), h.opts.WebhookBodyLimit)
	if err != nil {
		if errors.Is(err, domain.ErrBodyTooLarge) {
			return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body is too large"})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "body must be a JSON object"})
	}
	eventIDs, err := h.store().ListDescendantEventIDs(ctx, eventID, maxStopEventCount+1)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event descendants"})
	}
	if len(eventIDs) > maxStopEventCount {
		return c.JSON(http.StatusConflict, map[string]any{
			"error":           "event descendant limit exceeded",
			"max_event_count": maxStopEventCount,
		})
	}
	deliveries, err := h.store().ListEventDeliveries(ctx, eventIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event deliveries"})
	}
	response := stopEventRuns(ctx, h.opts.RunStopper, eventID, req.Reason, deliveries)
	if response.FailedRuns > 0 {
		response.Error = "failed to stop one or more event runs"
		return c.JSON(http.StatusInternalServerError, response)
	}
	return c.JSON(http.StatusOK, response)
}

func stopEventSourceID(payloadJSON string) (string, error) {
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return "", err
	}
	sourceID, _ := payload["webhookSourceId"].(string)
	return strings.TrimSpace(sourceID), nil
}

func decodeStopEventRequest(r *http.Request, bodyLimit int64) (stopEventRequest, error) {
	body, err := ReadBody(r, bodyLimit)
	if err != nil {
		return stopEventRequest{}, err
	}
	var req stopEventRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(&req); err != nil {
		if errors.Is(err, io.EOF) {
			return req, nil
		}
		return stopEventRequest{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return stopEventRequest{}, errors.New("body must contain one JSON object")
	}
	return req, nil
}

func stopEventRuns(ctx context.Context, stopper RunStopper, eventID, reason string, deliveries []domain.EventDelivery) stopEventResponse {
	response := stopEventResponse{EventID: eventID}
	seen := make(map[schedulerRunRef]struct{}, len(deliveries))
	for _, delivery := range deliveries {
		schedulerID := strings.TrimSpace(delivery.SchedulerID)
		runID := strings.TrimSpace(delivery.RunID)
		if schedulerID == "" || runID == "" {
			continue
		}
		ref := schedulerRunRef{schedulerID: schedulerID, runID: runID}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		requested, err := stopper.RequestSchedulerRunStop(ctx, schedulerID, runID, reason)
		if err != nil {
			response.FailedRunIDs = append(response.FailedRunIDs, runID)
			continue
		}
		if requested {
			response.RequestedRuns++
		}
	}
	response.StopRequested = response.RequestedRuns > 0
	response.FailedRuns = len(response.FailedRunIDs)
	return response
}

type schedulerRunRef struct {
	schedulerID string
	runID       string
}
