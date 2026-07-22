package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/sessionstore"
)

func TestReconcilePendingSessionStateMarksStaleStartupFailed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), RuntimeDriver: driverpkg.RuntimeDriverBoxlite}
	store, err := sessionstore.NewWithConfig(config)
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	session, err := store.CreateSandbox(ctx, "stale", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusPending
	session.Summary.CreatedAt = time.Now().Add(-time.Hour)
	if err := store.UpdateSandbox(ctx, session); err != nil {
		t.Fatalf("UpdateSession returned error: %v", err)
	}
	if err := store.SaveVMState(session.Summary.ID, domain.VMState{Driver: driverpkg.RuntimeDriverBoxlite}); err != nil {
		t.Fatalf("SaveVMState returned error: %v", err)
	}
	reconciled, err := reconcilePendingSandboxState(ctx, store, session, time.Now())
	if err != nil {
		t.Fatalf("reconcilePendingSandboxState returned error: %v", err)
	}
	if reconciled.Summary.VMStatus != domain.VMStatusFailed {
		t.Fatalf("status = %q", reconciled.Summary.VMStatus)
	}
	vmState, err := store.GetVMState(session.Summary.ID)
	if err != nil {
		t.Fatalf("GetVMState returned error: %v", err)
	}
	if vmState.LastError != stalePendingSandboxLastError || vmState.StoppedAt.IsZero() {
		t.Fatalf("vmState = %#v", vmState)
	}
	events, err := store.ListEvents(ctx, session.Summary.ID)
	if err != nil || len(events) != 1 || events[0].Type != "sandbox.startup_interrupted" {
		t.Fatalf("events=%#v err=%v", events, err)
	}

	running := &domain.Sandbox{Summary: domain.SandboxSummary{VMStatus: domain.VMStatusRunning}}
	if got, err := reconcilePendingSandboxState(ctx, store, running, time.Now()); err != nil || got != running {
		t.Fatalf("running session got=%#v err=%v", got, err)
	}
	fresh := &domain.Sandbox{Summary: domain.SandboxSummary{VMStatus: domain.VMStatusPending, CreatedAt: time.Now().Add(time.Hour)}}
	if got, err := reconcilePendingSandboxState(ctx, store, fresh, time.Now()); err != nil || got != fresh {
		t.Fatalf("fresh session got=%#v err=%v", got, err)
	}
	if err := startCapabilityProxy(context.Background(), nil); err != nil {
		t.Fatalf("startCapabilityProxy nil returned error: %v", err)
	}
}

func TestReconcilePersistedProjectRunsMarksInterruptedRunsFailed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:    root,
		SandboxRoot: filepath.Join(root, "sandboxes"),
		DbAddr:      filepath.Join(root, "data.db"),
	}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:         "project-1",
		Name:       "project",
		SourcePath: filepath.Join(root, "agent-compose.yml"),
		SourceJSON: `{}`,
	})
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	for _, run := range []domain.ProjectRunRecord{
		{RunID: "run-pending", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusPending},
		{RunID: "run-running", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning},
		{RunID: "run-succeeded", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusSucceeded, Error: "keep"},
		{RunID: "run-canceled", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusCanceled, Error: "keep canceled"},
	} {
		if _, err := store.CreateProjectRun(ctx, run); err != nil {
			t.Fatalf("CreateProjectRun(%s) returned error: %v", run.RunID, err)
		}
	}
	if err := reconcilePersistedProjectRuns(ctx, store, time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("reconcilePersistedProjectRuns returned error: %v", err)
	}
	for _, runID := range []string{"run-pending", "run-running"} {
		run, err := store.GetProjectRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetProjectRun(%s) returned error: %v", runID, err)
		}
		if run.Status != domain.ProjectRunStatusFailed || run.ExitCode != 1 || !strings.Contains(run.Error, "daemon interrupted") {
			t.Fatalf("reconciled run %s = %#v", runID, run)
		}
		if run.StartedAt.IsZero() || run.CompletedAt.IsZero() {
			t.Fatalf("reconciled run %s timestamps not set: %#v", runID, run)
		}
	}
	for runID, wantErr := range map[string]string{"run-succeeded": "keep", "run-canceled": "keep canceled"} {
		run, err := store.GetProjectRun(ctx, runID)
		if err != nil {
			t.Fatalf("GetProjectRun(%s) returned error: %v", runID, err)
		}
		if run.Error != wantErr {
			t.Fatalf("terminal run %s changed: %#v", runID, run)
		}
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-fresh", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning,
	}); err != nil {
		t.Fatalf("CreateProjectRun(run-fresh) returned error: %v", err)
	}
	if err := reconcilePersistedProjectRuns(ctx, store, time.Now().Add(-2*time.Second)); err != nil {
		t.Fatalf("fresh reconcilePersistedProjectRuns returned error: %v", err)
	}
	fresh, err := store.GetProjectRun(ctx, "run-fresh")
	if err != nil {
		t.Fatalf("GetProjectRun(run-fresh) returned error: %v", err)
	}
	if fresh.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("fresh run status = %#v", fresh)
	}
}

func TestIntegrationReconcilePendingSessionStateMarksStaleStartupFailed(t *testing.T) {
	TestReconcilePendingSessionStateMarksStaleStartupFailed(t)
}

func TestE2EReconcilePendingSessionStateMarksStaleStartupFailed(t *testing.T) {
	TestReconcilePendingSessionStateMarksStaleStartupFailed(t)
}
