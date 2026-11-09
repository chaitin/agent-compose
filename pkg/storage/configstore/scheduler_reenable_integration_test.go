package configstore

import (
	"context"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func TestIntegrationReconcileSchedulerResumesWithoutCatchUp(t *testing.T) {
	for _, kind := range []string{domain.SchedulerTriggerKindInterval, domain.SchedulerTriggerKindCron} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name             string
				initiallyEnabled bool
				enabled          bool
				changeConfig     bool
				futureSchedule   bool
				wantReset        bool
				wantDue          int
			}{
				{name: "resume overdue schedule", enabled: true, wantReset: true},
				{name: "resume future schedule", enabled: true, futureSchedule: true, wantReset: true},
				{name: "enabled config update preserves overdue schedule", initiallyEnabled: true, enabled: true, changeConfig: true, wantDue: 1},
				{name: "enabled config update preserves future schedule", initiallyEnabled: true, enabled: true, changeConfig: true, futureSchedule: true},
				{name: "unchanged enabled scheduler preserves catch up", initiallyEnabled: true, enabled: true, wantDue: 1},
				{name: "disabled scheduler stays idle"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					ctx := context.Background()
					store, record, definition := newSchedulerReenableFixture(t, kind, tc.initiallyEnabled)
					nextFireAt := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Millisecond)
					if tc.futureSchedule {
						nextFireAt = nextFireAt.Add(4 * time.Hour)
					}
					lastFiredAt := nextFireAt.Add(-24 * time.Hour)
					if err := store.MarkSchedulerTriggerFired(ctx, record.ID, "tick", lastFiredAt, nextFireAt); err != nil {
						t.Fatal(err)
					}
					record.Enabled = tc.enabled
					definition.Summary.Enabled = tc.enabled
					if tc.changeConfig {
						record.SpecJSON = `{"description":"updated"}`
					}
					beforeEnable := time.Now().UTC()
					_, _, err := projects.ReconcileSchedulers(ctx, store, projects.ReconcileSchedulersRequest{
						Project:     domain.ProjectRecord{ID: record.ProjectID},
						Schedulers:  []domain.ProjectSchedulerRecord{record},
						Definitions: []domain.Scheduler{definition},
					}, projects.ReconcileSchedulerOptions{})
					if err != nil {
						t.Fatal(err)
					}
					loaded, err := store.GetScheduler(ctx, record.ID)
					if err != nil {
						t.Fatal(err)
					}
					if loaded.Summary.Enabled != tc.enabled {
						t.Fatalf("enabled = %t, want %t", loaded.Summary.Enabled, tc.enabled)
					}
					trigger := loaded.Triggers[0]
					if tc.wantReset {
						if !trigger.NextFireAt.After(beforeEnable) || trigger.NextFireAt.Equal(nextFireAt) {
							t.Fatalf("next fire = %v, want a new schedule after %v", trigger.NextFireAt, beforeEnable)
						}
					} else if !trigger.NextFireAt.Equal(nextFireAt) {
						t.Fatalf("next fire = %v, want preserved %v", trigger.NextFireAt, nextFireAt)
					}
					if !trigger.LastFiredAt.Equal(lastFiredAt) {
						t.Fatalf("last fired = %v, want %v", trigger.LastFiredAt, lastFiredAt)
					}
					if paused := loaded.Triggers[1]; paused.Enabled || !paused.NextFireAt.IsZero() {
						t.Fatalf("disabled trigger resumed: %#v", paused)
					}
					jobs, _, scheduleErrors := schedulers.CollectDueScheduledRuns(map[string]domain.Scheduler{record.ID: loaded}, time.Now().UTC())
					if len(jobs) != tc.wantDue || len(scheduleErrors) != 0 {
						t.Fatalf("due runs/errors = %d/%v, want %d/no errors", len(jobs), scheduleErrors, tc.wantDue)
					}
					if tc.wantReset {
						jobs, _, scheduleErrors = schedulers.CollectDueScheduledRuns(map[string]domain.Scheduler{record.ID: loaded}, trigger.NextFireAt)
						if len(jobs) != 1 || len(scheduleErrors) != 0 {
							t.Fatalf("runs/errors at next fire = %d/%v, want 1/no errors", len(jobs), scheduleErrors)
						}
					}
				})
			}
		})
	}
}

func newSchedulerReenableFixture(t *testing.T, kind string, enabled bool) (*ConfigStore, domain.ProjectSchedulerRecord, domain.Scheduler) {
	t.Helper()
	ctx := context.Background()
	store := FromDB(newMemoryDB(t))
	if err := store.initSchema(ctx); err != nil {
		t.Fatal(err)
	}
	trigger := domain.SchedulerTrigger{ID: "tick", Kind: kind, Enabled: true, IntervalMs: 3_600_000}
	if kind == domain.SchedulerTriggerKindCron {
		trigger.IntervalMs = 0
		trigger.SpecJSON = `{"expr":"0 0 * * *","timezone":"UTC"}`
	}
	paused := trigger
	paused.ID = "tick-disabled"
	paused.Enabled = false
	definition, err := upsertNativeTestScheduler(ctx, store, domain.Scheduler{
		Summary:  domain.SchedulerSummary{ID: "resume-scheduler", Enabled: enabled},
		Script:   "function main() {}",
		Triggers: []domain.SchedulerTrigger{trigger, paused},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.GetProjectScheduler(ctx, definition.Summary.ProjectID, definition.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	return store, record, definition
}
