package llms

import (
	"context"
	"os"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

type anthropicCredential struct {
	apiKey     string
	authHeader string
	authScheme string
}

// layeredAnthropicCredential selects the credential value and its wire
// authentication semantics from one winning source layer. A generic key in a
// higher layer must not be paired with, or displaced by, a family-specific
// credential from a lower layer.
func layeredAnthropicCredential(ctx context.Context, config *appconfig.Config, store GlobalEnvStore, sandboxItems []domain.SandboxEnvVar) anthropicCredential {
	if credential, ok := anthropicCredentialFromItems(sandboxItems); ok {
		return credential
	}
	if store != nil {
		globalItems, err := store.ListGlobalEnv(ctx)
		if err == nil {
			if credential, ok := anthropicCredentialFromItems(globalItems); ok {
				return credential
			}
		}
	}
	if credential, ok := anthropicCredentialFromValues(
		os.Getenv("ANTHROPIC_API_KEY"),
		os.Getenv("ANTHROPIC_AUTH_TOKEN"),
		os.Getenv("LLM_API_KEY"),
	); ok {
		return credential
	}
	if credential, ok := anthropicCredentialFromValues("", "", configLLMEnvValue(config, "LLM_API_KEY")); ok {
		return credential
	}
	return anthropicCredential{authHeader: "x-api-key"}
}

func anthropicCredentialFromItems(items []domain.SandboxEnvVar) (anthropicCredential, bool) {
	return anthropicCredentialFromValues(
		EnvItemValue(items, "ANTHROPIC_API_KEY"),
		EnvItemValue(items, "ANTHROPIC_AUTH_TOKEN"),
		EnvItemValue(items, "LLM_API_KEY"),
	)
}

func anthropicCredentialFromValues(apiKey, authToken, genericKey string) (anthropicCredential, bool) {
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		return anthropicCredential{apiKey: apiKey, authHeader: "x-api-key"}, true
	}
	if authToken = strings.TrimSpace(authToken); authToken != "" {
		return anthropicCredential{apiKey: authToken, authHeader: "Authorization", authScheme: "Bearer"}, true
	}
	if genericKey = strings.TrimSpace(genericKey); genericKey != "" {
		return anthropicCredential{apiKey: genericKey, authHeader: "x-api-key"}, true
	}
	return anthropicCredential{}, false
}
