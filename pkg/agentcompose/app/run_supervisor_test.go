package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/samber/do/v2"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/internal/testutil"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/configstore"
)

func TestRunSupervisorStopActiveRunRemovesActiveBeforeMarkCanceled(t *testing.T) {
	ctx := context.Background()
	store := newRunSupervisorTestConfigStore(t)
	if _, err := store.UpsertProject(ctx, domain.ProjectRecord{
		ID:         "project-1",
		Name:       "Project",
		SourcePath: "/tmp/project",
		SourceJSON: "{}",
	}); err != nil {
		t.Fatalf("UpsertProject returned error: %v", err)
	}
	if _, err := store.CreateProjectRun(ctx, domain.ProjectRunRecord{
		RunID:       "run-1",
		ProjectID:   "project-1",
		ProjectName: "Project",
		AgentName:   "worker",
		Source:      domain.ProjectRunSourceManual,
		Status:      domain.ProjectRunStatusRunning,
		ResultJSON:  "{}",
	}); err != nil {
		t.Fatalf("CreateProjectRun returned error: %v", err)
	}

	cancelCalls := 0
	supervisor := &RunSupervisor{
		store:  store,
		active: map[string]*activeRun{"run-1": {cancel: func() { cancelCalls++ }}},
	}
	stopped, err := supervisor.StopActiveRun(ctx, "run-1", "user stop")
	if err != nil {
		t.Fatalf("StopActiveRun returned error: %v", err)
	}
	if !stopped || cancelCalls != 1 {
		t.Fatalf("first stop stopped=%v cancelCalls=%d, want true/1", stopped, cancelCalls)
	}
	if _, ok := supervisor.active["run-1"]; ok {
		t.Fatalf("run remained active after stop")
	}

	stopped, err = supervisor.StopActiveRun(ctx, "run-1", "second stop")
	if err != nil {
		t.Fatalf("second StopActiveRun returned error: %v", err)
	}
	if stopped || cancelCalls != 1 {
		t.Fatalf("second stop stopped=%v cancelCalls=%d, want false/1", stopped, cancelCalls)
	}
	run, err := store.GetProjectRun(ctx, "run-1")
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if run.Status != domain.ProjectRunStatusCanceled || run.Error != "user stop" {
		t.Fatalf("run after stop = %#v", run)
	}
}

func TestRunSupervisorUnregisterKeepsStoppingRun(t *testing.T) {
	active := &activeRun{
		cancel:   func() {},
		stopping: true,
	}
	supervisor := &RunSupervisor{
		active: map[string]*activeRun{"run-1": active},
	}
	supervisor.unregister("run-1")
	if got := supervisor.active["run-1"]; got != active {
		t.Fatalf("stopping run was unregistered: %#v", supervisor.active)
	}

	active.stopping = false
	supervisor.unregister("run-1")
	if _, ok := supervisor.active["run-1"]; ok {
		t.Fatalf("inactive run remained registered")
	}
}

func newRunSupervisorTestConfigStore(t *testing.T) *configstore.ConfigStore {
	t.Helper()
	root := t.TempDir()
	di := do.New()
	do.ProvideValue(di, context.Background())
	do.ProvideValue(di, &appconfig.Config{
		DataRoot: root,
		DbAddr:   filepath.Join(root, "data.db"),
	})
	store, err := testutil.OpenConfigStore(t, di)
	if err != nil {
		t.Fatalf("NewConfigStore returned error: %v", err)
	}
	t.Cleanup(func() {
		if db := store.DB(); db != nil {
			_ = db.Close()
		}
	})
	return store
}
