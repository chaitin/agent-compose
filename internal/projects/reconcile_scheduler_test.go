package projects

import (
	"context"
	"errors"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestReconcileSchedulersReportsUnifiedSchedulerStateChanges(t *testing.T) {
	t.Parallel()

	project, currentRecord, currentDefinition := schedulerReconcileTestState()
	tests := []struct {
		name            string
		prepare         func(*domain.ProjectSchedulerRecord, *domain.Scheduler, *domain.ProjectSchedulerRecord, *domain.Scheduler)
		wantAction      string
		wantEnableWrite bool
	}{
		{
			name: "missing scheduler is created",
			prepare: func(_ *domain.ProjectSchedulerRecord, _ *domain.Scheduler, existingRecord *domain.ProjectSchedulerRecord, _ *domain.Scheduler) {
				*existingRecord = domain.ProjectSchedulerRecord{}
			},
			wantAction:      ChangeActionCreated,
			wantEnableWrite: true,
		},
		{
			name: "projection-only change is updated",
			prepare: func(_ *domain.ProjectSchedulerRecord, _ *domain.Scheduler, existingRecord *domain.ProjectSchedulerRecord, _ *domain.Scheduler) {
				existingRecord.Revision--
			},
			wantAction:      ChangeActionUpdated,
			wantEnableWrite: true,
		},
		{
			name: "definition-only change is updated",
			prepare: func(_ *domain.ProjectSchedulerRecord, _ *domain.Scheduler, _ *domain.ProjectSchedulerRecord, existingDefinition *domain.Scheduler) {
				existingDefinition.Script = "function main() { return 'old'; }"
			},
			wantAction:      ChangeActionUpdated,
			wantEnableWrite: true,
		},
		{
			name: "enabled scheduler disabled by spec is updated",
			prepare: func(record *domain.ProjectSchedulerRecord, definition *domain.Scheduler, _ *domain.ProjectSchedulerRecord, _ *domain.Scheduler) {
				record.Enabled = false
				definition.Summary.Enabled = false
			},
			wantAction: ChangeActionUpdated,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			record := currentRecord
			definition := currentDefinition
			existingRecord := currentRecord
			existingDefinition := currentDefinition
			tt.prepare(&record, &definition, &existingRecord, &existingDefinition)
			store := &schedulerReconcileStateStore{existingDefinition: existingDefinition}
			if existingRecord.ID != "" {
				store.existingRecord = &existingRecord
			}

			changes, unchanged, err := ReconcileSchedulers(context.Background(), store, ReconcileSchedulersRequest{Project: project, Schedulers: []domain.ProjectSchedulerRecord{record}, Definitions: []domain.Scheduler{definition}}, ReconcileSchedulerOptions{})
			if err != nil {
				t.Fatalf("ReconcileSchedulers returned error: %v", err)
			}
			if unchanged {
				t.Fatal("ReconcileSchedulers reported changed scheduler state as unchanged")
			}
			assertUnifiedSchedulerChange(t, changes, tt.wantAction, record.ID)
			if store.enableWrites != boolToCount(tt.wantEnableWrite) {
				t.Fatalf("enable writes = %d, want %d", store.enableWrites, boolToCount(tt.wantEnableWrite))
			}
		})
	}
}

func TestReconcileSchedulersRemovalIsIdempotent(t *testing.T) {
	t.Parallel()

	project, record, _ := schedulerReconcileTestState()
	tests := []struct {
		name        string
		enabled     bool
		wantChanges int
		wantSame    bool
	}{
		{name: "enabled scheduler is removed", enabled: true, wantChanges: 1},
		{name: "disabled scheduler is already removed", wantSame: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing := record
			existing.Enabled = tt.enabled
			store := &schedulerReconcileStateStore{existingRecord: &existing, listedRecords: []domain.ProjectSchedulerRecord{existing}, listConfigured: true}
			changes, unchanged, err := ReconcileSchedulers(context.Background(), store, ReconcileSchedulersRequest{Project: project}, ReconcileSchedulerOptions{})
			if err != nil {
				t.Fatalf("ReconcileSchedulers returned error: %v", err)
			}
			if unchanged != tt.wantSame || len(changes) != tt.wantChanges {
				t.Fatalf("unchanged/changes = %t/%#v, want %t/%d", unchanged, changes, tt.wantSame, tt.wantChanges)
			}
			if tt.wantChanges == 1 {
				assertUnifiedSchedulerChange(t, changes, ChangeActionRemoved, record.ID)
			}
		})
	}
}

