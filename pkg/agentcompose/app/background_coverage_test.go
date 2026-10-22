package app

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	"github.com/chaitin/agent-compose/pkg/internal/testutil"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/storage/configstore"
	"github.com/chaitin/agent-compose/pkg/storage/sandboxstore"
)

func TestReconcilePendingSessionStateMarksStaleStartupFailed(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), RuntimeDriver: driverpkg.RuntimeDriverBoxlite}
	store, err := sandboxstore.NewWithConfig(config)
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
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("UpsertProjectAgent returned error: %v", err)
	}
	for _, run := range []domain.ProjectRunRecord{
		{RunID: "run-pending", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID, Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusPending},
		{RunID: "run-running", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID, Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning},
		{RunID: "run-succeeded", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID, Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusSucceeded, Error: "keep"},
		{RunID: "run-canceled", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID, Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusCanceled, Error: "keep canceled"},
	} {
		if _, err := store.CreateProjectRun(ctx, run); err != nil {
			t.Fatalf("CreateProjectRun(%s) returned error: %v", run.RunID, err)
		}
	}
	completions := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	if err := reconcilePersistedProjectRuns(ctx, projectRunReconcileContext{ConfigDB: store, Completions: completions, RootCtx: ctx, StartedAt: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatalf("reconcilePersistedProjectRuns returned error: %v", err)
	}
	for _, runID := range []string{"run-pending", "run-running"} {
		if _, err := completions.Complete(ctx, runs.TransitionRequest{RunID: runID, Status: domain.ProjectRunStatusFailed}); err != nil {
			t.Fatalf("complete reconciled run %s: %v", runID, err)
		}
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
		RunID: "run-fresh", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID, Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning,
	}); err != nil {
		t.Fatalf("CreateProjectRun(run-fresh) returned error: %v", err)
	}
	if err := reconcilePersistedProjectRuns(ctx, projectRunReconcileContext{ConfigDB: store, Completions: completions, RootCtx: ctx, StartedAt: time.Now().Add(-2 * time.Second)}); err != nil {
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

// failingReattacher fails every re-attach, as when the sandbox row or runtime
// can no longer be loaded after a restart.
type failingReattacher struct{ runIDs []string }

func (r *failingReattacher) ReattachRun(run domain.ProjectRunRecord, onFailure func(error)) {
	r.runIDs = append(r.runIDs, run.RunID)
	onFailure(errors.New("sandbox lookup failed"))
}

func TestReconcilePersistedProjectRunsFinalizesFailedReattach(t *testing.T) {
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
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: filepath.Join(root, "agent-compose.yml"), SourceJSON: `{}`})
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("UpsertProjectAgent returned error: %v", err)
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-detached", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID,
		Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning, Detach: true, DetachKind: domain.DetachKindAgent, ExecutionID: "exec-1",
	}); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	if err := store.SaveLLMFacadeToken(ctx, llms.FacadeToken{TokenHash: "hash-run-detached", SandboxID: "sandbox-1", Source: "agent", RunID: "run-detached"}); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}
	completions := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	reattacher := &failingReattacher{}
	if err := reconcilePersistedProjectRuns(ctx, projectRunReconcileContext{ConfigDB: store, Completions: completions, Reattacher: reattacher, RootCtx: ctx, StartedAt: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatalf("reconcilePersistedProjectRuns returned error: %v", err)
	}
	if len(reattacher.runIDs) != 1 || reattacher.runIDs[0] != "run-detached" {
		t.Fatalf("re-attached runs = %v, want [run-detached]", reattacher.runIDs)
	}
	// The re-attach failure is staged as interrupted, so the run is finalized
	// and the tokens kept for it are retired without waiting for completion.
	if n := countRunFacadeTokens(t, ctx, store, "run-detached"); n != 0 {
		t.Fatalf("facade tokens for the failed re-attach = %d, want 0", n)
	}
	// A failed re-attach is staged as interrupted instead of being left
	// running with no follower.
	if _, err := completions.Complete(ctx, runs.TransitionRequest{RunID: "run-detached", Status: domain.ProjectRunStatusFailed}); err != nil {
		t.Fatalf("complete reconciled run: %v", err)
	}
	run, err := store.GetProjectRun(ctx, "run-detached")
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if run.Status != domain.ProjectRunStatusFailed || !strings.Contains(run.Error, "re-attach failed: sandbox lookup failed") {
		t.Fatalf("run after failed re-attach = %#v", run)
	}
}

func TestIntegrationReconcilePendingSessionStateMarksStaleStartupFailed(t *testing.T) {
	TestReconcilePendingSessionStateMarksStaleStartupFailed(t)
}

