package capabilities

import (
	"context"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/capability"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type guideTestProvider struct {
	globalCalls []string
	scopedCalls []string
	scope       GuideScope
}

func TestGuideScopeFromSandboxUsesCanonicalTagPrecedence(t *testing.T) {
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{Tags: []domain.SandboxTag{
		{Name: "project_id", Value: "legacy-project"},
		{Name: domain.AgentSandboxTagID, Value: "agent-1"},
		{Name: "project", Value: "project-1"},
	}}}
	if scope := GuideScopeFromSandbox(sandbox); scope != (GuideScope{ProjectID: "project-1", AgentID: "agent-1"}) {
		t.Fatalf("scope = %#v", scope)
	}
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

func TestCapabilityGuideForScopeRoutesEveryDeclarationThroughScopedProvider(t *testing.T) {
	provider := &guideTestProvider{}
	scope := GuideScope{ProjectID: "project-1", AgentID: "agent-1", Username: "alice"}
	if _, err := CapabilityGuideForScope(context.Background(), provider, scope, "legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := CapabilityGuideForScope(context.Background(), provider, scope, "internal/dev"); err != nil {
		t.Fatal(err)
	}
	if len(provider.globalCalls) != 0 {
		t.Fatalf("global calls = %#v", provider.globalCalls)
	}
	if len(provider.scopedCalls) != 2 || provider.scopedCalls[0] != "legacy" || provider.scopedCalls[1] != "internal/dev" || provider.scope != scope {
		t.Fatalf("scoped calls = %#v scope=%#v", provider.scopedCalls, provider.scope)
	}
}

type legacyGuideProvider struct {
	globalCalls []string
}

func (*legacyGuideProvider) Status(context.Context) capability.Status { return capability.Status{} }
func (*legacyGuideProvider) ListCapsets(context.Context) ([]capability.Capset, error) {
	return nil, nil
}
func (*legacyGuideProvider) Catalog(context.Context, string) (capability.Catalog, error) {
	return capability.Catalog{}, nil
}
func (*legacyGuideProvider) ProxyTarget() string { return "" }
func (p *legacyGuideProvider) CapabilityGuide(_ context.Context, id string) ([]byte, error) {
	p.globalCalls = append(p.globalCalls, id)
	return []byte("global"), nil
}

func TestCapabilityGuideForScopeFallsBackToLegacyProvider(t *testing.T) {
	provider := &legacyGuideProvider{}
	guide, err := CapabilityGuideForScope(context.Background(), provider, GuideScope{ProjectID: "project-1"}, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	if string(guide) != "global" || len(provider.globalCalls) != 1 || provider.globalCalls[0] != "legacy" {
		t.Fatalf("guide = %q global calls = %#v", guide, provider.globalCalls)
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