func TestReconcileSchedulersPreservesPartialMutationSignalAcrossSchedulers(t *testing.T) {
	project, first, firstDefinition := schedulerReconcileTestState()
	first.Revision = 3
	firstDefinition.Summary.ProjectRevision = 3
	second := first
	second.ID = "scheduler-2"
	second.SchedulerID = "scheduler-2"
	second.AgentName = "worker-2"
	secondDefinition := firstDefinition
	secondDefinition.Summary.ID = second.ID
	secondDefinition.Summary.ProjectSchedulerID = second.SchedulerID
	secondDefinition.Summary.AgentName = second.AgentName
	store := &schedulerReconcileStateStore{
		existingRecord:     &domain.ProjectSchedulerRecord{ID: first.ID, ProjectID: project.ID, SchedulerID: first.SchedulerID, Revision: 2, Enabled: true, SpecJSON: first.SpecJSON},
		existingDefinition: firstDefinition,
		getErrOnCall:       2,
	}
	_, _, err := ReconcileSchedulers(context.Background(), store, ReconcileSchedulersRequest{
		Project:     project,
		Schedulers:  []domain.ProjectSchedulerRecord{first, second},
		Definitions: []domain.Scheduler{firstDefinition, secondDefinition},
	}, ReconcileSchedulerOptions{})
	if err == nil || !schedulerReconcileNeedsFailClosed(err) {
		t.Fatalf("partial multi-scheduler error = %v, want fail-closed signal", err)
	}
}

func TestReconcileSchedulersFailsClosedOnRemovedSchedulerWriteError(t *testing.T) {
	project, record, _ := schedulerReconcileTestState()
	writeErr := errors.New("disable removed scheduler failed")
	store := &schedulerReconcileStateStore{
		existingRecord: &record,
		listedRecords:  []domain.ProjectSchedulerRecord{record},
		listConfigured: true,
		setEnabledErr:  writeErr,
	}
	_, _, err := ReconcileSchedulers(context.Background(), store, ReconcileSchedulersRequest{Project: project}, ReconcileSchedulerOptions{})
	if err == nil || !errors.Is(err, writeErr) || !schedulerReconcileNeedsFailClosed(err) {
		t.Fatalf("removed scheduler write error = %v, want fail-closed error", err)
	}
}

func TestReconcileSchedulersTreatsMissingRemovedSchedulerAsConverged(t *testing.T) {
	project, record, _ := schedulerReconcileTestState()
	store := &schedulerReconcileStateStore{
		existingRecord: &record,
		listedRecords:  []domain.ProjectSchedulerRecord{record},
		listConfigured: true,
		setEnabledErr:  domain.ResourceError(domain.ErrNotFound, "scheduler", record.SchedulerID, "scheduler already removed", nil),
	}
	changes, unchanged, err := ReconcileSchedulers(context.Background(), store, ReconcileSchedulersRequest{Project: project}, ReconcileSchedulerOptions{})
	if err != nil {
		t.Fatalf("ReconcileSchedulers returned error: %v", err)
	}
	if unchanged || len(changes) != 1 || changes[0].Action != ChangeActionRemoved {
		t.Fatalf("unchanged/changes = %t/%#v, want false/one removed change", unchanged, changes)
	}
}

