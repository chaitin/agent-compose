package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/labstack/echo/v4"

	eventsdomain "agent-compose/internal/agentcompose/events"
)

type WebhookManagementStore interface {
	GetEvent(ctx context.Context, eventID string) (eventsdomain.TopicEventRecord, error)
	ListEvents(ctx context.Context, filter eventsdomain.TopicEventFilter) ([]eventsdomain.TopicEventRecord, error)
	ListDescendantEventIDs(ctx context.Context, rootEventID string, limit int) ([]string, error)
	ListEventSessionLinks(ctx context.Context, eventIDs []string) ([]eventsdomain.EventSessionTraceItem, error)
	ListEventDeliveries(ctx context.Context, eventIDs []string) ([]eventsdomain.EventDelivery, error)
	ListWebhookSources(ctx context.Context) ([]eventsdomain.WebhookSource, error)
	GetWebhookSource(ctx context.Context, sourceID string) (eventsdomain.WebhookSource, bool, error)
	UpsertWebhookSource(ctx context.Context, source eventsdomain.WebhookSource) (eventsdomain.WebhookSource, error)
	DeleteWebhookSource(ctx context.Context, sourceID string) error
}

type WebhookManagementAPI struct {
	Store WebhookManagementStore
}

type webhookSourceRequest struct {
	Name            string `json:"name"`
	Enabled         *bool  `json:"enabled,omitempty"`
	Provider        string `json:"provider"`
	TopicPrefix     string `json:"topic_prefix"`
	Token           string `json:"token"`
	TokenHash       string `json:"token_hash"`
	ClearToken      bool   `json:"clear_token"`
	SignatureType   string `json:"signature_type"`
	SignatureSecret string `json:"signature_secret"`
	ClearSignature  bool   `json:"clear_signature"`
	BodyLimitBytes  int64  `json:"body_limit_bytes"`
}

type webhookSourceJSON struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Enabled            bool   `json:"enabled"`
	Provider           string `json:"provider"`
	TopicPrefix        string `json:"topic_prefix"`
	HasToken           bool   `json:"has_token"`
	SignatureType      string `json:"signature_type,omitempty"`
	HasSignatureSecret bool   `json:"has_signature_secret"`
	BodyLimitBytes     int64  `json:"body_limit_bytes,omitempty"`
	CreatedAt          string `json:"created_at"`
	UpdatedAt          string `json:"updated_at"`
}

type webhookSourceListResponse struct {
	Items []webhookSourceJSON `json:"items"`
}

type webhookSourceResponse struct {
	Source webhookSourceJSON `json:"source"`
}

type topicEventJSON struct {
	EventID        string         `json:"event_id"`
	Sequence       int64          `json:"sequence"`
	Topic          string         `json:"topic"`
	Source         string         `json:"source"`
	Provider       string         `json:"provider,omitempty"`
	Intent         string         `json:"intent,omitempty"`
	CorrelationID  string         `json:"correlation_id"`
	IdempotencyKey string         `json:"idempotency_key,omitempty"`
	DeliveryID     string         `json:"delivery_id,omitempty"`
	DispatchStatus string         `json:"dispatch_status"`
	ParentEventID  string         `json:"parent_event_id,omitempty"`
	PublisherType  string         `json:"publisher_type,omitempty"`
	PublisherID    string         `json:"publisher_id,omitempty"`
	PublisherRunID string         `json:"publisher_run_id,omitempty"`
	CreatedAt      string         `json:"created_at"`
	DispatchedAt   string         `json:"dispatched_at,omitempty"`
	Payload        map[string]any `json:"payload"`
}

type topicEventResponse struct {
	Event topicEventJSON `json:"event"`
}

type topicEventListResponse struct {
	Items             []topicEventJSON `json:"items"`
	NextAfterSequence int64            `json:"next_after_sequence"`
}

type eventSessionsResponse struct {
	EventID       string             `json:"event_id"`
	CorrelationID string             `json:"correlation_id"`
	Sessions      []eventSessionJSON `json:"sessions"`
}

type eventRunsResponse struct {
	EventID       string         `json:"event_id"`
	CorrelationID string         `json:"correlation_id"`
	Runs          []eventRunJSON `json:"runs"`
}

