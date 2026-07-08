package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	domain "agent-compose/pkg/model"
)

type Store interface {
	FindEventByIdempotencyKey(context.Context, string, string) (domain.TopicEventRecord, bool, error)
	CreateEvent(context.Context, domain.TopicEventRecord) (domain.TopicEventRecord, error)
	UpdateEventPayload(context.Context, string, string) error
	GetEvent(context.Context, string) (domain.TopicEventRecord, error)
	ListEvents(context.Context, domain.TopicEventFilter) ([]domain.TopicEventRecord, error)
	ListDescendantEventIDs(context.Context, string, int) ([]string, error)
	ListEventSessionLinks(context.Context, []string) ([]domain.EventSessionTraceItem, error)
	ListEventDeliveries(context.Context, []string) ([]domain.EventDelivery, error)
	ListWebhookSources(context.Context) ([]domain.WebhookSource, error)
	GetWebhookSource(context.Context, string) (domain.WebhookSource, bool, error)
	UpsertWebhookSource(context.Context, domain.WebhookSource) (domain.WebhookSource, error)
	DeleteWebhookSource(context.Context, string) error
	ListEnabledWebhookSourcesForTopic(context.Context, string) ([]domain.WebhookSource, error)
}

type RouteOptions struct {
	Store              Store
	WebhookBodyLimit   int64
	NewEventID         func() string
	MarshalJSONCompact func(any) (string, error)
}

func RegisterRoutes(app *echo.Echo, opts RouteOptions) {
	h := routeHandler{opts: opts}
	app.POST("/api/webhooks/:topic", h.handleWebhook)
	app.GET("/api/webhook-sources", h.handleListWebhookSources)
	app.PUT("/api/webhook-sources/:source_id", h.handlePutWebhookSource)
	app.DELETE("/api/webhook-sources/:source_id", h.handleDeleteWebhookSource)
	app.GET("/api/events", h.handleListEvents)
	app.GET("/api/events/:event_id/sessions", h.handleGetEventSessions)
	app.GET("/api/events/:event_id/runs", h.handleGetEventRuns)
	app.GET("/api/events/:event_id", h.handleGetEvent)
}

type routeHandler struct {
	opts RouteOptions
}

func (h routeHandler) store() Store {
	return h.opts.Store
}

func (h routeHandler) newEventID() string {
	if h.opts.NewEventID != nil {
		return h.opts.NewEventID()
	}
	return "evt_" + uuid.NewString()
}

func (h routeHandler) marshalJSONCompact(value any) (string, error) {
	if h.opts.MarshalJSONCompact != nil {
		return h.opts.MarshalJSONCompact(value)
	}
	return domain.MarshalJSONCompact(value)
}

func (h routeHandler) handleWebhook(c echo.Context) error {
	if h.store() == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "webhook store is required"})
	}
	topic := strings.TrimSpace(c.Param("topic"))
	if err := ValidateExternalTopic(topic); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	source, bodyLimit, handled, err := h.authorizeWebhookRequest(c, topic)
	if handled {
		return err
	}
	if !RequestContentTypeIsJSON(c.Request()) {
		return c.JSON(http.StatusUnsupportedMediaType, map[string]string{"error": "content-type must be application/json"})
	}
	rawBody, err := ReadBody(c.Request(), bodyLimit)
	if err != nil {
		if errors.Is(err, domain.ErrBodyTooLarge) {
			return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body is too large"})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
	}
	body, compactBody, err := DecodeJSONObject(rawBody)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	idempotencyKey := ExtractIdempotencyKey(c.Request())
	if existing, ok, err := h.store().FindEventByIdempotencyKey(c.Request().Context(), topic, idempotencyKey); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook event"})
	} else if ok {
		if ExistingBodyHash(existing.PayloadJSON) != domain.TopicEventPayloadSHA256(compactBody) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "idempotency key conflicts with existing payload"})
		}
		return c.JSON(http.StatusAccepted, AcceptedResponse{
			Accepted:      true,
			Topic:         existing.Topic,
			EventID:       existing.ID,
			Sequence:      existing.Sequence,
			CorrelationID: existing.CorrelationID,
		})
	}

	eventID := h.newEventID()
	correlationID := ExtractCorrelationID(c.Request(), body)
	if correlationID == "" {
		correlationID = eventID
	}
	payload := BuildPayload(c.Request(), eventID, 0, topic, correlationID, idempotencyKey, source, body)
	payloadJSON, err := h.marshalJSONCompact(payload)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode webhook payload"})
	}
	payloadHash := domain.TopicEventPayloadSHA256(payloadJSON)
	created, err := h.store().CreateEvent(c.Request().Context(), domain.TopicEventRecord{
		ID:             eventID,
		Topic:          topic,
		Source:         domain.TopicEventSourceWebhook,
		Provider:       firstNonEmpty(source.Provider, ProviderFromTopic(topic)),
		Intent:         IntentFromBody(body),
		CorrelationID:  correlationID,
		IdempotencyKey: idempotencyKey,
		DeliveryID:     ExtractDeliveryID(c.Request()),
		PayloadHash:    payloadHash,
		PayloadJSON:    payloadJSON,
		DispatchStatus: domain.TopicEventDispatchPending,
		PublisherType:  domain.TopicEventSourceWebhook,
	})
	if err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "idempotency key conflicts with existing payload"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store webhook event"})
	}
	if created.ID != eventID {
		return c.JSON(http.StatusAccepted, AcceptedResponse{
			Accepted:      true,
			Topic:         created.Topic,
			EventID:       created.ID,
			Sequence:      created.Sequence,
			CorrelationID: created.CorrelationID,
		})
	}
	if created.Sequence != 0 {
		payload = BuildPayload(c.Request(), created.ID, created.Sequence, topic, created.CorrelationID, created.IdempotencyKey, source, body)
		payloadJSON, err = h.marshalJSONCompact(payload)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode webhook payload"})
		}
		if err := h.store().UpdateEventPayload(c.Request().Context(), created.ID, payloadJSON); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store webhook event payload"})
		}
	}
	return c.JSON(http.StatusAccepted, AcceptedResponse{
		Accepted:      true,
		Topic:         created.Topic,
		EventID:       created.ID,
		Sequence:      created.Sequence,
		CorrelationID: created.CorrelationID,
	})
}