func TestReconcileSchedulersFailureReportsCleanupAndPartialChanges(t *testing.T) {
	t.Parallel()

	project, currentRecord, currentDefinition := schedulerReconcileTestState()
	existingRecord := currentRecord
	existingRecord.Revision--
	replaceErr := errors.New("replace failed")
	enableErr := errors.New("enable failed")
	refreshErr := errors.New("refresh failed")
	tests := []struct {
		name           string
		getErr         error
		upsertErr      error
		replaceErr     error
		enableErr      error
		refreshErr     error
		wantCleanup    bool
		wantChanges    int
		wantFailClosed bool
	}{
		{name: "read before mutation", getErr: errors.New("read failed")},
		{name: "ambiguous scheduler write", upsertErr: errors.New("write result unknown"), wantFailClosed: true},
		{name: "trigger replacement", replaceErr: replaceErr, wantCleanup: true, wantFailClosed: true},
		{name: "scheduler enable", enableErr: enableErr, wantCleanup: true, wantFailClosed: true},
		{name: "controller refresh", refreshErr: refreshErr, wantChanges: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &schedulerReconcileStateStore{
				existingRecord:     &existingRecord,
				existingDefinition: currentDefinition,
				getErr:             tt.getErr,
				upsertErr:          tt.upsertErr,
				replaceErr:         tt.replaceErr,
				enableErr:          tt.enableErr,
			}
			cleanupID := ""
			changes, unchanged, err := ReconcileSchedulers(context.Background(), store, ReconcileSchedulersRequest{Project: project, Schedulers: []domain.ProjectSchedulerRecord{currentRecord}, Definitions: []domain.Scheduler{currentDefinition}}, ReconcileSchedulerOptions{
				CleanupFailedScheduler: func(_ context.Context, _ domain.ProjectSchedulerRecord, schedulerID string) {
					cleanupID = schedulerID
				},
				RefreshSchedulers: func(context.Context) error { return tt.refreshErr },
			})
			if err == nil {
				t.Fatal("ReconcileSchedulers returned nil error")
			}
			if unchanged || len(changes) != tt.wantChanges {
				t.Fatalf("unchanged/changes = %t/%#v, want false/%d", unchanged, changes, tt.wantChanges)
			}
			if got := schedulerReconcileNeedsFailClosed(err); got != tt.wantFailClosed {
				t.Fatalf("schedulerReconcileNeedsFailClosed = %t, want %t for %v", got, tt.wantFailClosed, err)
			}
			if (cleanupID != "") != tt.wantCleanup {
				t.Fatalf("cleanup scheduler id = %q, want cleanup %t", cleanupID, tt.wantCleanup)
			}
			if tt.wantCleanup && cleanupID != currentRecord.ID {
				t.Fatalf("cleanup scheduler id = %q, want %q", cleanupID, currentRecord.ID)
			}
			if tt.wantChanges == 1 {
				assertUnifiedSchedulerChange(t, changes, ChangeActionUpdated, currentRecord.ID)
			}
		})
	}
}

func schedulerReconcileTestState() (domain.ProjectRecord, domain.ProjectSchedulerRecord, domain.Scheduler) {
	project := domain.ProjectRecord{ID: "project-1"}
	record := domain.ProjectSchedulerRecord{
		ID: "scheduler-1", ProjectID: project.ID, SchedulerID: "scheduler-1", AgentName: "worker",
		Revision: 2, Enabled: true, TriggerCount: 1, SpecJSON: `{"enabled":true}`,
	}
	definition := domain.Scheduler{
		Summary: domain.SchedulerSummary{
			ID: "scheduler-1", Name: "worker scheduler", Enabled: true, Runtime: domain.SchedulerRuntimeScheduler,
			ProjectID: project.ID, ProjectRevision: 2, AgentName: "worker", ProjectSchedulerID: record.SchedulerID,
		},
		Script: "function main() { return 'current'; }",
		Triggers: []domain.SchedulerTrigger{{
			SchedulerID: record.ID, ID: "daily", Kind: domain.SchedulerTriggerKindInterval, IntervalMs: 86_400_000, Enabled: true,
		}},
	}
	return project, record, definition
}

func assertUnifiedSchedulerChange(t *testing.T, changes []Change, action, schedulerID string) {
	t.Helper()
	if len(changes) != 1 || changes[0].Action != action || changes[0].ResourceType != "scheduler" || changes[0].ResourceID != schedulerID {
		t.Fatalf("changes = %#v, want one %s scheduler %s", changes, action, schedulerID)
	}
}

func boolToCount(value bool) int {
	if value {
		return 1
	}
	return 0
}

type schedulerReconcileStateStore struct {
	existingRecord     *domain.ProjectSchedulerRecord
	existingDefinition domain.Scheduler
	listedRecords      []domain.ProjectSchedulerRecord
	listConfigured     bool
	savedRecord        domain.ProjectSchedulerRecord
	getErr             error
	upsertErr          error
	replaceErr         error
	enableErr          error
	setEnabledErr      error
	getErrOnCall       int
	getCalls           int
	enableWrites       int
}

func (s *schedulerReconcileStateStore) GetProjectScheduler(context.Context, string, string) (domain.ProjectSchedulerRecord, error) {
	s.getCalls++
	if s.getErr != nil || (s.getErrOnCall > 0 && s.getCalls == s.getErrOnCall) {
		if s.getErr != nil {
			return domain.ProjectSchedulerRecord{}, s.getErr
		}
		return domain.ProjectSchedulerRecord{}, errors.New("scheduler read failed after partial mutation")
	}
	if s.existingRecord == nil {
		return domain.ProjectSchedulerRecord{}, domain.ResourceError(domain.ErrNotFound, "scheduler", "scheduler-1", "scheduler not found", nil)
	}
	return *s.existingRecord, nil
}

func (s *schedulerReconcileStateStore) UpsertProjectScheduler(_ context.Context, item domain.ProjectSchedulerRecord) (domain.ProjectSchedulerRecord, error) {
	s.savedRecord = item
	if s.upsertErr != nil {
		return item, s.upsertErr
	}
	return item, nil
}

