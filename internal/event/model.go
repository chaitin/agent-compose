package event

import (
	eventtypes "agent-compose/internal/eventtypes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
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

type TopicEventRecord = eventtypes.TopicEventRecord

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

func validateTopicEventName(topic string) error {
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

func ValidateTopicEventName(topic string) error {
	return validateTopicEventName(topic)
}

func normalizeTopicEventSource(source string) string {
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

func NormalizeTopicEventSource(source string) string {
	return normalizeTopicEventSource(source)
}

func normalizeTopicEventDispatchStatus(status string) string {
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

func NormalizeTopicEventDispatchStatus(status string) string {
	return normalizeTopicEventDispatchStatus(status)
}

func normalizeEventDeliveryStatus(status string) string {
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

func NormalizeEventDeliveryStatus(status string) string {
	return normalizeEventDeliveryStatus(status)
}

func topicEventPayloadSHA256(payloadJSON string) string {
	sum := sha256.Sum256([]byte(payloadJSON))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TopicEventPayloadSHA256(payloadJSON string) string {
	return topicEventPayloadSHA256(payloadJSON)
}
