package runs

import (
	"context"
	"errors"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestCompletionStagingRetriesOnlyWhenDue(t *testing.T) {
	ctx := context.Background()
	store := &stagingAttemptStore{fault: errors.New("database busy")}
	manager := NewCompletionManager(CompletionManagerDeps{Store: store})
	now := time.Date(2026, time.November, 27, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	if err := manager.retainCompletion(TransitionRequest{RunID: "run", Status: domain.ProjectRunStatusSucceeded, Output: "first result"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.stageRetainedCompletion(ctx, "run"); !errors.Is(err, store.fault) {
		t.Fatalf("first stage = %v", err)
	}
	manager.stageDueCompletions(ctx)
	if store.attempts != 1 {
		t.Fatalf("retried before backoff elapsed: %d attempts", store.attempts)
	}
	now = now.Add(time.Second)
	manager.stageDueCompletions(ctx)
	if store.attempts != 2 {
		t.Fatalf("due retry was not attempted: %d attempts", store.attempts)
	}
	if pending, err := manager.Pending(ctx, "run"); err != nil || !pending {
		t.Fatalf("repeated failure lost ownership: pending=%v err=%v", pending, err)
	}
	now = now.Add(time.Second)
	manager.stageDueCompletions(ctx)
	if store.attempts != 2 {
		t.Fatalf("second failure did not increase backoff: %d attempts", store.attempts)
	}
	store.fault = nil
	now = now.Add(time.Second)
	manager.stageDueCompletions(ctx)
	if store.attempts != 3 || store.saved.TargetStatus != domain.ProjectRunStatusSucceeded {
		t.Fatalf("recovery did not persist original result: attempts=%d record=%#v", store.attempts, store.saved)
	}
	if len(manager.unstaged) != 0 {
		t.Fatal("successfully persisted result was not handed off")
	}
}

func TestCompletionStagingConcurrentCancellationKeepsFirstResult(t *testing.T) {
	ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
	defer stop()
	store := &stagingAttemptStore{fault: errors.New("database busy"), entered: make(chan struct{}), release: make(chan struct{})}
	manager := NewCompletionManager(CompletionManagerDeps{Store: store})
	now := time.Date(2026, time.November, 27, 0, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	first := make(chan error, 1)
	events := []domain.ProjectRunEventRecord{{ID: "message", RunID: "run", Text: "original message"}}
	go func() {
		_, err := manager.Complete(ctx, TransitionRequest{RunID: "run", Status: domain.ProjectRunStatusSucceeded, Output: "first result", TerminalEvents: events})
		first <- err
	}()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("initial write was not attempted")
	}
	// A canceled concurrent caller must stop waiting for the in-flight write,
	// while the manager continues to own the first caller's result.
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := manager.Complete(canceled, TransitionRequest{RunID: "run", Status: domain.ProjectRunStatusCanceled}); !errors.Is(err, context.Canceled) {
		t.Fatalf("concurrent canceled Complete = %v", err)
	}
	close(store.release)
	select {
	case err := <-first:
		if !errors.Is(err, store.fault) {
			t.Fatalf("first Complete = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first caller did not exit after write failure")
	}
	events[0].Text = "caller mutated its input"
	store.fault, store.entered = nil, nil
	now = now.Add(time.Hour)
	manager.stageDueCompletions(ctx)
	if store.saved.TargetStatus != domain.ProjectRunStatusSucceeded || len(store.events) != 1 || store.events[0].Text != "original message" {
		t.Fatalf("concurrent caller or mutation replaced first result: %#v %#v", store.saved, store.events)
	}
}

type stagingAttemptStore struct {
	CompletionStore
	fault    error
	attempts int
	saved    domain.ProjectRunCompletionRecord
	events   []domain.ProjectRunEventRecord
	entered  chan struct{}
	release  chan struct{}
}

func (*stagingAttemptStore) GetProjectRun(context.Context, string) (domain.ProjectRunRecord, error) {
	return domain.ProjectRunRecord{RunID: "run", Status: domain.ProjectRunStatusRunning}, nil
}

func (s *stagingAttemptStore) StageProjectRunCompletion(ctx context.Context, record domain.ProjectRunCompletionRecord, events []domain.ProjectRunEventRecord) (domain.ProjectRunCompletionRecord, bool, error) {
	s.attempts++
	if s.entered != nil {
		close(s.entered)
		select {
		case <-s.release:
		case <-ctx.Done():
			return domain.ProjectRunCompletionRecord{}, false, ctx.Err()
		}
	}
	if s.fault != nil {
		return domain.ProjectRunCompletionRecord{}, false, s.fault
	}
	s.saved, s.events = record, events
	return record, true, nil
}
