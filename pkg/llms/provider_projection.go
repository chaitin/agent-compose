package llms

import (
	"context"
	"os"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// ProjectDaemonLLMConfig materializes the daemon's environment LLM connection
// into the configuration store.
//
// The daemon has two ways to be told about an upstream connection: models.json,
// and the LLM_API_* / ANTHROPIC_* environment. models.json is projected when the
// daemon loads its catalog. This projects the environment once, at startup, so
// that afterwards the catalog is the single source every request reads.
//
// Projecting at startup rather than during resolution keeps two properties:
// a request path never writes configuration, and a connection that appears in
// the catalog is stable for the lifetime of the process.
//
// A daemon that declares no environment connection is a no-op, leaving every
// agent to its own authentication.
func ProjectDaemonLLMConfig(ctx context.Context, config *appconfig.Config, store DefaultConfigStore) error {
	if store == nil {
		return nil
	}
	lookup := daemonLLMEnvLookup(config)
	if hasDefaultAnthropicEnvProviderInput(lookup) {
		credential, ok := anthropicCredentialFromValues(
			lookup("ANTHROPIC_API_KEY"),
			lookup("ANTHROPIC_AUTH_TOKEN"),
			lookup("LLM_API_KEY"),
		)
		if !ok {
			credential = anthropicCredential{authHeader: "x-api-key"}
		}
		_, err := ensureAnthropicEnvProvider(ctx, store, lookup, anthropicEnvProviderInput{
			Credential: credential,
			EnvProviderRegistration: EnvProviderRegistration{
				ProviderID:   ProviderIDDefaultAnthropic,
				Name:         "anthropic",
				Scope:        ProviderScopeEnvDefault,
				DefaultModel: true,
			},
		})
		return err
	}
	// An endpoint and a model without a credential are not a connection: the
	// agent would be pointed at a gateway the daemon cannot authenticate to.
	// Requiring the credential here is what preserves "no key means no managed
	// connection" from the previous lazy bootstrap.
	if !hasCompleteDefaultOpenAIProvider(lookup) {
		return nil
	}
	_, err := EnsureOpenAIEnvProvider(ctx, store, lookup, EnvProviderRegistration{
		ProviderID:   ProviderIDDefaultOpenAI,
		Name:         "openai",
		Scope:        ProviderScopeEnvDefault,
		DefaultModel: true,
	})
	return err
}

// daemonLLMEnvLookup resolves the daemon's own LLM environment: the process
// environment first, then the daemon configuration. It deliberately omits the
// stored global environment and any sandbox scope, because those are not the
// daemon's own declaration and are resolved elsewhere.
func daemonLLMEnvLookup(config *appconfig.Config) EnvProviderLookup {
	return func(keys ...string) string {
		for _, key := range keys {
			if value := os.Getenv(key); strings.TrimSpace(value) != "" {
				return value
			}
		}
		for _, key := range keys {
			if value := configLLMEnvValue(config, key); strings.TrimSpace(value) != "" {
				return value
			}
		}
		return ""
	}
}
