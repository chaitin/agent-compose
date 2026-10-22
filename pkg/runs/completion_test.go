package runs_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
)

func TestCompletionCleanupAction(t *testing.T) {
	tests := []struct {
		name    string
		policy  string
		has     bool
		created bool
		want    string
	}{
		{name: "no sandbox", policy: domain.ProjectRunCleanupStopOnCompletion, want: domain.ProjectRunCompletionActionNone},
		{name: "keep running", policy: domain.ProjectRunCleanupKeepRunning, has: true, created: true, want: domain.ProjectRunCompletionActionNone},
		{name: "stop", policy: domain.ProjectRunCleanupStopOnCompletion, has: true, created: true, want: domain.ProjectRunCompletionActionStop},
		{name: "remove created", policy: domain.ProjectRunCleanupRemoveOnCompletion, has: true, created: true, want: domain.ProjectRunCompletionActionRemove},
		{name: "remove reused degrades to stop", policy: domain.ProjectRunCleanupRemoveOnCompletion, has: true, want: domain.ProjectRunCompletionActionStop},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := runs.CompletionCleanupAction(tt.policy, tt.has, tt.created); got != tt.want {
				t.Fatalf("CompletionCleanupAction() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCompletionManagerPersistsEventsBeforeCleanupAndRetriesFirstResult(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "completion-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, SandboxID: "sandbox-1", CleanupPolicy: domain.ProjectRunCleanupStopOnCompletion,
	})
	sandboxes := completionSandboxStoreFake{sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: run.SandboxID, VMStatus: domain.VMStatusRunning}}}
	timeoutCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stopper := &completionStopperFake{err: errors.New("stop unavailable"), onStop: cancel}
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store, Sandboxes: sandboxes, Lifecycle: stopper})

	staged, err := manager.Complete(timeoutCtx, runs.TransitionRequest{
		RunID: run.RunID, Status: domain.ProjectRunStatusSucceeded, Output: "exact output", ResultJSON: `{"ok":true}`,
		TerminalEvents: []domain.ProjectRunEventRecord{{ID: "final-agent-event", RunID: run.RunID, Kind: domain.ProjectRunEventKindAgentMessage, Text: "done"}},
	})
	if err != nil {
		t.Fatalf("Complete while cleanup failed: %v", err)
	}
	if staged.Status != domain.ProjectRunStatusRunning || staged.CleanupError == "" {
		t.Fatalf("staged run = %#v, want running with cleanup error", staged)
	}
	events, err := store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].ID != "final-agent-event" || events[0].Kind == domain.ProjectRunEventKindStatus {
		t.Fatalf("events before cleanup = %#v", events)
	}
	journal, err := store.GetProjectRunCompletion(ctx, run.RunID)
	if err != nil || journal.Attempt != 1 || journal.CleanupAction != domain.ProjectRunCompletionActionStop {
		t.Fatalf("journal = %#v, err=%v", journal, err)
	}

	stopper.err = nil
	if err := store.RecordProjectRunCompletionFailure(ctx, domain.ProjectRunCompletionFailure{
		RunID: run.RunID, Message: journal.LastError, Attempt: journal.Attempt, NextAttemptAt: time.Now().Add(-time.Second),
	}); err != nil {
		t.Fatal(err)
	}
	completed, err := manager.Complete(ctx, runs.TransitionRequest{RunID: run.RunID, Status: domain.ProjectRunStatusCanceled, Error: "late cancellation"})
	if err != nil {
		t.Fatalf("Complete retry: %v", err)
	}
	if completed.Status != domain.ProjectRunStatusSucceeded || completed.Output != "exact output" || completed.ResultJSON != `{"ok":true}` || completed.CleanupError != "" {
		t.Fatalf("completed run = %#v", completed)
	}
	events, err = store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Kind != domain.ProjectRunEventKindStatus {
		t.Fatalf("events after cleanup = %#v", events)
	}
	if _, err := store.GetProjectRunCompletion(ctx, run.RunID); err == nil {
		t.Fatal("completion journal remained after terminal commit")
	}
}

