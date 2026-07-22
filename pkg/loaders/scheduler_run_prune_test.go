package loaders

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domain "agent-compose/pkg/model"
)

func TestNormalizeSchedulerRunPruneFilter(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.FixedZone("test", 8*60*60))
	filter, err := normalizeSchedulerRunPruneFilter(SchedulerRunPruneRequest{
		LoaderIDs: []string{" loader-b ", "loader-a", "loader-b"},
		Statuses:  []string{" FAILED ", "succeeded", "failed"},
		TriggerID: " trigger-a ",
		OlderThan: 24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("normalize filter: %v", err)
	}
	if strings.Join(filter.LoaderIDs, ",") != "loader-a,loader-b" || strings.Join(filter.Statuses, ",") != "failed,succeeded" {
		t.Fatalf("normalized filter = %#v", filter)
	}
	if filter.TriggerID != "trigger-a" || filter.OlderThan != 24*time.Hour || !filter.Now.Equal(now.UTC()) {
		t.Fatalf("normalized filter = %#v", filter)
	}

	defaults, err := normalizeSchedulerRunPruneFilter(SchedulerRunPruneRequest{}, now)
	if err != nil {
		t.Fatalf("normalize default filter: %v", err)
	}
	if strings.Join(defaults.Statuses, ",") != "canceled,failed,skipped,succeeded" {
		t.Fatalf("default statuses = %#v", defaults.Statuses)
	}
	for _, request := range []SchedulerRunPruneRequest{
		{Statuses: []string{domain.LoaderRunStatusRunning}},
		{Statuses: []string{"pending"}},
		{OlderThan: -time.Second},
	} {
		if _, err := normalizeSchedulerRunPruneFilter(request, now); !errors.Is(err, domain.ErrInvalidArgument) {
			t.Fatalf("normalize %#v error = %v, want invalid argument", request, err)
		}
	}
}

func TestControllerPruneSchedulerRunsDryRunCountsWithoutDeleting(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs: []domain.LoaderRunSummary{
			{ID: "run-a", LoaderID: "loader-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"},
			{ID: "run-b", LoaderID: "loader-b", TriggerID: "trigger-b", ArtifactsDir: "/recorded/b"},
		},
		counted: SchedulerRunPruneDatabaseStats{Runs: 2, LoaderEvents: 5, EventDeliveries: 1, EventSandboxLinks: 2},
	}
	artifacts := &schedulerRunArtifactPrunerFake{inspected: map[string]SchedulerRunArtifactInfo{
		"loader-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 7},
		"loader-b/run-b": {Path: "/recorded/b", Exists: true, Bytes: 11},
	}}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{LoaderIDs: []string{"loader-b", "loader-a"}})
	if err != nil {
		t.Fatalf("prune dry-run: %v", err)
	}
	if !result.DryRun || store.deleteCalls != 0 || len(artifacts.removed) != 0 {
		t.Fatalf("dry-run result=%#v delete_calls=%d removed=%#v", result, store.deleteCalls, artifacts.removed)
	}
	want := SchedulerRunPruneStats{Runs: 2, LoaderEvents: 5, EventDeliveries: 1, EventSandboxLinks: 2, ArtifactDirs: 2, ArtifactBytes: 18}
	if result.Matched != want {
		t.Fatalf("matched=%#v, want %#v", result.Matched, want)
	}
	if strings.Join(store.filter.Statuses, ",") != "canceled,failed,skipped,succeeded" {
		t.Fatalf("default statuses = %#v", store.filter.Statuses)
	}
}

func TestControllerPruneSchedulerRunsForceRemovesDatabaseAndArtifacts(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs: []domain.LoaderRunSummary{
			{ID: "run-a", LoaderID: "loader-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"},
			{ID: "run-b", LoaderID: "loader-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/b"},
		},
		counted: SchedulerRunPruneDatabaseStats{Runs: 2, LoaderEvents: 4},
		deleted: SchedulerRunPruneDatabaseStats{Runs: 2, LoaderEvents: 4},
	}
	artifacts := &schedulerRunArtifactPrunerFake{
		inspected: map[string]SchedulerRunArtifactInfo{
			"loader-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 13},
			"loader-a/run-b": {Path: "/recorded/b"},
		},
		removeResults: map[string]SchedulerRunArtifactInfo{
			"loader-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 13},
		},
	}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{LoaderIDs: []string{"loader-a"}, Force: true})
	if err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if result.DryRun || store.deleteCalls != 1 || len(store.deletedKeys) != 2 {
		t.Fatalf("force result=%#v store=%#v", result, store)
	}
	wantRemoved := SchedulerRunPruneStats{Runs: 2, LoaderEvents: 4, ArtifactDirs: 1, ArtifactBytes: 13}
	if result.Removed != wantRemoved || len(artifacts.removed) != 1 {
		t.Fatalf("removed=%#v artifacts=%#v, want %#v", result.Removed, artifacts.removed, wantRemoved)
	}
}

