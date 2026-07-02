package events

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	TopicEventSourceWebhook = "webhook"
	TopicEventSourceLoader  = "loader"
	TopicEventSourceSystem  = "system"

	TopicEventDispatchPending        = "pending"
	TopicEventDispatchPublishing     = "publishing_to_bus"
	TopicEventDispatchPublishedToBus = "published_to_bus"
	TopicEventDispatchNoSubscriber   = "no_subscriber"
	TopicEventDispatchRetrying       = "retrying"
	TopicEventDispatchDeadLetter     = "dead_letter"

	EventDeliveryStatusMatched      = "matched"
	EventDeliveryStatusRunStarted   = "run_started"
	EventDeliveryStatusRunSucceeded = "run_succeeded"
	EventDeliveryStatusRunFailed    = "run_failed"
	EventDeliveryStatusSkipped      = "skipped"
)

var topicEventNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

type TopicEventRecord struct {
	ID              string    `json:"event_id"`
	Sequence        int64     `json:"sequence"`
	Topic           string    `json:"topic"`
	Source          string    `json:"source"`
	Provider        string    `json:"provider,omitempty"`
	Intent          string    `json:"intent,omitempty"`
	CorrelationID   string    `json:"correlation_id"`
	IdempotencyKey  string    `json:"idempotency_key,omitempty"`
	DeliveryID      string    `json:"delivery_id,omitempty"`
	PayloadHash     string    `json:"payload_hash"`
	PayloadJSON     string    `json:"payload_json"`
	DispatchStatus  string    `json:"dispatch_status"`
	ParentEventID   string    `json:"parent_event_id,omitempty"`
	PublisherType   string    `json:"publisher_type,omitempty"`
	PublisherID     string    `json:"publisher_id,omitempty"`
	PublisherRunID  string    `json:"publisher_run_id,omitempty"`
	ReplayOfEventID string    `json:"replay_of_event_id,omitempty"`
	ClaimID         string    `json:"claim_id,omitempty"`
	ClaimUntil      time.Time `json:"claim_until,omitempty"`
	AttemptCount    int       `json:"attempt_count,omitempty"`
	NextAttemptAt   time.Time `json:"next_attempt_at,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	DeadLetterAt    time.Time `json:"dead_letter_at,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	DispatchedAt    time.Time `json:"dispatched_at,omitempty"`
}

type TopicEventFilter struct {
	EventID        string
	Topic          string
	CorrelationID  string
	AfterSequence  int64
	Limit          int
	DispatchStatus string
}

