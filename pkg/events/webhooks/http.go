package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	"github.com/chaitin/agent-compose/pkg/events"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type Store interface {
	FindEventByIdempotencyKey(context.Context, string, string) (domain.TopicEventRecord, bool, error)
	CreateEvent(context.Context, domain.TopicEventRecord) (domain.TopicEventRecord, error)
	GetEvent(context.Context, string) (domain.TopicEventRecord, error)
	ListEvents(context.Context, domain.TopicEventFilter) ([]domain.TopicEventRecord, int, error)
	ListDescendantEventIDs(context.Context, string, int) ([]string, error)
	ListEventSandboxLinks(context.Context, []string) ([]domain.EventSandboxTraceItem, error)
	ListEventDeliveries(context.Context, []string) ([]domain.EventDelivery, error)
	CancelEventDispatch(context.Context, []string, string) (domain.EventDispatchCancellation, error)
	ListWebhookSources(context.Context) ([]domain.WebhookSource, error)
	GetWebhookSource(context.Context, string) (domain.WebhookSource, bool, error)
	UpsertWebhookSource(context.Context, domain.WebhookSource) (domain.WebhookSource, error)
	DeleteWebhookSource(context.Context, string) error
	ListEnabledWebhookSourcesForTopic(context.Context, string) ([]domain.WebhookSource, error)
}

type RouteOptions struct {
	Store              Store
	QueryStore         EventQueryStore
	Sandboxes          SandboxSummaryReader
	WebhookBodyLimit   int64
	NewEventID         func() string
	MarshalJSONCompact func(any) (string, error)
	RunStopper         RunStopper
}

func RegisterRoutes(app *echo.Echo, opts RouteOptions) {
	h := routeHandler{opts: opts}
	app.POST("/api/webhooks/:topic", h.handleWebhook)
	app.POST("/api/webhooks/events/:event_id/stop", h.handleStopEvent)
	app.GET("/api/webhook-sources", h.handleListWebhookSources)
	app.PUT("/api/webhook-sources/:source_id", h.handlePutWebhookSource)
	app.DELETE("/api/webhook-sources/:source_id", h.handleDeleteWebhookSource)
	app.GET("/api/events", h.handleListEvents)
	app.GET("/api/events/topics", h.handleListEventTopics)
	app.GET("/api/events/:event_id/sessions", h.handleGetEventSandboxes)
	app.GET("/api/events/:event_id/sandboxes", h.handleGetEventSandboxes)
	app.GET("/api/events/:event_id/runs", h.handleGetEventRuns)
	app.GET("/api/events/:event_id/trace", h.handleGetEventTrace)
	app.GET("/api/events/:event_id", h.handleGetEvent)
}

type routeHandler struct {
	opts RouteOptions
}

func (h routeHandler) store() Store {
	return h.opts.Store
}

func (h routeHandler) queryStore() EventQueryStore {
	store := h.opts.QueryStore
	if store == nil {
		store, _ = any(h.opts.Store).(EventQueryStore)
	}
	return store
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
	resolved, handled, err := h.resolveWebhookEvent(c)
	if handled {
		return err
	}
	return h.storeWebhookEvent(c, resolved)
}

// resolvedWebhookEvent carries the topic, source, and decoded body that
// survived validation and authorization, ready to be built into a stored
// event.
type resolvedWebhookEvent struct {
	Topic          string
	Source         domain.WebhookSource
	Body           map[string]any
	CompactBody    string
	IdempotencyKey string
}

