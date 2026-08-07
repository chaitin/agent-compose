package webhooks

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"

	domain "agent-compose/pkg/model"
)

const defaultWebhookBodyLimit int64 = 1 << 20

const parentEventIDHeader = domain.WebhookParentEventIDHeader

func PresentedToken(r *http.Request, tokenHeader ...string) string {
	headerName := ""
	if len(tokenHeader) > 0 {
		headerName = strings.TrimSpace(tokenHeader[0])
	}
	if headerName != "" {
		presented := strings.TrimSpace(r.Header.Get(headerName))
		if strings.EqualFold(headerName, "Authorization") && strings.HasPrefix(strings.ToLower(presented), "bearer ") {
			return strings.TrimSpace(presented[len("bearer "):])
		}
		return presented
	}

	presented := ""
	if auth := strings.TrimSpace(r.Header.Get("Authorization")); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		presented = strings.TrimSpace(auth[len("bearer "):])
	}
	if presented == "" {
		presented = strings.TrimSpace(r.Header.Get("X-WEBHOOK-TOKEN"))
	}
	return presented
}

func TokenHash(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func ValidTokenHash(r *http.Request, hash string, tokenHeader ...string) bool {
	hash = strings.TrimSpace(hash)
	token := PresentedToken(r, tokenHeader...)
	if hash == "" || token == "" {
		return false
	}
	actual := TokenHash(token)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(hash)) == 1
}

func ReadBody(r *http.Request, limit int64) ([]byte, error) {
	if limit <= 0 {
		limit = defaultWebhookBodyLimit
	}
	reader := io.LimitReader(r.Body, limit+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, domain.ErrBodyTooLarge
	}
	return data, nil
}

func ValidateExternalTopic(topic string) error {
	if err := domain.ValidateTopicEventName(topic); err != nil {
		return err
	}
	if !strings.HasPrefix(topic, "webhook.") {
		return fmt.Errorf("webhook topic must use webhook.* prefix")
	}
	return nil
}

func ProviderFromTopic(topic string) string {
	parts := strings.Split(topic, ".")
	if len(parts) >= 2 && parts[0] == "webhook" {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func IntentFromBody(body map[string]any) string {
	if value, ok := body["intent"].(string); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return "notification"
}

func ExtractCorrelationID(r *http.Request, body map[string]any) string {
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

func ExtractParentEventID(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(parentEventIDHeader))
}

func ExtractIdempotencyKey(r *http.Request) string {
	if value := strings.TrimSpace(r.Header.Get("Idempotency-Key")); value != "" {
		return value
	}
	if value := ExtractDeliveryID(r); value != "" {
		return value
	}
	return strings.TrimSpace(r.Header.Get("X-Request-ID"))
}

func ExtractDeliveryID(r *http.Request) string {
	for _, key := range []string{"X-GitHub-Delivery", "X-Gitlab-Event-UUID", "X-Request-ID"} {
		if value := strings.TrimSpace(r.Header.Get(key)); value != "" {
			return value
		}
	}
	return ""
}

func SanitizeHeaders(headers http.Header) map[string]string {
	allowed := map[string]struct{}{
		"content-type":                    {},
		"user-agent":                      {},
		"x-request-id":                    {},
		"x-correlation-id":                {},
		"x-agent-compose-parent-event-id": {},
		"x-github-event":                  {},
		"x-github-delivery":               {},
		"x-gitlab-event":                  {},
		"x-hub-signature-256":             {},
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

func BuildPayload(r *http.Request, eventID string, sequence int64, topic, correlationID, idempotencyKey string, source domain.WebhookSource, body map[string]any) map[string]any {
	headers := SanitizeHeaders(r.Header)
	delete(headers, strings.ToLower(strings.TrimSpace(source.TokenHeader)))
	payload := map[string]any{
		"eventId":        eventID,
		"sequence":       sequence,
		"source":         domain.TopicEventSourceWebhook,
		"provider":       firstNonEmpty(source.Provider, ProviderFromTopic(topic)),
		"intent":         IntentFromBody(body),
		"method":         r.Method,
		"path":           r.URL.Path,
		"topic":          topic,
		"correlationId":  correlationID,
		"idempotencyKey": idempotencyKey,
		"deliveryId":     ExtractDeliveryID(r),
		"remoteAddr":     r.RemoteAddr,
		"headers":        headers,
		"query":          QueryValuesToMap(r),
		"body":           body,
	}
	if source.ID != "" {
		payload["webhookSourceId"] = source.ID
	}
	return payload
}

func QueryValuesToMap(r *http.Request) map[string]any {
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
