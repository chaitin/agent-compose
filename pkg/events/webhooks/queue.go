package webhooks

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

const DefaultQueueName = "default"

type RunQueue struct {
	defaultWorkers int
	rules          []queueRule

	mu      sync.Mutex
	running map[string]int
}

type queueRule struct {
	Name    string
	Workers int
	Match   queueMatch
}

type queueMatch struct {
	Topic    string
	Provider string
	Payload  map[string]string
}

type queueRuleConfig struct {
	Name    string           `json:"name"`
	Workers int              `json:"workers"`
	Match   queueMatchConfig `json:"match"`
}

type queueMatchConfig struct {
	Topic    string         `json:"topic"`
	Provider string         `json:"provider"`
	Payload  map[string]any `json:"payload"`
}

type Reservation struct {
	queue *RunQueue
	name  string
}

func NewRunQueue(defaultWorkers int) *RunQueue {
	return &RunQueue{
		defaultWorkers: defaultWorkers,
		running:        map[string]int{},
	}
}

func NoopReservations(count int) []*Reservation {
	reservations := make([]*Reservation, 0, count)
	for i := 0; i < count; i++ {
		reservations = append(reservations, &Reservation{})
	}
	return reservations
}

func NewRunQueueFromConfig(config *appconfig.Config) (*RunQueue, error) {
	defaultWorkers := 8
	rulesJSON := ""
	if config != nil {
		defaultWorkers = config.WebhookQueueDefaultWorkers
		rulesJSON = strings.TrimSpace(config.WebhookQueueRulesJSON)
	}
	queue := NewRunQueue(defaultWorkers)
	if rulesJSON == "" {
		return queue, nil
	}
	var rawRules []queueRuleConfig
	if err := json.Unmarshal([]byte(rulesJSON), &rawRules); err != nil {
		return nil, fmt.Errorf("parse WEBHOOK_QUEUE_RULES_JSON: %w", err)
	}
	seen := map[string]struct{}{}
	for index, raw := range rawRules {
		rule, err := normalizeQueueRule(raw)
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

func normalizeQueueRule(raw queueRuleConfig) (queueRule, error) {
	name := strings.TrimSpace(raw.Name)
	if name == "" {
		return queueRule{}, fmt.Errorf("name is required")
	}
	if raw.Workers <= 0 {
		return queueRule{}, fmt.Errorf("workers must be greater than zero")
	}
	topic := strings.TrimSpace(raw.Match.Topic)
	if topic != "" {
		pattern := strings.TrimSuffix(topic, "*")
		if pattern == "" {
			return queueRule{}, fmt.Errorf("match.topic must not be only wildcard")
		}
		if err := domain.ValidateTopicEventName(pattern); err != nil {
			return queueRule{}, fmt.Errorf("match.topic is invalid: %w", err)
		}
	}
	payload := map[string]string{}
	for path, value := range raw.Match.Payload {
		path = strings.TrimSpace(path)
		if path == "" {
			return queueRule{}, fmt.Errorf("payload match path is required")
		}
		normalized, ok := normalizeWebhookQueueScalar(value)
		if !ok {
			return queueRule{}, fmt.Errorf("payload match %q must be string, number, boolean, or null", path)
		}
		payload[path] = normalized
	}
	if topic == "" && strings.TrimSpace(raw.Match.Provider) == "" && len(payload) == 0 {
		return queueRule{}, fmt.Errorf("at least one match condition is required")
	}
	return queueRule{
		Name:    name,
		Workers: raw.Workers,
		Match: queueMatch{
			Topic:    topic,
			Provider: strings.TrimSpace(raw.Match.Provider),
			Payload:  payload,
		},
	}, nil
}

func (q *RunQueue) Reserve(event domain.LoaderTopicEvent) (*Reservation, bool) {
	if q == nil {
		return &Reservation{}, true
	}
	name, workers := q.Match(event)
	if workers == 0 {
		return &Reservation{}, true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.running[name] >= workers {
		return nil, false
	}
	q.running[name]++
	return &Reservation{queue: q, name: name}, true
}

func (q *RunQueue) Match(event domain.LoaderTopicEvent) (string, int) {
	if q == nil {
		return DefaultQueueName, 0
	}
	for _, rule := range q.rules {
		if rule.matches(event) {
			return rule.Name, rule.Workers
		}
	}
	return DefaultQueueName, q.defaultWorkers
}

func (r queueRule) matches(event domain.LoaderTopicEvent) bool {
	if r.Match.Topic != "" && !domain.LoaderTriggerTopicMatches(r.Match.Topic, event.Topic) {
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

func (r *Reservation) Release() {
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