// resolveWebhookEvent validates, authorizes, and decodes the inbound webhook
// request. If handled is true, the caller must return err as-is (a response
// has already been written, whether that's an error or an accepted/idempotent
// duplicate).
func (h routeHandler) resolveWebhookEvent(c echo.Context) (resolvedWebhookEvent, bool, error) {
	topic := strings.TrimSpace(c.Param("topic"))
	if err := ValidateExternalTopic(topic); err != nil {
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	sources, bodyLimit, handled, err := h.webhookSources(c, topic)
	if handled {
		return resolvedWebhookEvent{}, true, err
	}
	bodyFormat := detectRequestBodyFormat(c.Request())
	if bodyFormat == requestBodyFormatUnsupported {
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusUnsupportedMediaType, map[string]string{"error": "content-type must be application/json or application/x-www-form-urlencoded"})
	}
	rawBody, err := ReadBody(c.Request(), bodyLimit)
	if err != nil {
		if errors.Is(err, domain.ErrBodyTooLarge) {
			return resolvedWebhookEvent{}, true, c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body is too large"})
		}
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
	}
	source, handled, err := h.authorizeWebhookRequest(c, sources, rawBody)
	if handled {
		return resolvedWebhookEvent{}, true, err
	}
	if limit := h.sourceBodyLimit(source); limit > 0 && int64(len(rawBody)) > limit {
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body is too large"})
	}
	githubMode, err := domain.GitHubWebhookModeForSource(source)
	if err != nil {
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid webhook source configuration"})
	}
	if githubMode != domain.GitHubWebhookModeGeneric {
		var ok bool
		topic, ok = githubTopic(c.Request())
		if !ok {
			return resolvedWebhookEvent{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": "invalid or missing X-GitHub-Event header"})
		}
	}
	if bodyFormat == requestBodyFormatForm && githubMode == domain.GitHubWebhookModeGeneric {
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusUnsupportedMediaType, map[string]string{"error": "application/x-www-form-urlencoded is supported only for GitHub webhooks"})
	}
	decodedBody := rawBody
	if bodyFormat == requestBodyFormatForm {
		decodedBody, err = decodeGitHubFormPayload(rawBody)
		if err != nil {
			return resolvedWebhookEvent{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}
	body, compactBody, err := DecodeJSONObject(decodedBody)
	if err != nil {
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	idempotencyKey := ExtractIdempotencyKey(c.Request())
	if existing, ok, err := h.store().FindEventByIdempotencyKey(c.Request().Context(), topic, idempotencyKey); err != nil {
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook event"})
	} else if ok {
		if ExistingBodyHash(existing.PayloadJSON) != events.PayloadSHA256(compactBody) {
			return resolvedWebhookEvent{}, true, c.JSON(http.StatusConflict, idempotencyConflictResponseFor(existing))
		}
		return resolvedWebhookEvent{}, true, c.JSON(http.StatusAccepted, acceptedResponseFor(existing))
	}
	return resolvedWebhookEvent{
		Topic:          topic,
		Source:         source,
		Body:           body,
		CompactBody:    compactBody,
		IdempotencyKey: idempotencyKey,
	}, false, nil
}

// storeWebhookEvent builds the delivery payload for a resolved webhook event,
// persists it, and writes the HTTP response.
func (h routeHandler) storeWebhookEvent(c echo.Context, resolved resolvedWebhookEvent) error {
	eventID := h.newEventID()
	correlationID := ExtractCorrelationID(c.Request(), resolved.Body)
	if correlationID == "" {
		correlationID = eventID
	}
	payload := BuildPayload(c.Request(), WebhookPayloadRequest{
		EventID:        eventID,
		Topic:          resolved.Topic,
		CorrelationID:  correlationID,
		IdempotencyKey: resolved.IdempotencyKey,
		Source:         resolved.Source,
		Body:           resolved.Body,
	})
	payloadJSON, err := h.marshalJSONCompact(payload)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode webhook payload"})
	}
	payloadHash := events.PayloadSHA256(payloadJSON)
	created, err := h.store().CreateEvent(c.Request().Context(), domain.TopicEventRecord{
		ID:             eventID,
		Topic:          resolved.Topic,
		Source:         domain.TopicEventSourceWebhook,
		Provider:       firstNonEmpty(resolved.Source.Provider, ProviderFromTopic(resolved.Topic)),
		Intent:         IntentFromBody(resolved.Body),
		CorrelationID:  correlationID,
		IdempotencyKey: resolved.IdempotencyKey,
		DeliveryID:     ExtractDeliveryID(c.Request()),
		PayloadHash:    payloadHash,
		PayloadJSON:    payloadJSON,
		DispatchStatus: domain.TopicEventDispatchPending,
		PublisherType:  domain.TopicEventSourceWebhook,
	})
	if err != nil {
		var conflict *domain.TopicEventIdempotencyConflictError
		if errors.As(err, &conflict) {
			existing := conflict.Existing
			if strings.TrimSpace(existing.ID) == "" || strings.TrimSpace(existing.Topic) != resolved.Topic {
				return c.JSON(http.StatusInternalServerError, map[string]string{"error": "invalid conflicting webhook event"})
			}
			if ExistingBodyHash(existing.PayloadJSON) == events.PayloadSHA256(resolved.CompactBody) {
				return c.JSON(http.StatusAccepted, acceptedResponseFor(existing))
			}
			return c.JSON(http.StatusConflict, idempotencyConflictResponseFor(existing))
		}
		if errors.Is(err, domain.ErrConflict) {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "webhook event conflict did not include the existing event"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store webhook event"})
	}
	return c.JSON(http.StatusAccepted, acceptedResponseFor(created))
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
	source := strings.TrimSpace(c.QueryParam("source"))
	if source != "" {
		source = events.NormalizeSource(source)
		if source == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "source is invalid"})
		}
	}
	topic := strings.TrimSpace(c.QueryParam("topic"))
	if topic != "" {
		if err := events.ValidateTopicName(topic); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
		}
	}
	correlationID := strings.TrimSpace(c.QueryParam("correlation_id"))
	if source == "" && topic == "" && correlationID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source, topic, or correlation_id is required"})
	}
	view := strings.ToLower(strings.TrimSpace(c.QueryParam("view")))
	if view != "" && view != "full" && view != "summary" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "view is invalid"})
	}
	afterSequence, err := parseOptionalInt64Query(c, "after_sequence")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "after_sequence is invalid"})
	}
	limit, err := parseLimitQuery(c, 100, 500)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit is invalid"})
	}
	offset, err := parseOptionalIntQuery(c, "offset")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "offset is invalid"})
	}
	offsetProvided := strings.TrimSpace(c.QueryParam("offset")) != ""
	if afterSequence > 0 && offsetProvided {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "offset and after_sequence cannot be combined"})
	}
	filter := domain.TopicEventFilter{
		Source:        source,
		Topic:         topic,
		CorrelationID: correlationID,
		AfterSequence: afterSequence,
		Offset:        offset,
		Limit:         limit,
		SequenceAsc:   !offsetProvided,
	}
	if view == "summary" {
		store := h.queryStore()
		if store == nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "event query store is required"})
		}
		items, total, err := store.ListEventSummaries(c.Request().Context(), filter)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list events"})
		}
		resp := EventSummaryListResponse{Items: make([]EventSummaryJSON, 0, len(items)), Total: total}
		for _, item := range items {
			resp.Items = append(resp.Items, eventSummaryToJSON(item))
			if !offsetProvided && item.Sequence > resp.NextAfterSequence {
				resp.NextAfterSequence = item.Sequence
			}
		}
		return c.JSON(http.StatusOK, resp)
	}
	items, total, err := h.store().ListEvents(c.Request().Context(), filter)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list events"})
	}
	resp := TopicEventListResponse{Items: make([]TopicEventJSON, 0, len(items)), Total: total}
	for _, item := range items {
		resp.Items = append(resp.Items, TopicEventToJSON(item))
		if !offsetProvided && item.Sequence > resp.NextAfterSequence {
			resp.NextAfterSequence = item.Sequence
		}
	}
	return c.JSON(http.StatusOK, resp)
}

