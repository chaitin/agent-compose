package webhooks

const idempotencyPayloadMismatchCode = "idempotency_payload_mismatch"

type AcceptedResponse struct {
	Accepted      bool   `json:"accepted"`
	Topic         string `json:"topic"`
	EventID       string `json:"event_id"`
	Sequence      int64  `json:"sequence"`
	CorrelationID string `json:"correlation_id"`
}

type idempotencyConflictResponse struct {
	Code          string           `json:"code"`
	Error         string           `json:"error"`
	ExistingEvent AcceptedResponse `json:"existing_event"`
}

type TopicEventResponse struct {
	Event TopicEventJSON `json:"event"`
}

type TopicEventListResponse struct {
	Items             []TopicEventJSON `json:"items"`
	Total             int              `json:"total"`
	NextAfterSequence int64            `json:"next_after_sequence,omitempty"`
}

type EventSummaryListResponse struct {
	Items             []EventSummaryJSON `json:"items"`
	Total             int                `json:"total"`
	NextAfterSequence int64              `json:"next_after_sequence,omitempty"`
}

type EventTopicListResponse struct {
	Items []EventTopicJSON `json:"items"`
	Total int              `json:"total"`
}

type EventTopicJSON struct {
	Topic         string `json:"topic"`
	EventCount    int    `json:"event_count"`
	LatestEventAt string `json:"latest_event_at"`
}

type EventSummaryJSON struct {
	EventID        string `json:"event_id"`
	Sequence       int64  `json:"sequence"`
	Topic          string `json:"topic"`
	Source         string `json:"source"`
	Provider       string `json:"provider,omitempty"`
	Intent         string `json:"intent,omitempty"`
	CorrelationID  string `json:"correlation_id"`
	DeliveryID     string `json:"delivery_id,omitempty"`
	DispatchStatus string `json:"dispatch_status"`
	ParentEventID  string `json:"parent_event_id,omitempty"`
	PublisherType  string `json:"publisher_type,omitempty"`
	PublisherID    string `json:"publisher_id,omitempty"`
	PublisherRunID string `json:"publisher_run_id,omitempty"`
	CreatedAt      string `json:"created_at"`
	DispatchedAt   string `json:"dispatched_at,omitempty"`
}

type EventTraceResponse struct {
	SandboxSummariesIncomplete bool                    `json:"sandbox_summaries_incomplete,omitempty"`
	Event                      EventSummaryJSON        `json:"event"`
	Runs                       []EventRunTraceJSON     `json:"runs"`
	Sandboxes                  []EventTraceSandboxJSON `json:"sandboxes"`
	DescendantsTruncated       bool                    `json:"descendants_truncated"`
}

type EventRunTraceJSON struct {
	Delivery  EventRunJSON              `json:"delivery"`
	Scheduler *EventSchedulerJSON       `json:"scheduler,omitempty"`
	Run       *EventSchedulerRunJSON    `json:"run,omitempty"`
	Events    []EventSchedulerEventJSON `json:"events"`
}

type EventSchedulerJSON struct {
	SchedulerID string `json:"scheduler_id"`
	ProjectID   string `json:"project_id"`
	AgentName   string `json:"agent_name"`
	Name        string `json:"name"`
}

type EventSchedulerRunJSON struct {
	RunID       string `json:"run_id"`
	Status      string `json:"status"`
	StartedAt   string `json:"started_at"`
	CompletedAt string `json:"completed_at,omitempty"`
	DurationMs  int64  `json:"duration_ms"`
	Error       string `json:"error,omitempty"`
}

type EventSchedulerEventJSON struct {
	EventID         string `json:"event_id"`
	Type            string `json:"type"`
	Level           string `json:"level"`
	Message         string `json:"message"`
	LinkedSandboxID string `json:"linked_sandbox_id,omitempty"`
	CreatedAt       string `json:"created_at"`
}

type EventTraceSandboxJSON struct {
	EventSandboxJSON
	Sandbox *EventSandboxSummaryJSON `json:"sandbox,omitempty"`
}

type EventSandboxSummaryJSON struct {
	SandboxID string `json:"sandbox_id"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	ProjectID string `json:"project_id,omitempty"`
	AgentName string `json:"agent_name,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type EventSandboxesResponse struct {
	EventID       string             `json:"event_id"`
	CorrelationID string             `json:"correlation_id"`
	Sandboxes     []EventSandboxJSON `json:"sandboxes"`
}

type EventRunsResponse struct {
	EventID       string         `json:"event_id"`
	CorrelationID string         `json:"correlation_id"`
	Runs          []EventRunJSON `json:"runs"`
}

type EventRunJSON struct {
	EventID     string `json:"event_id"`
	SchedulerID string `json:"scheduler_id"`
	RunID       string `json:"run_id,omitempty"`
	TriggerID   string `json:"trigger_id"`
	Status      string `json:"status"`
	Error       string `json:"error,omitempty"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

type EventSandboxJSON struct {
	SandboxID        string `json:"sandbox_id"`
	Relation         string `json:"relation"`
	SchedulerID      string `json:"scheduler_id,omitempty"`
	RunID            string `json:"run_id,omitempty"`
	TriggerID        string `json:"trigger_id,omitempty"`
	SchedulerEventID string `json:"scheduler_event_id,omitempty"`
	EventID          string `json:"event_id"`
	CreatedAt        string `json:"created_at"`
}

type SourceRequest struct {
	Name            string  `json:"name"`
	Enabled         *bool   `json:"enabled,omitempty"`
	Provider        string  `json:"provider"`
	TopicPrefix     string  `json:"topic_prefix"`
	Token           string  `json:"token"`
	TokenHash       string  `json:"token_hash"`
	TokenHeader     *string `json:"token_header,omitempty"`
	ClearToken      bool    `json:"clear_token"`
	SignatureType   string  `json:"signature_type"`
	SignatureSecret string  `json:"signature_secret"`
	ClearSignature  bool    `json:"clear_signature"`
	BodyLimitBytes  int64   `json:"body_limit_bytes"`
}

type SourceJSON struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Enabled            bool   `json:"enabled"`
	Provider           string `json:"provider"`
	TopicPrefix        string `json:"topic_prefix"`
	HasToken           bool   `json:"has_token"`
	TokenHeader        string `json:"token_header,omitempty"`
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
