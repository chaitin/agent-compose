package projects

import (
	"context"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestReconcileManagedSchedulersSkipsWritesForUnchangedScheduler(t *testing.T) {
	t.Parallel()

	project := domain.ProjectRecord{ID: "project-1"}
	trigger := domain.LoaderTrigger{
		LoaderID:   "loader-1",
		ID:         "daily",
		Kind:       domain.LoaderTriggerKindInterval,
		IntervalMs: 86_400_000,
		Enabled:    true,
	}
	scheduler := domain.ProjectSchedulerRecord{
		ProjectID:       project.ID,
		SchedulerID:     "scheduler-1",
		AgentName:       "worker",
		ManagedLoaderID: "loader-1",
		Revision:        3,
		Enabled:         true,
		TriggerCount:    1,
		SpecJSON:        `{"enabled":true}`,
	}
	loader := domain.Loader{
		Summary: domain.LoaderSummary{
			ID: "loader-1", Name: "worker scheduler", Enabled: true, Runtime: domain.LoaderRuntimeScheduler,
			DefaultAgent: "codex", SandboxPolicy: domain.LoaderSandboxPolicySticky,
			ManagedProjectID: project.ID, ManagedRevision: 3, ManagedAgentName: "worker", ManagedSchedulerID: scheduler.SchedulerID,
		},
		Script:   `scheduler.interval("daily", function daily() {}, 86400000);`,
		Triggers: []domain.LoaderTrigger{trigger},
	}
	store := &unchangedSchedulerReconcileStore{scheduler: scheduler, loader: loader}

	changes, unchanged, err := ReconcileManagedSchedulers(context.Background(), store, project, []domain.ProjectSchedulerRecord{scheduler}, []domain.Loader{loader}, ReconcileSchedulerOptions{})
	if err != nil {
		t.Fatalf("ReconcileManagedSchedulers returned error: %v", err)
	}
	if !unchanged {
		t.Fatal("ReconcileManagedSchedulers reported an identical scheduler as changed")
	}
	if len(changes) != 2 || changes[0].Action != ChangeActionUnchanged || changes[0].ResourceType != "project_scheduler" || changes[1].Action != ChangeActionUnchanged || changes[1].ResourceType != "loader" {
		t.Fatalf("changes = %#v", changes)
	}
	if len(store.writes) != 0 {
		t.Fatalf("identical scheduler caused writes: %v", store.writes)
	}
}

type unchangedSchedulerReconcileStore struct {
	scheduler domain.ProjectSchedulerRecord
	loader    domain.Loader
	writes    []string
}

func (s *unchangedSchedulerReconcileStore) GetProjectScheduler(context.Context, string, string) (domain.ProjectSchedulerRecord, error) {
	return s.scheduler, nil
}

func (s *unchangedSchedulerReconcileStore) UpsertProjectScheduler(_ context.Context, item domain.ProjectSchedulerRecord) (domain.ProjectSchedulerRecord, error) {
	s.writes = append(s.writes, "upsert scheduler")
	return item, nil
}

func (s *unchangedSchedulerReconcileStore) SetProjectSchedulerEnabled(_ context.Context, _, _ string, _ bool) (domain.ProjectSchedulerRecord, error) {
	s.writes = append(s.writes, "set scheduler enabled")
	return s.scheduler, nil
}

func (s *unchangedSchedulerReconcileStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	return []domain.ProjectSchedulerRecord{s.scheduler}, nil
}

func (s *unchangedSchedulerReconcileStore) GetLoaderIfExists(context.Context, string) (domain.Loader, bool, error) {
	return s.loader, true, nil
}

func (s *unchangedSchedulerReconcileStore) UpsertManagedLoader(_ context.Context, item domain.Loader) (domain.Loader, error) {
	s.writes = append(s.writes, "upsert loader")
	return item, nil
}

func (s *unchangedSchedulerReconcileStore) ReplaceLoaderTriggers(_ context.Context, _ string, triggers []domain.LoaderTrigger) ([]domain.LoaderTrigger, error) {
	s.writes = append(s.writes, "replace triggers")
	return triggers, nil
}

func (s *unchangedSchedulerReconcileStore) SetLoaderEnabled(context.Context, string, bool) error {
	s.writes = append(s.writes, "set loader enabled")
	return nil
}
