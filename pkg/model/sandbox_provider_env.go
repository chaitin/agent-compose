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

// DeclaredProviderEnv returns the provider environment one execution declares
// for its agent: the environment the sandbox was prepared with, plus the agent
// definition's own environment.
//
// Every entry point that prepares an agent's LLM configuration (sandbox start,
// release resume, run, prompt attach, scheduler command) decides direct versus
// managed from this declaration, so no entry point can disagree with another
// about which side owns the upstream.
//
// The prepared values are transient by design: they may hold credentials, so
// only their names survive in ProviderEnvOverrideNames and ProviderEnvItems is
// empty on a sandbox loaded back from storage. Merging the definition's
// environment keeps the part of the declaration that is persisted in play there,
// which is why this merges rather than replaces.
func (s *Sandbox) DeclaredProviderEnv(agentEnv []SandboxEnvVar) []SandboxEnvVar {
	if s == nil {
		return MergeEnvItems(nil, agentEnv)
	}
	return MergeEnvItems(s.ProviderEnvItems, agentEnv)
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
