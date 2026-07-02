package app

import (
	"bytes"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"
)

type webhookAcceptedResponse struct {
	Accepted      bool   `json:"accepted"`
	Topic         string `json:"topic"`
	EventID       string `json:"event_id"`
	Sequence      int64  `json:"sequence"`
	CorrelationID string `json:"correlation_id"`
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

func registerWebhookRoutes(app *echo.Echo, service *Service) {
	app.POST("/api/webhooks/:topic", service.handleWebhook)
	app.GET("/api/webhook-sources", service.handleListWebhookSources)
	app.PUT("/api/webhook-sources/:source_id", service.handlePutWebhookSource)
	app.DELETE("/api/webhook-sources/:source_id", service.handleDeleteWebhookSource)
	app.GET("/api/events", service.handleListEvents)
	app.GET("/api/events/:event_id/sessions", service.handleGetEventSessions)
	app.GET("/api/events/:event_id/runs", service.handleGetEventRuns)
	app.GET("/api/events/:event_id", service.handleGetEvent)
}

func (s *Service) handleWebhook(c echo.Context) error {
	topic := strings.TrimSpace(c.Param("topic"))
	if err := validateExternalWebhookTopic(topic); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	source, bodyLimit, handled, err := s.authorizeWebhookRequest(c, topic)
	if handled {
		return err
	}
	if !requestContentTypeIsJSON(c.Request()) {
		return c.JSON(http.StatusUnsupportedMediaType, map[string]string{"error": "content-type must be application/json"})
	}
	rawBody, err := readWebhookBody(c.Request(), bodyLimit)
	if err != nil {
		if errors.Is(err, errWebhookBodyTooLarge) {
			return c.JSON(http.StatusRequestEntityTooLarge, map[string]string{"error": "request body is too large"})
		}
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "failed to read request body"})
	}
	body, compactBody, err := decodeWebhookJSONObject(rawBody)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	idempotencyKey := extractIdempotencyKey(c.Request())
	if existing, ok, err := s.configDB.FindEventByIdempotencyKey(c.Request().Context(), topic, idempotencyKey); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook event"})
	} else if ok {
		if existingWebhookBodyHash(existing.PayloadJSON) != topicEventPayloadSHA256(compactBody) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "idempotency key conflicts with existing payload"})
		}
		return c.JSON(http.StatusAccepted, webhookAcceptedResponse{
			Accepted:      true,
			Topic:         existing.Topic,
			EventID:       existing.ID,
			Sequence:      existing.Sequence,
			CorrelationID: existing.CorrelationID,
		})
	}

	eventID := "evt_" + newUUIDString()
	correlationID := extractCorrelationID(c.Request(), body)
	if correlationID == "" {
		correlationID = eventID
	}
	payload := buildWebhookPayload(c, eventID, 0, topic, correlationID, idempotencyKey, source, body)
	payloadJSON, err := marshalJSONCompact(payload)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode webhook payload"})
	}
	payloadHash := topicEventPayloadSHA256(payloadJSON)
	created, err := s.configDB.CreateEvent(c.Request().Context(), TopicEventRecord{
		ID:             eventID,
		Topic:          topic,
		Source:         TopicEventSourceWebhook,
		Provider:       firstNonEmpty(source.Provider, providerFromWebhookTopic(topic)),
		Intent:         intentFromWebhookBody(body),
		CorrelationID:  correlationID,
		IdempotencyKey: idempotencyKey,
		DeliveryID:     extractDeliveryID(c.Request()),
		PayloadHash:    payloadHash,
		PayloadJSON:    payloadJSON,
		DispatchStatus: TopicEventDispatchPending,
		PublisherType:  TopicEventSourceWebhook,
	})
	if err != nil {
		if strings.Contains(err.Error(), "idempotency conflict") {
			return c.JSON(http.StatusConflict, map[string]string{"error": "idempotency key conflicts with existing payload"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store webhook event"})
	}
	if created.ID != eventID {
		return c.JSON(http.StatusAccepted, webhookAcceptedResponse{
			Accepted:      true,
			Topic:         created.Topic,
			EventID:       created.ID,
			Sequence:      created.Sequence,
			CorrelationID: created.CorrelationID,
		})
	}
	if created.Sequence != 0 {
		payload = buildWebhookPayload(c, created.ID, created.Sequence, topic, created.CorrelationID, created.IdempotencyKey, source, body)
		payloadJSON, err = marshalJSONCompact(payload)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode webhook payload"})
		}
		if err := s.configDB.UpdateEventPayload(c.Request().Context(), created.ID, payloadJSON); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store webhook event payload"})
		}
	}
	return c.JSON(http.StatusAccepted, webhookAcceptedResponse{
		Accepted:      true,
		Topic:         created.Topic,
		EventID:       created.ID,
		Sequence:      created.Sequence,
		CorrelationID: created.CorrelationID,
	})
}