type eventRunJSON struct {
	EventID   string `json:"event_id"`
	LoaderID  string `json:"loader_id"`
	RunID     string `json:"run_id,omitempty"`
	TriggerID string `json:"trigger_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type eventSessionJSON struct {
	SessionID     string `json:"session_id"`
	Relation      string `json:"relation"`
	LoaderID      string `json:"loader_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	TriggerID     string `json:"trigger_id,omitempty"`
	LoaderEventID string `json:"loader_event_id,omitempty"`
	EventID       string `json:"event_id"`
	CreatedAt     string `json:"created_at"`
}

func (h WebhookManagementAPI) HandleGetEvent(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := h.store().GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	return c.JSON(http.StatusOK, topicEventResponse{Event: toTopicEventJSON(item)})
}

func (h WebhookManagementAPI) HandleListEvents(c echo.Context) error {
	topic := strings.TrimSpace(c.QueryParam("topic"))
	if topic != "" {
		if err := eventsdomain.ValidateTopicEventName(topic); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}
	correlationID := strings.TrimSpace(c.QueryParam("correlation_id"))
	if topic == "" && correlationID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "topic or correlation_id is required"})
	}
	afterSequence, err := parseOptionalInt64Query(c, "after_sequence")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "after_sequence is invalid"})
	}
	limit, err := parseLimitQuery(c, 100, 500)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit is invalid"})
	}
	items, err := h.store().ListEvents(c.Request().Context(), eventsdomain.TopicEventFilter{
		Topic:         topic,
		CorrelationID: correlationID,
		AfterSequence: afterSequence,
		Limit:         limit,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list events"})
	}
	resp := topicEventListResponse{Items: make([]topicEventJSON, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, toTopicEventJSON(item))
		if item.Sequence > resp.NextAfterSequence {
			resp.NextAfterSequence = item.Sequence
		}
	}
	return c.JSON(http.StatusOK, resp)
}

func (h WebhookManagementAPI) HandleGetEventSessions(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := h.store().GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	eventIDs, err := h.store().ListDescendantEventIDs(c.Request().Context(), eventID, 1000)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to trace event descendants"})
	}
	links, err := h.store().ListEventSessionLinks(c.Request().Context(), eventIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list event sessions"})
	}
	resp := eventSessionsResponse{
		EventID:       item.ID,
		CorrelationID: item.CorrelationID,
		Sessions:      make([]eventSessionJSON, 0, len(links)),
	}
	for _, link := range links {
		resp.Sessions = append(resp.Sessions, eventSessionJSON{
			SessionID:     link.SessionID,
			Relation:      link.Relation,
			LoaderID:      link.LoaderID,
			RunID:         link.RunID,
			TriggerID:     link.TriggerID,
			LoaderEventID: link.LoaderEventID,
			EventID:       link.EventID,
			CreatedAt:     link.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return c.JSON(http.StatusOK, resp)
}

func (h WebhookManagementAPI) HandleGetEventRuns(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := h.store().GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	eventIDs, err := h.store().ListDescendantEventIDs(c.Request().Context(), eventID, 1000)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to trace event descendants"})
	}
	deliveries, err := h.store().ListEventDeliveries(c.Request().Context(), eventIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list event runs"})
	}
	resp := eventRunsResponse{
		EventID:       item.ID,
		CorrelationID: item.CorrelationID,
		Runs:          make([]eventRunJSON, 0, len(deliveries)),
	}
	for _, delivery := range deliveries {
		resp.Runs = append(resp.Runs, eventRunJSON{
			EventID:   delivery.EventID,
			LoaderID:  delivery.LoaderID,
			RunID:     delivery.RunID,
			TriggerID: delivery.TriggerID,
			Status:    delivery.Status,
			Error:     delivery.Error,
			CreatedAt: delivery.CreatedAt.UTC().Format(time.RFC3339Nano),
			UpdatedAt: delivery.UpdatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	return c.JSON(http.StatusOK, resp)
}

func (h WebhookManagementAPI) HandleListSources(c echo.Context) error {
	items, err := h.store().ListWebhookSources(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list webhook sources"})
	}
	resp := webhookSourceListResponse{Items: make([]webhookSourceJSON, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, toWebhookSourceJSON(item))
	}
	return c.JSON(http.StatusOK, resp)
}

func (h WebhookManagementAPI) HandlePutSource(c echo.Context) error {
	sourceID := strings.TrimSpace(c.Param("source_id"))
	if sourceID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source_id is required"})
	}
	var req webhookSourceRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "body must be valid JSON"})
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tokenHash := strings.TrimSpace(req.TokenHash)
	if existing, ok, err := h.store().GetWebhookSource(c.Request().Context(), sourceID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook source"})
	} else if ok {
		tokenHash = existing.TokenHash
		if strings.TrimSpace(req.SignatureType) == "" {
			req.SignatureType = existing.SignatureType
		}
		if strings.TrimSpace(req.SignatureSecret) == "" {
			req.SignatureSecret = existing.SignatureSecret
		}
	}
	if req.ClearToken {
		tokenHash = ""
	}
	if strings.TrimSpace(req.Token) != "" {
		tokenHash = WebhookTokenHash(req.Token)
	}
	if req.ClearSignature {
		req.SignatureSecret = ""
	}
	source, err := h.store().UpsertWebhookSource(c.Request().Context(), eventsdomain.WebhookSource{
		ID:              sourceID,
		Name:            req.Name,
		Enabled:         enabled,
		Provider:        req.Provider,
		TopicPrefix:     req.TopicPrefix,
		TokenHash:       tokenHash,
		SignatureType:   req.SignatureType,
		SignatureSecret: req.SignatureSecret,
		BodyLimitBytes:  req.BodyLimitBytes,
	})
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, webhookSourceResponse{Source: toWebhookSourceJSON(source)})
}

