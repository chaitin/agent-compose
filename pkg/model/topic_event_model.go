package model

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	WebhookParentEventIDHeader = "X-Agent-Compose-Parent-Event-ID"

	TopicEventSourceWebhook   = "webhook"
	TopicEventSourceScheduler = "scheduler"
	TopicEventSourceSystem    = "system"

	legacyTopicEventSourceLoader = "loader"

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
var httpHeaderNamePattern = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

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

// TopicEventIdempotencyConflictError reports a duplicate topic and idempotency
// key while carrying the event record that already owns them.
type TopicEventIdempotencyConflictError struct {
	Existing TopicEventRecord
}

func (e *TopicEventIdempotencyConflictError) Error() string {
	if e == nil || strings.TrimSpace(e.Existing.Topic) == "" {
		return "event idempotency conflict"
	}
	return fmt.Sprintf("event idempotency conflict for topic %q", e.Existing.Topic)
}

func (e *TopicEventIdempotencyConflictError) Unwrap() error {
	return ErrConflict
}

type TopicEventFilter struct {
	EventID        string
	Source         string
	Topic          string
	CorrelationID  string
	AfterSequence  int64
	Offset         int
	Limit          int
	SequenceAsc    bool
	DispatchStatus string
}

type EventSummary struct {
	ID             string
	Sequence       int64
	Topic          string
	Source         string
	Provider       string
	Intent         string
	CorrelationID  string
	DeliveryID     string
	DispatchStatus string
	ParentEventID  string
	PublisherType  string
	PublisherID    string
	PublisherRunID string
	CreatedAt      time.Time
	DispatchedAt   time.Time
}

type EventTopicSummary struct {
	Topic         string
	EventCount    int
	LatestEventAt time.Time
}

type EventSchedulerSummary struct {
	ID        string
	ProjectID string
	AgentName string
	Name      string
}

type EventSchedulerRunSummary struct {
	ID          string
	Status      string
	StartedAt   time.Time
	CompletedAt time.Time
	DurationMs  int64
	Error       string
}

type EventSchedulerEventSummary struct {
	ID              string
	Type            string
	Level           string
	Message         string
	LinkedSandboxID string
	CreatedAt       time.Time
}

type EventRunTrace struct {
	Delivery  EventDelivery
	Scheduler *EventSchedulerSummary
	Run       *EventSchedulerRunSummary
	Events    []EventSchedulerEventSummary
}

type EventTrace struct {
	Event                EventSummary
	Runs                 []EventRunTrace
	SandboxLinks         []EventSandboxTraceItem
	DescendantsTruncated bool
}

type WebhookSource struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Enabled         bool      `json:"enabled"`
	Provider        string    `json:"provider"`
	TopicPrefix     string    `json:"topic_prefix"`
	TokenHash       string    `json:"token_hash,omitempty"`
	TokenHeader     string    `json:"token_header,omitempty"`
	SignatureType   string    `json:"signature_type,omitempty"`
	SignatureSecret string    `json:"signature_secret,omitempty"`
	BodyLimitBytes  int64     `json:"body_limit_bytes,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

type EventDelivery struct {
	EventID     string    `json:"event_id"`
	SchedulerID string    `json:"scheduler_id"`
	TriggerID   string    `json:"trigger_id"`
	RunID       string    `json:"run_id,omitempty"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type EventSandboxLink struct {
	EventID          string    `json:"event_id"`
	SandboxID        string    `json:"sandbox_id"`
	Relation         string    `json:"relation"`
	SchedulerID      string    `json:"scheduler_id,omitempty"`
	RunID            string    `json:"run_id,omitempty"`
	TriggerID        string    `json:"trigger_id,omitempty"`
	SchedulerEventID string    `json:"scheduler_event_id,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

type EventSandboxTraceItem struct {
	SandboxID        string    `json:"sandbox_id"`
	Relation         string    `json:"relation"`
	SchedulerID      string    `json:"scheduler_id,omitempty"`
	RunID            string    `json:"run_id,omitempty"`
	TriggerID        string    `json:"trigger_id,omitempty"`
	SchedulerEventID string    `json:"scheduler_event_id,omitempty"`
	EventID          string    `json:"event_id"`
	CreatedAt        time.Time `json:"created_at"`
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

func NormalizeHTTPHeaderName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", nil
	}
	if !httpHeaderNamePattern.MatchString(name) {
		return "", fmt.Errorf("header name contains invalid characters")
	}
	return name, nil
}

func NormalizeTopicEventSource(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case TopicEventSourceWebhook:
		return TopicEventSourceWebhook
	case TopicEventSourceScheduler, legacyTopicEventSourceLoader:
		return TopicEventSourceScheduler
	case TopicEventSourceSystem:
		return TopicEventSourceSystem
	default:
		return ""
	}
}

// TopicEventSourceFilterValues returns every persisted source value that is
// equivalent to source. Loader was renamed to scheduler, but upgraded databases
// intentionally retain loader-era event rows, so scheduler filters must match
// both stored values.
func TopicEventSourceFilterValues(source string) []string {
	normalized := NormalizeTopicEventSource(source)
	switch normalized {
	case "":
		return nil
	case TopicEventSourceScheduler:
		return []string{TopicEventSourceScheduler, legacyTopicEventSourceLoader}
	default:
		return []string{normalized}
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
