package eventtypes

import (
	"context"
	"time"
)

type LoaderTopicEvent struct {
	EventID         string                                         `json:"event_id,omitempty"`
	Topic           string                                         `json:"topic"`
	Source          string                                         `json:"source,omitempty"`
	Provider        string                                         `json:"provider,omitempty"`
	Payload         map[string]any                                 `json:"payload,omitempty"`
	CreatedAt       time.Time                                      `json:"created_at"`
	Ack             func(context.Context) error                    `json:"-"`
	NoSubscriberAck func(context.Context) error                    `json:"-"`
	Retry           func(context.Context, string, time.Time) error `json:"-"`
	Release         func()                                         `json:"-"`
}

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
