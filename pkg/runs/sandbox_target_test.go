package runs

import (
	"context"
	"errors"
	"testing"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
)

func TestSandboxRunTargetResolverResolveBatch(t *testing.T) {
	legacyProjectID, err := domain.StableProjectID(projects.LegacyDefaultProjectName, "")
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	store := &sandboxRunTargetStoreStub{
		runs: map[string]domain.ProjectRunRecord{
			"run-sandbox": {SandboxID: "run-sandbox", ProjectID: "run-project", AgentName: "run-agent"},
		},
		managedAgents: map[string]domain.ProjectAgentRecord{
			"managed-id": {ProjectID: "managed-project", AgentName: "managed-agent", ManagedAgentID: "managed-id"},
		},
		legacyProjectID: legacyProjectID,
		legacyAgents: []domain.ProjectAgentRecord{
			{ProjectID: legacyProjectID, AgentName: "legacy-agent"},
		},
	}
	resolver, err := NewSandboxRunTargetResolver(store)
	if err != nil {
		t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
	}
	sandboxes := []*domain.Sandbox{
		sandboxWithTags("run-sandbox", domain.SandboxTag{Name: "project", Value: "ignored-project"}),
		sandboxWithTags("tag-sandbox", domain.SandboxTag{Name: "project", Value: "tag-project"}, domain.SandboxTag{Name: "agent", Value: "tag-agent"}),
		sandboxWithTags("legacy-tag-sandbox", domain.SandboxTag{Name: "project_id", Value: "legacy-tag-project"}, domain.SandboxTag{Name: "agent", Value: "legacy-tag-agent"}),
		sandboxWithTags("managed-sandbox", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "managed-id"}),
		sandboxWithTags("legacy-sandbox", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "old-uuid"}, domain.SandboxTag{Name: domain.AgentSandboxTagName, Value: "Legacy-Agent"}),
		sandboxWithTags("unknown-sandbox", domain.SandboxTag{Name: domain.AgentSandboxTagID, Value: "unknown"}),
	}

	targets, err := resolver.ResolveBatch(context.Background(), sandboxes)
	if err != nil {
		t.Fatalf("ResolveBatch returned error: %v", err)
	}
	wants := map[string]SandboxRunTarget{
		"run-sandbox":        {ProjectID: "run-project", AgentName: "run-agent"},
		"tag-sandbox":        {ProjectID: "tag-project", AgentName: "tag-agent"},
		"legacy-tag-sandbox": {ProjectID: "legacy-tag-project", AgentName: "legacy-tag-agent"},
		"managed-sandbox":    {ProjectID: "managed-project", AgentName: "managed-agent"},
		"legacy-sandbox":     {ProjectID: legacyProjectID, AgentName: "legacy-agent"},
	}
	for sandboxID, want := range wants {
		if got := targets[sandboxID]; got != want {
			t.Errorf("target %s = %#v, want %#v", sandboxID, got, want)
		}
	}
	if _, exists := targets["unknown-sandbox"]; exists {
		t.Fatalf("unknown sandbox unexpectedly resolved: %#v", targets["unknown-sandbox"])
	}
	if store.runCalls != 1 || store.managedCalls != 1 || store.legacyCalls != 1 {
		t.Fatalf("store calls = runs:%d managed:%d legacy:%d", store.runCalls, store.managedCalls, store.legacyCalls)
	}
}

func TestSandboxRunTargetResolverLegacyFailurePreservesResolvedTargets(t *testing.T) {
	store := &sandboxRunTargetStoreStub{
		runs: map[string]domain.ProjectRunRecord{
			"resolved": {SandboxID: "resolved", ProjectID: "project", AgentName: "agent"},
		},
		legacyErr: errors.New("legacy lookup failed"),
	}
	resolver, err := NewSandboxRunTargetResolver(store)
	if err != nil {
		t.Fatalf("NewSandboxRunTargetResolver returned error: %v", err)
	}
	targets, err := resolver.ResolveBatch(context.Background(), []*domain.Sandbox{
		sandboxWithTags("resolved"),
		sandboxWithTags("unresolved", domain.SandboxTag{Name: domain.AgentSandboxTagName, Value: "legacy"}),
	})
	if err != nil {
		t.Fatalf("ResolveBatch returned error: %v", err)
	}
	if got := targets["resolved"]; got != (SandboxRunTarget{ProjectID: "project", AgentName: "agent"}) {
		t.Fatalf("resolved target = %#v", got)
	}
	if _, ok := targets["unresolved"]; ok {
		t.Fatalf("unresolved target unexpectedly resolved: %#v", targets)
	}
}

func sandboxWithTags(id string, tags ...domain.SandboxTag) *domain.Sandbox {
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: id, Tags: tags}}
}

type sandboxRunTargetStoreStub struct {
	runs            map[string]domain.ProjectRunRecord
	managedAgents   map[string]domain.ProjectAgentRecord
	legacyProjectID string
	legacyAgents    []domain.ProjectAgentRecord
	legacyErr       error
	runCalls        int
	managedCalls    int
	legacyCalls     int
}

func (s *sandboxRunTargetStoreStub) ListLatestProjectRunsForSandboxes(context.Context, []string) (map[string]domain.ProjectRunRecord, error) {
	s.runCalls++
	return s.runs, nil
}

func (s *sandboxRunTargetStoreStub) ListProjectAgentsByManagedAgentIDs(context.Context, []string) (map[string]domain.ProjectAgentRecord, error) {
	s.managedCalls++
	return s.managedAgents, nil
}

func (s *sandboxRunTargetStoreStub) ListProjectAgents(_ context.Context, projectID string) ([]domain.ProjectAgentRecord, error) {
	s.legacyCalls++
	if s.legacyErr != nil {
		return nil, s.legacyErr
	}
	if projectID != s.legacyProjectID {
		return nil, nil
	}
	return s.legacyAgents, nil
}
