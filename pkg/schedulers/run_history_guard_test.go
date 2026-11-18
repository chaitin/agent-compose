package schedulers

import (
	"context"
	"errors"
	"testing"

	"github.com/chaitin/agent-compose/pkg/apierror"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRunHistoryGuardRequiresCompletionToLeaveScheduler(t *testing.T) {
	c := NewController(ControllerDependencies{})
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler"}}
	if !c.EnterRun(scheduler) {
		t.Fatal("could not enter")
	}
	called := false
	err := c.WithIdleScheduler(context.Background(), "scheduler", func(context.Context) error { called = true; return nil })
	if !errors.Is(err, domain.ErrFailedPrecondition) || !apierror.IsNotApplied(err) || called {
		t.Fatalf("err=%v called=%v", err, called)
	}
	c.LeaveRun("scheduler")
	if err := c.WithIdleScheduler(context.Background(), "scheduler", func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("err=%v called=%v", err, called)
	}
}

func TestRunHistoryRemovalBlocksPruneAndEntry(t *testing.T) {
	c := NewController(ControllerDependencies{})
	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{ID: "scheduler", ConcurrencyPolicy: domain.SchedulerConcurrencyPolicyParallel}}
	err := c.WithIdleScheduler(context.Background(), "scheduler", func(context.Context) error {
		if !c.schedulerBusy("scheduler") {
			t.Error("prune must treat a scheduler under history removal as busy")
		}
		if !c.HistoryRemoving(" scheduler ") {
			t.Error("HistoryRemoving did not report the active removal")
		}
		if c.EnterRun(scheduler) {
			t.Error("entry allowed during history removal")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.schedulerBusy("scheduler") || c.HistoryRemoving("scheduler") {
		t.Fatal("removal flag leaked after completion")
	}
}

func TestRunHistorySkipRecordExcludesRemoval(t *testing.T) {
	c := NewController(ControllerDependencies{})
	release, ok := c.BeginSkipRecord(" scheduler ")
	if !ok {
		t.Fatal("skip record refused without history removal")
	}
	// A skipped run is recorded outside the running count; removal and prune
	// must still wait for it so its last writes cannot follow a removal.
	called := false
	err := c.WithIdleScheduler(context.Background(), "scheduler", func(context.Context) error { called = true; return nil })
	if !errors.Is(err, domain.ErrFailedPrecondition) || !apierror.IsNotApplied(err) || called {
		t.Fatalf("removal during skip record: err=%v called=%v", err, called)
	}
	if !c.schedulerBusy("scheduler") {
		t.Fatal("prune must treat a scheduler recording a skip as busy")
	}
	release()
	if c.schedulerBusy("scheduler") {
		t.Fatal("skip record leaked after release")
	}
	err = c.WithIdleScheduler(context.Background(), "scheduler", func(context.Context) error {
		if _, ok := c.BeginSkipRecord("scheduler"); ok {
			t.Error("skip record allowed during history removal")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