func (h routeHandler) handleListEventTopics(c echo.Context) error {
	store := h.queryStore()
	if store == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "event query store is required"})
	}
	source := events.NormalizeSource(c.QueryParam("source"))
	if source == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source is required"})
	}
	limit, err := parseLimitQuery(c, 100, 500)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "limit is invalid"})
	}
	offset, err := parseOptionalIntQuery(c, "offset")
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "offset is invalid"})
	}
	items, total, err := store.ListEventTopics(c.Request().Context(), source, offset, limit)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list event topics"})
	}
	resp := EventTopicListResponse{Items: make([]EventTopicJSON, 0, len(items)), Total: total}
	for _, item := range items {
		resp.Items = append(resp.Items, EventTopicJSON{
			Topic:         item.Topic,
			EventCount:    item.EventCount,
			LatestEventAt: formatEventTime(item.LatestEventAt),
		})
	}
	return c.JSON(http.StatusOK, resp)
}

func (h routeHandler) handleGetEventTrace(c echo.Context) error {
	store := h.queryStore()
	if store == nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "event query store is required"})
	}
	eventID := strings.TrimSpace(c.Param("event_id"))
	view, err := newTraceService(store, h.opts.Sandboxes).trace(c.Request().Context(), eventID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to trace event"})
	}
	if view.SandboxSummaryError != nil {
		slog.WarnContext(c.Request().Context(), "event trace sandbox summary enrichment failed", "event_id", eventID, "error", view.SandboxSummaryError)
	}
	return c.JSON(http.StatusOK, eventTraceResponseFor(view))
}