func TestControllerPruneSchedulerRunsSkipsBusyAndUnsafeRuns(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs: []domain.LoaderRunSummary{
			{ID: "run-busy", LoaderID: "loader-busy", TriggerID: "trigger-a"},
			{ID: "run-unsafe", LoaderID: "loader-free", TriggerID: "trigger-a"},
		},
		counted: SchedulerRunPruneDatabaseStats{Runs: 2, LoaderEvents: 2},
	}
	artifacts := &schedulerRunArtifactPrunerFake{inspectErrors: map[string]error{
		"loader-free/run-unsafe": errors.New("recorded path mismatch"),
	}}
	controller := newSchedulerRunPruneController(store, artifacts, map[string]int{"loader-busy": 1})
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{LoaderIDs: []string{"loader-busy", "loader-free"}, Force: true})
	if err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if result.SkippedRuns != 2 || store.deleteCalls != 0 || len(result.Warnings) != 2 {
		t.Fatalf("result=%#v delete_calls=%d", result, store.deleteCalls)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "skipped 1 matching run(s) from 1 busy scheduler(s)") {
		t.Fatalf("warnings=%#v", result.Warnings)
	}
}

func TestControllerPruneSchedulerRunsReportsArtifactResidue(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs:    []domain.LoaderRunSummary{{ID: "run-a", LoaderID: "loader-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"}},
		counted: SchedulerRunPruneDatabaseStats{Runs: 1},
		deleted: SchedulerRunPruneDatabaseStats{Runs: 1},
	}
	artifacts := &schedulerRunArtifactPrunerFake{
		inspected:    map[string]SchedulerRunArtifactInfo{"loader-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 9}},
		removeErrors: map[string]error{"loader-a/run-a": errors.New("permission denied")},
	}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{LoaderIDs: []string{"loader-a"}, Force: true})
	if err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if result.Removed.Runs != 1 || result.Removed.ArtifactDirs != 0 || len(result.Residues) != 1 {
		t.Fatalf("result=%#v", result)
	}
	if result.Residues[0].LoaderID != "loader-a" || result.Residues[0].RunID != "run-a" || !strings.Contains(result.Residues[0].Error, "permission denied") {
		t.Fatalf("residue=%#v", result.Residues[0])
	}
}

func TestControllerPruneSchedulerRunsKeepsArtifactsForForceRecheckSkip(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs:        []domain.LoaderRunSummary{{ID: "run-a", LoaderID: "loader-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"}},
		counted:     SchedulerRunPruneDatabaseStats{Runs: 1},
		removedKeys: []LoaderRunKey{},
	}
	artifacts := &schedulerRunArtifactPrunerFake{
		inspected: map[string]SchedulerRunArtifactInfo{"loader-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 9}},
	}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{LoaderIDs: []string{"loader-a"}, Force: true})
	if err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if result.Removed.Runs != 0 || result.SkippedRuns != 1 || len(artifacts.removed) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("result=%#v artifacts=%#v", result, artifacts.removed)
	}
}

