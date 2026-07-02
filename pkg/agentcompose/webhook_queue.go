package agentcompose

import (
	appconfig "agent-compose/internal/config"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const defaultWebhookQueueName = "default"

type WebhookRunQueue struct {
	defaultWorkers int
	rules          []webhookQueueRule

	mu      sync.Mutex
	running map[string]int
}

type webhookQueueRule struct {
	Name    string
	Workers int
	Match   webhookQueueMatch
}

type webhookQueueMatch struct {
	Topic    string
	Provider string
	Payload  map[string]string
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

type webhookQueueReservation struct {
	queue *WebhookRunQueue
	name  string
}

func noopWebhookQueueReservations(count int) []*webhookQueueReservation {
	reservations := make([]*webhookQueueReservation, 0, count)
	for i := 0; i < count; i++ {
		reservations = append(reservations, &webhookQueueReservation{})
	}
	return reservations
}

func newWebhookRunQueueFromConfig(config *appconfig.Config) (*WebhookRunQueue, error) {
	defaultWorkers := 8
	rulesJSON := ""
	if config != nil {
		defaultWorkers = config.WebhookQueueDefaultWorkers
		rulesJSON = strings.TrimSpace(config.WebhookQueueRulesJSON)
	}
	queue := &WebhookRunQueue{
		defaultWorkers: defaultWorkers,
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
		queue.rules = append(queue.rules, rule)
	}
	return queue, nil
}

func normalizeWebhookQueueRule(raw webhookQueueRuleConfig) (webhookQueueRule, error) {
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		return webhookQueueRule{}, fmt.Errorf("name is required")
	}
	if raw.Workers <= 0 {
		return webhookQueueRule{}, fmt.Errorf("workers must be greater than zero")
	}
	topic := strings.TrimSpace(raw.Match.Topic)
	if topic != "" {
		pattern := strings.TrimSuffix(topic, "*")
		if pattern == "" {
			return webhookQueueRule{}, fmt.Errorf("match.topic must not be only wildcard")
		}
		if err := validateTopicEventName(pattern); err != nil {
			return webhookQueueRule{}, fmt.Errorf("match.topic is invalid: %w", err)
		}
	}
	payload := map[string]string{}
	for path, value := range raw.Match.Payload {
		path = strings.TrimSpace(path)
		if path == "" {
			return webhookQueueRule{}, fmt.Errorf("payload match path is required")
		}
		normalized, ok := normalizeWebhookQueueScalar(value)
		if !ok {
			return webhookQueueRule{}, fmt.Errorf("payload match %q must be string, number, boolean, or null", path)
		}
		payload[path] = normalized
	}
	if topic == "" && strings.TrimSpace(raw.Match.Provider) == "" && len(payload) == 0 {
		return webhookQueueRule{}, fmt.Errorf("at least one match condition is required")
	}
	return webhookQueueRule{
		Name:    name,
		Workers: raw.Workers,
		Match: webhookQueueMatch{
			Topic:    topic,
			Provider: strings.TrimSpace(raw.Match.Provider),
			Payload:  payload,
		},
	}, nil
}

func (q *WebhookRunQueue) Reserve(event LoaderTopicEvent) (*webhookQueueReservation, bool) {
	if q == nil {
		return &webhookQueueReservation{}, true
	}
	name, workers := q.match(event)
	if workers == 0 {
		return &webhookQueueReservation{}, true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running[name] >= workers {
		return nil, false
	}
	q.running[name]++
	return &webhookQueueReservation{queue: q, name: name}, true
}

func (q *WebhookRunQueue) match(event LoaderTopicEvent) (string, int) {
	if q == nil {
		return defaultWebhookQueueName, 0
	}
	for _, rule := range q.rules {
		if rule.matches(event) {
			return rule.Name, rule.Workers
		}
	}
	return defaultWebhookQueueName, q.defaultWorkers
}

func (r webhookQueueRule) matches(event LoaderTopicEvent) bool {
	if r.Match.Topic != "" && !loaderTriggerTopicMatches(r.Match.Topic, event.Topic) {
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

func (r *webhookQueueReservation) Release() {
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
