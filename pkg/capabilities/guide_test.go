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
	guide := QualifyCapabilityGuide([]byte("# Upstream\n\nx-octobus-capset: dev"), "internal/dev", "dev")
	content := string(guide)
	if !strings.Contains(content, "`x-octobus-capset: internal/dev`") || !strings.Contains(content, "x-octobus-capset: dev") {
		t.Fatalf("qualified guide = %s", content)
	}
}
