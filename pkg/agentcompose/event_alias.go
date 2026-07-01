package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	eventspkg "agent-compose/pkg/events"
	"agent-compose/pkg/model"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	"github.com/labstack/echo/v4"
)

const (
	TopicEventSourceWebhook = model.TopicEventSourceWebhook
	TopicEventSourceLoader  = model.TopicEventSourceLoader
	TopicEventSourceSystem  = model.TopicEventSourceSystem

	TopicEventDispatchPending        = model.TopicEventDispatchPending
	TopicEventDispatchPublishing     = model.TopicEventDispatchPublishing
	TopicEventDispatchPublishedToBus = model.TopicEventDispatchPublishedToBus
	TopicEventDispatchNoSubscriber   = model.TopicEventDispatchNoSubscriber
	TopicEventDispatchRetrying       = model.TopicEventDispatchRetrying
	TopicEventDispatchDeadLetter     = model.TopicEventDispatchDeadLetter

	EventDeliveryStatusMatched      = model.EventDeliveryStatusMatched
	EventDeliveryStatusRunStarted   = model.EventDeliveryStatusRunStarted
	EventDeliveryStatusRunSucceeded = model.EventDeliveryStatusRunSucceeded
	EventDeliveryStatusRunFailed    = model.EventDeliveryStatusRunFailed
	EventDeliveryStatusSkipped      = model.EventDeliveryStatusSkipped
)

type TopicEventRecord = model.TopicEventRecord
type TopicEventFilter = model.TopicEventFilter
type WebhookSource = model.WebhookSource
type EventDelivery = model.EventDelivery
type EventSessionLink = model.EventSessionLink
type EventSessionTraceItem = model.EventSessionTraceItem
type EventDispatcher = eventspkg.EventDispatcher
type WebhookRunQueue = eventspkg.WebhookRunQueue

type webhookAcceptedResponse struct {
	Accepted      bool   `json:"accepted"`
	Topic         string `json:"topic"`
	EventID       string `json:"event_id"`
	Sequence      int64  `json:"sequence"`
	CorrelationID string `json:"correlation_id"`
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

var topicEventNamePattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

func NewEventDispatcher(rootCtx context.Context, configDB *ConfigStore, bus *LoaderBus) *EventDispatcher {
	return eventspkg.NewEventDispatcher(rootCtx, configDB, bus)
}

func registerWebhookRoutes(app *echo.Echo, service *Service) {
	eventspkg.RegisterRoutes(app, eventspkg.NewService(service.config, service.configDB))
}

func newWebhookRunQueueFromConfig(config *appconfig.Config) (*WebhookRunQueue, error) {
	return eventspkg.NewWebhookRunQueueFromConfig(config)
}

func newWebhookRunQueue(defaultWorkers int) *WebhookRunQueue {
	return eventspkg.NewWebhookRunQueue(defaultWorkers)
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

func topicEventPayloadSHA256(payloadJSON string) string {
	sum := sha256.Sum256([]byte(payloadJSON))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func webhookTokenHash(token string) string {
	return eventspkg.WebhookTokenHash(token)
}

func existingWebhookBodyHash(payloadJSON string) string {
	return eventspkg.ExistingWebhookBodyHash(payloadJSON)
}

func providerFromWebhookTopic(topic string) string {
	return eventspkg.ProviderFromWebhookTopic(topic)
}