func (h WebhookManagementAPI) HandleDeleteSource(c echo.Context) error {
	sourceID := strings.TrimSpace(c.Param("source_id"))
	if sourceID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source_id is required"})
	}
	if err := h.store().DeleteWebhookSource(c.Request().Context(), sourceID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "webhook source not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to delete webhook source"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h WebhookManagementAPI) store() WebhookManagementStore {
	return h.Store
}

func toWebhookSourceJSON(source eventsdomain.WebhookSource) webhookSourceJSON {
	return webhookSourceJSON{
		ID:                 source.ID,
		Name:               source.Name,
		Enabled:            source.Enabled,
		Provider:           source.Provider,
		TopicPrefix:        source.TopicPrefix,
		HasToken:           strings.TrimSpace(source.TokenHash) != "",
		SignatureType:      source.SignatureType,
		HasSignatureSecret: strings.TrimSpace(source.SignatureSecret) != "",
		BodyLimitBytes:     source.BodyLimitBytes,
		CreatedAt:          source.CreatedAt.UTC().Format(time.RFC3339Nano),
		UpdatedAt:          source.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
}

func toTopicEventJSON(item eventsdomain.TopicEventRecord) topicEventJSON {
	payload := make(map[string]any)
	_ = json.Unmarshal([]byte(item.PayloadJSON), &payload)
	out := topicEventJSON{
		EventID:        item.ID,
		Sequence:       item.Sequence,
		Topic:          item.Topic,
		Source:         item.Source,
		Provider:       item.Provider,
		Intent:         item.Intent,
		CorrelationID:  item.CorrelationID,
		IdempotencyKey: item.IdempotencyKey,
		DeliveryID:     item.DeliveryID,
		DispatchStatus: item.DispatchStatus,
		ParentEventID:  item.ParentEventID,
		PublisherType:  item.PublisherType,
		PublisherID:    item.PublisherID,
		PublisherRunID: item.PublisherRunID,
		CreatedAt:      item.CreatedAt.UTC().Format(time.RFC3339Nano),
		Payload:        payload,
	}
	if !item.DispatchedAt.IsZero() {
		out.DispatchedAt = item.DispatchedAt.UTC().Format(time.RFC3339Nano)
	}
	return out
}

func parseOptionalInt64Query(c echo.Context, name string) (int64, error) {
	raw := strings.TrimSpace(c.QueryParam(name))
	if raw == "" {
		return 0, nil
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value < 0 {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return value, nil
}

func parseLimitQuery(c echo.Context, defaultValue, maxValue int) (int, error) {
	raw := strings.TrimSpace(c.QueryParam("limit"))
	if raw == "" {
		return defaultValue, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 || value > maxValue {
		return 0, fmt.Errorf("limit is invalid")
	}
	return value, nil
}
