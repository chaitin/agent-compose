package schedulers

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestRunHistoryGuardDoesNotBlockUnrelatedSchedulers(t *testing.T) {
	controller := NewController(ControllerDependencies{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- controller.WithIdleScheduler(ctx, "deleting", func(context.Context) error {
			close(entered)
			<-release
			return nil
		})
	}()
	defer func() {
		close(release)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	<-entered
	other := make(chan bool, 1)
	go func() {
		other <- controller.EnterRun(domain.Scheduler{Summary: domain.SchedulerSummary{ID: "unrelated"}})
	}()
	select {
	case ok := <-other:
		if !ok {
			t.Fatal("unrelated scheduler rejected")
		}
	case <-ctx.Done():
		t.Fatal("history deletion held the global scheduler lock")
	}
	if controller.EnterRun(domain.Scheduler{Summary: domain.SchedulerSummary{ID: "deleting", ConcurrencyPolicy: domain.SchedulerConcurrencyPolicyParallel}}) {
		t.Fatal("scheduler entered while its history was being deleted")
	}
	if err := controller.WithIdleScheduler(ctx, "deleting", func(context.Context) error { t.Fatal("concurrent removal entered"); return nil }); !errors.Is(err, domain.ErrFailedPrecondition) {
		t.Fatalf("concurrent deletion: %v", err)
	}
	controller.LeaveRun("unrelated")
}
