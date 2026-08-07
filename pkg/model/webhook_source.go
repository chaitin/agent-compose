package model

import (
	"fmt"
	"strings"
)

const WebhookSignatureGitHubSHA256 = "github_sha256"

var reservedWebhookTokenHeaders = map[string]struct{}{
	"idempotency-key":                 {},
	"x-agent-compose-parent-event-id": {},
	"x-correlation-id":                {},
	"x-github-delivery":               {},
	"x-gitlab-event-uuid":             {},
	"x-request-id":                    {},
}

// NormalizeWebhookTokenHeaderName validates a custom credential header while
// keeping it disjoint from headers that drive parsing, authentication, routing,
// lineage, or delivery identity. BuildPayload separately removes every allowed
// custom credential header from the persisted header projection.
func NormalizeWebhookTokenHeaderName(name string) (string, error) {
	normalized, err := NormalizeHTTPHeaderName(name)
	if err != nil || normalized == "" {
		return normalized, err
	}
	if _, reserved := reservedWebhookTokenHeaders[strings.ToLower(normalized)]; reserved {
		return "", fmt.Errorf("header name is reserved for webhook protocol metadata")
	}
	return normalized, nil
}

// GitHubWebhookMode describes whether a webhook source uses legacy generic
// handling, unsigned GitHub event routing, or signed GitHub event routing.
type GitHubWebhookMode int

const (
	GitHubWebhookModeGeneric GitHubWebhookMode = iota
	GitHubWebhookModeUnsigned
	GitHubWebhookModeSHA256
)

// GitHubWebhookModeForSource validates provider-specific configuration and
// reports whether the source uses GitHub event routing and authentication.
// An empty signature type retains the legacy URL-topic and token behavior.
func GitHubWebhookModeForSource(source WebhookSource) (GitHubWebhookMode, error) {
	provider := strings.TrimSpace(source.Provider)
	signatureType := strings.ToLower(strings.TrimSpace(source.SignatureType))
	secret := strings.TrimSpace(source.SignatureSecret)

	if signatureType == "" {
		return GitHubWebhookModeGeneric, nil
	}
	if signatureType != WebhookSignatureGitHubSHA256 {
		if provider == "github" {
			return GitHubWebhookModeGeneric, fmt.Errorf("github webhook source signature type must be %q", WebhookSignatureGitHubSHA256)
		}
		return GitHubWebhookModeGeneric, nil
	}
	if provider != "github" {
		return GitHubWebhookModeGeneric, fmt.Errorf("github sha256 webhook source provider must be github")
	}
	if source.TopicPrefix != "webhook.github." {
		return GitHubWebhookModeGeneric, fmt.Errorf("github webhook source topic prefix must be %q", "webhook.github.")
	}
	if secret == "" {
		return GitHubWebhookModeUnsigned, nil
	}
	return GitHubWebhookModeSHA256, nil
}
