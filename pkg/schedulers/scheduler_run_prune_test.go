package schedulers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestNormalizeSchedulerRunPruneFilter(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.FixedZone("test", 8*60*60))
	filter, err := normalizeSchedulerRunPruneFilter(SchedulerRunPruneRequest{
		SchedulerIDs: []string{" scheduler-b ", "scheduler-a", "scheduler-b"},
		Statuses:     []string{" FAILED ", "succeeded", "failed"},
		TriggerID:    " trigger-a ",
		OlderThan:    24 * time.Hour,
	}, now)
	if err != nil {
		t.Fatalf("normalize filter: %v", err)
	}
	if strings.Join(filter.SchedulerIDs, ",") != "scheduler-a,scheduler-b" || strings.Join(filter.Statuses, ",") != "failed,succeeded" {
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
		{Statuses: []string{domain.SchedulerRunStatusRunning}},
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
		runs: []domain.SchedulerRunSummary{
			{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"},
			{ID: "run-b", SchedulerID: "scheduler-b", TriggerID: "trigger-b", ArtifactsDir: "/recorded/b"},
		},
		counted: SchedulerRunPruneDatabaseStats{Runs: 2, SchedulerEvents: 5, EventDeliveries: 1, EventSandboxLinks: 2},
	}
	artifacts := &schedulerRunArtifactPrunerFake{inspected: map[string]SchedulerRunArtifactInfo{
		"scheduler-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 7},
		"scheduler-b/run-b": {Path: "/recorded/b", Exists: true, Bytes: 11},
	}}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{SchedulerIDs: []string{"scheduler-b", "scheduler-a"}})
	if err != nil {
		t.Fatalf("prune dry-run: %v", err)
	}
	if !result.DryRun || store.deleteCalls != 0 || len(artifacts.removed) != 0 {
		t.Fatalf("dry-run result=%#v delete_calls=%d removed=%#v", result, store.deleteCalls, artifacts.removed)
	}
	want := SchedulerRunPruneStats{Runs: 2, SchedulerEvents: 5, EventDeliveries: 1, EventSandboxLinks: 2, ArtifactDirs: 2, ArtifactBytes: 18}
	if result.Matched != want {
		t.Fatalf("matched=%#v, want %#v", result.Matched, want)
	}
	if strings.Join(store.filter.Statuses, ",") != "canceled,failed,skipped,succeeded" {
		t.Fatalf("default statuses = %#v", store.filter.Statuses)
	}
}

func TestControllerPruneSchedulerRunsForceRemovesDatabaseAndArtifacts(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs: []domain.SchedulerRunSummary{
			{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"},
			{ID: "run-b", SchedulerID: "scheduler-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/b"},
		},
		counted: SchedulerRunPruneDatabaseStats{Runs: 2, SchedulerEvents: 4},
		deleted: SchedulerRunPruneDatabaseStats{Runs: 2, SchedulerEvents: 4},
	}
	artifacts := &schedulerRunArtifactPrunerFake{
		inspected: map[string]SchedulerRunArtifactInfo{
			"scheduler-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 13},
			"scheduler-a/run-b": {Path: "/recorded/b"},
		},
		removeResults: map[string]SchedulerRunArtifactInfo{
			"scheduler-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 13},
		},
	}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{SchedulerIDs: []string{"scheduler-a"}, Force: true})
	if err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if result.DryRun || store.deleteCalls != 1 || len(store.deletedKeys) != 2 {
		t.Fatalf("force result=%#v store=%#v", result, store)
	}
	wantRemoved := SchedulerRunPruneStats{Runs: 2, SchedulerEvents: 4, ArtifactDirs: 1, ArtifactBytes: 13}
	if result.Removed != wantRemoved || len(artifacts.removed) != 1 {
		t.Fatalf("removed=%#v artifacts=%#v, want %#v", result.Removed, artifacts.removed, wantRemoved)
	}
}