func TestE2EReconcilePendingSessionStateMarksStaleStartupFailed(t *testing.T) {
	TestReconcilePendingSessionStateMarksStaleStartupFailed(t)
}

// A detached run whose result was staged before the restart is finishing: the
// completion manager finalizes it with that result, and re-attaching it would
// race that with a second result.
func TestReconcilePersistedProjectRunsLeavesStagedDetachedRunsToCompletion(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), DbAddr: filepath.Join(root, "data.db")}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: filepath.Join(root, "agent-compose.yml"), SourceJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	run, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-staged", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID,
		Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning, Detach: true, ExecutionID: "exec-1",
		CleanupPolicy: domain.ProjectRunCleanupKeepRunning,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.StageProjectRunCompletion(ctx, domain.ProjectRunCompletionRecord{
		RunID: run.RunID, TargetStatus: domain.ProjectRunStatusSucceeded, TransitionJSON: `{"run_id":"run-staged","status":"succeeded"}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	completions := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: store})
	reattacher := &failingReattacher{}
	if err := reconcilePersistedProjectRuns(ctx, projectRunReconcileContext{ConfigDB: store, Completions: completions, Reattacher: reattacher, RootCtx: ctx, StartedAt: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatalf("reconcilePersistedProjectRuns returned error: %v", err)
	}
	if len(reattacher.runIDs) != 0 {
		t.Fatalf("re-attached %v; a run with a staged result must be left to the completion manager", reattacher.runIDs)
	}
}

// failingStageCompletionStore refuses to stage an interrupted completion, as a
// database error would, so the run stays running and unfinalized.
type failingStageCompletionStore struct{ runs.CompletionStore }

func (failingStageCompletionStore) StageProjectRunCompletion(context.Context, domain.ProjectRunCompletionRecord, []domain.ProjectRunEventRecord) (domain.ProjectRunCompletionRecord, bool, error) {
	return domain.ProjectRunCompletionRecord{}, false, errors.New("stage failed")
}

func countRunFacadeTokens(t *testing.T, ctx context.Context, store *configstore.ConfigStore, runID string) int {
	t.Helper()
	var count int
	if err := store.DB().QueryRowContext(ctx, `SELECT COUNT(1) FROM llm_facade_token WHERE run_id = ?`, runID).Scan(&count); err != nil {
		t.Fatalf("count facade tokens: %v", err)
	}
	return count
}

// A failed staging leaves the run running with no follower, so credentials
// retained for a future re-attach must not be deleted yet.
func TestReconcilePersistedProjectRunsKeepsTokensWhenStagingFails(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{DataRoot: root, SandboxRoot: filepath.Join(root, "sandboxes"), DbAddr: filepath.Join(root, "data.db")}
	di := do.New()
	do.ProvideValue(di, ctx)
	do.ProvideValue(di, config)
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	project, err := store.UpsertProject(ctx, domain.ProjectRecord{ID: "project-1", Name: "project", SourcePath: filepath.Join(root, "agent-compose.yml"), SourceJSON: `{}`})
	if err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	agent, err := store.UpsertProjectAgent(ctx, domain.ProjectAgentRecord{ProjectID: project.ID, AgentName: "worker"})
	if err != nil {
		t.Fatalf("UpsertProjectAgent returned error: %v", err)
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID: "run-failing-stage", ProjectID: project.ID, ProjectName: project.Name, AgentName: "worker", AgentID: agent.ID,
		Source: domain.ProjectRunSourceManual, Status: domain.ProjectRunStatusRunning, Detach: true, DetachKind: domain.DetachKindAgent, ExecutionID: "exec-1",
	}); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}
	if err := store.SaveLLMFacadeToken(ctx, llms.FacadeToken{TokenHash: "hash-run-failing-stage", SandboxID: "sandbox-1", Source: "agent", RunID: "run-failing-stage"}); err != nil {
		t.Fatalf("SaveLLMFacadeToken returned error: %v", err)
	}
	completions := runs.NewCompletionManager(runs.CompletionManagerDeps{Store: failingStageCompletionStore{CompletionStore: store}})
	reattacher := &failingReattacher{}
	if err := reconcilePersistedProjectRuns(ctx, projectRunReconcileContext{ConfigDB: store, Completions: completions, Reattacher: reattacher, RootCtx: ctx, StartedAt: time.Now().Add(2 * time.Second)}); err != nil {
		t.Fatalf("reconcilePersistedProjectRuns returned error: %v", err)
	}
	if n := countRunFacadeTokens(t, ctx, store, "run-failing-stage"); n != 1 {
		t.Fatalf("facade tokens after a failed staging = %d, want 1", n)
	}
	run, err := store.GetProjectRun(ctx, "run-failing-stage")
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if run.Status != domain.ProjectRunStatusRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}
}
