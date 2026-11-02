package upgradeadvisor

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestStatsAggregatesRunsDurationsAndFailures(t *testing.T) {
	db := openFixtureDB(t)
	seedProject(t, db, fixtureProjectID, fixtureAgentID, fixtureSchedulerID, "agent")
	now := mustTime(t, "2026-11-03T12:00:00Z")
	insertTrigger(t, db, fixtureSchedulerID, "daily", domain.SchedulerTriggerKindCron, cronSpec(t, "0 9 * * *", "UTC"), 0, now.Add(21*time.Hour))
	insertSchedulerRun(t, db, fixtureSchedulerID, "run-a", "daily", domain.SchedulerTriggerKindCron, "cron", domain.SchedulerRunStatusSucceeded, now.Add(-5*time.Hour), 60*time.Second)
	insertSchedulerRun(t, db, fixtureSchedulerID, "run-b", "daily", domain.SchedulerTriggerKindCron, "cron", domain.SchedulerRunStatusSucceeded, now.Add(-3*time.Hour), 120*time.Second)
	insertSchedulerRun(t, db, fixtureSchedulerID, "run-c", "daily", domain.SchedulerTriggerKindCron, "cron", domain.SchedulerRunStatusFailed, now.Add(-time.Hour), 30*time.Second)
	insertSchedulerRun(t, db, fixtureSchedulerID, "run-d", "daily", domain.SchedulerTriggerKindCron, "cron", domain.SchedulerRunStatusRunning, now.Add(-10*time.Minute), 0)

	advisor := mustAdvisor(t, db, Options{Now: now, Days: 14, Location: time.UTC})
	report, err := advisor.Stats(context.Background())
	if err != nil {
		t.Fatalf("compute stats: %v", err)
	}
	if report.Totals.Runs != 4 || report.Totals.Failures != 1 || report.Totals.Running != 1 {
		t.Fatalf("totals = %+v, want runs=4 failures=1 running=1", report.Totals)
	}
	if len(report.Schedulers) != 1 {
		t.Fatalf("schedulers = %d, want 1", len(report.Schedulers))
	}
	activity := report.Schedulers[0]
	if activity.P50MS != 60_000 || activity.P90MS != 120_000 || activity.MaxMS != 120_000 {
		t.Fatalf("durations = p50 %d p90 %d max %d, want 60000/120000/120000", activity.P50MS, activity.P90MS, activity.MaxMS)
	}
	if got := activity.HourHistogram[11]; got != 2 {
		t.Fatalf("hour 11 histogram = %d, want 2 (failed run and running run)", got)
	}
	if len(activity.Triggers) != 1 || activity.Triggers[0].TriggerID != "daily" || activity.Triggers[0].Runs != 4 {
		t.Fatalf("trigger activity = %+v, want daily with 4 runs", activity.Triggers)
	}
}

func TestUpcomingForecastsCronAndIntervalFires(t *testing.T) {
	db := openFixtureDB(t)
	seedProject(t, db, fixtureProjectID, fixtureAgentID, fixtureSchedulerID, "agent")
	now := mustTime(t, "2026-11-02T00:00:00Z")
	insertTrigger(t, db, fixtureSchedulerID, "daily", domain.SchedulerTriggerKindCron, cronSpec(t, "0 9 * * *", "UTC"), 0, now.Add(9*time.Hour))
	insertTrigger(t, db, fixtureSchedulerID, "hourly", domain.SchedulerTriggerKindInterval, "", time.Hour.Milliseconds(), now.Add(time.Hour))

	advisor := mustAdvisor(t, db, Options{Now: now, Horizon: 12 * time.Hour, Limit: 100, Location: time.UTC})
	report, err := advisor.Upcoming(context.Background())
	if err != nil {
		t.Fatalf("forecast upcoming: %v", err)
	}
	if len(report.Fires) != 13 {
		t.Fatalf("fires = %d, want 12 interval + 1 cron = 13", len(report.Fires))
	}
	if !report.Fires[0].At.Equal(now.Add(time.Hour)) {
		t.Fatalf("first fire = %s, want %s", report.Fires[0].At, now.Add(time.Hour))
	}
	foundCron := false
	for _, fire := range report.Fires {
		if fire.TriggerID == "daily" && fire.At.Equal(now.Add(9*time.Hour)) {
			foundCron = true
		}
	}
	if !foundCron {
		t.Fatalf("cron fire at 09:00 missing from %+v", report.Fires)
	}
}

