package schedulers

import (
	"context"
	"strings"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runlog"
)

type stepRecoveryStoreFake struct {
	schedulerRunPruneStoreFake
	runEvents []domain.SchedulerEvent
}

func (s *stepRecoveryStoreFake) ListSchedulerEventsPage(_ context.Context, filter SchedulerEventPageFilter) ([]domain.SchedulerEvent, error) {
	var out []domain.SchedulerEvent
	for _, event := range s.runEvents {
		if event.RunID == filter.RunID {
			out = append(out, event)
		}
	}
	if filter.Offset >= len(out) {
		return nil, nil
	}
	return out[filter.Offset:], nil
}

func TestRecoverInterruptedRunsFailsTheStepsTheyLeftStarted(t *testing.T) {
	startedAt := time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)
	store := &stepRecoveryStoreFake{
		schedulerRunPruneStoreFake: schedulerRunPruneStoreFake{interrupted: []domain.SchedulerRunSummary{
			{ID: "run-a", SchedulerID: "scheduler-a", TriggerID: "trigger-a", Status: domain.SchedulerRunStatusRunning, StartedAt: startedAt},
		}},
		runEvents: []domain.SchedulerEvent{
			{RunID: "run-a", Type: "scheduler.command.started", LinkedSandboxID: "sandbox-1", LinkedCellID: "finished"},
			{RunID: "run-a", Type: "scheduler.command.completed", LinkedSandboxID: "sandbox-1", LinkedCellID: "finished"},
			{RunID: "run-a", Type: "scheduler.command.started", LinkedSandboxID: "sandbox-1", LinkedCellID: "interrupted"},
		},
	}
	artifacts := FSArtifacts{DataRoot: t.TempDir()}
	controller := newSchedulerRunPruneController(store, artifacts, nil)
	controller.deps.NewID = func() string { return "recovery-event" }
	if err := controller.RecoverInterruptedRuns(context.Background(), startedAt.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 2 {
		t.Fatalf("events = %#v", store.events)
	}
	step := store.events[0]
	if step.Type != "scheduler.command.failed" || step.LinkedCellID != "interrupted" || step.LinkedSandboxID != "sandbox-1" || step.RunID != "run-a" {
		t.Fatalf("step event = %#v", step)
	}
	if store.events[1].Type != "scheduler.run.failed" {
		t.Fatalf("run event = %#v", store.events[1])
	}
	entries, err := runlog.ReadEntries(artifacts.StepLogPath("scheduler-a", "run-a", "interrupted"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Type != runlog.EntryError || !strings.Contains(entries[0].Text, "interrupted") {
		t.Fatalf("step log = %#v", entries)
	}
}