func (s *Service) handleGetEvent(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := s.configDB.GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	return c.JSON(http.StatusOK, topicEventResponse{Event: toTopicEventJSON(item)})
}

func (s *Service) handleListEvents(c echo.Context) error {
	topic := strings.TrimSpace(c.QueryParam("topic"))
	if topic != "" {
		if err := validateTopicEventName(topic); err != nil {
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
	items, err := s.configDB.ListEvents(c.Request().Context(), TopicEventFilter{
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

func (s *Service) handleGetEventSessions(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := s.configDB.GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	eventIDs, err := s.configDB.ListDescendantEventIDs(c.Request().Context(), eventID, 1000)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to trace event descendants"})
	}
	links, err := s.configDB.ListEventSessionLinks(c.Request().Context(), eventIDs)
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

func (s *Service) handleGetEventRuns(c echo.Context) error {
	eventID := strings.TrimSpace(c.Param("event_id"))
	item, err := s.configDB.GetEvent(c.Request().Context(), eventID)
	if err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "event not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load event"})
	}
	eventIDs, err := s.configDB.ListDescendantEventIDs(c.Request().Context(), eventID, 1000)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to trace event descendants"})
	}
	deliveries, err := s.configDB.ListEventDeliveries(c.Request().Context(), eventIDs)
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

func (s *Service) handleListWebhookSources(c echo.Context) error {
	items, err := s.configDB.ListWebhookSources(c.Request().Context())
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to list webhook sources"})
	}
	resp := webhookSourceListResponse{Items: make([]webhookSourceJSON, 0, len(items))}
	for _, item := range items {
		resp.Items = append(resp.Items, toWebhookSourceJSON(item))
	}
	return c.JSON(http.StatusOK, resp)
}

func (s *Service) handlePutWebhookSource(c echo.Context) error {
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
	if existing, ok, err := s.configDB.GetWebhookSource(c.Request().Context(), sourceID); err != nil {
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
		tokenHash = webhookTokenHash(req.Token)
	}
	if req.ClearSignature {
		req.SignatureSecret = ""
	}
	source, err := s.configDB.UpsertWebhookSource(c.Request().Context(), WebhookSource{
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

func (s *Service) handleDeleteWebhookSource(c echo.Context) error {
	sourceID := strings.TrimSpace(c.Param("source_id"))
	if sourceID == "" {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": "source_id is required"})
	}
	if err := s.configDB.DeleteWebhookSource(c.Request().Context(), sourceID); err != nil {
		if strings.Contains(err.Error(), "not found") {
			return c.JSON(http.StatusNotFound, map[string]string{"error": "webhook source not found"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to delete webhook source"})
	}
	return c.NoContent(http.StatusNoContent)
}

func toWebhookSourceJSON(source WebhookSource) webhookSourceJSON {
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

func presentedWebhookToken(r *http.Request) string {
	presented := ""
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		presented = strings.TrimSpace(auth[len("bearer "):])
	}
	if presented == "" {
		presented = strings.TrimSpace(r.Header.Get("X-WEBHOOK-TOKEN"))
	}
	return presented
}

func webhookTokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validWebhookTokenHash(r *http.Request, hash string) bool {
	hash = strings.TrimSpace(hash)
	token := presentedWebhookToken(r)
	if hash == "" || token == "" {
		return false
	}
	actual := webhookTokenHash(token)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(hash)) == 1
}

func (s *Service) authorizeWebhookRequest(c echo.Context, topic string) (WebhookSource, int64, bool, error) {
	defaultLimit := s.config.WebhookBodyLimitBytes
	sources, err := s.configDB.ListEnabledWebhookSourcesForTopic(c.Request().Context(), topic)
	if err != nil {
		return WebhookSource{}, 0, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook sources"})
	}
	if len(sources) == 0 {
		return WebhookSource{}, 0, true, c.JSON(http.StatusNotFound, map[string]string{"error": "webhook source not found"})
	}
	matches := make([]WebhookSource, 0, 1)
	for _, source := range sources {
		if source.TokenHash != "" && validWebhookTokenHash(c.Request(), source.TokenHash) {
			matches = append(matches, source)
		}
	}
	if len(matches) == 0 {
		return WebhookSource{}, 0, true, c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid webhook source token"})
	}
	if len(matches) > 1 {
		return WebhookSource{}, 0, true, c.JSON(http.StatusConflict, map[string]string{"error": "webhook source is ambiguous"})
	}
	limit := matches[0].BodyLimitBytes
	if limit <= 0 {
		limit = defaultLimit
	}
	return matches[0], limit, false, nil
}

var errWebhookBodyTooLarge = errors.New("webhook body too large")

func readWebhookBody(r *http.Request, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = 1 << 20
	}
	reader := io.LimitReader(r.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errWebhookBodyTooLarge
	}
	return data, nil
}

func requestContentTypeIsJSON(r *http.Request) bool {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func decodeWebhookJSONObject(raw []byte) (map[string]any, string, error) {
	var body map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&body); err != nil {
		return nil, "", fmt.Errorf("body must be valid JSON")
	}
	if body == nil {
		return nil, "", fmt.Errorf("body must be a JSON object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, "", fmt.Errorf("body must contain one JSON document")
	}
	compact, err := marshalJSONCompact(body)
	if err != nil {
		return nil, "", err
	}
	return body, compact, nil
}

func existingWebhookBodyHash(payloadJSON string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return ""
	}
	body, ok := payload["body"]
	if !ok {
		return ""
	}
	compact, err := marshalJSONCompact(body)
	if err != nil {
		return ""
	}
	return topicEventPayloadSHA256(compact)
}

func validateExternalWebhookTopic(topic string) error {
	if err := validateTopicEventName(topic); err != nil {
		return err
	}
	if !strings.HasPrefix(topic, "webhook.") {
		return fmt.Errorf("webhook topic must use webhook.* prefix")
	}
	return nil
}

func providerFromWebhookTopic(topic string) string {
	parts := strings.Split(topic, ".")
	if len(parts) >= 2 && parts[0] == "webhook" {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func intentFromWebhookBody(body map[string]any) string {
	if value, ok := body["intent"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "notification"
}

func extractCorrelationID(r *http.Request, body map[string]any) string {
	if value := strings.TrimSpace(r.Header.Get("X-Correlation-ID")); value != "" {
		return value
	}
	if value, ok := body["correlation_id"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	if value, ok := body["correlationId"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return ""
}

func extractIdempotencyKey(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("Idempotency-Key")); value != "" {
		return value
	}
	if value := extractDeliveryID(r); value != "" {
		return value
	}
	return strings.TrimSpace(r.Header.Get("X-Request-ID"))
}

func extractDeliveryID(r *http.Request) string {
	for _, key := range []string{"X-GitHub-Delivery", "X-Gitlab-Event-UUID", "X-Request-ID"} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func sanitizeWebhookHeaders(headers http.Header) map[string]string {
	allowed := map[string]struct{}{
		"content-type":        {},
		"user-agent":          {},
		"x-request-id":        {},
		"x-correlation-id":    {},
		"x-github-event":      {},
		"x-github-delivery":   {},
		"x-gitlab-event":      {},
		"x-hub-signature-256": {},
	}
	out := make(map[string]string)
	for key, values := range headers {
		lower := strings.ToLower(strings.TrimSpace(key))
		if _, ok := allowed[lower]; !ok || len(values) == 0 {
			continue
		}
		out[lower] = strings.Join(values, ",")
	}
	return out
}

func buildWebhookPayload(c echo.Context, eventID string, sequence int64, topic, correlationID, idempotencyKey string, source WebhookSource, body map[string]any) map[string]any {
	r := c.Request()
	payload := map[string]any{
		"eventId":        eventID,
		"sequence":       sequence,
		"source":         TopicEventSourceWebhook,
		"provider":       firstNonEmpty(source.Provider, providerFromWebhookTopic(topic)),
		"intent":         intentFromWebhookBody(body),
		"method":         r.Method,
		"path":           r.URL.Path,
		"topic":          topic,
		"correlationId":  correlationID,
		"idempotencyKey": idempotencyKey,
		"deliveryId":     extractDeliveryID(r),
		"remoteAddr":     r.RemoteAddr,
		"headers":        sanitizeWebhookHeaders(r.Header),
		"query":          queryValuesToMap(r),
		"body":           body,
	}
	if source.ID != "" {
		payload["webhookSourceId"] = source.ID
	}
	return payload
}

func queryValuesToMap(r *http.Request) map[string]any {
	out := make(map[string]any)
	for key, values := range r.URL.Query() {
		if len(values) == 1 {
			out[key] = values[0]
			continue
		}
		out[key] = append([]string(nil), values...)
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

func toTopicEventJSON(item TopicEventRecord) topicEventJSON {
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

func newUUIDString() string {
	return uuid.NewString()
}
