package projects

import (
	"context"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestProjectSchedulersFromManagedLoadersIncludesDisabled(t *testing.T) {
	schedulers := ProjectSchedulersFromManagedLoaders([]domain.Loader{{
		Summary: domain.LoaderSummary{
			ID:                 "loader-1",
			Enabled:            false,
			ManagedProjectID:   "project-1",
			ManagedRevision:    3,
			ManagedAgentName:   "worker",
			ManagedSchedulerID: "scheduler-1",
		},
		Triggers: []domain.LoaderTrigger{{ID: "trigger-1"}, {ID: "trigger-2"}},
	}})
	if len(schedulers) != 1 {
		t.Fatalf("schedulers = %#v", schedulers)
	}
	got := schedulers[0]
	if got.ProjectID != "project-1" || got.AgentName != "worker" || got.SchedulerID != "scheduler-1" ||
		got.ManagedLoaderID != "loader-1" || got.Enabled || got.TriggerCount != 2 || got.Revision != 3 {
		t.Fatalf("scheduler projection = %#v", got)
	}
}

func TestReconcileManagedSchedulersDisablesRemovedManagedLoader(t *testing.T) {
	store := &reconcileProjectionStore{
		loaders: map[string]domain.Loader{
			"loader-1": managedSchedulerLoader("loader-1", true, "old script"),
		},
	}
	changes, unchanged, err := ReconcileManagedSchedulers(context.Background(), store, domain.ProjectRecord{ID: "project-1"}, nil, nil, ReconcileSchedulerOptions{})
	if err != nil {
		t.Fatalf("ReconcileManagedSchedulers returned error: %v", err)
	}
	if unchanged || !store.loaderDisabled["loader-1"] {
		t.Fatalf("unchanged=%v loaderDisabled=%#v", unchanged, store.loaderDisabled)
	}
	assertProjectChange(t, changes, ChangeActionRemoved, "project_scheduler", "scheduler-1")
	assertProjectChange(t, changes, ChangeActionRemoved, "loader", "loader-1")
}

func TestReconcileManagedSchedulersProjectActionFollowsManagedLoaderChange(t *testing.T) {
	currentLoader := managedSchedulerLoader("loader-1", true, "new script")
	currentScheduler := ProjectSchedulersFromManagedLoaders([]domain.Loader{currentLoader})[0]
	store := &reconcileProjectionStore{
		loaders: map[string]domain.Loader{
			"loader-1": managedSchedulerLoader("loader-1", true, "old script"),
		},
		schedulers: map[string]domain.ProjectSchedulerRecord{},
	}
	changes, unchanged, err := ReconcileManagedSchedulers(context.Background(), store, domain.ProjectRecord{ID: "project-1"}, []domain.ProjectSchedulerRecord{currentScheduler}, []domain.Loader{currentLoader}, ReconcileSchedulerOptions{})
	if err != nil {
		t.Fatalf("ReconcileManagedSchedulers returned error: %v", err)
	}
	if unchanged {
		t.Fatal("ReconcileManagedSchedulers reported unchanged for script update")
	}
	assertProjectChange(t, changes, ChangeActionUpdated, "project_scheduler", "scheduler-1")
	assertProjectChange(t, changes, ChangeActionUpdated, "loader", "loader-1")
}

func managedSchedulerLoader(id string, enabled bool, script string) domain.Loader {
	return domain.Loader{
		Summary: domain.LoaderSummary{
			ID:                 id,
			Name:               "worker",
			Enabled:            enabled,
			Runtime:            domain.LoaderRuntimeScheduler,
			ManagedProjectID:   "project-1",
			ManagedRevision:    1,
			ManagedAgentName:   "worker",
			ManagedSchedulerID: "scheduler-1",
		},
		Script:   script,
		Triggers: []domain.LoaderTrigger{{LoaderID: id, ID: "trigger-1", Kind: domain.LoaderTriggerKindInterval, Enabled: true, IntervalMs: 1000}},
	}
}

type reconcileProjectionStore struct {
	loaders        map[string]domain.Loader
	schedulers     map[string]domain.ProjectSchedulerRecord
	loaderDisabled map[string]bool
}

func (s *reconcileProjectionStore) GetProjectScheduler(_ context.Context, projectID, schedulerID string) (domain.ProjectSchedulerRecord, error) {
	if s.schedulers == nil {
		return domain.ProjectSchedulerRecord{}, domain.ErrNotFound
	}
	item, ok := s.schedulers[projectID+"/"+schedulerID]
	if !ok {
		return domain.ProjectSchedulerRecord{}, domain.ErrNotFound
	}
	return item, nil
}

func (s *reconcileProjectionStore) UpsertProjectScheduler(_ context.Context, scheduler domain.ProjectSchedulerRecord) (domain.ProjectSchedulerRecord, error) {
	if s.schedulers == nil {
		s.schedulers = map[string]domain.ProjectSchedulerRecord{}
	}
	s.schedulers[scheduler.ProjectID+"/"+scheduler.SchedulerID] = scheduler
	return scheduler, nil
}

func (s *reconcileProjectionStore) SetProjectSchedulerEnabled(_ context.Context, projectID, schedulerID string, enabled bool) (domain.ProjectSchedulerRecord, error) {
	item, ok := s.schedulers[projectID+"/"+schedulerID]
	if !ok {
		return domain.ProjectSchedulerRecord{}, domain.ErrNotFound
	}
	item.Enabled = enabled
	s.schedulers[projectID+"/"+schedulerID] = item
	return item, nil
}

func (s *reconcileProjectionStore) ListManagedLoaders(_ context.Context, projectID string) ([]domain.Loader, error) {
	items := make([]domain.Loader, 0, len(s.loaders))
	for _, loader := range s.loaders {
		if loader.Summary.ManagedProjectID == projectID {
			items = append(items, loader)
		}
	}
	return items, nil
}

func (s *reconcileProjectionStore) GetLoaderIfExists(_ context.Context, loaderID string) (domain.Loader, bool, error) {
	loader, ok := s.loaders[loaderID]
	return loader, ok, nil
}

func (s *reconcileProjectionStore) UpsertManagedLoader(_ context.Context, item domain.Loader) (domain.Loader, error) {
	if s.loaders == nil {
		s.loaders = map[string]domain.Loader{}
	}
	s.loaders[item.Summary.ID] = item
	return item, nil
}

func (s *reconcileProjectionStore) ReplaceLoaderTriggers(_ context.Context, loaderID string, triggers []domain.LoaderTrigger) ([]domain.LoaderTrigger, error) {
	item := s.loaders[loaderID]
	item.Triggers = triggers
	s.loaders[loaderID] = item
	return triggers, nil
}

func (s *reconcileProjectionStore) SetLoaderEnabled(_ context.Context, loaderID string, enabled bool) error {
	if s.loaderDisabled == nil {
		s.loaderDisabled = map[string]bool{}
	}
	item := s.loaders[loaderID]
	item.Summary.Enabled = enabled
	s.loaders[loaderID] = item
	s.loaderDisabled[loaderID] = !enabled
	return nil
}