func TestIntegrationCompletionManagerPersistsEventsBeforeCleanupAndRetriesFirstResult(t *testing.T) {
	TestCompletionManagerPersistsEventsBeforeCleanupAndRetriesFirstResult(t)
}

func TestE2ECompletionManagerPersistsEventsBeforeCleanupAndRetriesFirstResult(t *testing.T) {
	TestCompletionManagerPersistsEventsBeforeCleanupAndRetriesFirstResult(t)
}

// A permanent sandbox cleanup failure must not keep a run non-terminal
// forever: after a bounded number of attempts the run is finalized anyway, the
// cleanup failure is recorded on it, and the completion journal is deleted so
// no further retry is scheduled.
func TestCompletionManagerAbandonsCleanupAfterBoundedAttempts(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "abandoned-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, SandboxID: "sandbox-abandon", CleanupPolicy: domain.ProjectRunCleanupStopOnCompletion,
	})
	sandboxes := completionSandboxStoreFake{sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: run.SandboxID, VMStatus: domain.VMStatusRunning}}}
	stopper := &completionStopperFake{err: errors.New("stop unavailable")}
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store, Sandboxes: sandboxes, Lifecycle: stopper})

	// Each Complete call is cancelled as soon as cleanup is attempted, so one
	// loop iteration drives exactly one cleanup attempt instead of waiting out
	// the real retry delays. Without the attempt bound the run stays RUNNING
	// for every iteration and the assertions below fail.
	const maxIterations = 20
	var finalized domain.ProjectRunRecord
	attempts := 0
	for ; attempts < maxIterations; attempts++ {
		attemptCtx, cancel := context.WithCancel(ctx)
		stopper.onStop = cancel
		current, err := manager.Complete(attemptCtx, runs.TransitionRequest{
			RunID: run.RunID, Status: domain.ProjectRunStatusSucceeded, Output: "done",
		})
		cancel()
		if err != nil {
			t.Fatalf("Complete on attempt %d: %v", attempts+1, err)
		}
		if runs.StatusIsTerminal(current.Status) {
			finalized = current
			break
		}
		journal, err := store.GetProjectRunCompletion(ctx, run.RunID)
		if err != nil {
			t.Fatalf("read completion journal after attempt %d: %v", attempts+1, err)
		}
		if journal.Attempt != attempts+1 {
			t.Fatalf("journal attempt = %d after %d attempts", journal.Attempt, attempts+1)
		}
		if err := store.RecordProjectRunCompletionFailure(ctx, domain.ProjectRunCompletionFailure{
			RunID: run.RunID, Message: journal.LastError, Attempt: journal.Attempt, NextAttemptAt: time.Now().Add(-time.Second),
		}); err != nil {
			t.Fatalf("make completion due after attempt %d: %v", attempts+1, err)
		}
	}
	if finalized.Status != domain.ProjectRunStatusSucceeded {
		t.Fatalf("run did not reach a terminal state within %d attempts: %#v", maxIterations, finalized)
	}
	if !strings.Contains(finalized.CleanupError, "stop unavailable") {
		t.Fatalf("finalized run recorded no cleanup error: %#v", finalized)
	}
	if _, err := store.GetProjectRunCompletion(ctx, run.RunID); err == nil {
		t.Fatal("completion journal remained after the run was finalized")
	}
	persisted, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Status != domain.ProjectRunStatusSucceeded || !strings.Contains(persisted.CleanupError, "stop unavailable") {
		t.Fatalf("persisted run = %#v", persisted)
	}
	if stopper.calls != attempts+1 {
		t.Fatalf("cleanup attempts = %d, want %d", stopper.calls, attempts+1)
	}
	t.Logf("cleanup abandoned after %d attempts", attempts+1)
}

func TestIntegrationCompletionManagerAbandonsCleanupAfterBoundedAttempts(t *testing.T) {
	TestCompletionManagerAbandonsCleanupAfterBoundedAttempts(t)
}

