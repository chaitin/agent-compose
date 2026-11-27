package runs_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
)

// The first caller exits with a storage error. Only the manager's background
// worker can recover the result; no second execution or Complete call supplies it.
func TestIntegrationCompletionManagerRetainsResultBeforeStaging(t *testing.T) {
	for _, tc := range []struct {
		name              string
		failRead          bool
		commitBeforeError bool
	}{
		{name: "first insert fails"},
		{name: "initial read fails", failRead: true},
		{name: "commit succeeds but response fails", commitBeforeError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := newCompletionTestStore(t)
			run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
				RunID: "retained-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1",
				Status: domain.ProjectRunStatusRunning, SandboxID: "sandbox-1", CleanupPolicy: domain.ProjectRunCleanupKeepRunning,
			})
			fault := &completionStagingFaultStore{
				CompletionStore: store, failRead: tc.failRead, commitBeforeError: tc.commitBeforeError,
				finalized: make(chan domain.ProjectRunRecord, 1), fault: errors.New("storage temporarily unavailable"),
			}
			fault.unavailable.Store(true)
			manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: fault})
			events := []domain.ProjectRunEventRecord{{ID: "original-message", RunID: run.RunID, Kind: domain.ProjectRunEventKindAgentMessage, Text: "original text"}}
			caller, cancel := context.WithCancel(ctx)
			_, err := manager.Complete(caller, runs.TransitionRequest{
				RunID: run.RunID, SandboxID: run.SandboxID, Status: domain.ProjectRunStatusSucceeded,
				Output: "original output", ResultJSON: `{"ok":true}`, TerminalEvents: events,
			})
			cancel()
			if !errors.Is(err, fault.fault) {
				t.Fatalf("Complete() error = %v, want storage failure", err)
			}
			if !tc.commitBeforeError {
				if _, err := store.GetProjectRunCompletion(ctx, run.RunID); !errors.Is(err, sql.ErrNoRows) {
					t.Fatalf("completion unexpectedly persisted: %v", err)
				}
			}
			if pending, err := manager.Pending(ctx, run.RunID); err != nil || !pending {
				t.Fatalf("Pending() = %v, %v; original result lost its owner", pending, err)
			}
			if id, pending, err := manager.PendingForSandbox(ctx, run.SandboxID); err != nil || !pending || id != run.RunID {
				t.Fatalf("PendingForSandbox() = %q, %v, %v", id, pending, err)
			}
			// Callers own their input slice. Recovery must use the retained copy.
			events[0].Text = "caller changed this"
			_, err = manager.Complete(ctx, runs.TransitionRequest{RunID: run.RunID, Status: domain.ProjectRunStatusCanceled, Error: "late stop"})
			if !errors.Is(err, fault.fault) {
				t.Fatalf("late Complete() error = %v", err)
			}
			if err := manager.StageInterrupted(ctx, run, "late interruption"); !errors.Is(err, fault.fault) {
				t.Fatalf("StageInterrupted() error = %v", err)
			}
			fault.unavailable.Store(false)
			if err := manager.Start(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
				defer cancel()
				if err := manager.Shutdown(shutdown); err != nil {
					t.Error(err)
				}
			})
			select {
			case completed := <-fault.finalized:
				if completed.Status != domain.ProjectRunStatusSucceeded || completed.Output != "original output" || completed.ResultJSON != `{"ok":true}` {
					t.Fatalf("recovered run = %#v", completed)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("background manager did not recover the original result after the caller exited")
			}
			if pending, err := manager.Pending(ctx, run.RunID); err != nil || pending {
				t.Fatalf("Pending after completion = %v, %v", pending, err)
			}
			storedEvents, err := store.ListProjectRunEvents(ctx, run.RunID, 0, 10)
			if err != nil {
				t.Fatal(err)
			}
			if len(storedEvents) != 2 || storedEvents[0].Text != "original text" || storedEvents[1].Kind != domain.ProjectRunEventKindStatus {
				t.Fatalf("final events = %#v; want original message and exactly one terminal event", storedEvents)
			}
		})
	}
}

func TestIntegrationCompletionManagerReportsUnpersistedResultAtShutdown(t *testing.T) {
	ctx := context.Background()
	store := newCompletionTestStore(t)
	run := createCompletionTestRun(t, store, domain.ProjectRunRecord{
		RunID: "unpersisted-run", ProjectID: "project-1", AgentName: "worker", AgentID: "agent-1", Status: domain.ProjectRunStatusRunning,
	})
	fault := &completionStagingFaultStore{CompletionStore: store, fault: errors.New("persistent storage failure")}
	fault.unavailable.Store(true)
	var logs bytes.Buffer
	manager := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: fault, Logger: slog.New(slog.NewJSONHandler(&logs, nil))})
	if _, err := manager.Complete(ctx, runs.TransitionRequest{RunID: run.RunID, Status: domain.ProjectRunStatusSucceeded}); !errors.Is(err, fault.fault) {
		t.Fatalf("Complete() error = %v", err)
	}
	if err := manager.Start(ctx); err != nil {
		t.Fatal(err)
	}
	shutdown, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := manager.Shutdown(shutdown); err == nil {
		t.Fatal("Shutdown reported success with a result that exists only in memory")
	}
	// Shutdown waits for the logger's writers before logs are inspected.
	for _, want := range []string{run.RunID, `"phase":"staging"`, `"attempt":1`, "persisted"} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("logs missing %q: %s", want, logs.String())
		}
	}
	current, err := store.GetProjectRun(ctx, run.RunID)
	if err != nil || current.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("unpersisted result fabricated a terminal state: %#v, %v", current, err)
	}
}

type completionStagingFaultStore struct {
	runs.CompletionStore
	unavailable       atomic.Bool
	failRead          bool
	commitBeforeError bool
	fault             error
	finalized         chan domain.ProjectRunRecord
}

func (s *completionStagingFaultStore) GetProjectRun(ctx context.Context, id string) (domain.ProjectRunRecord, error) {
	if s.failRead && s.unavailable.Load() {
		return domain.ProjectRunRecord{}, s.fault
	}
	return s.CompletionStore.GetProjectRun(ctx, id)
}

func (s *completionStagingFaultStore) StageProjectRunCompletion(ctx context.Context, record domain.ProjectRunCompletionRecord, events []domain.ProjectRunEventRecord) (domain.ProjectRunCompletionRecord, bool, error) {
	if !s.unavailable.Load() {
		return s.CompletionStore.StageProjectRunCompletion(ctx, record, events)
	}
	if s.commitBeforeError {
		if _, _, err := s.CompletionStore.StageProjectRunCompletion(ctx, record, events); err != nil {
			return domain.ProjectRunCompletionRecord{}, false, err
		}
	}
	return domain.ProjectRunCompletionRecord{}, false, s.fault
}

func (s *completionStagingFaultStore) FinalizeProjectRunCompletion(ctx context.Context, run domain.ProjectRunRecord, event domain.ProjectRunEventRecord) (domain.ProjectRunRecord, error) {
	completed, err := s.CompletionStore.FinalizeProjectRunCompletion(ctx, run, event)
	if err == nil && s.finalized != nil {
		select {
		case s.finalized <- completed:
		default:
		}
	}
	return completed, err
}
