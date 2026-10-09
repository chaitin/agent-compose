package capmatrix

import (
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/llms"
)

// TestDeclaredProviderProtocolOrderMatchesDialect pins the RPC's protocol
// preference order to the implementation the runtime resolution path uses.
func TestDeclaredProviderProtocolOrderMatchesDialect(t *testing.T) {
	providers, err := DeclaredProviders()
	if err != nil {
		t.Fatalf("DeclaredProviders() error = %v", err)
	}
	if len(providers) != 5 {
		t.Fatalf("DeclaredProviders() returned %d providers, want 5", len(providers))
	}
	byName := make(map[string]ProviderCapabilities, len(providers))
	for _, provider := range providers {
		byName[provider.Provider] = provider
	}

	for _, name := range []string{"codex", "claude", "opencode", "pi", "dsh"} {
		t.Run(name, func(t *testing.T) {
			declaration, ok := byName[name]
			if !ok {
				t.Fatalf("provider %q is not declared", name)
			}
			dialect, err := llms.DialectFor(name)
			if err != nil {
				t.Fatalf("DialectFor(%q) error = %v", name, err)
			}
			want := make([]string, 0, len(dialect.Supported))
			for _, protocol := range dialect.PreferredProtocols() {
				want = append(want, string(protocol))
			}
			if !reflect.DeepEqual(declaration.PreferredProtocols, want) {
				t.Fatalf("PreferredProtocols = %v, want the dialect order %v", declaration.PreferredProtocols, want)
			}
		})
	}
}

// TestDeclaredProviderFeatureMatrix pins the execution feature matrix to the
// guest runner behavior in runtime/javascript/src/runners.
func TestDeclaredProviderFeatureMatrix(t *testing.T) {
	providers, err := DeclaredProviders()
	if err != nil {
		t.Fatalf("DeclaredProviders() error = %v", err)
	}
	want := map[string]map[ExecutionFeature]bool{
		"codex": {
			FeatureStructuredOutput: true, FeatureSessionResume: true, FeatureStreaming: true, FeatureSkillInjection: false,
		},
		"claude": {
			FeatureStructuredOutput: true, FeatureSessionResume: true, FeatureStreaming: true, FeatureSkillInjection: true,
		},
		"opencode": {
			FeatureStructuredOutput: false, FeatureSessionResume: true, FeatureStreaming: true, FeatureSkillInjection: true,
		},
		"pi": {
			FeatureStructuredOutput: false, FeatureSessionResume: true, FeatureStreaming: true, FeatureSkillInjection: true,
		},
		"dsh": {
			FeatureStructuredOutput: false, FeatureSessionResume: true, FeatureStreaming: true, FeatureSkillInjection: true,
		},
	}
	for _, provider := range providers {
		expected, ok := want[provider.Provider]
		if !ok {
			t.Fatalf("unexpected provider %q", provider.Provider)
		}
		for _, feature := range RequiredExecutionFeatures() {
			if got := provider.Feature(feature); got != expected[feature] {
				t.Errorf("provider %q feature %q = %v, want %v", provider.Provider, feature, got, expected[feature])
			}
		}
	}
}

func TestDeclaredProvidersRejectsMissingFeature(t *testing.T) {
	declaration := ProviderCapabilities{Provider: "codex", PreferredProtocols: []string{"responses"}}
	if err := declaration.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a missing-feature error")
	}
}