func (s *schedulerReconcileStateStore) SetProjectSchedulerEnabled(_ context.Context, _, _ string, enabled bool) (domain.ProjectSchedulerRecord, error) {
	s.enableWrites++
	if s.setEnabledErr != nil {
		return domain.ProjectSchedulerRecord{}, s.setEnabledErr
	}
	if s.enableErr != nil {
		return domain.ProjectSchedulerRecord{}, s.enableErr
	}
	item := s.savedRecord
	if item.ID == "" && s.existingRecord != nil {
		item = *s.existingRecord
	}
	item.Enabled = enabled
	s.savedRecord = item
	return item, nil
}

func (s *schedulerReconcileStateStore) ListProjectSchedulers(context.Context, string) ([]domain.ProjectSchedulerRecord, error) {
	if s.listConfigured {
		return append([]domain.ProjectSchedulerRecord(nil), s.listedRecords...), nil
	}
	if s.savedRecord.ID != "" {
		return []domain.ProjectSchedulerRecord{s.savedRecord}, nil
	}
	if s.existingRecord != nil {
		return []domain.ProjectSchedulerRecord{*s.existingRecord}, nil
	}
	return nil, nil
}

func (s *schedulerReconcileStateStore) GetScheduler(context.Context, string) (domain.Scheduler, error) {
	return s.existingDefinition, nil
}

func (s *schedulerReconcileStateStore) ReplaceSchedulerTriggers(_ context.Context, _ string, triggers []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error) {
	if s.replaceErr != nil {
		return nil, s.replaceErr
	}
	return triggers, nil
}

func TestReconcileSchedulersSkipsWritesForUnchangedScheduler(t *testing.T) {
	t.Parallel()

	project := domain.ProjectRecord{ID: "project-1"}
	trigger := domain.SchedulerTrigger{
		SchedulerID: "scheduler-1",
		ID:          "daily",
		Kind:        domain.SchedulerTriggerKindInterval,
		IntervalMs:  86_400_000,
		Enabled:     true,
	}
	scheduler := domain.ProjectSchedulerRecord{
		ProjectID:    project.ID,
		SchedulerID:  "scheduler-1",
		AgentName:    "worker",
		ID:           "scheduler-1",
		Revision:     3,
		Enabled:      true,
		TriggerCount: 1,
		SpecJSON:     `{"enabled":true}`,
	}
	definition := domain.Scheduler{
		Summary: domain.SchedulerSummary{
			ID: "scheduler-1", Name: "worker scheduler", Enabled: true, Runtime: domain.SchedulerRuntimeScheduler,
			DefaultAgent: "codex", SandboxPolicy: domain.SchedulerSandboxPolicySticky,
			ProjectID: project.ID, ProjectRevision: 3, AgentName: "worker", ProjectSchedulerID: scheduler.SchedulerID,
		},
		Script:   `scheduler.interval("daily", function daily() {}, 86400000);`,
		Triggers: []domain.SchedulerTrigger{trigger},
	}
	store := &unchangedSchedulerReconcileStore{scheduler: scheduler, definition: definition}

	changes, unchanged, err := ReconcileSchedulers(context.Background(), store, ReconcileSchedulersRequest{Project: project, Schedulers: []domain.ProjectSchedulerRecord{scheduler}, Definitions: []domain.Scheduler{definition}}, ReconcileSchedulerOptions{})
	if err != nil {
		t.Fatalf("ReconcileSchedulers returned error: %v", err)
	}
	if !unchanged {
		t.Fatal("ReconcileSchedulers reported an identical scheduler as changed")
	}
	if len(changes) != 1 || changes[0].Action != ChangeActionUnchanged || changes[0].ResourceType != "scheduler" || changes[0].ResourceID != scheduler.ID {
		t.Fatalf("changes = %#v", changes)
	}
	if len(store.writes) != 0 {
		t.Fatalf("identical scheduler caused writes: %v", store.writes)
	}
}

type unchangedSchedulerReconcileStore struct {
	scheduler  domain.ProjectSchedulerRecord
	definition domain.Scheduler
	writes     []string
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

func (s *unchangedSchedulerReconcileStore) GetScheduler(context.Context, string) (domain.Scheduler, error) {
	return s.definition, nil
}

func (s *unchangedSchedulerReconcileStore) ReplaceSchedulerTriggers(_ context.Context, _ string, triggers []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error) {
	s.writes = append(s.writes, "replace triggers")
	return triggers, nil
}
