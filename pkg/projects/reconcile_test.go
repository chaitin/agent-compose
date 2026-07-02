package projects

import (
	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/storage"
	"context"
	"testing"
	"time"
)

func TestReconcilePersistedRunsMarksStalePendingAndRunningFailed(t *testing.T) {
	ctx := context.Background()
	store := newReconcileTestConfigStore(t)
	createReconcileTestProject(t, ctx, store)
	startedAt := time.Now().UTC()

	stalePending := createReconcileTestRun(t, ctx, store, "stale-pending", ProjectRunStatusPending, 0)
	staleRunning := createReconcileTestRun(t, ctx, store, "stale-running", ProjectRunStatusRunning, 9)
	freshPending := createReconcileTestRun(t, ctx, store, "fresh-pending", ProjectRunStatusPending, 0)
	terminalSucceeded := createReconcileTestRun(t, ctx, store, "terminal-succeeded", ProjectRunStatusSucceeded, 0)

	backdateReconcileTestRuns(t, ctx, store, startedAt.Add(-time.Minute), stalePending.RunID, staleRunning.RunID, terminalSucceeded.RunID)
	backdateReconcileTestRuns(t, ctx, store, startedAt.Add(time.Minute), freshPending.RunID)

	if err := ReconcilePersistedRuns(ctx, store, startedAt); err != nil {
		t.Fatalf("ReconcilePersistedRuns returned error: %v", err)
	}

	stalePending = getReconcileTestRun(t, ctx, store, stalePending.RunID)
	if stalePending.Status != ProjectRunStatusFailed || stalePending.ExitCode != 1 || stalePending.Error != StaleProjectRunError || stalePending.CompletedAt.IsZero() {
		t.Fatalf("stale pending after reconcile = %#v", stalePending)
	}
	staleRunning = getReconcileTestRun(t, ctx, store, staleRunning.RunID)
	if staleRunning.Status != ProjectRunStatusFailed || staleRunning.ExitCode != 9 || staleRunning.Error != StaleProjectRunError || staleRunning.CompletedAt.IsZero() {
		t.Fatalf("stale running after reconcile = %#v", staleRunning)
	}
	freshPending = getReconcileTestRun(t, ctx, store, freshPending.RunID)
	if freshPending.Status != ProjectRunStatusPending || !freshPending.CompletedAt.IsZero() {
		t.Fatalf("fresh pending after reconcile = %#v", freshPending)
	}
	terminalSucceeded = getReconcileTestRun(t, ctx, store, terminalSucceeded.RunID)
	if terminalSucceeded.Status != ProjectRunStatusSucceeded || terminalSucceeded.Error != "" {
		t.Fatalf("terminal run after reconcile = %#v", terminalSucceeded)
	}
}

func TestReconcilePersistedRunsAllowsNilStore(t *testing.T) {
	if err := ReconcilePersistedRuns(context.Background(), nil, time.Now()); err != nil {
		t.Fatalf("ReconcilePersistedRuns(nil) returned error: %v", err)
	}
}

func newReconcileTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	store, err := storage.NewConfigStoreFromConfig(&appconfig.Config{DataRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("NewConfigStoreFromConfig returned error: %v", err)
	}
	t.Cleanup(func() { _ = store.DB().Close() })
	return store
}

func createReconcileTestProject(t *testing.T, ctx context.Context, store *ConfigStore) {
	t.Helper()
	if _, err := store.UpsertProject(ctx, ProjectRecord{
		ID:       "project-reconcile",
		Name:     "reconcile",
		SpecHash: "spec-reconcile",
	}); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
}

func createReconcileTestRun(t *testing.T, ctx context.Context, store *ConfigStore, id string, status string, exitCode int) ProjectRunRecord {
	t.Helper()
	completedAt := time.Time{}
	if status == ProjectRunStatusSucceeded {
		completedAt = time.Now().UTC()
	}
	run, err := store.CreateProjectRun(ctx, ProjectRunRecord{
		RunID:           id,
		ProjectID:       "project-reconcile",
		ProjectName:     "reconcile",
		ProjectRevision: 1,
		AgentName:       "reviewer",
		ManagedAgentID:  "agent-reconcile",
		Source:          ProjectRunSourceManual,
		Status:          status,
		ExitCode:        exitCode,
		Prompt:          id,
		ResultJSON:      "{}",
		Driver:          "boxlite",
		ImageRef:        "guest:latest",
		CompletedAt:     completedAt,
	})
	if err != nil {
		t.Fatalf("CreateProjectRun(%s) returned error: %v", id, err)
	}
	return run
}

func backdateReconcileTestRuns(t *testing.T, ctx context.Context, store *ConfigStore, createdAt time.Time, runIDs ...string) {
	t.Helper()
	for _, runID := range runIDs {
		if _, err := store.DB().ExecContext(ctx, `UPDATE project_run SET created_at = ?, updated_at = ? WHERE run_id = ?`, createdAt.Unix(), createdAt.Unix(), runID); err != nil {
			t.Fatalf("backdate project run %s: %v", runID, err)
		}
	}
}

func getReconcileTestRun(t *testing.T, ctx context.Context, store *ConfigStore, runID string) ProjectRunRecord {
	t.Helper()
	run, err := store.GetProjectRun(ctx, runID)
	if err != nil {
		t.Fatalf("GetProjectRun(%s) returned error: %v", runID, err)
	}
	return run
}