type WebhookSource struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Provider        string    `json:"provider"`
	TopicPrefix     string    `json:"topic_prefix"`
	TokenHash       string    `json:"token_hash,omitempty"`
	SignatureType   string    `json:"signature_type,omitempty"`
	SignatureSecret string    `json:"signature_secret,omitempty"`
	BodyLimitBytes  int64     `json:"body_limit_bytes,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type EventDelivery struct {
	EventID   string    `json:"event_id"`
	LoaderID  string    `json:"loader_id"`
	TriggerID string    `json:"trigger_id"`
	RunID     string    `json:"run_id,omitempty"`
	Status    string    `json:"status"`
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type EventSessionLink struct {
	EventID       string    `json:"event_id"`
	SessionID     string    `json:"session_id"`
	Relation      string    `json:"relation"`
	LoaderID      string    `json:"loader_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	TriggerID     string    `json:"trigger_id,omitempty"`
	LoaderEventID string    `json:"loader_event_id,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type EventSessionTraceItem struct {
	SessionID     string    `json:"session_id"`
	Relation      string    `json:"relation"`
	LoaderID      string    `json:"loader_id,omitempty"`
	RunID         string    `json:"run_id,omitempty"`
	TriggerID     string    `json:"trigger_id,omitempty"`
	LoaderEventID string    `json:"loader_event_id,omitempty"`
	EventID       string    `json:"event_id"`
	CreatedAt     time.Time `json:"created_at"`
}

func ValidateTopicEventName(topic string) error {
	topic = strings.TrimSpace(topic)
	if topic == "" {
		return fmt.Errorf("topic is required")
	}
	if len(topic) > 128 {
		return fmt.Errorf("topic is too long")
	}
	if !topicEventNamePattern.MatchString(topic) {
		return fmt.Errorf("topic contains invalid characters")
	}
	return nil
}

func NormalizeTopicEventSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case TopicEventSourceWebhook:
		return TopicEventSourceWebhook
	case TopicEventSourceLoader:
		return TopicEventSourceLoader
	case TopicEventSourceSystem:
		return TopicEventSourceSystem
	default:
		return ""
	}
}

func NormalizeTopicEventDispatchStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", TopicEventDispatchPending:
		return TopicEventDispatchPending
	case TopicEventDispatchPublishing:
		return TopicEventDispatchPublishing
	case TopicEventDispatchPublishedToBus:
		return TopicEventDispatchPublishedToBus
	case TopicEventDispatchNoSubscriber:
		return TopicEventDispatchNoSubscriber
	case TopicEventDispatchRetrying:
		return TopicEventDispatchRetrying
	case TopicEventDispatchDeadLetter:
		return TopicEventDispatchDeadLetter
	default:
		return ""
	}
}

func NormalizeEventDeliveryStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case EventDeliveryStatusMatched:
		return EventDeliveryStatusMatched
	case EventDeliveryStatusRunStarted:
		return EventDeliveryStatusRunStarted
	case EventDeliveryStatusRunSucceeded:
		return EventDeliveryStatusRunSucceeded
	case EventDeliveryStatusRunFailed:
		return EventDeliveryStatusRunFailed
	case EventDeliveryStatusSkipped:
		return EventDeliveryStatusSkipped
	default:
		return ""
	}
}

func TopicEventPayloadSHA256(payloadJSON string) string {
	sum := sha256.Sum256([]byte(payloadJSON))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func NormalizeJSONDocument(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, []byte(raw)); err != nil {
		return "", fmt.Errorf("normalize json document: %w", err)
	}
	return compact.String(), nil
}

func NormalizeTopicEventRecord(item TopicEventRecord, assignID bool) (TopicEventRecord, error) {
	item.ID = strings.TrimSpace(item.ID)
	if assignID && item.ID == "" {
		item.ID = "evt_" + uuid.NewString()
	}
	if item.ID == "" {
		return TopicEventRecord{}, fmt.Errorf("event id is required")
	}
	item.Topic = strings.TrimSpace(item.Topic)
	if err := ValidateTopicEventName(item.Topic); err != nil {
		return TopicEventRecord{}, err
	}
	item.Source = NormalizeTopicEventSource(item.Source)
	if item.Source == "" {
		return TopicEventRecord{}, fmt.Errorf("event source is required")
	}
	item.DispatchStatus = NormalizeTopicEventDispatchStatus(item.DispatchStatus)
	if item.DispatchStatus == "" {
		return TopicEventRecord{}, fmt.Errorf("event dispatch status is invalid")
	}
	item.Provider = strings.TrimSpace(item.Provider)
	item.Intent = strings.TrimSpace(item.Intent)
	item.CorrelationID = strings.TrimSpace(item.CorrelationID)
	if item.CorrelationID == "" {
		item.CorrelationID = item.ID
	}
	item.IdempotencyKey = strings.TrimSpace(item.IdempotencyKey)
	item.DeliveryID = strings.TrimSpace(item.DeliveryID)
	item.PayloadJSON = strings.TrimSpace(item.PayloadJSON)
	if item.PayloadJSON == "" {
		item.PayloadJSON = "{}"
	}
	if _, err := NormalizeJSONDocument(item.PayloadJSON); err != nil {
		return TopicEventRecord{}, err
	}
	item.PayloadHash = strings.TrimSpace(item.PayloadHash)
	if item.PayloadHash == "" {
		item.PayloadHash = TopicEventPayloadSHA256(item.PayloadJSON)
	}
	item.ParentEventID = strings.TrimSpace(item.ParentEventID)
	item.PublisherType = strings.TrimSpace(item.PublisherType)
	item.PublisherID = strings.TrimSpace(item.PublisherID)
	item.PublisherRunID = strings.TrimSpace(item.PublisherRunID)
	item.ReplayOfEventID = strings.TrimSpace(item.ReplayOfEventID)
	item.ClaimID = strings.TrimSpace(item.ClaimID)
	if !item.ClaimUntil.IsZero() {
		item.ClaimUntil = item.ClaimUntil.UTC()
	}
	if item.AttemptCount < 0 {
		item.AttemptCount = 0
	}
	if !item.NextAttemptAt.IsZero() {
		item.NextAttemptAt = item.NextAttemptAt.UTC()
	}
	item.LastError = strings.TrimSpace(item.LastError)
	if !item.DeadLetterAt.IsZero() {
		item.DeadLetterAt = item.DeadLetterAt.UTC()
	}
	if item.CreatedAt.IsZero() {
		item.CreatedAt = time.Now().UTC()
	} else {
		item.CreatedAt = item.CreatedAt.UTC()
	}
	if !item.DispatchedAt.IsZero() {
		item.DispatchedAt = item.DispatchedAt.UTC()
	}
	return item, nil
}

func WebhookSourceTopicMatches(topic, topicPrefix string) bool {
	topic = strings.TrimSpace(topic)
	topicPrefix = strings.TrimSpace(topicPrefix)
	if topic == "" || topicPrefix == "" {
		return false
	}
	if strings.HasPrefix(topic, topicPrefix) {
		return true
	}
	return strings.HasSuffix(topicPrefix, ".") && topic == strings.TrimSuffix(topicPrefix, ".")
}