func (h routeHandler) handleGetEvent(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := h.store().GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	return c.JSON(http.StatusOK, TopicEventResponse{Event: TopicEventToJSON(item)})
}

func (h routeHandler) handleListEvents(c echo.Context) error {
	topic := strings.TrimSpace(c.QueryParam("topic"))
	if topic != "" {
		if err := domain.ValidateTopicEventName(topic); err != nil {
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
	items, err := h.store().ListEvents(c.Request().Context(), domain.TopicEventFilter{
		Topic:         topic,
		CorrelationID: correlationID,
		AfterSequence: afterSequence,
		Limit:         limit,
	})
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list events"})
	}
	resp := TopicEventListResponse{Items: make([]TopicEventJSON, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, TopicEventToJSON(item))
		if item.Sequence > resp.NextAfterSequence {
			resp.NextAfterSequence = item.Sequence
		}
	}
	return c.JSON(http.StatusOK, resp)
}

func (h routeHandler) handleGetEventSessions(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := h.store().GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
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
	return c.JSON(http.StatusOK, EventSessionsResponse(EventSessionsResponseFor(item, links)))
}

func (h routeHandler) handleGetEventRuns(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := h.store().GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
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
	return c.JSON(http.StatusOK, EventRunsResponse(EventRunsResponseFor(item, deliveries)))
}

func (h routeHandler) handleListWebhookSources(c echo.Context) error {
	items, err := h.store().ListWebhookSources(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list webhook sources"})
	}
	resp := SourceListResponse{Items: make([]SourceJSON, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, SourceToJSON(item))
	}
	return c.JSON(http.StatusOK, resp)
}

func (h routeHandler) handlePutWebhookSource(c echo.Context) error {
	sourceID := strings.TrimSpace(c.Param("source_id"))
	if sourceID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source_id is required"})
	}
	var req SourceRequest
	if err := json.NewDecoder(c.Request().Body).Decode(&req); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "body must be valid JSON"})
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	tokenHash := strings.TrimSpace(req.TokenHash)
	tokenHeader := ""
	if req.TokenHeader != nil {
		tokenHeader = strings.TrimSpace(*req.TokenHeader)
	}
	if existing, ok, err := h.store().GetWebhookSource(c.Request().Context(), sourceID); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook source"})
	} else if ok {
		tokenHash = existing.TokenHash
		if req.TokenHeader == nil {
			tokenHeader = existing.TokenHeader
		}
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
		tokenHash = TokenHash(req.Token)
	}
	if req.ClearSignature {
		req.SignatureSecret = ""
	}
	source, err := h.store().UpsertWebhookSource(c.Request().Context(), domain.WebhookSource{
		ID:              sourceID,
		Name:            req.Name,
		Enabled:         enabled,
		Provider:        req.Provider,
		TopicPrefix:     req.TopicPrefix,
		TokenHash:       tokenHash,
		TokenHeader:     tokenHeader,
		SignatureType:   req.SignatureType,
		SignatureSecret: req.SignatureSecret,
		BodyLimitBytes:  req.BodyLimitBytes,
	})
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	return c.JSON(http.StatusOK, SourceResponse{Source: SourceToJSON(source)})
}

func (h routeHandler) handleDeleteWebhookSource(c echo.Context) error {
	sourceID := strings.TrimSpace(c.Param("source_id"))
	if sourceID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source_id is required"})
	}
	if err := h.store().DeleteWebhookSource(c.Request().Context(), sourceID); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "webhook source not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to delete webhook source"})
	}
	return c.NoContent(http.StatusNoContent)
}

func (h routeHandler) authorizeWebhookRequest(c echo.Context, topic string) (domain.WebhookSource, int64, bool, error) {
	sources, err := h.store().ListEnabledWebhookSourcesForTopic(c.Request().Context(), topic)
	if err != nil {
		return domain.WebhookSource{}, 0, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook sources"})
	}
	if len(sources) == 0 {
		return domain.WebhookSource{}, 0, true, c.JSON(http.StatusNotFound, map[string]string{"error": "webhook source not found"})
	}
	matches := make([]domain.WebhookSource, 0, 1)
	for _, source := range sources {
		if source.TokenHash != "" && ValidTokenHash(c.Request(), source.TokenHash, source.TokenHeader) {
			matches = append(matches, source)
		}
	}
	if len(matches) == 0 {
		return domain.WebhookSource{}, 0, true, c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid webhook source token"})
	}
	if len(matches) > 1 {
		return domain.WebhookSource{}, 0, true, c.JSON(http.StatusConflict, map[string]string{"error": "webhook source is ambiguous"})
	}
	limit := matches[0].BodyLimitBytes
	if limit <= 0 {
		limit = h.opts.WebhookBodyLimit
	}
	return matches[0], limit, false, nil
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
