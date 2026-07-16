package api

import (
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestSandboxToV2IncludesWorkspaceReclamation(t *testing.T) {
	started := time.Date(2026, 7, 17, 1, 2, 3, 0, time.UTC)
	completed := started.Add(time.Minute)
	got := sandboxToV2(&domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-1"},
		WorkspaceReclamation: &domain.SandboxWorkspaceReclamation{
			State: domain.SandboxWorkspaceReclamationStateReclaimed, StartedAt: started, CompletedAt: completed,
		},
	})
	if got.GetWorkspaceReclamationState() != domain.SandboxWorkspaceReclamationStateReclaimed ||
		!got.GetWorkspaceReclamationStartedAt().AsTime().Equal(started) ||
		!got.GetWorkspaceReclamationCompletedAt().AsTime().Equal(completed) {
		t.Fatalf("workspace reclamation = %#v", got)
	}
}
