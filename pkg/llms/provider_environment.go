package llms

import (
	"context"
	"slices"
	"strings"

	domain "agent-compose/pkg/model"
)

// SetSandboxProviderEnvItems assigns the transient sandbox-owned provider
// environment and records only its non-empty names for restart recovery.
// Provider values must never be persisted in sandbox metadata.
func SetSandboxProviderEnvItems(sandbox *domain.Sandbox, items []domain.SandboxEnvVar) {
	if sandbox == nil {
		return
	}
	sandbox.ProviderEnvItems = append([]domain.SandboxEnvVar(nil), domain.NormalizeEnvItems(items)...)
	sandbox.ProviderEnvOverrideNames = providerEnvOverrideNames(items)
}

func providerEnvOverrideNames(items []domain.SandboxEnvVar) []string {
	names := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		name := strings.ToUpper(strings.TrimSpace(item.Name))
		if !providerEnvName(name) || strings.TrimSpace(item.Value) == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	slices.Sort(names)
	// Preserve a non-nil empty slice. JSON [] distinguishes a sandbox with no
	// overrides from metadata without provenance, where the field is absent/null.
	return names
}

// SandboxProviderEnvItems returns the sandbox-owned provider layer for one LLM
// family. For current executions, transient values win. After restart, values
// are reconstructed from recorded names, persisted non-secret env, and the
// family-scoped session provider row that owns the credential.
func SandboxProviderEnvItems(ctx context.Context, store ProviderListStore, sandbox *domain.Sandbox, providerFamily string) ([]domain.SandboxEnvVar, error) {
	if sandbox == nil {
		return nil, nil
	}
	providerFamily = NormalizeOptionalProviderType(providerFamily)
	if sandbox.ProviderEnvOverrideNames == nil {
		// Metadata written before provenance tracking has no source information.
		// Preserve its environment snapshot while removing names owned by the
		// opposite provider family.
		items := sandbox.ProviderEnvItems
		if len(items) == 0 {
			items = sandbox.EnvItems
		}
		return filterProviderEnvItems(items, providerFamily), nil
	}

	persisted := make([]domain.SandboxEnvVar, 0, len(sandbox.ProviderEnvOverrideNames))
	missingProviderKeys := make([]string, 0, 1)
	for _, name := range sandbox.ProviderEnvOverrideNames {
		name = strings.ToUpper(strings.TrimSpace(name))
		if name == "" || !providerEnvNameForFamily(name, providerFamily) {
			continue
		}
		if item, ok := envItemByName(sandbox.EnvItems, name); ok && strings.TrimSpace(item.Value) != "" {
			persisted = append(persisted, item)
			continue
		}
		if item, ok := envItemByName(sandbox.ProviderEnvItems, name); ok && strings.TrimSpace(item.Value) != "" {
			continue
		}
		if providerKeyOverrideName(name, providerFamily) {
			missingProviderKeys = append(missingProviderKeys, name)
		}
	}

	if len(missingProviderKeys) > 0 && store != nil {
		providers, err := store.ListEnabledLLMProviders(ctx)
		if err != nil {
			return nil, err
		}
		providerItems := domain.MergeEnvItems(persisted, sandbox.ProviderEnvItems)
		for _, name := range missingProviderKeys {
			ownerFamily := providerKeyOwnerFamily(name, providerFamily, providerItems)
			providerKey := sessionProviderAPIKey(sandbox.Summary.ID, ownerFamily, providers)
			if providerKey != "" {
				persisted = append(persisted, domain.SandboxEnvVar{Name: name, Value: providerKey, Secret: true})
			}
		}
	}

	return filterProviderEnvItems(domain.MergeEnvItems(persisted, sandbox.ProviderEnvItems), providerFamily), nil
}

func filterProviderEnvItems(items []domain.SandboxEnvVar, providerFamily string) []domain.SandboxEnvVar {
	providerFamily = NormalizeOptionalProviderType(providerFamily)
	filtered := make([]domain.SandboxEnvVar, 0, len(items))
	for _, item := range domain.NormalizeEnvItems(items) {
		if providerEnvNameForFamily(item.Name, providerFamily) {
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
	switch {
	case strings.HasPrefix(name, "ANTHROPIC_"), name == "CLAUDE_MODEL":
		return providerFamily == "" || providerFamily == ProviderFamilyAnthropic
	case strings.HasPrefix(name, "OPENAI_"), strings.HasPrefix(name, "AZURE_OPENAI_"), strings.HasPrefix(name, "OPENROUTER_"):
		return providerFamily == "" || providerFamily == ProviderFamilyOpenAI
	default:
		// LLM_* is intentionally generic and remains visible to both families.
		return strings.HasPrefix(name, "LLM_")
	}
}

func providerEnvName(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	return strings.HasPrefix(name, "LLM_") ||
		strings.HasPrefix(name, "OPENAI_") ||
		strings.HasPrefix(name, "ANTHROPIC_") ||
		strings.HasPrefix(name, "AZURE_OPENAI_") ||
		strings.HasPrefix(name, "OPENROUTER_") ||
		name == "CLAUDE_MODEL"
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
	name = strings.ToUpper(strings.TrimSpace(name))
	switch NormalizeOptionalProviderType(providerFamily) {
	case ProviderFamilyOpenAI:
		return name == "LLM_API_KEY" || name == "OPENAI_API_KEY"
	case ProviderFamilyAnthropic:
		return name == "LLM_API_KEY" || name == "ANTHROPIC_API_KEY" || name == "ANTHROPIC_AUTH_TOKEN"
	default:
		return false
	}
}

func providerKeyOwnerFamily(name, requestedFamily string, envItems []domain.SandboxEnvVar) string {
	name = strings.ToUpper(strings.TrimSpace(name))
	switch name {
	case "LLM_API_KEY":
		if family := genericLLMEnvProviderFamily(envItems); family != "" {
			return family
		}
		return NormalizeOptionalProviderType(requestedFamily)
	case "OPENAI_API_KEY":
		return ProviderFamilyOpenAI
	case "ANTHROPIC_API_KEY", "ANTHROPIC_AUTH_TOKEN":
		return ProviderFamilyAnthropic
	default:
		return ""
	}
}
