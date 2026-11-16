package capmatrix

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/guestabi"
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

// manifestWithRequiredProviders builds a manifest covering every required
// provider so a case can vary one provider without repeating the whole set.
func manifestWithRequiredProviders() guestabi.Manifest {
	return guestabi.Manifest{
		GuestAbi: guestabi.CurrentGuestAbi,
		Providers: map[string]guestabi.Provider{
			"codex":    {Version: "0.146.0", Features: []string{"structured_output", "session_resume", "streaming"}},
			"claude":   {Version: "2.1.220", Features: []string{"structured_output", "session_resume", "streaming", "skill_injection"}},
			"opencode": {Version: "1.18.9", Features: []string{"session_resume", "streaming", "skill_injection"}},
			"pi":       {Version: "1.3.0", Features: []string{"session_resume", "streaming", "skill_injection"}},
			"dsh":      {Version: "0.2.0-rc.1", Features: []string{"session_resume", "streaming", "skill_injection"}},
		},
	}
}

// TestDeclaredProvidersFromManifest asserts the manifest is mapped into the
// canonical provider order and that only feature names this daemon understands
// become supported.
func TestDeclaredProvidersFromManifest(t *testing.T) {
	manifest := manifestWithRequiredProviders()
	// pi advertises one feature this daemon understands plus one it does not;
	// dsh advertises none it understands.
	manifest.Providers["pi"] = guestabi.Provider{Version: "1.3.0", Features: []string{"session_resume", "telepathy"}}
	manifest.Providers["dsh"] = guestabi.Provider{Version: "0.2.0-rc.1"}

	providers, err := DeclaredProvidersFromManifest(manifest)
	if err != nil {
		t.Fatalf("DeclaredProvidersFromManifest() error = %v", err)
	}
	gotOrder := make([]string, 0, len(providers))
	byName := make(map[string]ProviderCapabilities, len(providers))
	for _, provider := range providers {
		gotOrder = append(gotOrder, provider.Provider)
		byName[provider.Provider] = provider
	}
	if want := []string{"codex", "claude", "opencode", "pi", "dsh"}; !reflect.DeepEqual(gotOrder, want) {
		t.Fatalf("provider order = %v, want the canonical order %v", gotOrder, want)
	}

	for provider, want := range map[string]map[ExecutionFeature]bool{
		"codex":    {FeatureStructuredOutput: true, FeatureSkillInjection: false},
		"claude":   {FeatureStructuredOutput: true, FeatureSkillInjection: true},
		"opencode": {FeatureStructuredOutput: false, FeatureSkillInjection: true},
		"pi":       {FeatureSessionResume: true, FeatureSkillInjection: false},
	} {
		for feature, wantSupported := range want {
			if got := byName[provider].Feature(feature); got != wantSupported {
				t.Errorf("provider %q feature %q = %v, want %v", provider, feature, got, wantSupported)
			}
		}
	}
	for _, feature := range RequiredExecutionFeatures() {
		if byName["dsh"].Feature(feature) {
			t.Errorf("provider %q with no understood feature reports %q = true, want false", "dsh", feature)
		}
	}
}

// TestDeclaredProvidersFromManifestProtocolOrderMatchesDialect pins a
// manifest-derived matrix to the same protocol order the runtime resolution path
// uses, so the two sources cannot drift.
func TestDeclaredProvidersFromManifestProtocolOrderMatchesDialect(t *testing.T) {
	providers, err := DeclaredProvidersFromManifest(manifestWithRequiredProviders())
	if err != nil {
		t.Fatalf("DeclaredProvidersFromManifest() error = %v", err)
	}
	for _, provider := range providers {
		dialect, err := llms.DialectFor(provider.Provider)
		if err != nil {
			t.Fatalf("DialectFor(%q) error = %v", provider.Provider, err)
		}
		want := make([]string, 0, len(dialect.Supported))
		for _, protocol := range dialect.PreferredProtocols() {
			want = append(want, string(protocol))
		}
		if !reflect.DeepEqual(provider.PreferredProtocols, want) {
			t.Errorf("provider %q PreferredProtocols = %v, want the dialect order %v", provider.Provider, provider.PreferredProtocols, want)
		}
	}
}

func TestDeclaredProvidersFromManifestRejectsMissingProvider(t *testing.T) {
	manifest := manifestWithRequiredProviders()
	delete(manifest.Providers, "pi")

	providers, err := DeclaredProvidersFromManifest(manifest)
	if err == nil {
		t.Fatal("DeclaredProvidersFromManifest() error = nil, want a missing-provider error")
	}
	if !errors.Is(err, ErrInvalidCapability) {
		t.Fatalf("error = %v, want ErrInvalidCapability", err)
	}
	if providers != nil {
		t.Fatalf("providers = %v, want no partial list", providers)
	}
}

func TestDeclaredProvidersFromManifestRejectsInvalidManifest(t *testing.T) {
	manifest := manifestWithRequiredProviders()
	manifest.GuestAbi = 0

	providers, err := DeclaredProvidersFromManifest(manifest)
	if err == nil {
		t.Fatal("DeclaredProvidersFromManifest() error = nil, want a validation error")
	}
	if providers != nil {
		t.Fatalf("providers = %v, want no partial list", providers)
	}
}

// TestCommittedGuestABIManifestMatchesDeclaredFeatures is the drift guard for
// the committed manifest: it must declare this release's ABI and exactly the
// feature set declaredExecutionFeatures reports. The runner-source test above
// pins that declaration to the actual guest runners, so together they stop
// guest-images/guest-abi.json from lying about this release's runners.
func TestCommittedGuestABIManifestMatchesDeclaredFeatures(t *testing.T) {
	manifest := committedGuestABIManifest(t)
	if manifest.GuestAbi != guestabi.CurrentGuestAbi {
		t.Fatalf("guest-images/guest-abi.json declares guestAbi %d, this daemon speaks %d", manifest.GuestAbi, guestabi.CurrentGuestAbi)
	}
	knownFeatures := make(map[ExecutionFeature]bool, len(canonicalFeatures))
	for _, feature := range canonicalFeatures {
		knownFeatures[feature] = true
	}
	for _, provider := range canonicalProviderOrder {
		declared, ok := manifest.Providers[provider]
		if !ok {
			t.Errorf("guest-images/guest-abi.json omits required provider %q", provider)
			continue
		}
		want := declaredExecutionFeatures(provider)
		if want == nil {
			t.Errorf("provider %q has no declared execution features", provider)
			continue
		}
		got := make(map[ExecutionFeature]bool, len(declared.Features))
		for _, name := range declared.Features {
			feature := ExecutionFeature(name)
			if !knownFeatures[feature] {
				t.Errorf("guest-images/guest-abi.json provider %q declares unknown feature %q", provider, name)
			}
			got[feature] = true
		}
		for _, feature := range canonicalFeatures {
			if got[feature] != want[feature] {
				t.Errorf("guest-images/guest-abi.json provider %q feature %q = %v, but declaredExecutionFeatures(%q) = %v; update the manifest or the declaration",
					provider, feature, got[feature], provider, want[feature])
			}
		}
	}
}

func committedGuestABIManifest(t *testing.T) guestabi.Manifest {
	t.Helper()
	path := filepath.Join("..", "..", "guest-images", "guest-abi.json")
	manifest, err := guestabi.LoadFile(path)
	if err != nil {
		t.Fatalf("load committed guest ABI manifest %s: %v", path, err)
	}
	return manifest
}
