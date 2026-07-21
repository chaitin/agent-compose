package llms

import (
	"context"
	"fmt"
	"os"
	"strings"

	appconfig "agent-compose/pkg/config"
	domain "agent-compose/pkg/model"
)

type providerEnvironment struct {
	endpoint   string
	protocol   string
	apiKey     string
	model      string
	authHeader string
	authScheme string
	configured bool
}

type providerEnvValue struct {
	name  string
	value string
}

type providerEnvSource func(string) string

type providerEnvSources []providerEnvSource

func (sources providerEnvSources) lookup(keys ...string) providerEnvValue {
	for _, source := range sources {
		if source == nil {
			continue
		}
		for _, key := range keys {
			key = strings.TrimSpace(key)
			if key == "" {
				continue
			}
			if value := strings.TrimSpace(source(key)); value != "" {
				return providerEnvValue{name: key, value: value}
			}
		}
	}
	return providerEnvValue{}
}

func (sources providerEnvSources) openAIEnvironment() providerEnvironment {
	environment := providerEnvironment{
		endpoint: sources.lookup("LLM_API_ENDPOINT").value,
		protocol: sources.lookup("LLM_API_PROTOCOL").value,
		apiKey:   sources.lookup("LLM_API_KEY", "OPENAI_API_KEY").value,
		model:    sources.lookup("LLM_MODEL").value,
	}
	environment.configured = environment.endpoint != "" || environment.protocol != "" || environment.apiKey != "" || environment.model != ""
	return environment
}

func (sources providerEnvSources) anthropicEnvironment() providerEnvironment {
	key := sources.lookup("ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN")
	model := sources.lookup("ANTHROPIC_MODEL", "CLAUDE_MODEL")
	authHeader, authScheme := "x-api-key", ""
	if strings.EqualFold(key.name, "ANTHROPIC_AUTH_TOKEN") {
		authHeader, authScheme = "Authorization", "Bearer"
	}
	environment := providerEnvironment{
		endpoint:   sources.lookup("ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT").value,
		apiKey:     key.value,
		model:      model.value,
		authHeader: authHeader,
		authScheme: authScheme,
	}
	environment.configured = environment.endpoint != "" || environment.apiKey != "" || environment.model != ""
	return environment
}

func defaultProviderEnvSources(ctx context.Context, config *appconfig.Config, store GlobalEnvStore) (providerEnvSources, error) {
	globalItems, err := listGlobalEnvItems(ctx, store)
	if err != nil {
		return nil, err
	}
	return providerEnvSources{
		envItemsSource(globalItems),
		daemonProviderEnvSource(config),
	}, nil
}

func layeredProviderEnvSources(ctx context.Context, config *appconfig.Config, store GlobalEnvStore, sandboxItems []domain.SandboxEnvVar) (providerEnvSources, []domain.SandboxEnvVar, error) {
	globalItems, err := listGlobalEnvItems(ctx, store)
	if err != nil {
		return nil, nil, err
	}
	sandboxItems = domain.NormalizeEnvItems(sandboxItems)
	return providerEnvSources{
		envItemsSource(sandboxItems),
		envItemsSource(globalItems),
		daemonProviderEnvSource(config),
	}, sandboxItems, nil
}

func listGlobalEnvItems(ctx context.Context, store GlobalEnvStore) ([]domain.SandboxEnvVar, error) {
	if store == nil {
		return nil, nil
	}
	items, err := store.ListGlobalEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("list global provider environment: %w", err)
	}
	return domain.NormalizeEnvItems(items), nil
}

func envItemsSource(items []domain.SandboxEnvVar) providerEnvSource {
	if len(items) == 0 {
		return nil
	}
	return func(key string) string {
		return EnvItemValue(items, key)
	}
}

func daemonProviderEnvSource(config *appconfig.Config) providerEnvSource {
	return func(key string) string {
		if value := strings.TrimSpace(os.Getenv(strings.TrimSpace(key))); value != "" {
			return value
		}
		return strings.TrimSpace(configLLMEnvValue(config, key))
	}
}

// SetSandboxProviderEnvItems stores only daemon-side LLM provider controls on
// the sandbox. The driver boundary never projects this field into a guest.
func SetSandboxProviderEnvItems(sandbox *domain.Sandbox, items []domain.SandboxEnvVar) {
	if sandbox == nil {
		return
	}
	sandbox.ProviderEnvItems = filterSandboxProviderEnvItems(items)
}

func filterSandboxProviderEnvItems(items []domain.SandboxEnvVar) []domain.SandboxEnvVar {
	filtered := make([]domain.SandboxEnvVar, 0, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		if !sandboxProviderEnvName(item.Name) || strings.TrimSpace(item.Value) == "" {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func sandboxProviderEnvName(name string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "LLM_API_ENDPOINT", "LLM_API_PROTOCOL", "LLM_API_KEY", "OPENAI_API_KEY", "LLM_MODEL",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "CLAUDE_MODEL",
		RuntimeBaseURLEnvName:
		return true
	default:
		return false
	}
}

// SandboxProviderEnvItems returns the effective explicit provider layer for one
// execution. Execution values override the persisted sandbox layer; Global Env
// and daemon configuration are deliberately not included here.
func SandboxProviderEnvItems(sandbox *domain.Sandbox, providerFamily string) []domain.SandboxEnvVar {
	if sandbox == nil {
		return nil
	}
	providerFamily = NormalizeOptionalProviderType(providerFamily)
	items := domain.MergeEnvItems(sandbox.ProviderEnvItems, sandbox.ExecutionProviderEnvItems)
	filtered := make([]domain.SandboxEnvVar, 0, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		if providerEnvNameForFamily(item.Name, providerFamily) && strings.TrimSpace(item.Value) != "" {
			filtered = append(filtered, item)
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func providerEnvNameForFamily(name, providerFamily string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch NormalizeOptionalProviderType(providerFamily) {
	case ProviderFamilyOpenAI:
		switch name {
		case "LLM_API_ENDPOINT", "LLM_API_PROTOCOL", "LLM_API_KEY", "OPENAI_API_KEY", "LLM_MODEL":
			return true
		}
	case ProviderFamilyAnthropic:
		switch name {
		case "ANTHROPIC_BASE_URL", "ANTHROPIC_API_ENDPOINT", "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_MODEL", "CLAUDE_MODEL":
			return true
		}
	}
	return false
}

func providerEnvironmentLookup(sources providerEnvSources) EnvProviderLookup {
	return func(keys ...string) string {
		return sources.lookup(keys...).value
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
