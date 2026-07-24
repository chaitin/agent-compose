package capabilities

import (
	"context"
	"strings"
	"testing"

	"agent-compose/pkg/capability"
)

type guideTestProvider struct {
	globalCalls []string
	scopedCalls []string
	scope       GuideScope
}

func (*guideTestProvider) Status(context.Context) capability.Status                 { return capability.Status{} }
func (*guideTestProvider) ListCapsets(context.Context) ([]capability.Capset, error) { return nil, nil }
func (*guideTestProvider) Catalog(context.Context, string) (capability.Catalog, error) {
	return capability.Catalog{}, nil
}
func (p *guideTestProvider) CapabilityGuide(_ context.Context, id string) ([]byte, error) {
	p.globalCalls = append(p.globalCalls, id)
	return []byte("global"), nil
}
func (*guideTestProvider) ProxyTarget() string { return "proxy:1" }
func (p *guideTestProvider) CapabilityGuideForScope(_ context.Context, scope GuideScope, declaration string) ([]byte, error) {
	p.scope = scope
	p.scopedCalls = append(p.scopedCalls, declaration)
	return []byte("scoped"), nil
}

func TestCapabilityGuideForScopePreservesLegacyAndRoutesQualifiedDeclarations(t *testing.T) {
	provider := &guideTestProvider{}
	scope := GuideScope{ManagedProjectID: "project-1", ManagedAgentID: "agent-1"}
	if _, err := CapabilityGuideForScope(context.Background(), provider, scope, "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := CapabilityGuideForScope(context.Background(), provider, scope, "internal/dev"); err != nil {
		t.Fatal(err)
	}
	if len(provider.globalCalls) != 1 || provider.globalCalls[0] != "legacy" {
		t.Fatalf("global calls = %#v", provider.globalCalls)
	}
	if len(provider.scopedCalls) != 1 || provider.scopedCalls[0] != "internal/dev" || provider.scope != scope {
		t.Fatalf("scoped calls = %#v scope=%#v", provider.scopedCalls, provider.scope)
	}
}

func TestQualifyCapabilityGuidePresentsAuthoritativeDeclaration(t *testing.T) {
	guide := QualifyCapabilityGuide([]byte("# Upstream\n\nx-octobus-capset: dev\n\nUse `x-octobus-capset: dev` when calling.\n\nThe text x-octobus-capset: dev is descriptive.\nother-field: dev\ncapset: dev-other"), "internal/dev", "dev")
	content := string(guide)
	if strings.Count(content, "x-octobus-capset: internal/dev") != 3 {
		t.Fatalf("qualified guide assignments = %s", content)
	}
	for _, unchanged := range []string{"The text x-octobus-capset: dev is descriptive.", "other-field: dev", "capset: dev-other"} {
		if !strings.Contains(content, unchanged) {
			t.Fatalf("qualified guide changed non-metadata content %q: %s", unchanged, content)
		}
	}
	if strings.Contains(content, "\nx-octobus-capset: dev\n") || strings.Contains(content, "`x-octobus-capset: dev`") {
		t.Fatalf("qualified guide = %s", content)
	}
}

func TestCapabilityGuideForScopeRejectsUnsafeDeclaration(t *testing.T) {
	provider := &guideTestProvider{}
	for _, declaration := range []string{"internal/dev\nother", "internal/dev\x00", "internal/`dev`"} {
		if _, err := CapabilityGuideForScope(context.Background(), provider, GuideScope{}, declaration); err == nil {
			t.Fatalf("CapabilityGuideForScope(%q) returned nil error", declaration)
		}
	}
	if len(provider.globalCalls) != 0 || len(provider.scopedCalls) != 0 {
		t.Fatalf("unsafe declarations reached provider: global=%#v scoped=%#v", provider.globalCalls, provider.scopedCalls)
	}
}