func TestWindowsAvoidHistoricallyBusyHour(t *testing.T) {
	db := openFixtureDB(t)
	seedProject(t, db, fixtureProjectID, fixtureAgentID, fixtureSchedulerID, "agent")
	now := mustTime(t, "2026-11-02T00:00:00Z")
	for daysAgo := 1; daysAgo <= 14; daysAgo++ {
		startedAt := now.Add(-time.Duration(daysAgo)*24*time.Hour + 9*time.Hour)
		insertSchedulerRun(t, db, fixtureSchedulerID, "busy-"+startedAt.Format("20060102"), "daily", domain.SchedulerTriggerKindCron, "cron", domain.SchedulerRunStatusSucceeded, startedAt, time.Hour)
	}
	insertTrigger(t, db, fixtureSchedulerID, "daily", domain.SchedulerTriggerKindCron, cronSpec(t, "0 9 * * *", "UTC"), 0, now.Add(9*time.Hour))

	advisor := mustAdvisor(t, db, Options{
		Now: now, Days: 14, Horizon: 24 * time.Hour, Bin: 5 * time.Minute,
		Window: 10 * time.Minute, Downtime: 10 * time.Minute, Top: 5, AssumePause: true, Location: time.UTC,
	})
	report, err := advisor.Windows(context.Background())
	if err != nil {
		t.Fatalf("recommend windows: %v", err)
	}
	if len(report.Candidates) == 0 {
		t.Fatal("no candidate windows returned")
	}
	best := report.Candidates[0]
	if best.Start.Hour() == 9 {
		t.Fatalf("best candidate starts in the historically busy 09:00 hour: %+v", best)
	}
	if best.ExpectedInFlight >= 0.5 {
		t.Fatalf("best candidate expected in-flight = %.2f, want < 0.5", best.ExpectedInFlight)
	}
}

func TestForecastEvaluatesProposedTime(t *testing.T) {
	db := openFixtureDB(t)
	seedProject(t, db, fixtureProjectID, fixtureAgentID, fixtureSchedulerID, "agent")
	now := mustTime(t, "2026-11-02T00:00:00Z")
	insertTrigger(t, db, fixtureSchedulerID, "hourly", domain.SchedulerTriggerKindInterval, "", time.Hour.Milliseconds(), now.Add(time.Hour))
	advisor := mustAdvisor(t, db, Options{
		Now: now, Bin: 5 * time.Minute, Window: 10 * time.Minute, Downtime: 10 * time.Minute,
		AssumePause: true, Location: time.UTC,
	})
	at := now.Add(2 * time.Hour)
	report, err := advisor.Forecast(context.Background(), at)
	if err != nil {
		t.Fatalf("forecast: %v", err)
	}
	if !report.Candidate.Start.Equal(at) || !report.Candidate.End.Equal(at.Add(10*time.Minute)) {
		t.Fatalf("candidate window = %s..%s, want %s..%s", report.Candidate.Start, report.Candidate.End, at, at.Add(10*time.Minute))
	}
	if report.Candidate.ScheduledFires != 1 {
		t.Fatalf("scheduled fires = %d, want 1 (the 02:00 fire)", report.Candidate.ScheduledFires)
	}
}

func TestStatusReportsRunningWorkAndAnomalies(t *testing.T) {
	db := openFixtureDB(t)
	seedProject(t, db, fixtureProjectID, fixtureAgentID, fixtureSchedulerID, "agent")
	now := mustTime(t, "2026-11-03T12:00:00Z")
	insertSchedulerRun(t, db, fixtureSchedulerID, "run-running", "daily", domain.SchedulerTriggerKindCron, "cron", domain.SchedulerRunStatusRunning, now.Add(-2*time.Hour), 0)
	insertSchedulerRun(t, db, fixtureSchedulerID, "run-failed", "daily", domain.SchedulerTriggerKindCron, "cron", domain.SchedulerRunStatusFailed, now.Add(-time.Hour), time.Second)
	insertTrigger(t, db, fixtureSchedulerID, "uninitialized", domain.SchedulerTriggerKindCron, cronSpec(t, "0 9 * * *", "UTC"), 0, time.Time{})
	insertSandbox(t, db, "sbx-pending", domain.VMStatusPending, now.Add(-time.Hour))
	insertProjectRun(t, db, "prun-running", domain.ProjectRunStatusRunning, now.Add(-30*time.Minute))

	advisor := mustAdvisor(t, db, Options{Now: now, StuckAfter: time.Hour, PendingSandboxAfter: 30 * time.Minute, Location: time.UTC})
	report, err := advisor.Status(context.Background())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if len(report.RunningSchedulerRuns) != 1 || report.RunningSchedulerRuns[0].RunID != "run-running" {
		t.Fatalf("running scheduler runs = %+v", report.RunningSchedulerRuns)
	}
	if len(report.RunningProjectRuns) != 1 || report.RunningProjectRuns[0].RunID != "prun-running" {
		t.Fatalf("running project runs = %+v", report.RunningProjectRuns)
	}
	if len(report.RecentFailures) != 1 {
		t.Fatalf("recent failures = %d, want 1", len(report.RecentFailures))
	}
	kinds := map[string]bool{}
	for _, anomaly := range report.Anomalies {
		kinds[anomaly.Kind] = true
	}
	for _, want := range []string{"scheduler_run_long_running", "scheduler_trigger_without_next_fire", "sandbox_creation_stuck"} {
		if !kinds[want] {
			t.Fatalf("anomaly %q missing from %+v", want, report.Anomalies)
		}
	}
}

func TestStatusRejectsNonAgentComposeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open empty database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	advisor := mustAdvisor(t, db, Options{Now: time.Now(), Location: time.UTC})
	_, err = advisor.Status(context.Background())
	if !errors.Is(err, ErrNotAgentComposeDatabase) {
		t.Fatalf("status error = %v, want ErrNotAgentComposeDatabase", err)
	}
}
