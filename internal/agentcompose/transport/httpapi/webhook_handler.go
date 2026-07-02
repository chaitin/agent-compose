package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/labstack/echo/v4"

	eventsdomain "agent-compose/internal/agentcompose/events"
)

type WebhookEventStore interface {
	ListEnabledWebhookSourcesForTopic(ctx context.Context, topic string) ([]eventsdomain.WebhookSource, error)
	FindEventByIdempotencyKey(ctx context.Context, topic, idempotencyKey string) (eventsdomain.TopicEventRecord, bool, error)
	CreateEvent(ctx context.Context, item eventsdomain.TopicEventRecord) (eventsdomain.TopicEventRecord, error)
	UpdateEventPayload(ctx context.Context, eventID, payloadJSON string) error
}

type WebhookReceiver struct {
	Store              WebhookEventStore
	DefaultBodyLimit   int64
	NewID              func() string
	MarshalJSONCompact func(value any) (string, error)
}

type WebhookAcceptedResponse struct {
	Accepted      bool   `json:"accepted"`
	Topic         string `json:"topic"`
	EventID       string `json:"event_id"`
	Sequence      int64  `json:"sequence"`
	CorrelationID string `json:"correlation_id"`
}

func (h WebhookReceiver) HandleWebhook(c echo.Context) error {
	topic := strings.TrimSpace(c.Param("topic"))
	if err := validateExternalWebhookTopic(topic); err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	source, bodyLimit, handled, err := h.authorizeWebhookRequest(c, topic)
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
	body, compactBody, err := h.decodeWebhookJSONObject(rawBody)
	if err != nil {
		return c.JSON(http.StatusBadRequest, map[string]string{"error": err.Error()})
	}
	idempotencyKey := extractIdempotencyKey(c.Request())
	if existing, ok, err := h.Store.FindEventByIdempotencyKey(c.Request().Context(), topic, idempotencyKey); err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook event"})
	} else if ok {
		if existingWebhookBodyHash(existing.PayloadJSON, h.compactJSON) != eventsdomain.TopicEventPayloadSHA256(compactBody) {
			return c.JSON(http.StatusConflict, map[string]string{"error": "idempotency key conflicts with existing payload"})
		}
		return c.JSON(http.StatusAccepted, WebhookAcceptedResponse{
			Accepted:      true,
			Topic:         existing.Topic,
			EventID:       existing.ID,
			Sequence:      existing.Sequence,
			CorrelationID: existing.CorrelationID,
		})
	}

	eventID := h.newEventID()
	correlationID := extractCorrelationID(c.Request(), body)
	if correlationID == "" {
		correlationID = eventID
	}
	payload := buildWebhookPayload(c, eventID, 0, topic, correlationID, idempotencyKey, source, body)
	payloadJSON, err := h.compactJSON(payload)
	if err != nil {
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode webhook payload"})
	}
	payloadHash := eventsdomain.TopicEventPayloadSHA256(payloadJSON)
	created, err := h.Store.CreateEvent(c.Request().Context(), eventsdomain.TopicEventRecord{
		ID:             eventID,
		Topic:          topic,
		Source:         eventsdomain.TopicEventSourceWebhook,
		Provider:       firstNonEmpty(source.Provider, ProviderFromWebhookTopic(topic)),
		Intent:         intentFromWebhookBody(body),
		CorrelationID:  correlationID,
		IdempotencyKey: idempotencyKey,
		DeliveryID:     extractDeliveryID(c.Request()),
		PayloadHash:    payloadHash,
		PayloadJSON:    payloadJSON,
		DispatchStatus: eventsdomain.TopicEventDispatchPending,
		PublisherType:  eventsdomain.TopicEventSourceWebhook,
	})
	if err != nil {
		if strings.Contains(err.Error(), "idempotency conflict") {
			return c.JSON(http.StatusConflict, map[string]string{"error": "idempotency key conflicts with existing payload"})
		}
		return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store webhook event"})
	}
	if created.ID != eventID {
		return c.JSON(http.StatusAccepted, WebhookAcceptedResponse{
			Accepted:      true,
			Topic:         created.Topic,
			EventID:       created.ID,
			Sequence:      created.Sequence,
			CorrelationID: created.CorrelationID,
		})
	}
	if created.Sequence != 0 {
		payload = buildWebhookPayload(c, created.ID, created.Sequence, topic, created.CorrelationID, created.IdempotencyKey, source, body)
		payloadJSON, err = h.compactJSON(payload)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to encode webhook payload"})
		}
		if err := h.Store.UpdateEventPayload(c.Request().Context(), created.ID, payloadJSON); err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to store webhook event payload"})
		}
	}
	return c.JSON(http.StatusAccepted, WebhookAcceptedResponse{
		Accepted:      true,
		Topic:         created.Topic,
		EventID:       created.ID,
		Sequence:      created.Sequence,
		CorrelationID: created.CorrelationID,
	})
}

