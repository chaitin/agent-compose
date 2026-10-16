package runs_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

// stageStuckCompletion leaves a run non-terminal with a staged completion whose
// cleanup keeps failing - the state issue #771 reports as having no operator
// exit. Cancelling during the cleanup attempt stops Complete's wait loop after
// that first recorded failure instead of retrying up to the bound.
func stageStuckCompletion(t *testing.T, store *configstore.ConfigStore, run domain.ProjectRunRecord) {
	t.Helper()
	timeoutCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{
		Store:     store,
		Sandboxes: completionSandboxStoreFake{sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: run.SandboxID, VMStatus: domain.VMStatusRunning}}},
		Lifecycle: &completionStopperFake{err: errors.New("cluster unreachable"), onStop: cancel},
	})
	staged, err := manager.Complete(timeoutCtx, runs.TransitionRequest{
		RunID: run.RunID, Status: domain.ProjectRunStatusFailed, Error: "agent failed",
	})
	if err != nil {
		t.Fatalf("stage stuck completion: %v", err)
	}
	if staged.Status != domain.ProjectRunStatusRunning || staged.CleanupError != "cluster unreachable" {
		t.Fatalf("staged run = %#v, want a running run recording the failed cleanup", staged)
	}
	journal, err := store.GetProjectRunCompletion(context.Background(), run.RunID)
	if err != nil || journal.Attempt != 1 || journal.LastError != "cluster unreachable" {
		t.Fatalf("staged completion = %#v err=%v, want one failed attempt", journal, err)
	}
}

// TestCompletionManagerAbandonCompletionEndsARunWhoseCleanupCannotConverge pins
// the operator exit issue #771 asks for: a run whose staged sandbox cleanup will
// not converge must be endable, must reach the terminal state its first
// completion writer selected, and must record that the runtime may still exist
// instead of pretending the cleanup happened.
func TestCompletionManagerAbandonCompletionEndsARunWhoseCleanupCannotConverge(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "abandon-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, SandboxID: "sandbox-1", CleanupPolicy: domain.ProjectRunCleanupStopOnCompletion,
	})
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	stageStuckCompletion(t, store, run)

	finalized, abandoned, err := manager.AbandonCompletion(ctx, run.RunID, "cluster is gone")
	if err != nil || !abandoned {
		t.Fatalf("AbandonCompletion = %#v abandoned=%v err=%v, want an abandoned completion", finalized, abandoned, err)
	}
	if finalized.Status != domain.ProjectRunStatusFailed || finalized.CompletedAt.IsZero() {
		t.Fatalf("finalized run = %#v, want the failed terminal state with a completion time", finalized)
	}
	for _, want := range []string{"may still exist", "last cleanup error: cluster unreachable", "operator note: cluster is gone"} {
		if !strings.Contains(finalized.CleanupError, want) {
			t.Fatalf("cleanup error = %q, want it to contain %q", finalized.CleanupError, want)
		}
	}
	if _, err := store.GetProjectRunCompletion(ctx, run.RunID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("staged completion after abandon = %v, want it released", err)
	}
	events, err := store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
	if err != nil {
		t.Fatalf("ListProjectRunEvents: %v", err)
	}
	var statusEvents int
	for _, event := range events {
		if event.Kind == domain.ProjectRunEventKindStatus {
			statusEvents++
		}
	}
	if statusEvents != 1 {
		t.Fatalf("status events = %d in %#v, want the terminal status event recorded once", statusEvents, events)
	}

	// Repeating the call is how an operator retries a lost response: the run is
	// already terminal, so nothing is abandoned twice.
	again, abandonedAgain, err := manager.AbandonCompletion(ctx, run.RunID, "")
	if err != nil || abandonedAgain || again.Status != domain.ProjectRunStatusFailed {
		t.Fatalf("second AbandonCompletion = %#v abandoned=%v err=%v, want an idempotent terminal run", again, abandonedAgain, err)
	}
}

func TestCompletionManagerAbandonCompletionRefusesARunThatIsStillExecuting(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "executing-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, SandboxID: "sandbox-1", CleanupPolicy: domain.ProjectRunCleanupStopOnCompletion,
	})
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})

	// Nothing is staged while the run executes, so abandoning would end the run
	// without cleaning up its runtime: that run has to be stopped instead.
	current, abandoned, err := manager.AbandonCompletion(ctx, run.RunID, "")
	if !errors.Is(err, domain.ErrFailedPrecondition) || abandoned {
		t.Fatalf("AbandonCompletion = %#v abandoned=%v err=%v, want a failed precondition", current, abandoned, err)
	}
	if current.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("run status = %q, want it left running for the stop path", current.Status)
	}
	if _, _, err := manager.AbandonCompletion(ctx, "   ", ""); !errors.Is(err, domain.ErrInvalidArgument) {
		t.Fatalf("AbandonCompletion(empty) error = %v, want invalid argument", err)
	}
}

func TestIntegrationCompletionManagerAbandonCompletionEndsARunWhoseCleanupCannotConverge(t *testing.T) {
	TestCompletionManagerAbandonCompletionEndsARunWhoseCleanupCannotConverge(t)
}

func TestE2ECompletionManagerAbandonCompletionEndsARunWhoseCleanupCannotConverge(t *testing.T) {
	TestCompletionManagerAbandonCompletionEndsARunWhoseCleanupCannotConverge(t)
}

func TestIntegrationCompletionManagerAbandonCompletionRefusesARunThatIsStillExecuting(t *testing.T) {
	TestCompletionManagerAbandonCompletionRefusesARunThatIsStillExecuting(t)
}

func TestE2ECompletionManagerAbandonCompletionRefusesARunThatIsStillExecuting(t *testing.T) {
	TestCompletionManagerAbandonCompletionRefusesARunThatIsStillExecuting(t)
}