func (h routeHandler) handleGetEventSandboxes(c echo.Context) error {
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
	links, err := h.store().ListEventSandboxLinks(c.Request().Context(), eventIDs)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list event sandboxes"})
	}
	return c.JSON(http.StatusOK, EventSandboxesResponse(EventSandboxesResponseFor(item, links)))
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

func (h routeHandler) webhookSources(c echo.Context, topic string) ([]domain.WebhookSource, int64, bool, error) {
	sources, err := h.store().ListEnabledWebhookSourcesForTopic(c.Request().Context(), topic)
	if err != nil {
		return nil, 0, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook sources"})
	}
	if len(sources) == 0 {
		return nil, 0, true, c.JSON(http.StatusNotFound, map[string]string{"error": "webhook source not found"})
	}
	validSources := make([]domain.WebhookSource, 0, len(sources))
	var limit int64
	for _, source := range sources {
		if _, err := domain.GitHubWebhookModeForSource(source); err != nil {
			continue
		}
		validSources = append(validSources, source)
		if sourceLimit := h.sourceBodyLimit(source); sourceLimit > limit {
			limit = sourceLimit
		}
	}
	if len(validSources) == 0 {
		return nil, 0, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "webhook source configuration is invalid"})
	}
	return validSources, limit, false, nil
}

func (h routeHandler) sourceBodyLimit(source domain.WebhookSource) int64 {
	if source.BodyLimitBytes > 0 {
		return source.BodyLimitBytes
	}
	if h.opts.WebhookBodyLimit > 0 {
		return h.opts.WebhookBodyLimit
	}
	return defaultWebhookBodyLimit
}

func (h routeHandler) authorizeWebhookRequest(c echo.Context, sources []domain.WebhookSource, rawBody []byte) (domain.WebhookSource, bool, error) {
	for _, source := range sources {
		githubMode, err := domain.GitHubWebhookModeForSource(source)
		if err == nil && githubMode == domain.GitHubWebhookModeUnsigned && source.TokenHash == "" && len(sources) > 1 {
			return domain.WebhookSource{}, true, c.JSON(http.StatusConflict, map[string]string{"error": "unsigned webhook source is ambiguous"})
		}
	}

	matches := make([]domain.WebhookSource, 0, 1)
	for _, source := range sources {
		githubMode, err := domain.GitHubWebhookModeForSource(source)
		if err != nil {
			continue
		}
		authenticated := source.TokenHash != "" && ValidTokenHash(c.Request(), source.TokenHash, source.TokenHeader)
		switch githubMode {
		case domain.GitHubWebhookModeUnsigned:
			if source.TokenHash == "" {
				authenticated = true
			}
		case domain.GitHubWebhookModeSHA256:
			authenticated = validGitHubSignature(c.Request(), rawBody, source.SignatureSecret)
		}
		if authenticated {
			matches = append(matches, source)
		}
	}
	if len(matches) == 0 {
		return domain.WebhookSource{}, true, c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid webhook source authentication"})
	}
	if len(matches) > 1 {
		return domain.WebhookSource{}, true, c.JSON(http.StatusConflict, map[string]string{"error": "webhook source is ambiguous"})
	}
	return matches[0], false, nil
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

func parseOptionalIntQuery(c echo.Context, name string) (int, error) {
	value, err := parseOptionalInt64Query(c, name)
	if err != nil || value > int64(^uint(0)>>1) {
		return 0, fmt.Errorf("%s is invalid", name)
	}
	return int(value), nil
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
