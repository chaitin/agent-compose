package webhooks

type AcceptedResponse struct {
	Accepted      bool   `json:"accepted"`
	Topic         string `json:"topic"`
	EventID       string `json:"event_id"`
	Sequence      int64  `json:"sequence"`
	CorrelationID string `json:"correlation_id"`
}

type TopicEventResponse struct {
	Event TopicEventJSON `json:"event"`
}

type TopicEventListResponse struct {
	Items             []TopicEventJSON `json:"items"`
	NextAfterSequence int64            `json:"next_after_sequence"`
}

type EventSessionsResponse struct {
	EventID       string             `json:"event_id"`
	CorrelationID string             `json:"correlation_id"`
	Sessions      []EventSessionJSON `json:"sessions"`
}

type EventRunsResponse struct {
	EventID       string         `json:"event_id"`
	CorrelationID string         `json:"correlation_id"`
	Runs          []EventRunJSON `json:"runs"`
}

type EventRunJSON struct {
	EventID   string `json:"event_id"`
	LoaderID  string `json:"loader_id"`
	RunID     string `json:"run_id,omitempty"`
	TriggerID string `json:"trigger_id"`
	Status    string `json:"status"`
	Error     string `json:"error,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type EventSessionJSON struct {
	SessionID     string `json:"session_id"`
	Relation      string `json:"relation"`
	LoaderID      string `json:"loader_id,omitempty"`
	RunID         string `json:"run_id,omitempty"`
	TriggerID     string `json:"trigger_id,omitempty"`
	LoaderEventID string `json:"loader_event_id,omitempty"`
	EventID       string `json:"event_id"`
	CreatedAt     string `json:"created_at"`
}

type SourceRequest struct {
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

type SourceJSON struct {
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

type SourceListResponse struct {
	Items []SourceJSON `json:"items"`
}

type SourceResponse struct {
	Source SourceJSON `json:"source"`
}

type TopicEventJSON struct {
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
