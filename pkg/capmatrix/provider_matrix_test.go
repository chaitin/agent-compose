package capmatrix

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// TestDeclaredProviderFeatureMatrix cross-asserts the execution feature matrix
// against the guest runner sources in runtime/javascript/src/runners, which are
// the authority for what a provider can actually do. A runner that starts or
// stops rejecting a schema, resuming a session, injecting skills, or emitting
// streaming events changes its marker and fails this test until the declaration
// is updated.
func TestDeclaredProviderFeatureMatrix(t *testing.T) {
	providers, err := DeclaredProviders()
	if err != nil {
		t.Fatalf("DeclaredProviders() error = %v", err)
	}
	if len(providers) == 0 {
		t.Fatal("DeclaredProviders() returned no providers")
	}
	for _, provider := range providers {
		source := runnerSource(t, provider.Provider)
		derived := map[ExecutionFeature]bool{
			FeatureStructuredOutput: !strings.Contains(source, "structured JSON output is not supported by"),
			FeatureSessionResume:    strings.Contains(source, "readStoredThread"),
			FeatureStreaming:        strings.Contains(source, "this.emit("),
			FeatureSkillInjection:   strings.Contains(source, "options.skills"),
		}
		for _, feature := range RequiredExecutionFeatures() {
			if got := provider.Feature(feature); got != derived[feature] {
				t.Errorf("provider %q feature %q = %v, but runtime/javascript/src/runners/%s.ts derives %v; update the declaration or the runner",
					provider.Provider, feature, got, provider.Provider, derived[feature])
			}
		}
	}
}

// runnerSource reads one guest runner file. Go tests run with the package
// directory as the working directory, so the repository-relative path is
// stable.
func runnerSource(t *testing.T, provider string) string {
	t.Helper()
	path := filepath.Join("..", "..", "runtime", "javascript", "src", "runners", provider+".ts")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read guest runner %s: %v", path, err)
	}
	return string(data)
}

func TestDeclaredProvidersRejectsMissingFeature(t *testing.T) {
	declaration := ProviderCapabilities{Provider: "codex", PreferredProtocols: []string{"responses"}}
	if err := declaration.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want a missing-feature error")
	}
}
