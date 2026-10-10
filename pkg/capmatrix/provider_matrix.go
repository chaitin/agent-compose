package capmatrix

import (
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// ExecutionFeature is a stable key for one provider execution capability. The
// names match the guest runner options in runtime/javascript/src/runners.
type ExecutionFeature string

const (
	// FeatureStructuredOutput covers a provider accepting a JSON schema and
	// returning schema-validated structured output.
	FeatureStructuredOutput ExecutionFeature = "structured_output"
	// FeatureSessionResume covers resuming a previously stored provider
	// session instead of starting a fresh one.
	FeatureSessionResume ExecutionFeature = "session_resume"
	// FeatureStreaming covers the provider emitting incremental
	// provider-neutral events while a turn runs.
	FeatureStreaming ExecutionFeature = "streaming"
	// FeatureSkillInjection covers the engine passing resolved skill
	// directories to the provider CLI.
	FeatureSkillInjection ExecutionFeature = "skill_injection"
)

var canonicalFeatures = []ExecutionFeature{
	FeatureStructuredOutput,
	FeatureSessionResume,
	FeatureStreaming,
	FeatureSkillInjection,
}

// RequiredExecutionFeatures returns every provider execution feature the
// engine reports, in canonical order.
func RequiredExecutionFeatures() []ExecutionFeature {
	return append([]ExecutionFeature(nil), canonicalFeatures...)
}

// ProviderFeature is one execution feature's support claim.
type ProviderFeature struct {
	Feature   ExecutionFeature
	Supported bool
}

// ProviderCapabilities is one guest provider's LLM capability declaration.
type ProviderCapabilities struct {
	// Provider is the normalized provider key, for example "opencode".
	Provider string
	// PreferredProtocols is the upstream protocol preference order, most
	// preferred first. It is derived from pkg/llms so the two cannot drift.
	PreferredProtocols []string
	// Features covers RequiredExecutionFeatures exactly once each.
	Features []ProviderFeature
}

// Validate rejects a provider declaration that omits a required feature or
// names no protocol.
func (p ProviderCapabilities) Validate() error {
	if strings.TrimSpace(p.Provider) == "" {
		return fmt.Errorf("%w: provider name is empty", ErrInvalidCapability)
	}
	if len(p.PreferredProtocols) == 0 {
		return fmt.Errorf("%w: provider %q declares no preferred protocol", ErrInvalidCapability, p.Provider)
	}
	seen := make(map[ExecutionFeature]struct{}, len(p.Features))
	for _, feature := range p.Features {
		if _, duplicate := seen[feature.Feature]; duplicate {
			return fmt.Errorf("%w: provider %q declares feature %q more than once", ErrInvalidCapability, p.Provider, feature.Feature)
		}
		seen[feature.Feature] = struct{}{}
	}
	for _, feature := range canonicalFeatures {
		if _, ok := seen[feature]; !ok {
			return fmt.Errorf("%w: provider %q does not declare required feature %q", ErrInvalidCapability, p.Provider, feature)
		}
	}
	return nil
}

// Feature reports whether the provider supports feature.
func (p ProviderCapabilities) Feature(feature ExecutionFeature) bool {
	for _, candidate := range p.Features {
		if candidate.Feature == feature {
			return candidate.Supported
		}
	}
	return false
}

// declaredExecutionFeatures is the static guest-provider feature matrix.
//
// It is a declaration about runtime/javascript runners, not a probe: the
// runners are guest code the daemon cannot introspect. structured_output is
// supported only where the runner passes a schema through (codex, claude);
// opencode, pi, and dsh reject it explicitly. skill_injection is supported
// everywhere except codex, which has no skill wiring.
func declaredExecutionFeatures(provider string) map[ExecutionFeature]bool {
	switch provider {
	case "codex":
		return map[ExecutionFeature]bool{
			FeatureStructuredOutput: true,
			FeatureSessionResume:    true,
			FeatureStreaming:        true,
			FeatureSkillInjection:   false,
		}
	case "claude":
		return map[ExecutionFeature]bool{
			FeatureStructuredOutput: true,
			FeatureSessionResume:    true,
			FeatureStreaming:        true,
			FeatureSkillInjection:   true,
		}
	case "opencode":
		return map[ExecutionFeature]bool{
			FeatureStructuredOutput: false,
			FeatureSessionResume:    true,
			FeatureStreaming:        true,
			FeatureSkillInjection:   true,
		}
	case "pi", "dsh":
		return map[ExecutionFeature]bool{
			FeatureStructuredOutput: false,
			FeatureSessionResume:    true,
			FeatureStreaming:        true,
			FeatureSkillInjection:   true,
		}
	default:
		return nil
	}
}

// DeclaredProviders returns the provider capability matrix for every guest
// provider the engine supports. Protocol preference order is read from
// pkg/llms Dialect.PreferredProtocols so the RPC and the runtime resolution
// path cannot disagree.
func DeclaredProviders() ([]ProviderCapabilities, error) {
	providers := []string{"codex", "claude", "opencode", "pi", "dsh"}
	out := make([]ProviderCapabilities, 0, len(providers))
	for _, provider := range providers {
		dialect, err := llms.DialectFor(provider)
		if err != nil {
			return nil, fmt.Errorf("resolve dialect for provider %q: %w", provider, err)
		}
		preference := dialect.PreferredProtocols()
		protocols := make([]string, 0, len(preference))
		for _, protocol := range preference {
			protocols = append(protocols, string(protocol))
		}
		features := declaredExecutionFeatures(provider)
		if features == nil {
			return nil, fmt.Errorf("%w: provider %q has no declared execution features", ErrInvalidCapability, provider)
		}
		declaration := ProviderCapabilities{Provider: provider, PreferredProtocols: protocols}
		for _, feature := range canonicalFeatures {
			declaration.Features = append(declaration.Features, ProviderFeature{Feature: feature, Supported: features[feature]})
		}
		if err := declaration.Validate(); err != nil {
			return nil, err
		}
		out = append(out, declaration)
	}
	return out, nil
}
