package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestServiceReconcilePersistedSessionsMarksStalePendingFailed(t *testing.T) {
	testServiceReconcilePersistedSessionsMarksStalePendingFailed(t)
}

func TestServiceReconcilePersistedSessionsMarksStaleProjectRunsFailed(t *testing.T) {
	testServiceReconcilePersistedSessionsMarksStaleProjectRunsFailed(t)
}

func testServiceReconcilePersistedSessionsMarksStaleProjectRunsFailed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	store, service, projectID := setupRunCoordinatorProject(t)
	if err := os.MkdirAll(service.config.SessionRoot, 0o755); err != nil {
		t.Fatalf("create session root: %v", err)
	}
	coordinator := NewRunCoordinator(store)

	stalePending, err := coordinator.BeginRun(ctx, ProjectRunStartRequest{
		ProjectID:       projectID,
		AgentName:       "reviewer",
		Source:          ProjectRunSourceManual,
		Prompt:          "stale pending",
		ClientRequestID: "stale-pending",
	})
	if err != nil {
		t.Fatalf("BeginRun(stale pending) returned error: %v", err)
	}
	staleRunning, err := coordinator.BeginRun(ctx, ProjectRunStartRequest{
		ProjectID:       projectID,
		AgentName:       "reviewer",
		Source:          ProjectRunSourceManual,
		Prompt:          "stale running",
		ClientRequestID: "stale-running",
	})
	if err != nil {
		t.Fatalf("BeginRun(stale running) returned error: %v", err)
	}
	staleRunning, err = coordinator.MarkRunning(ctx, staleRunning.RunID, "session-stale")
	if err != nil {
		t.Fatalf("MarkRunning(stale running) returned error: %v", err)
	}
	freshPending, err := coordinator.BeginRun(ctx, ProjectRunStartRequest{
		ProjectID:       projectID,
		AgentName:       "reviewer",
		Source:          ProjectRunSourceManual,
		Prompt:          "fresh pending",
		ClientRequestID: "fresh-pending",
	})
	if err != nil {
		t.Fatalf("BeginRun(fresh pending) returned error: %v", err)
	}

	startedAt := time.Now().UTC()
	staleCreatedAt := startedAt.Add(-time.Minute).Unix()
	freshCreatedAt := startedAt.Add(time.Minute).Unix()
	for _, runID := range []string{stalePending.RunID, staleRunning.RunID} {
		if _, err := store.db.ExecContext(ctx, `UPDATE project_run SET created_at = ?, updated_at = ? WHERE run_id = ?`, staleCreatedAt, staleCreatedAt, runID); err != nil {
			t.Fatalf("backdate project run %s: %v", runID, err)
		}
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE project_run SET created_at = ?, updated_at = ? WHERE run_id = ?`, freshCreatedAt, freshCreatedAt, freshPending.RunID); err != nil {
		t.Fatalf("forward-date fresh project run: %v", err)
	}

	service.startedAt = startedAt
	if err := service.reconcilePersistedSessions(ctx); err != nil {
		t.Fatalf("reconcilePersistedSessions returned error: %v", err)
	}

	for _, runID := range []string{stalePending.RunID, staleRunning.RunID} {
		run, err := store.GetProjectRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetProjectRun(%s) returned error: %v", runID, err)
		}
		if run.Status != ProjectRunStatusFailed || run.CompletedAt.IsZero() || run.ExitCode != 1 || run.Error != staleProjectRunError {
			t.Fatalf("stale run after reconcile = %#v", run)
		}
	}
	fresh, err := store.GetProjectRun(ctx, freshPending.RunID)
	if err != nil {
		t.Fatalf("GetProjectRun(fresh) returned error: %v", err)
	}
	if fresh.Status != ProjectRunStatusPending || !fresh.CompletedAt.IsZero() {
		t.Fatalf("fresh run after reconcile = %#v", fresh)
	}
}

func TestServiceAndBridgeReconcileMicrosandboxRuntimeTypeBranches(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:                 root,
		SessionRoot:              filepath.Join(root, "sessions"),
		RuntimeDriver:            driverpkg.RuntimeDriverBoxlite,
		DefaultImage:             "debian:bookworm-slim",
		MicrosandboxDefaultImage: "devbox:archlinux",
		GuestWorkspacePath:       "/data/workspace",
		JupyterGuestPort:         8888,
		JupyterProxyBasePath:     "/agent-compose/session",
	}
	store := &Store{config: config}
	session, err := store.CreateSession(ctx, "running micro", "", driverpkg.RuntimeDriverMicrosandbox, "devbox:archlinux", "", SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = VMStatusRunning
	if err := store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	proxyState, err := store.GetProxyState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	proxyState.HostPort = unusedLocalTCPPort(t)
	proxyState.GuestHost = "127.0.0.1"
	if err := store.SaveProxyState(session.Summary.ID, proxyState); err != nil {
		t.Fatalf("SaveProxyState returned error: %v", err)
	}
	runtimes := fixedRuntimeProvider{runtime: &fakeLoaderAgentRuntime{}}
	service := &Service{config: config, store: store, runtimes: runtimes}
	reconciled, err := service.reconcileSessionRuntimeState(ctx, session)
	if err != nil {
		t.Fatalf("service reconcile returned error: %v", err)
	}
	if reconciled.Summary.VMStatus != VMStatusRunning {
		t.Fatalf("service reconciled status = %q", reconciled.Summary.VMStatus)
	}

	bridge := &SessionRPCBridge{config: config, store: store, runtimes: runtimes}
	reconciled, err = bridge.reconcileSessionRuntimeState(ctx, session)
	if err != nil {
		t.Fatalf("bridge reconcile returned error: %v", err)
	}
	if reconciled.Summary.VMStatus != VMStatusRunning {
		t.Fatalf("bridge reconciled status = %q", reconciled.Summary.VMStatus)
	}

	missingProxySession, err := store.CreateSession(ctx, "missing proxy", "", driverpkg.RuntimeDriverMicrosandbox, "devbox:archlinux", "", SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession missing proxy returned error: %v", err)
	}
	missingProxySession.Summary.VMStatus = VMStatusRunning
	if err := store.UpdateSession(ctx, missingProxySession); err != nil {
		t.Fatalf("UpdateSession missing proxy returned error: %v", err)
	}
	if err := os.Remove(store.proxyStatePath(missingProxySession.Summary.ID)); err != nil {
		t.Fatalf("remove proxy state: %v", err)
	}
	if _, err := service.reconcileSessionRuntimeState(ctx, missingProxySession); err == nil {
		t.Fatalf("service reconcile missing proxy returned nil error")
	}
	if _, err := bridge.reconcileSessionRuntimeState(ctx, missingProxySession); err == nil {
		t.Fatalf("bridge reconcile missing proxy returned nil error")
	}
}

func testServiceReconcilePersistedSessionsMarksStalePendingFailed(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:                 root,
		SessionRoot:              filepath.Join(root, "sessions"),
		RuntimeDriver:            driverpkg.RuntimeDriverBoxlite,
		DefaultImage:             "debian:bookworm-slim",
		MicrosandboxDefaultImage: "devbox:archlinux",
		GuestWorkspacePath:       "/data/workspace",
		JupyterGuestPort:         8888,
		JupyterProxyBasePath:     "/agent-compose/session",
	}
	store := &Store{config: config}
	staleSession, err := store.CreateSession(ctx, "stale", "", driverpkg.RuntimeDriverMicrosandbox, "devbox:archlinux", "", SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession(stale) returned error: %v", err)
	}
	service := &Service{config: config, store: store, startedAt: time.Now().UTC()}
	freshSession, err := store.CreateSession(ctx, "fresh", "", driverpkg.RuntimeDriverMicrosandbox, "devbox:archlinux", "", SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession(fresh) returned error: %v", err)
	}
	if err := service.reconcilePersistedSessions(ctx); err != nil {
		t.Fatalf("reconcilePersistedSessions returned error: %v", err)
	}
	staleLoaded, err := store.GetSession(ctx, staleSession.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession(stale) returned error: %v", err)
	}
	if got, want := staleLoaded.Summary.VMStatus, VMStatusFailed; got != want {
		t.Fatalf("stale session vm status = %q, want %q", got, want)
	}
	staleVMState, err := store.GetVMState(staleSession.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState(stale) returned error: %v", err)
	}
	if got, want := staleVMState.LastError, stalePendingSessionLastError; got != want {
		t.Fatalf("stale session last error = %q, want %q", got, want)
	}
	if staleVMState.StoppedAt.IsZero() {
		t.Fatalf("expected stale pending session to record stopped_at")
	}
	events, err := store.ListEvents(ctx, staleSession.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents(stale) returned error: %v", err)
	}
	if len(events) != 1 || events[0].Type != "session.startup_interrupted" {
		t.Fatalf("stale session events = %#v, want one session.startup_interrupted event", events)
	}
	freshLoaded, err := store.GetSession(ctx, freshSession.Summary.ID)
	if err != nil {
		t.Fatalf("GetSession(fresh) returned error: %v", err)
	}
	if got, want := freshLoaded.Summary.VMStatus, VMStatusPending; got != want {
		t.Fatalf("fresh session vm status = %q, want %q", got, want)
	}
}

func TestServiceReconcileOrphanedRunningCells(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:                 root,
		SessionRoot:              filepath.Join(root, "sessions"),
		RuntimeDriver:            driverpkg.RuntimeDriverBoxlite,
		DefaultImage:             "debian:bookworm-slim",
		MicrosandboxDefaultImage: "devbox:archlinux",
		GuestWorkspacePath:       "/data/workspace",
		JupyterGuestPort:         8888,
		JupyterProxyBasePath:     "/agent-compose/session",
	}
	store := &Store{config: config}

	session, err := store.CreateSession(ctx, "orphaned-cells", "", driverpkg.RuntimeDriverMicrosandbox, "devbox:archlinux", "", SessionTypeManual, nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}

	// Add a running cell that was created before daemon started (orphaned).
	orphanedCell := NotebookCell{
		ID:        "cell-orphaned",
		Type:      CellTypeShell,
		Source:    "sleep 60",
		Running:   true,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := store.AddCell(ctx, session, orphanedCell); err != nil {
		t.Fatalf("AddCell(orphaned) returned error: %v", err)
	}

	// Add a completed cell that should be left untouched.
	completedCell := NotebookCell{
		ID:        "cell-completed",
		Type:      CellTypeShell,
		Source:    "echo done",
		Running:   false,
		Success:   true,
		ExitCode:  0,
		CreatedAt: time.Now().UTC().Add(-time.Minute),
	}
	if err := store.AddCell(ctx, session, completedCell); err != nil {
		t.Fatalf("AddCell(completed) returned error: %v", err)
	}

	// Add a running cell created after daemon started (fresh, should not be converged).
	startedAt := time.Now().UTC()
	freshRunningCell := NotebookCell{
		ID:        "cell-fresh-running",
		Type:      CellTypeShell,
		Source:    "sleep 10",
		Running:   true,
		CreatedAt: startedAt.Add(time.Minute),
	}
	if err := store.AddCell(ctx, session, freshRunningCell); err != nil {
		t.Fatalf("AddCell(fresh-running) returned error: %v", err)
	}

	service := &Service{config: config, store: store, startedAt: startedAt}
	if err := service.reconcileOrphanedRunningCells(ctx, session); err != nil {
		t.Fatalf("reconcileOrphanedRunningCells returned error: %v", err)
	}

	// Verify orphaned cell was converged.
	cells, err := store.ListCells(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}

	cellByID := make(map[string]NotebookCell)
	for _, c := range cells {
		cellByID[c.ID] = c
	}

	// Orphaned cell should no longer be running.
	orphaned, ok := cellByID["cell-orphaned"]
	if !ok {
		t.Fatalf("orphaned cell not found in cells")
	}
	if orphaned.Running {
		t.Fatalf("orphaned cell still running, want Running=false")
	}
	if orphaned.Success {
		t.Fatalf("orphaned cell Success=true, want false")
	}
	if orphaned.ExitCode != 1 {
		t.Fatalf("orphaned cell ExitCode=%d, want 1", orphaned.ExitCode)
	}

	// Completed cell should be unchanged.
	completed, ok := cellByID["cell-completed"]
	if !ok {
		t.Fatalf("completed cell not found in cells")
	}
	if !completed.Success || completed.ExitCode != 0 {
		t.Fatalf("completed cell was modified: Success=%v ExitCode=%d", completed.Success, completed.ExitCode)
	}

	// Fresh running cell should still be running.
	fresh, ok := cellByID["cell-fresh-running"]
	if !ok {
		t.Fatalf("fresh running cell not found in cells")
	}
	if !fresh.Running {
		t.Fatalf("fresh running cell was converged, want Running=true")
	}

	// Verify an event was recorded for the converged cell.
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	var interruptedCount int
	for _, e := range events {
		if e.Type == "cell.execution_interrupted" {
			interruptedCount++
		}
	}
	if interruptedCount != 1 {
		t.Fatalf("expected 1 cell.execution_interrupted event, got %d", interruptedCount)
	}
}
