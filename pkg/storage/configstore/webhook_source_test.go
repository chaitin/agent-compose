package configstore

import (
	"context"
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestUpsertWebhookSourceValidatesGitHubSignatureConfiguration(t *testing.T) {
	valid := domain.WebhookSource{
		ID:              "github",
		Provider:        "github",
		TopicPrefix:     "webhook.github.",
		SignatureType:   "github_sha256",
		SignatureSecret: "secret",
	}
	tests := []struct {
		name   string
		mutate func(*domain.WebhookSource)
		want   string
	}{
		{name: "provider", mutate: func(source *domain.WebhookSource) { source.Provider = "gitlab" }, want: "github sha256 webhook source provider must be github"},
		{name: "topic prefix", mutate: func(source *domain.WebhookSource) { source.TopicPrefix = "webhook.other." }, want: `github webhook source topic prefix must be "webhook.github."`},
		{name: "case insensitive signature type", mutate: func(source *domain.WebhookSource) {
			source.SignatureType = "GitHub_SHA256"
			source.Provider = "gitlab"
		}, want: "github sha256 webhook source provider must be github"},
		{name: "unknown signature type", mutate: func(source *domain.WebhookSource) {
			source.SignatureType = "unknown"
		}, want: `github webhook source signature type must be "github_sha256"`},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := FromDB(newMemoryDB(t))
			source := valid
			test.mutate(&source)
			if _, err := store.UpsertWebhookSource(context.Background(), source); err == nil {
				t.Fatal("UpsertWebhookSource returned nil error")
			} else if err.Error() != test.want {
				t.Fatalf("UpsertWebhookSource error = %q, want %q", err, test.want)
			}
		})
	}

	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatalf("init schema: %v", err)
	}
	if _, err := store.UpsertWebhookSource(ctx, valid); err != nil {
		t.Fatalf("UpsertWebhookSource with valid GitHub signature configuration returned error: %v", err)
	}
	unsigned := valid
	unsigned.ID = "github-unsigned"
	unsigned.SignatureSecret = ""
	if _, err := store.UpsertWebhookSource(ctx, unsigned); err != nil {
		t.Fatalf("UpsertWebhookSource with unsigned GitHub configuration returned error: %v", err)
	}
}

func TestUpsertWebhookSourceRejectsProtocolTokenHeaders(t *testing.T) {
	store := FromDB(newMemoryDB(t))
	for _, tokenHeader := range []string{domain.WebhookParentEventIDHeader, "X-Correlation-ID", "Idempotency-Key", "X-GitHub-Delivery", "X-Gitlab-Event-UUID", "X-Request-ID"} {
		_, err := store.UpsertWebhookSource(context.Background(), domain.WebhookSource{
			ID: "internal", Name: "Internal", Enabled: true, Provider: "internal",
			TopicPrefix: "webhook.internal.", TokenHash: "hash", TokenHeader: tokenHeader,
		})
		if err == nil || !strings.Contains(err.Error(), "reserved for webhook protocol metadata") {
			t.Fatalf("UpsertWebhookSource reserved token header %q error = %v", tokenHeader, err)
		}
	}
}
