package llms

import (
	"context"
	"fmt"
	"os"
	"slices"
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

// SetSandboxProviderEnvItems keeps provider values transient while persisting
// the names of non-empty sandbox overrides. Names are sufficient to distinguish
// explicit values from the Global Env values merged into Sandbox.EnvItems.
func SetSandboxProviderEnvItems(sandbox *domain.Sandbox, items []domain.SandboxEnvVar) {
	if sandbox == nil {
		return
	}
	sandbox.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), items...)
	sandbox.ProviderEnvOverrideNames = providerEnvOverrideNames(items)
}

func providerEnvOverrideNames(items []domain.SandboxEnvVar) []string {
	names := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		name := strings.ToUpper(strings.TrimSpace(item.Name))
		if name == "" || strings.TrimSpace(item.Value) == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) == 0 {
		return nil
	}
	return names
}

// SandboxProviderEnvItems reconstructs only the sandbox-owned provider layer.
// Persisted EnvItems also contain the Global Env snapshot from sandbox creation,
// so values are selected by recorded name provenance rather than equality with
// today's mutable Global Env. Explicit provider keys are recovered from the
// session provider row because their values are deliberately absent from
// sandbox metadata.
func SandboxProviderEnvItems(sandbox *domain.Sandbox, providerFamily string, providers []Provider) []domain.SandboxEnvVar {
	if sandbox == nil {
		return nil
	}
	providerFamily = NormalizeOptionalProviderType(providerFamily)
	persisted := make([]domain.SandboxEnvVar, 0, len(sandbox.ProviderEnvOverrideNames))
	providerKey := sessionProviderAPIKey(sandbox.Summary.ID, providerFamily, providers)
	for _, name := range sandbox.ProviderEnvOverrideNames {
		name = strings.ToUpper(strings.TrimSpace(name))
		if name == "" {
			continue
		}
		if item, ok := envItemByName(sandbox.EnvItems, name); ok && strings.TrimSpace(item.Value) != "" {
			persisted = append(persisted, item)
			continue
		}
		if providerKey != "" && providerKeyOverrideName(name, providerFamily) {
			persisted = append(persisted, domain.SandboxEnvVar{Name: name, Value: providerKey, Secret: true})
		}
	}
	return domain.MergeEnvItems(persisted, sandbox.ProviderEnvItems)
}

func envItemByName(items []domain.SandboxEnvVar, name string) (domain.SandboxEnvVar, bool) {
	for _, item := range domain.NormalizeEnvItems(items) {
		if strings.EqualFold(strings.TrimSpace(item.Name), strings.TrimSpace(name)) {
			return item, true
		}
	}
	return domain.SandboxEnvVar{}, false
}

func sessionProviderAPIKey(sessionID, providerFamily string, providers []Provider) string {
	providerID := SessionEnvProviderID(sessionID, providerFamily)
	for _, provider := range providers {
		if provider.ID == providerID {
			return strings.TrimSpace(provider.APIKey)
		}
	}
	return ""
}

func providerKeyOverrideName(name, providerFamily string) bool {
	switch NormalizeOptionalProviderType(providerFamily) {
	case ProviderFamilyOpenAI:
		return name == "LLM_API_KEY" || name == "OPENAI_API_KEY"
	case ProviderFamilyAnthropic:
		return name == "ANTHROPIC_API_KEY" || name == "ANTHROPIC_AUTH_TOKEN"
	default:
		return false
	}
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