func TestControllerRecoverInterruptedRunsMarksFailedAndRecordsEvent(t *testing.T) {
	startedAt := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store := &schedulerRunPruneStoreFake{interrupted: []domain.LoaderRunSummary{
		{ID: "run-a", LoaderID: "loader-a", TriggerID: "trigger-a", Status: domain.LoaderRunStatusRunning, StartedAt: startedAt},
		{ID: "run-b", LoaderID: "loader-b", TriggerID: "trigger-b", Status: domain.LoaderRunStatusRunning, StartedAt: startedAt.Add(10 * time.Minute)},
	}}
	controller := newSchedulerRunPruneController(store, nil, nil)
	controller.deps.NewID = func() string { return "recovery-event" }
	if err := controller.RecoverInterruptedRuns(context.Background(), startedAt.Add(time.Hour)); err != nil {
		t.Fatalf("recover interrupted runs: %v", err)
	}
	if len(store.updatedRuns) != 2 || len(store.events) != 2 {
		t.Fatalf("updated=%#v events=%#v", store.updatedRuns, store.events)
	}
	for index, run := range store.updatedRuns {
		if run.Status != domain.LoaderRunStatusFailed || run.CompletedAt.IsZero() || run.DurationMs <= 0 || run.Error != interruptedSchedulerRunError {
			t.Fatalf("updated run=%#v", run)
		}
		event := store.events[index]
		if event.Type != "loader.run.failed" || event.Level != "error" || event.RunID != run.ID || event.TriggerID != run.TriggerID || !strings.Contains(event.PayloadJSON, "daemon_interrupted") {
			t.Fatalf("recovery event=%#v", event)
		}
	}
}

type schedulerRunPruneStoreFake struct {
	ControllerStore
	runs        []domain.LoaderRunSummary
	filter      SchedulerRunPruneFilter
	counted     SchedulerRunPruneDatabaseStats
	deleted     SchedulerRunPruneDatabaseStats
	countErr    error
	deleteErr   error
	deleteCalls int
	deletedKeys []LoaderRunKey
	removedKeys []LoaderRunKey
	interrupted []domain.LoaderRunSummary
	updatedRuns []domain.LoaderRunSummary
	events      []domain.LoaderEvent
}

func (s *schedulerRunPruneStoreFake) ListInterruptedLoaderRuns(context.Context, time.Time) ([]domain.LoaderRunSummary, error) {
	return append([]domain.LoaderRunSummary(nil), s.interrupted...), nil
}

func (s *schedulerRunPruneStoreFake) UpdateLoaderRun(_ context.Context, run domain.LoaderRunSummary) error {
	s.updatedRuns = append(s.updatedRuns, run)
	return nil
}

func (s *schedulerRunPruneStoreFake) AddLoaderEvent(_ context.Context, event domain.LoaderEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *schedulerRunPruneStoreFake) ListLoaderRunsForPrune(_ context.Context, filter SchedulerRunPruneFilter) ([]domain.LoaderRunSummary, error) {
	s.filter = filter
	return append([]domain.LoaderRunSummary(nil), s.runs...), nil
}

func (s *schedulerRunPruneStoreFake) CountLoaderRunPruneData(context.Context, []LoaderRunKey) (SchedulerRunPruneDatabaseStats, error) {
	return s.counted, s.countErr
}

func (s *schedulerRunPruneStoreFake) DeleteLoaderRunPruneData(_ context.Context, keys []LoaderRunKey) (SchedulerRunPruneDatabaseResult, error) {
	s.deleteCalls++
	s.deletedKeys = append([]LoaderRunKey(nil), keys...)
	removedKeys := s.removedKeys
	if removedKeys == nil {
		removedKeys = keys
	}
	return SchedulerRunPruneDatabaseResult{Stats: s.deleted, RemovedKeys: append([]LoaderRunKey(nil), removedKeys...)}, s.deleteErr
}

type schedulerRunArtifactPrunerFake struct {
	ControllerArtifacts
	inspected     map[string]SchedulerRunArtifactInfo
	inspectErrors map[string]error
	removeResults map[string]SchedulerRunArtifactInfo
	removeErrors  map[string]error
	removed       []string
}

func (p *schedulerRunArtifactPrunerFake) InspectRunArtifacts(loaderID, runID, _ string) (SchedulerRunArtifactInfo, error) {
	key := loaderID + "/" + runID
	return p.inspected[key], p.inspectErrors[key]
}

func (p *schedulerRunArtifactPrunerFake) RemoveRunArtifacts(loaderID, runID, _ string) (SchedulerRunArtifactInfo, error) {
	key := loaderID + "/" + runID
	p.removed = append(p.removed, key)
	return p.removeResults[key], p.removeErrors[key]
}

func newSchedulerRunPruneController(store ControllerStore, artifacts ControllerArtifacts, running map[string]int) *Controller {
	if running == nil {
		running = map[string]int{}
	}
	return &Controller{
		deps: ControllerDependencies{
			Store: store, Artifacts: artifacts,
			Now: func() time.Time { return time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC) },
		},
		running: running,
	}
}