func TestControllerPruneSchedulerRunsSkipsBusyAndUnsafeRuns(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs: []domain.SchedulerRunSummary{
			{ID: "run-busy", SchedulerID: "scheduler-busy", TriggerID: "trigger-a"},
			{ID: "run-unsafe", SchedulerID: "scheduler-free", TriggerID: "trigger-a"},
		},
		counted: SchedulerRunPruneDatabaseStats{Runs: 2, SchedulerEvents: 2},
	}
	artifacts := &schedulerRunArtifactPrunerFake{inspectErrors: map[string]error{
		"scheduler-free/run-unsafe": errors.New("recorded path mismatch"),
	}}
	controller := newSchedulerRunPruneController(store, artifacts, map[string]int{"scheduler-busy": 1})
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{SchedulerIDs: []string{"scheduler-busy", "scheduler-free"}, Force: true})
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
		runs:    []domain.SchedulerRunSummary{{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"}},
		counted: SchedulerRunPruneDatabaseStats{Runs: 1},
		deleted: SchedulerRunPruneDatabaseStats{Runs: 1},
	}
	artifacts := &schedulerRunArtifactPrunerFake{
		inspected:    map[string]SchedulerRunArtifactInfo{"scheduler-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 9}},
		removeErrors: map[string]error{"scheduler-a/run-a": errors.New("permission denied")},
	}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{SchedulerIDs: []string{"scheduler-a"}, Force: true})
	if err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if result.Removed.Runs != 1 || result.Removed.ArtifactDirs != 0 || len(result.Residues) != 1 {
		t.Fatalf("result=%#v", result)
	}
	if result.Residues[0].SchedulerID != "scheduler-a" || result.Residues[0].RunID != "run-a" || !strings.Contains(result.Residues[0].Error, "permission denied") {
		t.Fatalf("residue=%#v", result.Residues[0])
	}
}

