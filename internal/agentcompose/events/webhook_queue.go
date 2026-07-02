package events

import (
	appconfig "agent-compose/internal/config"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const defaultWebhookQueueName = "default"

type WebhookRunQueue struct {
	DefaultWorkers int
	Rules          []WebhookQueueRule

	mu      sync.Mutex
	running map[string]int
}

type WebhookQueueRule struct {
	Name    string
	Workers int
	Match   WebhookQueueMatch
}

type WebhookQueueMatch struct {
	Topic    string
	Provider string
	Payload  map[string]string
}

type WebhookQueueEvent struct {
	Topic    string
	Provider string
	Payload  map[string]any
}

type webhookQueueRuleConfig struct {
	Name    string                  `json:"name"`
	Workers int                     `json:"workers"`
	Match   webhookQueueMatchConfig `json:"match"`
}

type webhookQueueMatchConfig struct {
	Topic    string         `json:"topic"`
	Provider string         `json:"provider"`
	Payload  map[string]any `json:"payload"`
}

type WebhookQueueReservation struct {
	queue *WebhookRunQueue
	name  string
}

func NoopWebhookQueueReservations(count int) []*WebhookQueueReservation {
	reservations := make([]*WebhookQueueReservation, 0, count)
	for i := 0; i < count; i++ {
		reservations = append(reservations, &WebhookQueueReservation{})
	}
	return reservations
}

func NewWebhookRunQueueFromConfig(config *appconfig.Config) (*WebhookRunQueue, error) {
	defaultWorkers := 8
	rulesJSON := ""
	if config != nil {
		defaultWorkers = config.WebhookQueueDefaultWorkers
		rulesJSON = strings.TrimSpace(config.WebhookQueueRulesJSON)
	}
	queue := &WebhookRunQueue{
		DefaultWorkers: defaultWorkers,
		running:        map[string]int{},
	}
	if rulesJSON == "" {
		return queue, nil
	}
	var rawRules []webhookQueueRuleConfig
	if err := json.Unmarshal([]byte(rulesJSON), &rawRules); err != nil {
		return nil, fmt.Errorf("parse WEBHOOK_QUEUE_RULES_JSON: %w", err)
	}
	seen := map[string]struct{}{}
	for index, raw := range rawRules {
		rule, err := normalizeWebhookQueueRule(raw)
		if err != nil {
			return nil, fmt.Errorf("webhook queue rule %d: %w", index, err)
		}
		if _, ok := seen[rule.Name]; ok {
			return nil, fmt.Errorf("webhook queue rule %d duplicates name %q", index, rule.Name)
		}
		seen[rule.Name] = struct{}{}
		queue.Rules = append(queue.Rules, rule)
	}
	return queue, nil
}

func normalizeWebhookQueueRule(raw webhookQueueRuleConfig) (WebhookQueueRule, error) {
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		return WebhookQueueRule{}, fmt.Errorf("name is required")
	}
	if raw.Workers <= 0 {
		return WebhookQueueRule{}, fmt.Errorf("workers must be greater than zero")
	}
	topic := strings.TrimSpace(raw.Match.Topic)
	if topic != "" {
		pattern := strings.TrimSuffix(topic, "*")
		if pattern == "" {
			return WebhookQueueRule{}, fmt.Errorf("match.topic must not be only wildcard")
		}
		if err := ValidateTopicEventName(pattern); err != nil {
			return WebhookQueueRule{}, fmt.Errorf("match.topic is invalid: %w", err)
		}
	}
	payload := map[string]string{}
	for path, value := range raw.Match.Payload {
		path = strings.TrimSpace(path)
		if path == "" {
			return WebhookQueueRule{}, fmt.Errorf("payload match path is required")
		}
		normalized, ok := normalizeWebhookQueueScalar(value)
		if !ok {
			return WebhookQueueRule{}, fmt.Errorf("payload match %q must be string, number, boolean, or null", path)
		}
		payload[path] = normalized
	}
	if topic == "" && strings.TrimSpace(raw.Match.Provider) == "" && len(payload) == 0 {
		return WebhookQueueRule{}, fmt.Errorf("at least one match condition is required")
	}
	return WebhookQueueRule{
		Name:    name,
		Workers: raw.Workers,
		Match: WebhookQueueMatch{
			Topic:    topic,
			Provider: strings.TrimSpace(raw.Match.Provider),
			Payload:  payload,
		},
	}, nil
}

func (q *WebhookRunQueue) Reserve(event WebhookQueueEvent) (*WebhookQueueReservation, bool) {
	if q == nil {
		return &WebhookQueueReservation{}, true
	}
	name, workers := q.match(event)
	if workers == 0 {
		return &WebhookQueueReservation{}, true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running[name] >= workers {
		return nil, false
	}
	q.running[name]++
	return &WebhookQueueReservation{queue: q, name: name}, true
}

func (q *WebhookRunQueue) match(event WebhookQueueEvent) (string, int) {
	if q == nil {
		return defaultWebhookQueueName, 0
	}
	for _, rule := range q.Rules {
		if rule.matches(event) {
			return rule.Name, rule.Workers
		}
	}
	return defaultWebhookQueueName, q.DefaultWorkers
}

func (r WebhookQueueRule) matches(event WebhookQueueEvent) bool {
	if r.Match.Topic != "" && !triggerTopicMatches(r.Match.Topic, event.Topic) {
		return false
	}
	if r.Match.Provider != "" && r.Match.Provider != event.Provider {
		return false
	}
	for path, want := range r.Match.Payload {
		got, ok := payloadPathScalar(event.Payload, path)
		if !ok || got != want {
			return false
		}
	}
	return true
}

func triggerTopicMatches(pattern, topic string) bool {
	pattern = strings.TrimSpace(pattern)
	topic = strings.TrimSpace(topic)
	if pattern == "" {
		return false
	}
	if strings.HasSuffix(pattern, "*") {
		return strings.HasPrefix(topic, strings.TrimSuffix(pattern, "*"))
	}
	return pattern == topic
}

func (r *WebhookQueueReservation) Release() {
	if r == nil || r.queue == nil || r.name == "" {
		return
	}
	r.queue.mu.Lock()
	defer r.queue.mu.Unlock()
	if r.queue.running[r.name] <= 1 {
		delete(r.queue.running, r.name)
		return
	}
	r.queue.running[r.name]--
}

func payloadPathScalar(payload map[string]any, path string) (string, bool) {
	var current any = payload
	for _, part := range strings.Split(path, ".") {
		part = strings.TrimSpace(part)
		if part == "" {
			return "", false
		}
		object, ok := current.(map[string]any)
		if !ok {
			return "", false
		}
		current, ok = object[part]
		if !ok {
			return "", false
		}
	}
	return normalizeWebhookQueueScalar(current)
}

func normalizeWebhookQueueScalar(value any) (string, bool) {
	switch typed := value.(type) {
	case nil:
		return "null", true
	case string:
		return typed, true
	case bool:
		if typed {
			return "true", true
		}
		return "false", true
	case float64:
		return fmt.Sprintf("%g", typed), true
	case json.Number:
		return typed.String(), true
	default:
		return "", false
	}
}