func (h WebhookReceiver) authorizeWebhookRequest(c echo.Context, topic string) (eventsdomain.WebhookSource, int64, bool, error) {
	if h.Store == nil {
		return eventsdomain.WebhookSource{}, 0, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "webhook store is not configured"})
	}
	sources, err := h.Store.ListEnabledWebhookSourcesForTopic(c.Request().Context(), topic)
	if err != nil {
		return eventsdomain.WebhookSource{}, 0, true, c.JSON(http.StatusInternalServerError, map[string]string{"error": "failed to load webhook sources"})
	}
	if len(sources) == 0 {
		return eventsdomain.WebhookSource{}, 0, true, c.JSON(http.StatusNotFound, map[string]string{"error": "webhook source not found"})
	}
	matches := make([]eventsdomain.WebhookSource, 0, 1)
	for _, source := range sources {
		if source.TokenHash != "" && ValidWebhookTokenHash(c.Request(), source.TokenHash) {
			matches = append(matches, source)
		}
	}
	if len(matches) == 0 {
		return eventsdomain.WebhookSource{}, 0, true, c.JSON(http.StatusUnauthorized, map[string]string{"error": "invalid webhook source token"})
	}
	if len(matches) > 1 {
		return eventsdomain.WebhookSource{}, 0, true, c.JSON(http.StatusConflict, map[string]string{"error": "webhook source is ambiguous"})
	}
	limit := matches[0].BodyLimitBytes
	if limit <= 0 {
		limit = h.DefaultBodyLimit
	}
	return matches[0], limit, false, nil
}

func (h WebhookReceiver) decodeWebhookJSONObject(raw []byte) (map[string]any, string, error) {
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
	compact, err := h.compactJSON(body)
	if err != nil {
		return nil, "", err
	}
	return body, compact, nil
}

func (h WebhookReceiver) compactJSON(value any) (string, error) {
	if h.MarshalJSONCompact != nil {
		return h.MarshalJSONCompact(value)
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (h WebhookReceiver) newEventID() string {
	if h.NewID != nil {
		return h.NewID()
	}
	return "evt_" + uuid.NewString()
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

func validateExternalWebhookTopic(topic string) error {
	if err := eventsdomain.ValidateTopicEventName(topic); err != nil {
		return err
	}
	if !strings.HasPrefix(topic, "webhook.") {
		return fmt.Errorf("webhook topic must use webhook.* prefix")
	}
	return nil
}

func requestContentTypeIsJSON(r *http.Request) bool {
	contentType := strings.TrimSpace(r.Header.Get("Content-Type"))
	if contentType == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && strings.EqualFold(mediaType, "application/json")
}

func PresentedWebhookToken(r *http.Request) string {
	presented := ""
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		presented = strings.TrimSpace(auth[len("bearer "):])
	}
	if presented == "" {
		presented = strings.TrimSpace(r.Header.Get("X-WEBHOOK-TOKEN"))
	}
	return presented
}

func WebhookTokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ValidWebhookTokenHash(r *http.Request, hash string) bool {
	hash = strings.TrimSpace(hash)
	token := PresentedWebhookToken(r)
	if hash == "" || token == "" {
		return false
	}
	actual := WebhookTokenHash(token)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(hash)) == 1
}

func ProviderFromWebhookTopic(topic string) string {
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

func buildWebhookPayload(c echo.Context, eventID string, sequence int64, topic, correlationID, idempotencyKey string, source eventsdomain.WebhookSource, body map[string]any) map[string]any {
	r := c.Request()
	payload := map[string]any{
		"eventId":        eventID,
		"sequence":       sequence,
		"source":         eventsdomain.TopicEventSourceWebhook,
		"provider":       firstNonEmpty(source.Provider, ProviderFromWebhookTopic(topic)),
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

func existingWebhookBodyHash(payloadJSON string, compact func(any) (string, error)) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		return ""
	}
	body, ok := payload["body"]
	if !ok {
		return ""
	}
	compactBody, err := compact(body)
	if err != nil {
		return ""
	}
	return eventsdomain.TopicEventPayloadSHA256(compactBody)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