func TestControllerPruneSchedulerRunsKeepsArtifactsForForceRecheckSkip(t *testing.T) {
	store := &schedulerRunPruneStoreFake{
		runs:        []domain.SchedulerRunSummary{{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", ArtifactsDir: "/recorded/a"}},
		counted:     SchedulerRunPruneDatabaseStats{Runs: 1},
		removedKeys: []SchedulerRunKey{},
	}
	artifacts := &schedulerRunArtifactPrunerFake{
		inspected: map[string]SchedulerRunArtifactInfo{"scheduler-a/run-a": {Path: "/recorded/a", Exists: true, Bytes: 9}},
	}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	result, err := controller.PruneSchedulerRuns(context.Background(), SchedulerRunPruneRequest{SchedulerIDs: []string{"scheduler-a"}, Force: true})
	if err != nil {
		t.Fatalf("force prune: %v", err)
	}
	if result.Removed.Runs != 0 || result.SkippedRuns != 1 || len(artifacts.removed) != 0 || len(result.Warnings) != 1 {
		t.Fatalf("result=%#v artifacts=%#v", result, artifacts.removed)
	}
}

func TestControllerRecoverInterruptedRunsMarksFailedAndRecordsEvent(t *testing.T) {
	startedAt := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store := &schedulerRunPruneStoreFake{interrupted: []domain.SchedulerRunSummary{
		{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt},
		{ID: "run-b", SchedulerID: "scheduler-b", TriggerID: "trigger-b", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt.Add(10 * time.Minute)},
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
		if run.Status != domain.SchedulerRunStatusFailed || run.CompletedAt.IsZero() || run.DurationMs <= 0 || run.Error != interruptedSchedulerRunError {
			t.Fatalf("updated run=%#v", run)
		}
		event := store.events[index]
		if event.Type != "scheduler.run.failed" || event.Level != "error" || event.RunID != run.ID || event.TriggerID != run.TriggerID || !strings.Contains(event.PayloadJSON, "daemon_interrupted") {
			t.Fatalf("recovery event=%#v", event)
		}
	}
}

func TestControllerRecoverInterruptedRunsStopsScheduledSandboxes(t *testing.T) {
	startedAt := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	run := domain.SchedulerRunSummary{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt}
	scheduled := func(sandboxID string) domain.SchedulerEvent {
		return domain.SchedulerEvent{SchedulerID: run.SchedulerID, RunID: run.ID, Type: SandboxStopPendingEventType, LinkedSandboxID: sandboxID}
	}
	store := &schedulerRunPruneStoreFake{
		interrupted: []domain.SchedulerRunSummary{run},
		runEvents: map[string][]domain.SchedulerEvent{run.ID: {
			scheduled("sandbox-stop"),
			// A sandbox the run only used, such as a sticky command sandbox,
			// stays running across runs and must survive recovery.
			{SchedulerID: run.SchedulerID, RunID: run.ID, Type: "scheduler.command.started", LinkedSandboxID: "sandbox-keep"},
			scheduled("sandbox-stop"),
			scheduled("sandbox-gone"),
			scheduled("sandbox-stuck"),
		}},
	}
	stopper := &interruptedSandboxStopperFake{errs: map[string]error{
		"sandbox-gone":  fmt.Errorf("load sandbox: %w", os.ErrNotExist),
		"sandbox-stuck": errors.New("runtime unreachable"),
	}}
	controller := newSchedulerRunPruneController(store, nil, nil)
	controller.deps.InterruptedSandboxes = stopper
	controller.deps.NewID = func() string { return "recovery-event" }

	err := controller.RecoverInterruptedRuns(context.Background(), startedAt.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "sandbox-stuck") || strings.Contains(err.Error(), "sandbox-gone") {
		t.Fatalf("recover error = %v, want only the sandbox-stuck stop failure", err)
	}
	if want := []string{"sandbox-stop", "sandbox-gone", "sandbox-stuck"}; !slices.Equal(stopper.stopped, want) {
		t.Fatalf("stopped sandboxes = %#v, want %#v", stopper.stopped, want)
	}
	var recorded []string
	for _, event := range store.events {
		if event.LinkedSandboxID != "" {
			recorded = append(recorded, event.Type+":"+event.LinkedSandboxID)
			if event.RunID != run.ID || event.TriggerID != run.TriggerID {
				t.Fatalf("sandbox stop event is not linked to the interrupted run: %#v", event)
			}
		}
	}
	if want := []string{"scheduler.sandbox.stopped:sandbox-stop", "scheduler.sandbox.stop_failed:sandbox-stuck"}; !slices.Equal(recorded, want) {
		t.Fatalf("sandbox stop events = %#v, want %#v", recorded, want)
	}
}

func TestControllerRecoverInterruptedRunsStopsSandboxesWhenRunUpdateFails(t *testing.T) {
	startedAt := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	run := domain.SchedulerRunSummary{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt}
	store := &schedulerRunPruneStoreFake{
		interrupted: []domain.SchedulerRunSummary{run},
		updateErr:   errors.New("database is locked"),
		runEvents: map[string][]domain.SchedulerEvent{run.ID: {
			{SchedulerID: run.SchedulerID, RunID: run.ID, Type: SandboxStopPendingEventType, LinkedSandboxID: "sandbox-stop"},
		}},
	}
	stopper := &interruptedSandboxStopperFake{}
	controller := newSchedulerRunPruneController(store, nil, nil)
	controller.deps.InterruptedSandboxes = stopper

	err := controller.RecoverInterruptedRuns(context.Background(), startedAt.Add(time.Hour))
	if err == nil || !strings.Contains(err.Error(), "database is locked") {
		t.Fatalf("recover error = %v, want the run update failure", err)
	}
	if want := []string{"sandbox-stop"}; !slices.Equal(stopper.stopped, want) {
		t.Fatalf("stopped sandboxes = %#v, want %#v despite the failed run update", stopper.stopped, want)
	}
}

type interruptedSandboxStopperFake struct {
	stopped []string
	errs    map[string]error
}

func (s *interruptedSandboxStopperFake) Shutdown(_ context.Context, sandboxID string) error {
	s.stopped = append(s.stopped, sandboxID)
	return s.errs[sandboxID]
}

type schedulerRunPruneStoreFake struct {
	ControllerStore
	runs        []domain.SchedulerRunSummary
	filter      SchedulerRunPruneFilter
	counted     SchedulerRunPruneDatabaseStats
	deleted     SchedulerRunPruneDatabaseStats
	countErr    error
	deleteErr   error
	deleteCalls int
	deletedKeys []SchedulerRunKey
	removedKeys []SchedulerRunKey
	interrupted []domain.SchedulerRunSummary
	updatedRuns []domain.SchedulerRunSummary
	events      []domain.SchedulerEvent
	runEvents   map[string][]domain.SchedulerEvent
	updateErr   error
}

func (s *schedulerRunPruneStoreFake) ListSchedulerEventsPage(_ context.Context, filter SchedulerEventPageFilter) ([]domain.SchedulerEvent, error) {
	events := s.runEvents[filter.RunID]
	if filter.Offset >= len(events) {
		return nil, nil
	}
	events = events[filter.Offset:]
	if filter.Limit > 0 && len(events) > filter.Limit {
		events = events[:filter.Limit]
	}
	return append([]domain.SchedulerEvent(nil), events...), nil
}

func (s *schedulerRunPruneStoreFake) ListInterruptedSchedulerRuns(context.Context, time.Time) ([]domain.SchedulerRunSummary, error) {
	return append([]domain.SchedulerRunSummary(nil), s.interrupted...), nil
}

func (s *schedulerRunPruneStoreFake) UpdateSchedulerRun(_ context.Context, run domain.SchedulerRunSummary) error {
	if s.updateErr != nil {
		return s.updateErr
	}
	s.updatedRuns = append(s.updatedRuns, run)
	return nil
}

func (s *schedulerRunPruneStoreFake) AddSchedulerEvent(_ context.Context, event domain.SchedulerEvent) error {
	s.events = append(s.events, event)
	return nil
}

func (s *schedulerRunPruneStoreFake) ListSchedulerRunsForPrune(_ context.Context, filter SchedulerRunPruneFilter) ([]domain.SchedulerRunSummary, error) {
	s.filter = filter
	return append([]domain.SchedulerRunSummary(nil), s.runs...), nil
}

func (s *schedulerRunPruneStoreFake) CountSchedulerRunPruneData(context.Context, []SchedulerRunKey) (SchedulerRunPruneDatabaseStats, error) {
	return s.counted, s.countErr
}

func (s *schedulerRunPruneStoreFake) DeleteSchedulerRunPruneData(_ context.Context, keys []SchedulerRunKey) (SchedulerRunPruneDatabaseResult, error) {
	s.deleteCalls++
	s.deletedKeys = append([]SchedulerRunKey(nil), keys...)
	removedKeys := s.removedKeys
	if removedKeys == nil {
		removedKeys = keys
	}
	return SchedulerRunPruneDatabaseResult{Stats: s.deleted, RemovedKeys: append([]SchedulerRunKey(nil), removedKeys...)}, s.deleteErr
}

type schedulerRunArtifactPrunerFake struct {
	ControllerArtifacts
	inspected     map[string]SchedulerRunArtifactInfo
	inspectErrors map[string]error
	removeResults map[string]SchedulerRunArtifactInfo
	removeErrors  map[string]error
	removed       []string
}

func (p *schedulerRunArtifactPrunerFake) InspectRunArtifacts(schedulerID, runID, _ string) (SchedulerRunArtifactInfo, error) {
	key := schedulerID + "/" + runID
	return p.inspected[key], p.inspectErrors[key]
}

func (p *schedulerRunArtifactPrunerFake) RemoveRunArtifacts(schedulerID, runID, _ string) (SchedulerRunArtifactInfo, error) {
	key := schedulerID + "/" + runID
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
