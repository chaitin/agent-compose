package model

import (
	"slices"
	"strings"
)

// SetProviderEnvItems assigns the transient sandbox-owned provider environment
// and records only its non-empty provider names for restart recovery. Provider
// values must never be persisted in sandbox metadata.
func (s *Sandbox) SetProviderEnvItems(items []SandboxEnvVar) {
	if s == nil {
		return
	}
	s.ProviderEnvItems = append([]SandboxEnvVar(nil), NormalizeEnvItems(items)...)
	s.ProviderEnvOverrideNames = providerEnvOverrideNames(items)
}

func providerEnvOverrideNames(items []SandboxEnvVar) []string {
	names := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range NormalizeEnvItems(items) {
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

func providerEnvName(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	return strings.HasPrefix(name, "LLM_") ||
		strings.HasPrefix(name, "OPENAI_") ||
		strings.HasPrefix(name, "ANTHROPIC_") ||
		strings.HasPrefix(name, "AZURE_OPENAI_") ||
		strings.HasPrefix(name, "OPENROUTER_") ||
		name == "CLAUDE_MODEL"
}