func TestE2ECompletionManagerAbandonsCleanupAfterBoundedAttempts(t *testing.T) {
	TestCompletionManagerAbandonsCleanupAfterBoundedAttempts(t)
}

type completionSandboxStoreFake struct{ sandbox *domain.Sandbox }

func (s completionSandboxStoreFake) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	if s.sandbox == nil {
		return nil, domain.ErrNotFound
	}
	return s.sandbox, nil
}

type completionStopperFake struct {
	err    error
	calls  int
	onStop func()
}

func (s *completionStopperFake) Stop(context.Context, *domain.Sandbox) error {
	s.calls++
	if s.onStop != nil {
		s.onStop()
	}
	return s.err
}

func newCompletionTestStore(t *testing.T) *configstore.ConfigStore {
	t.Helper()
	root := t.TempDir()
	di := do.New()
	do.ProvideValue(di, context.Background())
	do.ProvideValue(di, &appconfig.Config{DataRoot: root, DbAddr: filepath.Join(root, "data.db")})
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.DB().Close() })
	if _, err := store.UpsertProject(context.Background(), domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: "/project", SourceJSON: `{}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertProjectAgent(context.Background(), domain.ProjectAgentRecord{ID: "agent-1", ProjectID: "project-1", AgentName: "worker"}); err != nil {
		t.Fatal(err)
	}
	return store
}

func createCompletionTestRun(t *testing.T, store *configstore.ConfigStore, run domain.ProjectRunRecord) domain.ProjectRunRecord {
	t.Helper()
	created, err := store.CreateProjectRun(context.Background(), run)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

// A second completion writer that read the run while it was still running
// can reach staging only after the first writer's result was finalized (a
// detached run re-attached at startup while the manager finishes the result
// staged before the restart). Its stage must not land, or finalizing it would
// overwrite the first result.
func TestCompletionStageDoesNotReopenAFinalizedRun(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "finalized-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, CleanupPolicy: domain.ProjectRunCleanupKeepRunning,
	})
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store, Sandboxes: completionSandboxStoreFake{}, Lifecycle: &completionStopperFake{}})
	if _, err := manager.Complete(ctx, runs.TransitionRequest{RunID: run.RunID, Status: domain.ProjectRunStatusSucceeded, Output: "first"}); err != nil {
		t.Fatalf("first Complete: %v", err)
	}
	pending, err := manager.Pending(ctx, run.RunID)
	if err != nil || pending {
		t.Fatalf("Pending after finalize = %v, %v; want false", pending, err)
	}

	_, created, err := store.StageProjectRunCompletion(ctx, domain.ProjectRunCompletionRecord{
		RunID: run.RunID, TargetStatus: domain.ProjectRunStatusFailed, TransitionJSON: `{"run_id":"finalized-run","status":"failed","error":"late writer"}`,
	}, nil)
	if err != nil {
		t.Fatalf("late stage: %v", err)
	}
	if created {
		t.Fatal("a completion was staged for a run that is already terminal")
	}
	if _, err := store.GetProjectRunCompletion(ctx, run.RunID); err == nil {
		t.Fatal("late stage left a completion row behind")
	}
	got, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domain.ProjectRunStatusSucceeded || got.Output != "first" {
		t.Fatalf("run = %s %q, want the first writer's succeeded result", got.Status, got.Output)
	}
}

func TestCompletionManagerPendingReportsStagedRuns(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "staged-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
		Status: domain.ProjectRunStatusRunning, CleanupPolicy: domain.ProjectRunCleanupKeepRunning,
	})
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store, Sandboxes: completionSandboxStoreFake{}, Lifecycle: &completionStopperFake{}})
	if pending, err := manager.Pending(ctx, run.RunID); err != nil || pending {
		t.Fatalf("Pending before staging = %v, %v; want false", pending, err)
	}
	if err := manager.StageInterrupted(ctx, run, "interrupted"); err != nil {
		t.Fatal(err)
	}
	if pending, err := manager.Pending(ctx, run.RunID); err != nil || !pending {
		t.Fatalf("Pending after staging = %v, %v; want true", pending, err)
	}
}
