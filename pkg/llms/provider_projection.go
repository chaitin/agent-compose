package llms

import (
	"context"
	"os"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
)

// DefaultConfigStore is the write surface daemon-environment projection needs.
// It is distinct from CatalogStore, which is the read surface: projection runs
// once at startup, and afterwards every request reads the catalog.
type DefaultConfigStore interface {
	UpsertDefaultLLMConfig(ctx context.Context, provider Provider, model Model) error
}

// EnvProviderLookup resolves an environment value for LLM connection
// projection. It accepts candidate keys and returns the first non-empty value
// (source-major: an earlier source wins across all candidate keys before a
// later source is consulted).
type EnvProviderLookup func(keys ...string) string

// EnvProviderRegistration groups the identity/model fields the environment
// projection uses to register one connection: which provider/model to write,
// under which scope, and whether it becomes the default model.
type EnvProviderRegistration struct {
	ProviderID     string
	Name           string
	Scope          string
	RequestedModel string
	DefaultModel   bool
}

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
	_, err := ensureOpenAIEnvProvider(ctx, store, lookup, EnvProviderRegistration{
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
// daemon's own declaration and are no longer part of the catalog.
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

func configLLMEnvValue(config *appconfig.Config, key string) string {
	if config == nil {
		return ""
	}
	switch strings.ToUpper(strings.TrimSpace(key)) {
	case "LLM_API_ENDPOINT":
		return config.LLMAPIEndpoint
	case "LLM_API_PROTOCOL":
		return config.LLMAPIProtocol
	case "LLM_API_KEY":
		return config.LLMAPIKey
	case "LLM_MODEL":
		return config.LLMModel
	default:
		return ""
	}
}

// hasCompleteDefaultOpenAIProvider reports whether the daemon environment
// declares a complete OpenAI upstream: a credential plus a non-messages
// protocol. An endpoint and a model without a credential are not a connection.
func hasCompleteDefaultOpenAIProvider(lookup EnvProviderLookup) bool {
	if lookup == nil || NormalizeWireAPI(lookup("LLM_API_PROTOCOL")) == APIProtocolMessages {
		return false
	}
	return firstNonEmptyTrimmed(lookup("LLM_API_KEY", "OPENAI_API_KEY")) != ""
}

// hasDefaultAnthropicEnvProviderInput reports whether the daemon environment
// declares an Anthropic connection, either through the ANTHROPIC_* names or
// through the generic LLM_* names under the messages protocol.
func hasDefaultAnthropicEnvProviderInput(lookup EnvProviderLookup) bool {
	if strings.TrimSpace(firstNonEmpty(
		lookup("ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT"),
		lookup("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN"),
		lookup("ANTHROPIC_MODEL", "CLAUDE_MODEL"),
	)) != "" {
		return true
	}
	if NormalizeWireAPI(lookup("LLM_API_PROTOCOL")) != APIProtocolMessages {
		return false
	}
	return strings.TrimSpace(firstNonEmpty(
		lookup("LLM_API_ENDPOINT"),
		lookup("LLM_API_KEY"),
		lookup("LLM_MODEL"),
	)) != ""
}

func ensureOpenAIEnvProvider(ctx context.Context, store DefaultConfigStore, lookup EnvProviderLookup, reg EnvProviderRegistration) (string, error) {
	providerID, name, scope, requestedModel, defaultModel := reg.ProviderID, reg.Name, reg.Scope, reg.RequestedModel, reg.DefaultModel
	endpoint := firstNonEmpty(lookup("LLM_API_ENDPOINT"), "https://api.openai.com")
	protocol := NormalizeWireAPI(lookup("LLM_API_PROTOCOL"))
	if protocol == APIProtocolMessages {
		return "", nil
	}
	apiKey := lookup("LLM_API_KEY", "OPENAI_API_KEY")
	model := strings.TrimSpace(firstNonEmpty(requestedModel, lookup("LLM_MODEL")))
	if providerID == "" || model == "" {
		return "", nil
	}
	headersJSON, err := envProviderHeadersJSON(lookup, nil)
	if err != nil {
		return "", err
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, Provider{
		ID:             providerID,
		Name:           name,
		ProviderType:   ProviderFamilyOpenAI,
		DefaultWireAPI: protocol,
		BaseURL:        endpoint,
		APIKey:         apiKey,
		AuthHeader:     "Authorization",
		AuthScheme:     "Bearer",
		HeadersJSON:    headersJSON,
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, Model{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}

// anthropicEnvProviderInput groups ensureAnthropicEnvProvider's credential
// and registration inputs.
type anthropicEnvProviderInput struct {
	Credential anthropicCredential
	EnvProviderRegistration
}

func ensureAnthropicEnvProvider(ctx context.Context, store DefaultConfigStore, lookup EnvProviderLookup, in anthropicEnvProviderInput) (string, error) {
	credential, reg := in.Credential, in.EnvProviderRegistration
	providerID, name, scope, requestedModel, defaultModel := reg.ProviderID, reg.Name, reg.Scope, reg.RequestedModel, reg.DefaultModel
	anthropicEndpoint := lookup("ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT")
	genericEndpoint := lookup("LLM_API_ENDPOINT")
	anthropicModel := lookup("ANTHROPIC_MODEL", "CLAUDE_MODEL")
	genericModel := lookup("LLM_MODEL")
	if anthropicEndpoint == "" && strings.TrimSpace(credential.apiKey) == "" && strings.TrimSpace(anthropicModel) == "" && genericEndpoint == "" && strings.TrimSpace(genericModel) == "" {
		return "", nil
	}
	anthropicEndpoint = firstNonEmpty(anthropicEndpoint, genericEndpoint)
	anthropicModel = firstNonEmpty(anthropicModel, genericModel)
	endpoint := firstNonEmpty(anthropicEndpoint, "https://api.anthropic.com")
	model := strings.TrimSpace(firstNonEmpty(requestedModel, anthropicModel))
	if providerID == "" || model == "" {
		return "", nil
	}
	headersJSON, err := envProviderHeadersJSON(lookup, map[string]string{
		"anthropic-version": "2023-06-01",
	})
	if err != nil {
		return "", err
	}
	return providerID, store.UpsertDefaultLLMConfig(ctx, Provider{
		ID:             providerID,
		Name:           name,
		ProviderType:   ProviderFamilyAnthropic,
		DefaultWireAPI: APIProtocolMessages,
		BaseURL:        endpoint,
		APIKey:         credential.apiKey,
		AuthHeader:     credential.authHeader,
		AuthScheme:     credential.authScheme,
		HeadersJSON:    headersJSON,
		Weight:         10,
		Enabled:        true,
		Scope:          scope,
	}, Model{ID: model, Name: model, DefaultModel: defaultModel, Enabled: true, Scope: scope})
}
