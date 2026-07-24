package runs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/capability"
	domain "agent-compose/pkg/model"
)

type scopedRunGuideProvider struct {
	scope capabilities.GuideScope
}

func (scopedRunGuideProvider) Status(context.Context) capability.Status { return capability.Status{} }
func (scopedRunGuideProvider) ListCapsets(context.Context) ([]capability.Capset, error) {
	return nil, nil
}
func (scopedRunGuideProvider) Catalog(context.Context, string) (capability.Catalog, error) {
	return capability.Catalog{}, nil
}
func (scopedRunGuideProvider) ProxyTarget() string { return "proxy:1" }
func (scopedRunGuideProvider) CapabilityGuide(_ context.Context, id string) ([]byte, error) {
	return []byte("global " + id), nil
}
func (p *scopedRunGuideProvider) CapabilityGuideForScope(_ context.Context, scope capabilities.GuideScope, declaration string) ([]byte, error) {
	p.scope = scope
	if declaration == "public/fail" {
		return nil, errors.New("unavailable")
	}
	return []byte("scoped " + declaration), nil
}

func TestProjectRunCapabilityGuideRoutesManagedScopeAndMergesBestEffort(t *testing.T) {
	provider := &scopedRunGuideProvider{}
	sandbox := &domain.Sandbox{Summary: domain.SandboxSummary{
		ID:            "run-sandbox",
		WorkspacePath: filepath.Join(t.TempDir(), "workspace"),
		Tags: []domain.SandboxTag{
			{Name: "project", Value: "project-1"},
			{Name: domain.AgentSandboxTagID, Value: "agent-1"},
		},
	}}

	writeCapabilityGuide(context.Background(), provider, nil, nil, sandbox, []string{"legacy", "internal/dev", "public/fail"})
	guide, err := os.ReadFile(capabilities.SandboxGuidePath(sandbox))
	if err != nil {
		t.Fatalf("read merged guide: %v", err)
	}
	if provider.scope != (capabilities.GuideScope{ManagedProjectID: "project-1", ManagedAgentID: "agent-1"}) {
		t.Fatalf("scope = %#v", provider.scope)
	}
	if content := string(guide); !strings.Contains(content, "global legacy") || !strings.Contains(content, "scoped internal/dev") {
		t.Fatalf("merged guide = %s", content)
	}
}
