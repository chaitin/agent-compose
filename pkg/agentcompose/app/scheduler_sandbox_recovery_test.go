package app

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type fakeSchedulerSandboxLister struct {
	listed  domain.SandboxListResult
	err     error
	options domain.SandboxListOptions
}

func (f *fakeSchedulerSandboxLister) ListSandboxes(_ context.Context, options domain.SandboxListOptions) (domain.SandboxListResult, error) {
	f.options = options
	return f.listed, f.err
}

type fakeSchedulerRunReader struct {
	runs     map[string]domain.SchedulerRunSummary
	batch    map[string]domain.SchedulerRunSummary
	err      error
	batchErr error
}

func (f *fakeSchedulerRunReader) GetSchedulerRun(_ context.Context, schedulerID, runID string) (domain.SchedulerRunSummary, error) {
	if f.err != nil {
		return domain.SchedulerRunSummary{}, f.err
	}
	key := schedulerID + "/" + runID
	run, ok := f.runs[key]
	if !ok {
		return domain.SchedulerRunSummary{}, domain.ResourceError(domain.ErrNotFound, "scheduler run", key, "scheduler run "+key+" not found", nil)
	}
	return run, nil
}

func (f *fakeSchedulerRunReader) BatchGetLatestSchedulerRunsBySandboxIDs(_ context.Context, _, _ []string) (map[string]domain.SchedulerRunSummary, error) {
	if f.batchErr != nil {
		return nil, f.batchErr
	}
	return f.batch, nil
}

type fakeSchedulerSandboxStopper struct {
	stopped []string
	err     error
	failOn  map[string]error
}

func (f *fakeSchedulerSandboxStopper) StopSandbox(_ context.Context, sandboxID string) (*domain.Sandbox, error) {
	if err, ok := f.failOn[sandboxID]; ok {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	f.stopped = append(f.stopped, sandboxID)
	return &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID}}, nil
}

func schedulerSandboxFixture(id, origin, schedulerID, runID, policy string, createdAt time.Time) *domain.Sandbox {
	tags := []domain.SandboxTag{{Name: "origin", Value: origin}}
	if schedulerID != "" {
		tags = append(tags, domain.SandboxTag{Name: "scheduler_id", Value: schedulerID})
	}
	if runID != "" {
		tags = append(tags, domain.SandboxTag{Name: "scheduler_run_id", Value: runID})
	}
	return &domain.Sandbox{
		Summary:              domain.SandboxSummary{ID: id, CreatedAt: createdAt, Tags: tags},
		StoppedRuntimePolicy: policy,
	}
}

