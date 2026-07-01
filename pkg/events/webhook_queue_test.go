package events

import (
	"testing"

	appconfig "agent-compose/pkg/config"
)

func TestWebhookRunQueueMatchesPayloadRules(t *testing.T) {
	testWebhookRunQueueMatchesPayloadRules(t)
}

func TestWebhookQueueIntegrationMatchesPayloadRules(t *testing.T) {
	testWebhookRunQueueMatchesPayloadRules(t)
}

func TestWebhookQueueE2EMatchesPayloadRules(t *testing.T) {
	testWebhookRunQueueMatchesPayloadRules(t)
}

func testWebhookRunQueueMatchesPayloadRules(t *testing.T) {
	t.Helper()
	queue, err := NewWebhookRunQueueFromConfig(&appconfig.Config{
		WebhookQueueDefaultWorkers: 8,
		WebhookQueueRulesJSON: `[
			{"name":"repo-a","workers":1,"match":{"topic":"webhook.github.push","payload":{"body.repository.full_name":"org/repo-a"}}},
			{"name":"github-default","workers":3,"match":{"topic":"webhook.github.*"}}
		]`,
	})
	if err != nil {
		t.Fatalf("NewWebhookRunQueueFromConfig returned error: %v", err)
	}

	name, workers := queue.Match(LoaderTopicEvent{
		Topic: "webhook.github.push",
		Payload: map[string]any{
			"body": map[string]any{
				"repository": map[string]any{"full_name": "org/repo-a"},
			},
		},
	})
	if name != "repo-a" || workers != 1 {
		t.Fatalf("repo-a queue = %s/%d", name, workers)
	}

	name, workers = queue.Match(LoaderTopicEvent{
		Topic: "webhook.github.push",
		Payload: map[string]any{
			"body": map[string]any{
				"repository": map[string]any{"full_name": "org/repo-b"},
			},
		},
	})
	if name != "github-default" || workers != 3 {
		t.Fatalf("repo-b queue = %s/%d", name, workers)
	}
}

func TestWebhookRunQueueReserveAndRelease(t *testing.T) {
	testWebhookRunQueueReserveAndRelease(t)
}

func TestWebhookQueueIntegrationReserveAndRelease(t *testing.T) {
	testWebhookRunQueueReserveAndRelease(t)
}

func TestWebhookQueueE2EReserveAndRelease(t *testing.T) {
	testWebhookRunQueueReserveAndRelease(t)
}

func testWebhookRunQueueReserveAndRelease(t *testing.T) {
	t.Helper()
	queue, err := NewWebhookRunQueueFromConfig(&appconfig.Config{
		WebhookQueueDefaultWorkers: 1,
	})
	if err != nil {
		t.Fatalf("NewWebhookRunQueueFromConfig returned error: %v", err)
	}
	event := LoaderTopicEvent{Topic: "webhook.github.push", Payload: map[string]any{}}
	reservation, ok := queue.Reserve(event)
	if !ok {
		t.Fatalf("first reserve failed")
	}
	if _, ok := queue.Reserve(event); ok {
		t.Fatalf("second reserve succeeded while queue was full")
	}
	reservation.Release()
	if _, ok := queue.Reserve(event); !ok {
		t.Fatalf("reserve after release failed")
	}
}
