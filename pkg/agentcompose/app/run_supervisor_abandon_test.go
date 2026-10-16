package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

// TestRunSupervisorAbandonRunCompletionForwardsToTheCompletionManager pins the
// app-layer half of the operator exit for a run whose sandbox cleanup cannot
// converge (issue #771): the supervisor must reach the staged completion, and a
// supervisor built without the completion manager must refuse rather than report
// a run as abandoned.
func TestRunSupervisorAbandonRunCompletionForwardsToTheCompletionManager(t *testing.T) {
	ctx := context.Background()
	store := newRunSupervisorTestConfigStore(t)
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/project", SourceJSON: `{}`}); err != nil {
		t.Fatalf("UpsertProject: %v", err)
	}
	if _, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ID: "agent-1", ProjectID: "project-1", AgentName: "worker"}); err != nil {
		t.Fatalf("UpsertProjectAgent: %v", err)
	}
	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-stuck", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, CleanupPolicy: domain.ProjectRunCleanupStopOnCompletion,
	})
	if err != nil {
		t.Fatalf("CreateProjectRun: %v", err)
	}
	completions := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	if err := completions.StageInterrupted(ctx, run, "daemon interrupted"); err != nil {
		t.Fatalf("StageInterrupted: %v", err)
	}

	supervisor := &RunSupervisor{completions: completions}
	finalized, abandoned, err := supervisor.AbandonRunCompletion(ctx, run.RunID, "cluster gone")
	if err != nil || !abandoned {
		t.Fatalf("AbandonRunCompletion = %#v abandoned=%v err=%v, want the staged completion abandoned", finalized, abandoned, err)
	}
	if finalized.Status != domain.ProjectRunStatusFailed || !strings.Contains(finalized.CleanupError, "may still exist") {
		t.Fatalf("finalized run = %#v, want a terminal run recording that the runtime may still exist", finalized)
	}

	unwired := &RunSupervisor{}
	if _, abandoned, err := unwired.AbandonRunCompletion(ctx, run.RunID, ""); !errors.Is(err, domain.ErrFailedPrecondition) || abandoned {
		t.Fatalf("unwired AbandonRunCompletion abandoned=%v err=%v, want a failed precondition", abandoned, err)
	}
}