func TestReconcileOrphanedSchedulerSandboxes(t *testing.T) {
	startedAt := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	before := startedAt.Add(-time.Hour)
	after := startedAt.Add(time.Minute)
	runningRun := domain.SchedulerRunSummary{ID: "run-running", SchedulerID: "sched", Status: domain.SchedulerRunStatusRunning}
	failedRun := domain.SchedulerRunSummary{ID: "run-failed", SchedulerID: "sched", Status: domain.SchedulerRunStatusFailed}

	tests := []struct {
		name        string
		sandboxes   []*domain.Sandbox
		runs        map[string]domain.SchedulerRunSummary
		batch       map[string]domain.SchedulerRunSummary
		failOn      map[string]error
		wantStopped []string
		wantWarning string
	}{
		{
			name:      "running owner is kept",
			sandboxes: []*domain.Sandbox{schedulerSandboxFixture("sb-running", "scheduler", "sched", "run-running", "remove", before)},
			runs:      map[string]domain.SchedulerRunSummary{"sched/run-running": runningRun},
		},
		{
			name:        "terminal owner is stopped",
			sandboxes:   []*domain.Sandbox{schedulerSandboxFixture("sb-failed", "scheduler", "sched", "run-failed", "remove", before)},
			runs:        map[string]domain.SchedulerRunSummary{"sched/run-failed": failedRun},
			wantStopped: []string{"sb-failed"},
		},
		{
			name:        "missing owner is stopped",
			sandboxes:   []*domain.Sandbox{schedulerSandboxFixture("sb-missing", "scheduler", "sched", "run-gone", "remove", before)},
			runs:        map[string]domain.SchedulerRunSummary{},
			wantStopped: []string{"sb-missing"},
		},
		{
			name:      "sticky sandbox is kept",
			sandboxes: []*domain.Sandbox{schedulerSandboxFixture("sb-sticky", "scheduler", "sched", "run-failed", "retain", before)},
			runs:      map[string]domain.SchedulerRunSummary{"sched/run-failed": failedRun},
		},
		{
			name:      "non-scheduler origin is ignored",
			sandboxes: []*domain.Sandbox{schedulerSandboxFixture("sb-project", "project", "sched", "run-failed", "remove", before)},
			runs:      map[string]domain.SchedulerRunSummary{"sched/run-failed": failedRun},
		},
		{
			name:      "sandbox created after startup is ignored",
			sandboxes: []*domain.Sandbox{schedulerSandboxFixture("sb-fresh", "scheduler", "sched", "run-failed", "remove", after)},
			runs:      map[string]domain.SchedulerRunSummary{"sched/run-failed": failedRun},
		},
		{
			name:        "legacy sandbox resolves linked terminal run",
			sandboxes:   []*domain.Sandbox{schedulerSandboxFixture("sb-legacy", "scheduler", "sched", "", "remove", before)},
			batch:       map[string]domain.SchedulerRunSummary{"sb-legacy": failedRun},
			wantStopped: []string{"sb-legacy"},
		},
		{
			name:        "legacy sandbox without link is left for review",
			sandboxes:   []*domain.Sandbox{schedulerSandboxFixture("sb-orphan-unknown", "scheduler", "sched", "", "remove", before)},
			batch:       map[string]domain.SchedulerRunSummary{},
			wantWarning: "manual review",
		},
		{
			name: "stop failure does not block the next sandbox",
			sandboxes: []*domain.Sandbox{
				schedulerSandboxFixture("sb-stop-fails", "scheduler", "sched", "run-failed", "remove", before),
				schedulerSandboxFixture("sb-stop-works", "scheduler", "sched", "run-failed-2", "remove", before),
			},
			runs: map[string]domain.SchedulerRunSummary{
				"sched/run-failed":   failedRun,
				"sched/run-failed-2": {ID: "run-failed-2", SchedulerID: "sched", Status: domain.SchedulerRunStatusSucceeded},
			},
			failOn:      map[string]error{"sb-stop-fails": errors.New("boom")},
			wantStopped: []string{"sb-stop-works"},
			wantWarning: "sb-stop-fails",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			lister := &fakeSchedulerSandboxLister{listed: domain.SandboxListResult{Sandboxes: tc.sandboxes}}
			reader := &fakeSchedulerRunReader{runs: tc.runs, batch: tc.batch}
			stopper := &fakeSchedulerSandboxStopper{failOn: tc.failOn}

			warnings := reconcileOrphanedSchedulerSandboxes(context.Background(), lister, reader, stopper, startedAt)

			if strings.Join(stopper.stopped, ",") != strings.Join(tc.wantStopped, ",") {
				t.Fatalf("stopped = %v, want %v (warnings: %v)", stopper.stopped, tc.wantStopped, warnings)
			}
			if tc.wantWarning != "" && !containsWarning(warnings, tc.wantWarning) {
				t.Fatalf("warnings = %v, want one containing %q", warnings, tc.wantWarning)
			}
			if !containsString(lister.options.VMStatuses, domain.VMStatusRunning) {
				t.Fatalf("list options = %+v, want running sandboxes only", lister.options)
			}
			if !lister.options.CreatedTo.Equal(startedAt) {
				t.Fatalf("list options CreatedTo = %v, want %v", lister.options.CreatedTo, startedAt)
			}
		})
	}
}

func TestReconcileOrphanedSchedulerSandboxesListError(t *testing.T) {
	lister := &fakeSchedulerSandboxLister{err: errors.New("db down")}
	reader := &fakeSchedulerRunReader{}
	stopper := &fakeSchedulerSandboxStopper{}
	warnings := reconcileOrphanedSchedulerSandboxes(context.Background(), lister, reader, stopper, time.Now().UTC())
	if len(warnings) != 1 || !strings.Contains(warnings[0], "db down") {
		t.Fatalf("warnings = %v, want the list error", warnings)
	}
}

func containsWarning(warnings []string, part string) bool {
	for _, warning := range warnings {
		if strings.Contains(warning, part) {
			return true
		}
	}
	return false
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
