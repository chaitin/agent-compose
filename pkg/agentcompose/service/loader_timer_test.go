package agentcompose

import (
	appconfig "agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/execution"
	"agent-compose/pkg/loaders"
	domain "agent-compose/pkg/model"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoaderEngineValidateSupportsTimeoutAndClearTimers(t *testing.T) {
	engine := &loaders.QJSLoaderEngine{}
	script := strings.TrimSpace(`
const heartbeat = scheduler.interval("heartbeat", function heartbeat() {
  scheduler.log("heartbeat");
}, 60000);
scheduler.clearInterval(heartbeat);

scheduler.interval("poll", function poll() {
  scheduler.log("poll");
}, 60000);

scheduler.timeout("boot", function boot() {
  scheduler.log("boot");
}, 2500);
scheduler.clearTimeout("boot");

scheduler.timeout(function once() {
  scheduler.log("once");
}, 7500);

scheduler.cron("*/15 * * * *", function quarterHour() {
  scheduler.log("quarter hour");
}, { id: "quarter-hour", timezone: "UTC" });

scheduler.on("agent-compose.session.created", function onSession(event) {
  scheduler.log("session created", event);
});
`)

	result, err := engine.Validate(context.Background(), domain.LoaderRuntimeScheduler, script)
	if err != nil {
		t.Fatalf("Validate returned error: %v", err)
	}
	if len(result.Triggers) != 4 {
		t.Fatalf("expected 4 triggers after clear operations, got %d", len(result.Triggers))
	}
	if result.Triggers[0].Kind != domain.LoaderTriggerKindInterval {
		t.Fatalf("first trigger kind = %q, want %q", result.Triggers[0].Kind, domain.LoaderTriggerKindInterval)
	}
	if result.Triggers[0].ID != "poll" {
		t.Fatalf("first trigger id = %q, want %q", result.Triggers[0].ID, "poll")
	}
	if result.Triggers[0].IntervalMs != 60000 {
		t.Fatalf("first trigger interval = %d, want %d", result.Triggers[0].IntervalMs, 60000)
	}
	if result.Triggers[1].Kind != domain.LoaderTriggerKindTimeout {
		t.Fatalf("second trigger kind = %q, want %q", result.Triggers[1].Kind, domain.LoaderTriggerKindTimeout)
	}
	if result.Triggers[1].IntervalMs != 7500 {
		t.Fatalf("second trigger delay = %d, want %d", result.Triggers[1].IntervalMs, 7500)
	}
	if strings.TrimSpace(result.Triggers[1].ID) == "" {
		t.Fatalf("expected timeout trigger id to be assigned")
	}
	if result.Triggers[2].Kind != domain.LoaderTriggerKindCron {
		t.Fatalf("third trigger kind = %q, want %q", result.Triggers[2].Kind, domain.LoaderTriggerKindCron)
	}
	if result.Triggers[2].ID != "quarter-hour" {
		t.Fatalf("third trigger id = %q, want %q", result.Triggers[2].ID, "quarter-hour")
	}
	if !strings.Contains(result.Triggers[2].SpecJSON, `"expr":"*/15 * * * *"`) {
		t.Fatalf("expected cron spec json to include expression, got %s", result.Triggers[2].SpecJSON)
	}
	if !strings.Contains(result.Triggers[2].SpecJSON, `"timezone":"UTC"`) {
		t.Fatalf("expected cron spec json to include timezone, got %s", result.Triggers[2].SpecJSON)
	}
	if result.Triggers[3].Kind != domain.LoaderTriggerKindEvent {
		t.Fatalf("fourth trigger kind = %q, want %q", result.Triggers[3].Kind, domain.LoaderTriggerKindEvent)
	}
	if result.Triggers[3].Topic != "agent-compose.session.created" {
		t.Fatalf("fourth trigger topic = %q, want %q", result.Triggers[3].Topic, "agent-compose.session.created")
	}
}

func TestLoaderEngineSchedulerRuntimeDoesNotInjectADP(t *testing.T) {
	engine := &loaders.QJSLoaderEngine{}
	result, err := engine.Execute(context.Background(), loaders.LoaderExecutionRequest{
		Runtime: domain.LoaderRuntimeScheduler,
		Script: `function main() {
  return {
    runtime: scheduler.runtime.name,
    adpType: typeof globalThis["a" + "dp"],
    hasInterval: typeof scheduler.interval,
    hasSession: typeof scheduler.session.createSession,
    hasZ: typeof scheduler.z.object,
  };
}`,
	}, &recordingLoaderHost{})
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	for _, want := range []string{
		`"runtime":"scheduler"`,
		`"adpType":"undefined"`,
		`"hasInterval":"function"`,
		`"hasSession":"function"`,
		`"hasZ":"function"`,
	} {
		if !strings.Contains(result.ResultJSON, want) {
			t.Fatalf("result json = %s, want %s", result.ResultJSON, want)
		}
	}
}

func TestLoaderEngineExecutesSchedulerIntervalAndEventTriggers(t *testing.T) {
	engine := &loaders.QJSLoaderEngine{}
	host := &statefulRecordingLoaderHost{state: map[string]string{}}

	intervalResult, err := engine.Execute(context.Background(), loaders.LoaderExecutionRequest{
		Runtime:     domain.LoaderRuntimeScheduler,
		PayloadJSON: `{"value":42}`,
		Trigger:     &domain.LoaderTrigger{ID: "state-tick"},
		Script: `
scheduler.interval("state-tick", function(event) {
  scheduler.state.set("last", { event });
  const last = scheduler.state.get("last");
  scheduler.state.delete("last");
  return { last, missingType: typeof scheduler.state.get("last") };
}, 1000);
`,
	}, host)
	if err != nil {
		t.Fatalf("Execute interval trigger returned error: %v", err)
	}
	if !strings.Contains(intervalResult.ResultJSON, `"value":42`) || !strings.Contains(intervalResult.ResultJSON, `"missingType":"undefined"`) {
		t.Fatalf("interval result json = %s", intervalResult.ResultJSON)
	}
	if _, ok := host.state["last"]; ok {
		t.Fatalf("expected scheduler.state.delete to remove key, state = %#v", host.state)
	}
	if len(host.deleted) != 1 || host.deleted[0] != "last" {
		t.Fatalf("deleted keys = %#v, want [last]", host.deleted)
	}

	eventResult, err := engine.Execute(context.Background(), loaders.LoaderExecutionRequest{
		Runtime:     domain.LoaderRuntimeScheduler,
		PayloadJSON: `{"topic":"agent-compose.session.created","sessionId":"session-1"}`,
		Trigger:     &domain.LoaderTrigger{ID: "session-created"},
		Script: `
scheduler.on("agent-compose.session.created", "session-created", function(event) {
  scheduler.log("session created", event);
  return { topic: event.topic, sessionId: event.sessionId };
});
`,
	}, host)
	if err != nil {
		t.Fatalf("Execute event trigger returned error: %v", err)
	}
	if !strings.Contains(eventResult.ResultJSON, `"topic":"agent-compose.session.created"`) || !strings.Contains(eventResult.ResultJSON, `"sessionId":"session-1"`) {
		t.Fatalf("event result json = %s", eventResult.ResultJSON)
	}
}

func TestLoaderEngineMaxExecutionTimeHonorsLongDeadline(t *testing.T) {
	if got, want := loaders.EngineMaxExecutionTime(context.Background()), int((60*time.Minute)/time.Millisecond); got != want {
		t.Fatalf("max execution time without deadline = %d, want %d", got, want)
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(90*time.Minute))
	defer cancel()
	got := loaders.EngineMaxExecutionTime(ctx)
	if got <= int((60*time.Minute)/time.Millisecond) {
		t.Fatalf("max execution time with 90m deadline = %d, want above 60m", got)
	}
	if got > int((90*time.Minute)/time.Millisecond) {
		t.Fatalf("max execution time with 90m deadline = %d, want no more than 90m", got)
	}
}

func TestLoaderManagerCollectDueScheduledRunsConsumesTimeout(t *testing.T) {
	testLoaderManagerCollectDueScheduledRunsConsumesTimeout(t)
}

func testLoaderManagerCollectDueScheduledRunsConsumesTimeout(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)

	triggers, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, []domain.LoaderTrigger{{
		ID:         "once",
		Kind:       domain.LoaderTriggerKindTimeout,
		IntervalMs: 5000,
		Enabled:    true,
		SpecJSON:   `{"kind":"timeout","delayMs":5000}`,
	}})
	if err != nil {
		t.Fatalf("ReplaceLoaderTriggers returned error: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(triggers))
	}
	if triggers[0].NextFireAt.IsZero() {
		t.Fatalf("expected timeout trigger to be scheduled")
	}

	loaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader returned error: %v", err)
	}
	now := loaded.Triggers[0].NextFireAt
	manager := &LoaderManager{
		rootCtx:  ctx,
		configDB: store,
		loaders:  map[string]Loader{loaded.Summary.ID: loaded},
		running:  map[string]int{},
	}

	jobs := manager.collectDueScheduledRuns(now)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(jobs))
	}
	if jobs[0].trigger.Kind != domain.LoaderTriggerKindTimeout {
		t.Fatalf("job trigger kind = %q, want %q", jobs[0].trigger.Kind, domain.LoaderTriggerKindTimeout)
	}
	if jobs[0].trigger.LastFiredAt.IsZero() {
		t.Fatalf("expected timeout trigger last_fired_at to be recorded")
	}
	if !jobs[0].trigger.NextFireAt.IsZero() {
		t.Fatalf("expected timeout trigger next_fire_at to be cleared after firing")
	}

	reloaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader after fire returned error: %v", err)
	}
	if len(reloaded.Triggers) != 1 {
		t.Fatalf("expected 1 trigger after fire, got %d", len(reloaded.Triggers))
	}
	if !reloaded.Triggers[0].NextFireAt.IsZero() {
		t.Fatalf("expected persisted timeout trigger next_fire_at to be cleared")
	}
	if reloaded.Triggers[0].LastFiredAt.IsZero() {
		t.Fatalf("expected persisted timeout trigger last_fired_at to be set")
	}
}

func TestParseLoaderRunTimeout(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    time.Duration
		wantErr bool
	}{
		{name: "empty uses default", raw: "", want: 0},
		{name: "minutes", raw: "15m", want: 15 * time.Minute},
		{name: "compound", raw: "1h30m", want: 90 * time.Minute},
		{name: "invalid", raw: "15 minutes", wantErr: true},
		{name: "zero", raw: "0s", wantErr: true},
		{name: "negative", raw: "-1s", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := loaders.ParseRunTimeout(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLoaderRunTimeout returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("parseLoaderRunTimeout = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestLoaderManagerLoaderRunTimeout(t *testing.T) {
	manager := &LoaderManager{config: &appconfig.Config{LoaderRunTimeout: 45 * time.Minute}}
	if got := manager.loaderRunTimeout(5 * time.Minute); got != 5*time.Minute {
		t.Fatalf("loaderRunTimeout override = %s, want 5m", got)
	}
	if got := manager.loaderRunTimeout(0); got != 45*time.Minute {
		t.Fatalf("loaderRunTimeout config = %s, want 45m", got)
	}
	manager.config.LoaderRunTimeout = 0
	if got := manager.loaderRunTimeout(0); got != 20*time.Minute {
		t.Fatalf("loaderRunTimeout default = %s, want 20m", got)
	}
}

func TestConfigStoreReplaceLoaderTriggersStoresMillisecondScheduleTimes(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)

	before := time.Now().UTC()
	triggers, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, []domain.LoaderTrigger{{
		ID:         "fast",
		Kind:       domain.LoaderTriggerKindInterval,
		IntervalMs: 125,
		Enabled:    true,
		SpecJSON:   `{"kind":"interval","intervalMs":125}`,
	}})
	if err != nil {
		t.Fatalf("ReplaceLoaderTriggers returned error: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(triggers))
	}
	var nextFireAtRaw int64
	if err := store.db.QueryRowContext(ctx, `SELECT next_fire_at FROM loader_trigger WHERE loader_id = ? AND trigger_id = ?`, loader.Summary.ID, "fast").Scan(&nextFireAtRaw); err != nil {
		t.Fatalf("QueryRowContext returned error: %v", err)
	}
	if nextFireAtRaw < storedUnixMillisecondThreshold {
		t.Fatalf("expected millisecond next_fire_at, got %d", nextFireAtRaw)
	}
	if delta := triggers[0].NextFireAt.Sub(before); delta <= 0 || delta > time.Second {
		t.Fatalf("expected sub-second trigger schedule after %s, got %s (delta %s)", before, triggers[0].NextFireAt, delta)
	}

	loaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader returned error: %v", err)
	}
	if len(loaded.Triggers) != 1 {
		t.Fatalf("expected 1 trigger after reload, got %d", len(loaded.Triggers))
	}
	if delta := loaded.Triggers[0].NextFireAt.Sub(triggers[0].NextFireAt); delta < -time.Millisecond || delta > time.Millisecond {
		t.Fatalf("expected trigger next_fire_at round-trip to preserve millisecond precision, delta %s", delta)
	}
}

func TestScanLoaderTriggerParsesLegacySecondPrecisionTimes(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)

	_, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, []domain.LoaderTrigger{{
		ID:         "legacy",
		Kind:       domain.LoaderTriggerKindInterval,
		IntervalMs: 5000,
		Enabled:    true,
		SpecJSON:   `{"kind":"interval","intervalMs":5000}`,
	}})
	if err != nil {
		t.Fatalf("ReplaceLoaderTriggers returned error: %v", err)
	}
	legacyNext := time.Now().UTC().Add(5 * time.Second).Truncate(time.Second)
	legacyLast := time.Now().UTC().Add(-3 * time.Second).Truncate(time.Second)
	if _, err := store.db.ExecContext(ctx, `UPDATE loader_trigger SET next_fire_at = ?, last_fired_at = ? WHERE loader_id = ? AND trigger_id = ?`, legacyNext.Unix(), legacyLast.Unix(), loader.Summary.ID, "legacy"); err != nil {
		t.Fatalf("ExecContext returned error: %v", err)
	}

	loaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader returned error: %v", err)
	}
	if len(loaded.Triggers) != 1 {
		t.Fatalf("expected 1 trigger after reload, got %d", len(loaded.Triggers))
	}
	if !loaded.Triggers[0].NextFireAt.Equal(legacyNext) {
		t.Fatalf("expected legacy second next_fire_at %s, got %s", legacyNext, loaded.Triggers[0].NextFireAt)
	}
	if !loaded.Triggers[0].LastFiredAt.Equal(legacyLast) {
		t.Fatalf("expected legacy second last_fired_at %s, got %s", legacyLast, loaded.Triggers[0].LastFiredAt)
	}
}

func TestConfigStoreSetLoaderTriggerEnabledRearmsTimeout(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)

	_, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, []domain.LoaderTrigger{{
		ID:         "once",
		Kind:       domain.LoaderTriggerKindTimeout,
		IntervalMs: 3000,
		Enabled:    true,
		SpecJSON:   `{"kind":"timeout","delayMs":3000}`,
	}})
	if err != nil {
		t.Fatalf("ReplaceLoaderTriggers returned error: %v", err)
	}
	if err := store.MarkLoaderTriggerFired(ctx, loader.Summary.ID, "once", time.Now().UTC(), time.Time{}); err != nil {
		t.Fatalf("MarkLoaderTriggerFired returned error: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "once", false); err != nil {
		t.Fatalf("SetLoaderTriggerEnabled(false) returned error: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "once", true); err != nil {
		t.Fatalf("SetLoaderTriggerEnabled(true) returned error: %v", err)
	}

	reloaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader returned error: %v", err)
	}
	if len(reloaded.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(reloaded.Triggers))
	}
	if !reloaded.Triggers[0].Enabled {
		t.Fatalf("expected timeout trigger to be enabled after rearm")
	}
	if reloaded.Triggers[0].NextFireAt.IsZero() {
		t.Fatalf("expected timeout trigger to be rescheduled after re-enable")
	}
}

func TestLoaderManagerCollectDueScheduledRunsReschedulesCron(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)

	specJSON, err := loaders.LoaderCronSpecJSON("*/5 * * * *", "UTC")
	if err != nil {
		t.Fatalf("loaderCronSpecJSON returned error: %v", err)
	}
	triggers, err := store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, []domain.LoaderTrigger{{
		ID:       "every-five-minutes",
		Kind:     domain.LoaderTriggerKindCron,
		Enabled:  true,
		SpecJSON: specJSON,
	}})
	if err != nil {
		t.Fatalf("ReplaceLoaderTriggers returned error: %v", err)
	}
	if len(triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(triggers))
	}
	if triggers[0].NextFireAt.IsZero() {
		t.Fatalf("expected cron trigger to be scheduled")
	}

	loaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader returned error: %v", err)
	}
	now := loaded.Triggers[0].NextFireAt
	manager := &LoaderManager{
		rootCtx:  ctx,
		configDB: store,
		loaders:  map[string]Loader{loaded.Summary.ID: loaded},
		running:  map[string]int{},
	}

	jobs := manager.collectDueScheduledRuns(now)
	if len(jobs) != 1 {
		t.Fatalf("expected 1 due job, got %d", len(jobs))
	}
	if jobs[0].trigger.Kind != domain.LoaderTriggerKindCron {
		t.Fatalf("job trigger kind = %q, want %q", jobs[0].trigger.Kind, domain.LoaderTriggerKindCron)
	}
	if jobs[0].trigger.LastFiredAt.IsZero() {
		t.Fatalf("expected cron trigger last_fired_at to be recorded")
	}
	if !jobs[0].trigger.NextFireAt.After(now) {
		t.Fatalf("expected cron trigger next_fire_at to advance beyond %s, got %s", now, jobs[0].trigger.NextFireAt)
	}

	reloaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader after fire returned error: %v", err)
	}
	if len(reloaded.Triggers) != 1 {
		t.Fatalf("expected 1 trigger after fire, got %d", len(reloaded.Triggers))
	}
	if !reloaded.Triggers[0].NextFireAt.After(now) {
		t.Fatalf("expected persisted cron trigger next_fire_at to advance beyond %s, got %s", now, reloaded.Triggers[0].NextFireAt)
	}
	if reloaded.Triggers[0].LastFiredAt.IsZero() {
		t.Fatalf("expected persisted cron trigger last_fired_at to be set")
	}
}

func TestConfigStoreSetLoaderTriggerEnabledRearmsCron(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)

	specJSON, err := loaders.LoaderCronSpecJSON("0 9 * * 1-5", "UTC")
	if err != nil {
		t.Fatalf("loaderCronSpecJSON returned error: %v", err)
	}
	_, err = store.ReplaceLoaderTriggers(ctx, loader.Summary.ID, []domain.LoaderTrigger{{
		ID:       "weekday-morning",
		Kind:     domain.LoaderTriggerKindCron,
		Enabled:  true,
		SpecJSON: specJSON,
	}})
	if err != nil {
		t.Fatalf("ReplaceLoaderTriggers returned error: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "weekday-morning", false); err != nil {
		t.Fatalf("SetLoaderTriggerEnabled(false) returned error: %v", err)
	}
	if err := store.SetLoaderTriggerEnabled(ctx, loader.Summary.ID, "weekday-morning", true); err != nil {
		t.Fatalf("SetLoaderTriggerEnabled(true) returned error: %v", err)
	}

	reloaded, err := store.GetLoader(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoader returned error: %v", err)
	}
	if len(reloaded.Triggers) != 1 {
		t.Fatalf("expected 1 trigger, got %d", len(reloaded.Triggers))
	}
	if !reloaded.Triggers[0].Enabled {
		t.Fatalf("expected cron trigger to be enabled after rearm")
	}
	if reloaded.Triggers[0].NextFireAt.IsZero() {
		t.Fatalf("expected cron trigger to be rescheduled after re-enable")
	}
}

func TestConfigStoreCreateLoaderRunStoresMillisecondStartedAt(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)
	startedAt := time.Now().UTC().Truncate(time.Millisecond)
	completedAt := startedAt.Add(145 * time.Millisecond)

	run := domain.LoaderRunSummary{
		ID:          "run-ms",
		LoaderID:    loader.Summary.ID,
		Status:      domain.LoaderRunStatusSucceeded,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		DurationMs:  completedAt.Sub(startedAt).Milliseconds(),
	}
	if err := store.CreateLoaderRun(ctx, run); err != nil {
		t.Fatalf("CreateLoaderRun returned error: %v", err)
	}

	var startedAtRaw int64
	var completedAtRaw int64
	if err := store.db.QueryRowContext(ctx, `SELECT started_at, completed_at FROM loader_run WHERE loader_id = ? AND run_id = ?`, loader.Summary.ID, run.ID).Scan(&startedAtRaw, &completedAtRaw); err != nil {
		t.Fatalf("QueryRowContext returned error: %v", err)
	}
	if startedAtRaw < storedUnixMillisecondThreshold {
		t.Fatalf("expected millisecond started_at, got %d", startedAtRaw)
	}
	if completedAtRaw < storedUnixMillisecondThreshold {
		t.Fatalf("expected millisecond completed_at, got %d", completedAtRaw)
	}

	loaded, err := store.GetLoaderRun(ctx, loader.Summary.ID, run.ID)
	if err != nil {
		t.Fatalf("GetLoaderRun returned error: %v", err)
	}
	if !loaded.StartedAt.Equal(startedAt) {
		t.Fatalf("expected started_at %s, got %s", startedAt, loaded.StartedAt)
	}
	if !loaded.CompletedAt.Equal(completedAt) {
		t.Fatalf("expected completed_at %s, got %s", completedAt, loaded.CompletedAt)
	}
}

func TestConfigStoreAddLoaderEventStoresMillisecondCreatedAt(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)
	createdAt := time.Now().UTC().Truncate(time.Millisecond)

	event := domain.LoaderEvent{
		ID:        "event-ms",
		LoaderID:  loader.Summary.ID,
		Type:      "loader.test",
		Level:     "info",
		Message:   "millisecond event",
		CreatedAt: createdAt,
	}
	if err := store.AddLoaderEvent(ctx, event); err != nil {
		t.Fatalf("AddLoaderEvent returned error: %v", err)
	}

	var createdAtRaw int64
	if err := store.db.QueryRowContext(ctx, `SELECT created_at FROM loader_event WHERE loader_id = ? AND event_id = ?`, loader.Summary.ID, event.ID).Scan(&createdAtRaw); err != nil {
		t.Fatalf("QueryRowContext returned error: %v", err)
	}
	if createdAtRaw < storedUnixMillisecondThreshold {
		t.Fatalf("expected millisecond created_at, got %d", createdAtRaw)
	}

	events, err := store.ListLoaderEvents(ctx, loader.Summary.ID, 10)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if !events[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("expected created_at %s, got %s", createdAt, events[0].CreatedAt)
	}
}

func TestEnsureLoaderSchemaMigratesLegacyRunAndEventTimestamps(t *testing.T) {
	ctx := context.Background()
	store := newTestConfigStore(t)
	loader := createTestLoader(t, ctx, store)

	legacyStartedAt := time.Now().UTC().Add(-4 * time.Second).Truncate(time.Second)
	legacyCompletedAt := legacyStartedAt.Add(2 * time.Second)
	legacyEventAt := time.Now().UTC().Add(-2 * time.Second).Truncate(time.Second)
	if _, err := store.db.ExecContext(ctx, `INSERT INTO loader_run(loader_id, run_id, status, started_at, completed_at) VALUES(?, ?, ?, ?, ?)`, loader.Summary.ID, "legacy-run", domain.LoaderRunStatusSucceeded, legacyStartedAt.Unix(), legacyCompletedAt.Unix()); err != nil {
		t.Fatalf("insert legacy run returned error: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO loader_event(loader_id, event_id, type, level, message, created_at) VALUES(?, ?, ?, ?, ?, ?)`, loader.Summary.ID, "legacy-event", "loader.test", "info", "legacy", legacyEventAt.Unix()); err != nil {
		t.Fatalf("insert legacy event returned error: %v", err)
	}

	if err := store.ensureLoaderSchema(ctx); err != nil {
		t.Fatalf("ensureLoaderSchema returned error: %v", err)
	}

	var startedAtRaw int64
	var completedAtRaw int64
	var createdAtRaw int64
	if err := store.db.QueryRowContext(ctx, `SELECT started_at, completed_at FROM loader_run WHERE loader_id = ? AND run_id = ?`, loader.Summary.ID, "legacy-run").Scan(&startedAtRaw, &completedAtRaw); err != nil {
		t.Fatalf("query migrated run returned error: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT created_at FROM loader_event WHERE loader_id = ? AND event_id = ?`, loader.Summary.ID, "legacy-event").Scan(&createdAtRaw); err != nil {
		t.Fatalf("query migrated event returned error: %v", err)
	}
	if startedAtRaw != legacyStartedAt.UnixMilli() {
		t.Fatalf("expected migrated started_at %d, got %d", legacyStartedAt.UnixMilli(), startedAtRaw)
	}
	if completedAtRaw != legacyCompletedAt.UnixMilli() {
		t.Fatalf("expected migrated completed_at %d, got %d", legacyCompletedAt.UnixMilli(), completedAtRaw)
	}
	if createdAtRaw != legacyEventAt.UnixMilli() {
		t.Fatalf("expected migrated created_at %d, got %d", legacyEventAt.UnixMilli(), createdAtRaw)
	}

	run, err := store.GetLoaderRun(ctx, loader.Summary.ID, "legacy-run")
	if err != nil {
		t.Fatalf("GetLoaderRun returned error: %v", err)
	}
	if !run.StartedAt.Equal(legacyStartedAt) {
		t.Fatalf("expected legacy run started_at %s, got %s", legacyStartedAt, run.StartedAt)
	}
	if !run.CompletedAt.Equal(legacyCompletedAt) {
		t.Fatalf("expected legacy run completed_at %s, got %s", legacyCompletedAt, run.CompletedAt)
	}
	events, err := store.ListLoaderEvents(ctx, loader.Summary.ID, 10)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("expected 1 migrated event, got %d", len(events))
	}
	if !events[0].CreatedAt.Equal(legacyEventAt) {
		t.Fatalf("expected legacy event created_at %s, got %s", legacyEventAt, events[0].CreatedAt)
	}
}

func TestLoaderManagerNextScheduledFireAtUsesEarliestMillisecondTrigger(t *testing.T) {
	now := time.Now().UTC()
	manager := &LoaderManager{
		loaders: map[string]Loader{
			"slow": {
				Summary: domain.LoaderSummary{ID: "slow", Enabled: true},
				Triggers: []domain.LoaderTrigger{{
					ID:         "slow-interval",
					Kind:       domain.LoaderTriggerKindInterval,
					IntervalMs: 1000,
					Enabled:    true,
					NextFireAt: now.Add(800 * time.Millisecond),
				}},
			},
			"fast": {
				Summary: domain.LoaderSummary{ID: "fast", Enabled: true},
				Triggers: []domain.LoaderTrigger{{
					ID:         "fast-interval",
					Kind:       domain.LoaderTriggerKindInterval,
					IntervalMs: 125,
					Enabled:    true,
					NextFireAt: now.Add(125 * time.Millisecond),
				}},
			},
		},
	}
	got, ok := manager.nextScheduledFireAt()
	if !ok {
		t.Fatalf("expected next scheduled fire time to exist")
	}
	want := now.Add(125 * time.Millisecond)
	if !got.Equal(want) {
		t.Fatalf("nextScheduledFireAt = %s, want %s", got, want)
	}
}

func TestLoaderManagerEnsureLoaderSessionAppliesAgentSessionOverrides(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:                 root,
		SessionRoot:              filepath.Join(root, "sessions"),
		RuntimeDriver:            driverpkg.RuntimeDriverBoxlite,
		DefaultImage:             "loader-box:latest",
		MicrosandboxDefaultImage: "loader-microsandbox:latest",
		BoxliteHome:              filepath.Join(root, "boxlite"),
		MicrosandboxHome:         filepath.Join(root, "microsandbox"),
		GuestWorkspacePath:       "/data/workspace",
		JupyterGuestPort:         8888,
		JupyterProxyBasePath:     "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(session root) returned error: %v", err)
	}

	configDB := newTestConfigStore(t)
	if _, err := configDB.ReplaceGlobalEnv(ctx, []SessionEnvVar{{Name: "GLOBAL_ONLY", Value: "global"}, {Name: "SHARED", Value: "global"}}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}

	repoDir := createLocalGitRepo(t)
	workspaceConfigJSON, err := json.Marshal(map[string]string{"url": repoDir})
	if err != nil {
		t.Fatalf("json.Marshal returned error: %v", err)
	}
	workspace, err := configDB.CreateWorkspaceConfig(ctx, WorkspaceConfig{
		Name:       "Loader Override Workspace",
		Type:       "git",
		ConfigJSON: string(workspaceConfigJSON),
	})
	if err != nil {
		t.Fatalf("CreateWorkspaceConfig returned error: %v", err)
	}

	loader := createTestLoader(t, ctx, configDB)
	loader.Summary.SessionPolicy = domain.LoaderSessionPolicySticky
	loader.EnvItems = []SessionEnvVar{{Name: "LOADER_ONLY", Value: "loader"}, {Name: "SHARED", Value: "loader"}}
	loader, err = configDB.UpdateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}

	driver := &fakeSessionDriver{}
	manager := &LoaderManager{
		config:   config,
		rootCtx:  ctx,
		store:    &Store{config: config},
		configDB: configDB,
		driver:   driver,
	}

	firstSession, eventType, err := manager.ensureLoaderSession(ctx, loader, domain.LoaderAgentRequest{})
	if err != nil {
		t.Fatalf("ensureLoaderSession(first) returned error: %v", err)
	}
	if eventType != "loader.session.created" {
		t.Fatalf("first event type = %q, want %q", eventType, "loader.session.created")
	}

	secondSession, eventType, err := manager.ensureLoaderSession(ctx, loader, domain.LoaderAgentRequest{
		Title:       "Loader Override Session",
		Driver:      driverpkg.RuntimeDriverMicrosandbox,
		GuestImage:  "override-guest:latest",
		WorkspaceID: workspace.ID,
		SessionEnv:  []SessionEnvVar{{Name: "REQUEST_ONLY", Value: "request"}, {Name: "SHARED", Value: "request"}},
	})
	if err != nil {
		t.Fatalf("ensureLoaderSession(second) returned error: %v", err)
	}
	if eventType != "loader.session.created" {
		t.Fatalf("second event type = %q, want %q", eventType, "loader.session.created")
	}
	if firstSession.Summary.ID == secondSession.Summary.ID {
		t.Fatalf("expected override request to create a new session, got %q", secondSession.Summary.ID)
	}
	if got, want := secondSession.Summary.Title, "Loader Override Session"; got != want {
		t.Fatalf("second session title = %q, want %q", got, want)
	}
	if got, want := secondSession.Summary.Driver, driverpkg.RuntimeDriverMicrosandbox; got != want {
		t.Fatalf("second session driver = %q, want %q", got, want)
	}
	if got, want := secondSession.Summary.GuestImage, "override-guest:latest"; got != want {
		t.Fatalf("second session guest image = %q, want %q", got, want)
	}
	if got, want := secondSession.WorkspaceID, workspace.ID; got != want {
		t.Fatalf("second session workspace id = %q, want %q", got, want)
	}
	if secondSession.Workspace == nil || secondSession.Workspace.ID != workspace.ID {
		t.Fatalf("second session workspace snapshot = %#v, want id %q", secondSession.Workspace, workspace.ID)
	}
	if _, err := os.Stat(filepath.Join(secondSession.Summary.WorkspacePath, "README.md")); err != nil {
		t.Fatalf("expected cloned workspace README.md: %v", err)
	}
	if len(driver.startCalls) != 2 {
		t.Fatalf("StartSessionVM call count = %d, want %d", len(driver.startCalls), 2)
	}

	env := sessionEnvMap(secondSession.EnvItems)
	if got, want := env["GLOBAL_ONLY"], "global"; got != want {
		t.Fatalf("GLOBAL_ONLY = %q, want %q", got, want)
	}
	if got, want := env["LOADER_ONLY"], "loader"; got != want {
		t.Fatalf("LOADER_ONLY = %q, want %q", got, want)
	}
	if got, want := env["REQUEST_ONLY"], "request"; got != want {
		t.Fatalf("REQUEST_ONLY = %q, want %q", got, want)
	}
	if got, want := env["SHARED"], "request"; got != want {
		t.Fatalf("SHARED = %q, want %q", got, want)
	}

	binding, ok, err := configDB.GetLoaderBinding(ctx, loader.Summary.ID)
	if err != nil {
		t.Fatalf("GetLoaderBinding returned error: %v", err)
	}
	if !ok {
		t.Fatalf("expected sticky loader binding to exist")
	}
	if got, want := binding.SessionID, secondSession.Summary.ID; got != want {
		t.Fatalf("loader binding session id = %q, want %q", got, want)
	}
}

func TestLoaderRunHostAgentStopsSessionAfterExecution(t *testing.T) {
	testLoaderRunHostAgentStopsSessionAfterExecution(t)
}

func testLoaderRunHostAgentStopsSessionAfterExecution(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "loader-box:latest",
		GuestWorkspacePath:   "/data/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(session root) returned error: %v", err)
	}

	configDB := newTestConfigStore(t)
	store := &Store{config: config}
	runtime := &fakeLoaderAgentRuntime{}
	driver := &fakeSessionDriver{}
	manager := &LoaderManager{
		config:   config,
		rootCtx:  ctx,
		store:    store,
		configDB: configDB,
		driver:   driver,
		executor: &Executor{config: config, store: store, runtimes: fixedRuntimeProvider{runtime: runtime}},
		engine:   &loaders.QJSLoaderEngine{},
		running:  map[string]int{},
	}
	loader := createTestLoader(t, ctx, configDB)
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-agent-stop", LoaderID: loader.Summary.ID},
	}

	result, err := host.Agent(ctx, "summarize this loader run", domain.LoaderAgentRequest{Agent: "codex"})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if result.SessionID == "" {
		t.Fatalf("expected Agent to return a session id")
	}
	if result.Agent != "codex" {
		t.Fatalf("result agent = %q, want %q", result.Agent, "codex")
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSessionVM call count = %d, want %d", len(driver.startCalls), 1)
	}
	if len(driver.stopCalls) != 1 {
		t.Fatalf("StopSessionVM call count = %d, want %d", len(driver.stopCalls), 1)
	}
	if driver.stopCalls[0] != result.SessionID {
		t.Fatalf("StopSessionVM session id = %q, want %q", driver.stopCalls[0], result.SessionID)
	}
	if runtime.execCalls != 1 {
		t.Fatalf("runtime ExecStream call count = %d, want %d", runtime.execCalls, 1)
	}
	if len(runtime.providers) != 1 || runtime.providers[0] != "codex" {
		t.Fatalf("runtime providers = %#v, want []string{\"codex\"}", runtime.providers)
	}

	session, err := store.GetSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if got, want := session.Summary.VMStatus, domain.VMStatusStopped; got != want {
		t.Fatalf("session vm status = %q, want %q", got, want)
	}
	if session.Summary.CellCount == 0 {
		t.Fatalf("expected agent run to persist at least one cell")
	}
	events, err := store.ListEvents(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("ListEvents returned error: %v", err)
	}
	foundStopped := false
	for _, event := range events {
		if event.Type == "session.stopped" {
			foundStopped = true
			break
		}
	}
	if !foundStopped {
		t.Fatalf("expected session.stopped event, got %#v", events)
	}
}

const fakeGuestCommandResultSentinel = "[guest-written]"

func TestLoaderRunHostCommandPersistsShellCellArtifactsAndEvents(t *testing.T) {
	testLoaderRunHostCommandPersistsShellCellArtifactsAndEvents(t)
}

func TestLoaderRunHostCommandPersistsRunningCellOutput(t *testing.T) {
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command-stream", LoaderID: loader.Summary.ID},
	}
	var streamedCell NotebookCell
	runtime.commandStreamHook = func() {
		sessions, err := manager.store.ListSessions(ctx, SessionListOptions{Limit: 10})
		if err != nil {
			t.Fatalf("ListSessions returned error: %v", err)
		}
		for _, session := range sessions.Sessions {
			cells, err := manager.store.ListCells(ctx, session.Summary.ID)
			if err != nil {
				t.Fatalf("ListCells returned error: %v", err)
			}
			for _, cell := range cells {
				if cell.Running && strings.Contains(cell.Output, "command stdout\n") {
					streamedCell = cell
					return
				}
			}
		}
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Mode:    "exec",
		Command: "python3",
		Args:    []string{"-V"},
	})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if streamedCell.ID != result.CellID {
		t.Fatalf("streamed running cell = %#v, want cell id %q", streamedCell, result.CellID)
	}
	if streamedCell.Stdout != "command stdout\n" || streamedCell.Stderr != "" || streamedCell.Output != "command stdout\n" {
		t.Fatalf("streamed cell output = %#v", streamedCell)
	}
}

func TestLoaderRunHostCommandDeletesLLMFacadeToken(t *testing.T) {
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	manager.config.RuntimeBaseURL = "http://agent-compose.test"
	if _, err := manager.configDB.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://openai-compatible.example.invalid/api"},
		{Name: "LLM_API_KEY", Value: "provider-key", Secret: true},
		{Name: "LLM_MODEL", Value: "svip/gpt-5.5"},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command-facade-token", LoaderID: loader.Summary.ID},
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Mode:    "exec",
		Command: "python3",
		Args:    []string{"-V"},
		Env:     map[string]string{"PROJECT_AGENT_PROVIDER": "codex"},
	})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("command result = %#v", result)
	}
	if len(runtime.commandSpecs) != 1 {
		t.Fatalf("command ExecStream calls = %d, want 1", len(runtime.commandSpecs))
	}
	token := runtime.commandSpecs[0].Env["AGENT_COMPOSE_SESSION_TOKEN"]
	if token == "" {
		t.Fatalf("command spec missing AGENT_COMPOSE_SESSION_TOKEN: %#v", runtime.commandSpecs[0].Env)
	}
	if _, err := manager.configDB.GetLLMFacadeToken(ctx, token); err == nil {
		t.Fatalf("loader command facade token should be deleted after command returns")
	}
}

func TestLoaderRunHostCommandUsesLLMProviderOverrideForFacade(t *testing.T) {
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	manager.config.RuntimeBaseURL = "http://agent-compose.test"
	if _, err := manager.configDB.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "ANTHROPIC_BASE_URL", Value: "https://anthropic.example.invalid"},
		{Name: "ANTHROPIC_AUTH_TOKEN", Value: "provider-key", Secret: true},
		{Name: "ANTHROPIC_MODEL", Value: "vip/deepseek-v4-pro"},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command-llm-provider-override", LoaderID: loader.Summary.ID},
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Mode:    "exec",
		Command: "python3",
		Args:    []string{"-V"},
		Env: map[string]string{
			"PROJECT_AGENT_PROVIDER":     "pilot",
			"PROJECT_AGENT_LLM_PROVIDER": "claude",
			"ANTHROPIC_MODEL":            "vip/deepseek-v4-pro",
		},
	})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("command result = %#v", result)
	}
	if len(runtime.commandSpecs) != 1 {
		t.Fatalf("command ExecStream calls = %d, want 1", len(runtime.commandSpecs))
	}
	env := runtime.commandSpecs[0].Env
	token := env["AGENT_COMPOSE_SESSION_TOKEN"]
	if token == "" {
		t.Fatalf("command spec missing AGENT_COMPOSE_SESSION_TOKEN: %#v", env)
	}
	if env["ANTHROPIC_API_KEY"] != token {
		t.Fatalf("ANTHROPIC_API_KEY = %q, want facade token", env["ANTHROPIC_API_KEY"])
	}
	if strings.TrimSpace(env["ANTHROPIC_BASE_URL"]) == "" {
		t.Fatalf("ANTHROPIC_BASE_URL missing from command env: %#v", env)
	}
}

func TestLoaderRunHostCommandSkipsOpenCodeFacadeWithoutModel(t *testing.T) {
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	manager.config.RuntimeBaseURL = "http://agent-compose.test"
	if _, err := manager.configDB.ReplaceGlobalEnv(ctx, []SessionEnvVar{
		{Name: "LLM_API_ENDPOINT", Value: "https://openai-compatible.example.invalid/api"},
		{Name: "LLM_API_KEY", Value: "provider-key", Secret: true},
	}); err != nil {
		t.Fatalf("ReplaceGlobalEnv returned error: %v", err)
	}
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command-opencode-no-model", LoaderID: loader.Summary.ID},
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Mode:    "exec",
		Command: "python3",
		Args:    []string{"-V"},
		Env:     map[string]string{"PROJECT_AGENT_PROVIDER": "opencode"},
	})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("command result = %#v", result)
	}
	if len(runtime.commandSpecs) != 1 {
		t.Fatalf("command ExecStream calls = %d, want 1", len(runtime.commandSpecs))
	}
	if token := runtime.commandSpecs[0].Env["AGENT_COMPOSE_SESSION_TOKEN"]; token != "" {
		t.Fatalf("opencode command without model should not receive facade token %q", token)
	}
}

func testLoaderRunHostCommandPersistsShellCellArtifactsAndEvents(t *testing.T) {
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	runtime.commandTruncated = true
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command", LoaderID: loader.Summary.ID},
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Mode:           "exec",
		Command:        "python3",
		Args:           []string{"-V"},
		Cwd:            "/data/workspace",
		Env:            map[string]string{"FOO": "bar"},
		MaxOutputBytes: 32,
	})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if !result.Success || result.Stdout != "command stdout\n" {
		t.Fatalf("command result = %#v", result)
	}
	if !result.StdoutTruncated || !result.OutputTruncated {
		t.Fatalf("command truncation flags = stdout:%t output:%t, want true", result.StdoutTruncated, result.OutputTruncated)
	}
	if result.SessionID == "" || result.CellID == "" {
		t.Fatalf("expected session and cell ids in result: %#v", result)
	}
	if len(runtime.commandSpecs) != 1 {
		t.Fatalf("command ExecStream call count = %d, want %d", len(runtime.commandSpecs), 1)
	}
	if !strings.Contains(strings.Join(runtime.commandSpecs[0].Args, " "), "agent-compose-runtime exec") {
		t.Fatalf("command exec spec args = %#v", runtime.commandSpecs[0].Args)
	}

	cells, err := manager.store.ListCells(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("cell count = %d, want %d", len(cells), 1)
	}
	cell := cells[0]
	if cell.ID != result.CellID || cell.Type != execution.CellTypeShell || cell.Source != "python3 -V" {
		t.Fatalf("persisted cell = %#v", cell)
	}
	if cell.Running || !cell.Success || cell.Stdout != "command stdout\n" {
		t.Fatalf("persisted cell result = %#v", cell)
	}

	for _, artifact := range []string{"stdout", "stderr", "output", "request", "result", "cellDir"} {
		if strings.TrimSpace(result.Artifacts[artifact]) == "" {
			t.Fatalf("missing artifact %q in %#v", artifact, result.Artifacts)
		}
	}
	stdout, err := os.ReadFile(result.Artifacts["stdout"])
	if err != nil {
		t.Fatalf("ReadFile(stdout artifact) returned error: %v", err)
	}
	if string(stdout) != "command stdout\nfull" {
		t.Fatalf("stdout artifact = %q", string(stdout))
	}
	requestData, err := os.ReadFile(result.Artifacts["request"])
	if err != nil {
		t.Fatalf("ReadFile(request artifact) returned error: %v", err)
	}
	if !strings.Contains(string(requestData), `"command": "python3"`) || !strings.Contains(string(requestData), `"maxOutputBytes": 32`) {
		t.Fatalf("request artifact = %s", string(requestData))
	}
	resultData, err := os.ReadFile(result.Artifacts["result"])
	if err != nil {
		t.Fatalf("ReadFile(result artifact) returned error: %v", err)
	}
	var savedResult RuntimeCommandResult
	if err := json.Unmarshal(resultData, &savedResult); err != nil {
		t.Fatalf("decode result artifact: %v", err)
	}
	wantGuestStdout := "command stdout\n" + fakeGuestCommandResultSentinel
	if savedResult.Stdout != wantGuestStdout || savedResult.Output != "command stdout\n" || !savedResult.Success || savedResult.ExitCode != 0 {
		t.Fatalf("result artifact = %#v", savedResult)
	}
	if !savedResult.StdoutTruncated || !savedResult.OutputTruncated {
		t.Fatalf("result artifact truncation flags = stdout:%t output:%t, want true", savedResult.StdoutTruncated, savedResult.OutputTruncated)
	}
	wantGuestResultPath := filepath.Join("/data/state/cells", result.CellID, "command-result.json")
	if savedResult.Artifacts.Result != wantGuestResultPath {
		t.Fatalf("result artifact guest path = %q, want %q", savedResult.Artifacts.Result, wantGuestResultPath)
	}

	events, err := manager.configDB.ListLoaderEvents(ctx, loader.Summary.ID, 10)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	foundCommandEvent := false
	for _, event := range events {
		if event.Type != "loader.command.completed" {
			continue
		}
		foundCommandEvent = true
		if event.Level != "info" || event.LinkedSessionID != result.SessionID || event.LinkedCellID != result.CellID {
			t.Fatalf("loader command event = %#v", event)
		}
		if !strings.Contains(event.PayloadJSON, `"command":"python3"`) {
			t.Fatalf("loader command event payload = %s", event.PayloadJSON)
		}
	}
	if !foundCommandEvent {
		t.Fatalf("expected loader.command.completed event, got %#v", events)
	}
}

func TestMirrorRuntimeCommandArtifactsDoesNotRewriteCommandResult(t *testing.T) {
	hostCellDir := t.TempDir()
	resultPath := filepath.Join(hostCellDir, "command-result.json")
	sentinelResult := []byte(`{"stdout":"guest-sentinel","success":true}`)
	if err := os.WriteFile(resultPath, sentinelResult, 0o444); err != nil {
		t.Fatalf("write sentinel result artifact: %v", err)
	}

	result := RuntimeCommandResult{
		Stdout: "host stdout",
		Stderr: "host stderr",
		Output: "host output",
	}
	if err := mirrorRuntimeCommandArtifacts(hostCellDir, result); err != nil {
		t.Fatalf("mirrorRuntimeCommandArtifacts returned error: %v", err)
	}

	resultData, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("ReadFile(result artifact) returned error: %v", err)
	}
	if string(resultData) != string(sentinelResult) {
		t.Fatalf("result artifact was rewritten: %s", string(resultData))
	}

	for _, tc := range []struct {
		name string
		want string
	}{
		{name: "stdout.txt", want: "host stdout"},
		{name: "stderr.txt", want: "host stderr"},
		{name: "output.txt", want: "host output"},
	} {
		data, err := os.ReadFile(filepath.Join(hostCellDir, tc.name))
		if err != nil {
			t.Fatalf("ReadFile(%s) returned error: %v", tc.name, err)
		}
		if string(data) != tc.want {
			t.Fatalf("%s = %q, want %q", tc.name, string(data), tc.want)
		}
	}
}

func TestLoaderRunHostCommandPersistsDockerProxyTarget(t *testing.T) {
	ctx := context.Background()
	manager, runtime, driver, loader := newTestLoaderCommandManager(t, ctx)
	manager.config.RuntimeDriver = driverpkg.RuntimeDriverDocker
	manager.config.DockerDefaultImage = "loader-docker:latest"
	driver.startHook = func(_ context.Context, session *Session) error {
		proxyState, err := manager.store.GetProxyState(session.Summary.ID)
		if err != nil {
			return err
		}
		proxyState.GuestHost = session.Summary.RuntimeRef
		proxyState.GuestPort = manager.config.JupyterGuestPort
		proxyState.JupyterURL = "http://127.0.0.1:" + fmt.Sprint(proxyState.HostPort) + "/lab?token=" + proxyState.Token
		return manager.store.SaveProxyState(session.Summary.ID, proxyState)
	}
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-docker-command", LoaderID: loader.Summary.ID},
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Driver:  driverpkg.RuntimeDriverDocker,
		Mode:    "exec",
		Command: "python3",
		Args:    []string{"-V"},
	})
	if err != nil {
		t.Fatalf("Command returned error: %v", err)
	}
	if !result.Success {
		t.Fatalf("Command success = false: %+v", result)
	}
	if len(runtime.commandSpecs) != 1 {
		t.Fatalf("command ExecStream calls = %d, want 1", len(runtime.commandSpecs))
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSessionVM calls = %#v, want one start", driver.startCalls)
	}
	session, err := manager.store.GetSession(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Summary.Driver != driverpkg.RuntimeDriverDocker {
		t.Fatalf("session driver = %q, want docker", session.Summary.Driver)
	}
	proxyState, err := manager.store.GetProxyState(result.SessionID)
	if err != nil {
		t.Fatalf("GetProxyState returned error: %v", err)
	}
	if proxyState.GuestHost != session.Summary.RuntimeRef || proxyState.GuestPort != manager.config.JupyterGuestPort {
		t.Fatalf("proxy target = %s:%d, want %s:%d", proxyState.GuestHost, proxyState.GuestPort, session.Summary.RuntimeRef, manager.config.JupyterGuestPort)
	}
	hostPortHost, hostPort := driverpkg.JupyterConnectTarget(execution.ToDriverProxyState(ProxyState{GuestHost: "127.0.0.1", HostPort: proxyState.HostPort, GuestPort: proxyState.GuestPort}))
	if hostPortHost != "127.0.0.1" || hostPort != proxyState.HostPort {
		t.Fatalf("host-port fallback target = %s:%d, want 127.0.0.1:%d", hostPortHost, hostPort, proxyState.HostPort)
	}
	guestHost, guestPort := driverpkg.JupyterConnectTarget(execution.ToDriverProxyState(proxyState))
	if guestHost != session.Summary.RuntimeRef || guestPort != manager.config.JupyterGuestPort {
		t.Fatalf("jupyterConnectTarget = %s:%d, want %s:%d", guestHost, guestPort, session.Summary.RuntimeRef, manager.config.JupyterGuestPort)
	}
}

func TestLoaderRunHostCommandNonZeroExitCodeDoesNotThrow(t *testing.T) {
	testLoaderRunHostCommandNonZeroExitCodeDoesNotThrow(t)
}

func testLoaderRunHostCommandNonZeroExitCodeDoesNotThrow(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	runtime.commandExitCode = 9
	runtime.commandStdout = ""
	runtime.commandStderr = "command failed\n"
	runtime.commandOutput = "command failed\n"
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command-fail", LoaderID: loader.Summary.ID},
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Mode:   "shell",
		Script: "exit 9",
	})
	if err != nil {
		t.Fatalf("Command returned error for non-zero exit code: %v", err)
	}
	if result.Success || result.ExitCode != 9 || result.Stderr != "command failed\n" {
		t.Fatalf("command result = %#v", result)
	}

	events, err := manager.configDB.ListLoaderEvents(ctx, loader.Summary.ID, 10)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	for _, event := range events {
		if event.Type == "loader.command.completed" {
			if event.Level != "error" {
				t.Fatalf("non-zero command event level = %q, want error", event.Level)
			}
			return
		}
	}
	t.Fatalf("expected loader.command.completed event, got %#v", events)
}

func TestLoaderRunHostCommandRecoversArtifactsWhenCommandPayloadMissing(t *testing.T) {
	testLoaderRunHostCommandRecoversArtifactsWhenCommandPayloadMissing(t)
}

func testLoaderRunHostCommandRecoversArtifactsWhenCommandPayloadMissing(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	runtime.commandNoPayload = true
	runtime.commandStdout = "command stdout\n"
	runtime.commandStderr = "command stderr\n"
	runtime.commandExitCode = 9
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command-payload-missing", LoaderID: loader.Summary.ID},
	}

	result, err := host.Command(ctx, domain.LoaderCommandRequest{
		Mode:    "exec",
		Command: "python3",
		Args:    []string{"-V"},
	})
	if err == nil || !strings.Contains(err.Error(), "decode command result: no result payload found") {
		t.Fatalf("Command error = %v, want payload decode failure", err)
	}
	if result.SessionID == "" || result.CellID == "" {
		t.Fatalf("expected session and cell ids in failure result: %#v", result)
	}
	if !strings.HasSuffix(result.Stdout, "full") || !strings.HasSuffix(result.Stderr, "full") || !strings.HasSuffix(result.Output, "full") {
		t.Fatalf("failure result did not recover artifacts: %#v", result)
	}

	cells, err := manager.store.ListCells(ctx, result.SessionID)
	if err != nil {
		t.Fatalf("ListCells returned error: %v", err)
	}
	if len(cells) != 1 {
		t.Fatalf("cell count = %d, want %d", len(cells), 1)
	}
	cell := cells[0]
	if cell.Running || cell.Success || cell.ExitCode != 9 {
		t.Fatalf("persisted failed cell state = %#v", cell)
	}
	if !strings.HasSuffix(cell.Stdout, "full") || !strings.HasSuffix(cell.Stderr, "full") || !strings.HasSuffix(cell.Output, "full") {
		t.Fatalf("persisted failed cell logs = %#v", cell)
	}
	if _, statErr := os.Stat(result.Artifacts["request"]); statErr != nil {
		t.Fatalf("request artifact missing: %v", statErr)
	}
}

func TestLoaderRunHostCommandPersistsRunningOutputBeforeCompletion(t *testing.T) {
	testLoaderRunHostCommandPersistsRunningOutputBeforeCompletion(t)
}

func testLoaderRunHostCommandPersistsRunningOutputBeforeCompletion(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	runtime.commandStdout = "stream stdout\n"
	runtime.commandStderr = "stream stderr\n"
	runtime.commandBlock = make(chan struct{})
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-command-streaming", LoaderID: loader.Summary.ID},
	}

	resultCh := make(chan domain.LoaderCommandResult, 1)
	errCh := make(chan error, 1)
	go func() {
		result, err := host.Command(ctx, domain.LoaderCommandRequest{
			Mode:    "exec",
			Command: "python3",
			Args:    []string{"-V"},
		})
		if err != nil {
			errCh <- err
			return
		}
		resultCh <- result
	}()

	var runningCell NotebookCell
	var sessionID string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		summaries, err := manager.store.ListSessions(ctx, SessionListOptions{})
		if err == nil && len(summaries.Sessions) == 1 {
			sessionID = summaries.Sessions[0].Summary.ID
			cells, cellErr := manager.store.ListCells(ctx, sessionID)
			if cellErr == nil && len(cells) == 1 {
				runningCell = cells[0]
				if runningCell.Running && strings.Contains(runningCell.Stdout, "stream stdout") && strings.Contains(runningCell.Stderr, "stream stderr") {
					break
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !runningCell.Running || !strings.Contains(runningCell.Stdout, "stream stdout") || !strings.Contains(runningCell.Stderr, "stream stderr") {
		close(runtime.commandBlock)
		t.Fatalf("running loader command cell did not persist streamed output: session=%q cell=%#v", sessionID, runningCell)
	}

	close(runtime.commandBlock)
	select {
	case err := <-errCh:
		t.Fatalf("Command returned error: %v", err)
	case result := <-resultCh:
		if !result.Success || result.Stdout != "stream stdout\n" || result.Stderr != "stream stderr\n" {
			t.Fatalf("Command result = %#v", result)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("timed out waiting for loader command to finish")
	}
}

func TestLoaderRunCommandNewSessionsReuseAndCleanupAtRunEnd(t *testing.T) {
	testLoaderRunCommandNewSessionsReuseAndCleanupAtRunEnd(t)
}

func testLoaderRunCommandNewSessionsReuseAndCleanupAtRunEnd(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	manager, runtime, driver, loader := newTestLoaderCommandManager(t, ctx)
	loader.Script = `function main() {
  const first = scheduler.exec({ command: "python3", args: ["-V"], sessionPolicy: "new" });
  const second = scheduler.shell("echo hello", { sessionPolicy: "new" });
  return { firstSession: first.sessionId, secondSession: second.sessionId };
}`
	loader, err := manager.configDB.UpdateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}

	run, err := manager.runLoader(ctx, loader, nil, `{}`, "manual", false, loaderRunOptions{})
	if err != nil {
		t.Fatalf("runLoader returned error: %v", err)
	}
	if run.Status != domain.LoaderRunStatusSucceeded {
		t.Fatalf("run status = %q, want %q: %s", run.Status, domain.LoaderRunStatusSucceeded, run.Error)
	}
	if len(runtime.commandSpecs) != 2 {
		t.Fatalf("command ExecStream calls = %d, want %d", len(runtime.commandSpecs), 2)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSessionVM calls = %#v, want one start", driver.startCalls)
	}
	if len(driver.stopCalls) != 1 {
		t.Fatalf("StopSessionVM calls = %#v, want one run-end stop", driver.stopCalls)
	}
	if driver.stopCalls[0] != driver.startCalls[0] {
		t.Fatalf("stopped session = %q, want started session %q", driver.stopCalls[0], driver.startCalls[0])
	}
	stoppedSessionID := driver.stopCalls[0]

	session, err := manager.store.GetSession(ctx, stoppedSessionID)
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Summary.VMStatus != domain.VMStatusStopped {
		t.Fatalf("session vm status = %q, want %q", session.Summary.VMStatus, domain.VMStatusStopped)
	}

	events, err := manager.configDB.ListLoaderEvents(ctx, loader.Summary.ID, 20)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	commandCompleted := 0
	foundStopped := false
	for _, event := range events {
		switch event.Type {
		case "loader.command.completed":
			commandCompleted++
			if event.LinkedSessionID != stoppedSessionID {
				t.Fatalf("command event linked session = %q, want %q", event.LinkedSessionID, stoppedSessionID)
			}
		case "loader.session.stopped":
			if event.LinkedSessionID == stoppedSessionID {
				foundStopped = true
			}
		}
	}
	if commandCompleted != 2 {
		t.Fatalf("loader.command.completed count = %d, want 2 in events %#v", commandCompleted, events)
	}
	if !foundStopped {
		t.Fatalf("expected loader.session.stopped for command session, got %#v", events)
	}
}

func TestLoaderRunCommandStickySessionPersistsAcrossRunsWithTitle(t *testing.T) {
	ctx := context.Background()
	manager, runtime, driver, loader := newTestLoaderCommandManager(t, ctx)
	loader.Summary.SessionPolicy = domain.LoaderSessionPolicySticky
	loader.Script = `function main() {
  const result = scheduler.exec({
    command: "python3",
    args: ["-V"],
    sessionPolicy: "reuse",
    title: "Loader Exec Heartbeat"
  });
  return { sessionId: result.sessionId };
}`
	loader, err := manager.configDB.UpdateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}

	firstRun, err := manager.runLoader(ctx, loader, nil, `{}`, "manual", false, loaderRunOptions{})
	if err != nil {
		t.Fatalf("runLoader(first) returned error: %v", err)
	}
	if firstRun.Status != domain.LoaderRunStatusSucceeded {
		t.Fatalf("first run status = %q, want %q: %s", firstRun.Status, domain.LoaderRunStatusSucceeded, firstRun.Error)
	}
	secondRun, err := manager.runLoader(ctx, loader, nil, `{}`, "manual", false, loaderRunOptions{})
	if err != nil {
		t.Fatalf("runLoader(second) returned error: %v", err)
	}
	if secondRun.Status != domain.LoaderRunStatusSucceeded {
		t.Fatalf("second run status = %q, want %q: %s", secondRun.Status, domain.LoaderRunStatusSucceeded, secondRun.Error)
	}
	if len(runtime.commandSpecs) != 2 {
		t.Fatalf("command ExecStream calls = %d, want 2", len(runtime.commandSpecs))
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("StartSessionVM calls = %#v, want one sticky command session start", driver.startCalls)
	}
	if len(driver.stopCalls) != 0 {
		t.Fatalf("StopSessionVM calls = %#v, want no sticky command cleanup", driver.stopCalls)
	}

	session, err := manager.store.GetSession(ctx, driver.startCalls[0])
	if err != nil {
		t.Fatalf("GetSession returned error: %v", err)
	}
	if session.Summary.Title != "Loader Exec Heartbeat" {
		t.Fatalf("session title = %q, want Loader Exec Heartbeat", session.Summary.Title)
	}
	if session.Summary.VMStatus != domain.VMStatusRunning {
		t.Fatalf("session vm status = %q, want %q", session.Summary.VMStatus, domain.VMStatusRunning)
	}
	events, err := manager.configDB.ListLoaderEvents(ctx, loader.Summary.ID, 20)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	stopped := 0
	commandCompleted := 0
	for _, event := range events {
		switch event.Type {
		case "loader.command.completed":
			commandCompleted++
			if event.LinkedSessionID != driver.startCalls[0] {
				t.Fatalf("command event linked session = %q, want %q", event.LinkedSessionID, driver.startCalls[0])
			}
		case "loader.session.stopped":
			stopped++
		}
	}
	if commandCompleted != 2 {
		t.Fatalf("loader.command.completed count = %d, want 2 in events %#v", commandCompleted, events)
	}
	if stopped != 0 {
		t.Fatalf("loader.session.stopped count = %d, want 0 in events %#v", stopped, events)
	}
}

func TestManualLoaderRunTimeoutDoesNotOverrideAgentTimeout(t *testing.T) {
	ctx := context.Background()
	manager, runtime, _, loader := newTestLoaderCommandManager(t, ctx)
	manager.config.AgentTimeout = 2 * time.Second
	loader.Script = `function main() {
  return scheduler.agent("summarize this loader run");
}`
	loader, err := manager.configDB.UpdateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}

	run, err := manager.RunNow(ctx, loader.Summary.ID, "", `{}`, time.Hour)
	if err != nil {
		t.Fatalf("RunNow returned error: %v", err)
	}
	if run.Status != domain.LoaderRunStatusSucceeded {
		t.Fatalf("run status = %q, want %q: %s", run.Status, domain.LoaderRunStatusSucceeded, run.Error)
	}
	if len(runtime.agentDeadlineDurations) != 1 {
		t.Fatalf("agent deadline count = %d, want 1", len(runtime.agentDeadlineDurations))
	}
	if runtime.agentDeadlineDurations[0] > 10*time.Second {
		t.Fatalf("agent deadline = %s, want config timeout near 2s, not manual run timeout", runtime.agentDeadlineDurations[0])
	}
}

func TestLoaderManagerRunLifecycleStateLLMAndEventDispatch(t *testing.T) {
	testLoaderManagerRunLifecycleStateLLMAndEventDispatch(t)
}

func TestLoaderManagerDispatchScheduledRunsAndSessionResumeBranches(t *testing.T) {
	testLoaderManagerDispatchScheduledRunsAndSessionResumeBranches(t)
}

func testLoaderManagerDispatchScheduledRunsAndSessionResumeBranches(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager, _, driver, loader := newTestLoaderCommandManager(t, ctx)
	engine := &scheduledRecordingLoaderEngine{requests: make(chan loaders.LoaderExecutionRequest, 1)}
	manager.engine = engine

	trigger := domain.LoaderTrigger{
		ID:      "scheduled-1",
		Kind:    domain.LoaderTriggerKindInterval,
		Enabled: true,
	}
	manager.dispatchScheduledRuns([]scheduledLoaderRun{{
		loader:      loader,
		trigger:     trigger,
		payloadJSON: `{"scheduled":true}`,
		source:      "interval:scheduled-1",
	}})

	select {
	case request := <-engine.requests:
		if request.Trigger == nil || request.Trigger.ID != trigger.ID || request.PayloadJSON != `{"scheduled":true}` {
			t.Fatalf("scheduled request = %#v", request)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for scheduled loader execution")
	}
	deadline := time.Now().Add(time.Second)
	for {
		runs, err := manager.configDB.ListLoaderRuns(ctx, loader.Summary.ID, 10)
		if err != nil {
			t.Fatalf("ListLoaderRuns returned error: %v", err)
		}
		if len(runs) == 1 && runs[0].Status == domain.LoaderRunStatusSucceeded {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("scheduled runs did not complete: %#v", runs)
		}
		time.Sleep(10 * time.Millisecond)
	}

	stopTimer(nil)
	longTimer := time.NewTimer(time.Hour)
	stopTimer(longTimer)
	expiredTimer := time.NewTimer(time.Nanosecond)
	time.Sleep(time.Millisecond)
	stopTimer(expiredTimer)

	session, err := manager.store.CreateSession(ctx, "Reusable Loader Session", "", driverpkg.RuntimeDriverBoxlite, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("CreateSession returned error: %v", err)
	}
	session.Summary.VMStatus = domain.VMStatusStopped
	if err := manager.store.UpdateSession(ctx, session); err != nil {
		t.Fatalf("UpdateSession stopped returned error: %v", err)
	}
	driver.startCalls = nil
	loaded, eventType, err := manager.loadOrResumeLoaderSession(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("loadOrResumeLoaderSession stopped returned error: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusRunning || eventType != "loader.session.resumed" {
		t.Fatalf("resumed session/event = %#v/%q", loaded.Summary, eventType)
	}
	if len(driver.startCalls) != 1 || driver.startCalls[0] != session.Summary.ID {
		t.Fatalf("driver start calls = %#v", driver.startCalls)
	}
	loaded, eventType, err = manager.loadOrResumeLoaderSession(ctx, session.Summary.ID)
	if err != nil {
		t.Fatalf("loadOrResumeLoaderSession running returned error: %v", err)
	}
	if loaded.Summary.VMStatus != domain.VMStatusRunning || eventType != "" {
		t.Fatalf("running session/event = %#v/%q", loaded.Summary, eventType)
	}
	if len(driver.startCalls) != 1 {
		t.Fatalf("driver start calls after running reuse = %#v", driver.startCalls)
	}
	if _, _, err := manager.loadOrResumeLoaderSession(ctx, "missing-session"); err == nil {
		t.Fatalf("loadOrResumeLoaderSession missing returned nil error")
	}
}

func testLoaderManagerRunLifecycleStateLLMAndEventDispatch(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	manager, _, _, loader := newTestLoaderCommandManager(t, ctx)
	bus := newTestLoaderBus(8)
	manager.bus = bus
	manager.llm = newTestLLMClient(t, manager.configDB, "loader llm text")
	engine := &recordingLoaderEngine{}
	manager.engine = engine
	loader.Summary.ConcurrencyPolicy = domain.LoaderConcurrencyPolicySkip
	loader.Script = "function main() { return { ok: true }; }"
	loader, err := manager.configDB.UpdateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}

	run, err := manager.RunNow(ctx, loader.Summary.ID, "", `{"eventId":"evt-parent","sequence":7,"correlationId":"corr-parent","body":{"value":1}}`, 0)
	if err != nil {
		t.Fatalf("RunNow returned error: %v", err)
	}
	if run.Status != domain.LoaderRunStatusSucceeded || run.ResultJSON != `{"ok":true}` {
		t.Fatalf("run summary = %#v", run)
	}
	if len(engine.requests) != 1 {
		t.Fatalf("loader engine requests = %d, want 1", len(engine.requests))
	}
	if engine.requests[0].Trigger != nil {
		t.Fatalf("engine trigger = %#v, want nil for manual run", engine.requests[0].Trigger)
	}
	if _, err := os.Stat(filepath.Join(run.ArtifactsDir, "payload.json")); err != nil {
		t.Fatalf("payload artifact missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(run.ArtifactsDir, "result.json")); err != nil {
		t.Fatalf("result artifact missing: %v", err)
	}

	if value, ok, err := manager.configDB.GetLoaderState(ctx, loader.Summary.ID, "last"); err != nil || !ok || value != `{"value":1}` {
		t.Fatalf("loader state = %q/%t/%v, want value", value, ok, err)
	}
	if value, ok, err := manager.configDB.GetLoaderState(ctx, loader.Summary.ID, "temporary"); err != nil || ok || value != "" {
		t.Fatalf("temporary loader state = %q/%t/%v, want deleted", value, ok, err)
	}

	events, err := manager.configDB.ListLoaderEvents(ctx, loader.Summary.ID, 20)
	if err != nil {
		t.Fatalf("ListLoaderEvents returned error: %v", err)
	}
	for _, want := range []string{"loader.run.started", "loader.log", "loader.llm.completed", "loader.event.published", "loader.run.completed"} {
		found := false
		for _, event := range events {
			if event.Type == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing loader event %q in %#v", want, events)
		}
	}

	pending, err := manager.configDB.ListPendingEvents(ctx, 10)
	if err != nil {
		t.Fatalf("ListPendingEvents returned error: %v", err)
	}
	if len(pending) != 1 || pending[0].CorrelationID == "" || pending[0].CorrelationID != pending[0].ID || pending[0].ParentEventID != "" {
		t.Fatalf("pending events = %#v", pending)
	}
	dispatcher := NewEventDispatcher(ctx, manager.configDB, bus)
	dispatcher.DispatchOnce(ctx, 10)
	select {
	case event := <-bus.Events():
		if event.Topic != "runtime.test.completed" || event.EventID != pending[0].ID {
			t.Fatalf("dispatched event = %#v", event)
		}
		if err := event.Ack(ctx); err != nil {
			t.Fatalf("event Ack returned error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for dispatched event")
	}
	dispatched, err := manager.configDB.GetEvent(ctx, pending[0].ID)
	if err != nil {
		t.Fatalf("GetEvent returned error: %v", err)
	}
	if dispatched.DispatchStatus != domain.TopicEventDispatchPublishedToBus || dispatched.DispatchedAt.IsZero() {
		t.Fatalf("dispatched event status = %#v", dispatched)
	}

	manager.running[loader.Summary.ID] = 1
	skipped, err := manager.RunNow(ctx, loader.Summary.ID, "", `{}`, 0)
	if err != nil {
		t.Fatalf("RunNow(skip) returned error: %v", err)
	}
	if skipped.Status != domain.LoaderRunStatusSkipped || !strings.Contains(skipped.Error, "already running") {
		t.Fatalf("skipped run = %#v", skipped)
	}
}

func TestLoaderRunHostAgentUsesLoaderDefaultAgentWhenRequestOmitsProvider(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "loader-box:latest",
		GuestWorkspacePath:   "/data/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(session root) returned error: %v", err)
	}

	configDB := newTestConfigStore(t)
	store := &Store{config: config}
	runtime := &fakeLoaderAgentRuntime{}
	driver := &fakeSessionDriver{}
	manager := &LoaderManager{
		config:   config,
		rootCtx:  ctx,
		store:    store,
		configDB: configDB,
		driver:   driver,
		executor: &Executor{config: config, store: store, runtimes: fixedRuntimeProvider{runtime: runtime}},
		running:  map[string]int{},
	}
	loader := createTestLoader(t, ctx, configDB)
	loader.Summary.DefaultAgent = "claude"
	loader, err := configDB.UpdateLoader(ctx, loader)
	if err != nil {
		t.Fatalf("UpdateLoader returned error: %v", err)
	}
	host := &loaderRunHost{
		manager: manager,
		loader:  loader,
		run:     &domain.LoaderRunSummary{ID: "run-agent-default", LoaderID: loader.Summary.ID},
	}

	result, err := host.Agent(ctx, "summarize this loader run", domain.LoaderAgentRequest{Title: "Loader Agent Session"})
	if err != nil {
		t.Fatalf("Agent returned error: %v", err)
	}
	if result.Agent != "claude" {
		t.Fatalf("result agent = %q, want %q", result.Agent, "claude")
	}
	if len(runtime.providers) != 1 || runtime.providers[0] != "claude" {
		t.Fatalf("runtime providers = %#v, want []string{\"claude\"}", runtime.providers)
	}
}

func TestLoaderManagerProjectAgentRunnerCarriesImageBackend(t *testing.T) {
	backend := &fakeImageBackend{}
	manager := &LoaderManager{images: backend}
	runner, ok := manager.projectAgentRunnerComponent().(serviceProjectAgentRunner)
	if !ok {
		t.Fatalf("projectAgentRunner type = %T, want serviceProjectAgentRunner", manager.projectAgentRunner)
	}
	if runner.service.images != backend {
		t.Fatalf("project agent runner images = %#v, want manager image backend", runner.service.images)
	}
}

func newTestLoaderCommandManager(t *testing.T, ctx context.Context) (*LoaderManager, *fakeLoaderAgentRuntime, *fakeSessionDriver, Loader) {
	t.Helper()
	root := t.TempDir()
	config := &appconfig.Config{
		DataRoot:             root,
		SessionRoot:          filepath.Join(root, "sessions"),
		RuntimeDriver:        driverpkg.RuntimeDriverBoxlite,
		DefaultImage:         "loader-box:latest",
		GuestWorkspacePath:   "/data/workspace",
		JupyterGuestPort:     8888,
		JupyterProxyBasePath: "/agent-compose/session",
	}
	if err := os.MkdirAll(config.SessionRoot, 0o755); err != nil {
		t.Fatalf("os.MkdirAll(session root) returned error: %v", err)
	}

	configDB := newTestConfigStore(t)
	store := &Store{config: config}
	runtime := &fakeLoaderAgentRuntime{}
	driver := &fakeSessionDriver{}
	manager := &LoaderManager{
		config:   config,
		rootCtx:  ctx,
		store:    store,
		configDB: configDB,
		driver:   driver,
		executor: &Executor{config: config, store: store, configDB: configDB, runtimes: fixedRuntimeProvider{runtime: runtime}},
		engine:   &loaders.QJSLoaderEngine{},
		running:  map[string]int{},
	}
	loader := createTestLoader(t, ctx, configDB)
	return manager, runtime, driver, loader
}

func newTestConfigStore(t *testing.T) *ConfigStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "data.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open returned error: %v", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &ConfigStore{db: db}
	if err := store.initSchema(context.Background()); err != nil {
		_ = db.Close()
		t.Fatalf("initSchema returned error: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return store
}

func newTestLLMClient(t *testing.T, configDB *ConfigStore, text string) *LLMClient {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"id":"resp-loader","model":"model-a","status":"completed","output_text":%q}`, text)
	}))
	t.Cleanup(server.Close)
	return &LLMClient{
		config:   &appconfig.Config{LLMAPIEndpoint: server.URL, LLMModel: "model-a"},
		configDB: configDB,
		client:   server.Client(),
	}
}

type fixedRuntimeProvider struct {
	runtime BoxRuntime
}

func (p fixedRuntimeProvider) ForDriver(string) (BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

func (p fixedRuntimeProvider) ForSession(*Session) (BoxRuntime, error) {
	if p.runtime == nil {
		return nil, fmt.Errorf("runtime is required")
	}
	return p.runtime, nil
}

type fakeLoaderAgentRuntime struct {
	execCalls              int
	providers              []string
	agentSpecs             []ExecSpec
	agentDeadlineDurations []time.Duration
	agentStdout            string
	agentStderr            string
	agentOutput            string
	agentNoPayload         bool
	agentWaitForContext    bool
	commandSpecs           []ExecSpec
	commandExitCode        int
	commandStdout          string
	commandStderr          string
	commandOutput          string
	commandBlock           chan struct{}
	commandNoPayload       bool
	commandTruncated       bool
	commandStreamHook      func()
	agentExitCode          int
}

func (r *fakeLoaderAgentRuntime) EnsureSession(context.Context, *Session, VMState, ProxyState) (domain.SessionVMInfo, error) {
	return domain.SessionVMInfo{}, nil
}

func (r *fakeLoaderAgentRuntime) StopSession(context.Context, *Session, VMState) (bool, error) {
	return true, nil
}

func (r *fakeLoaderAgentRuntime) Exec(context.Context, *Session, VMState, ExecSpec) (ExecResult, error) {
	return ExecResult{}, fmt.Errorf("unexpected Exec call")
}

func (r *fakeLoaderAgentRuntime) ExecStream(ctx context.Context, session *Session, _ VMState, spec ExecSpec, stream ExecStreamWriter) (ExecResult, error) {
	r.execCalls++
	if isLoaderCommandExecSpec(spec) {
		r.commandSpecs = append(r.commandSpecs, spec)
		stdout := firstNonEmpty(r.commandStdout, "command stdout\n")
		stderr := r.commandStderr
		output := firstNonEmpty(r.commandOutput, stdout+stderr)
		cellID := loaderCommandCellIDFromExecSpec(spec)
		guestCellDir := filepath.Join("/data/state/cells", cellID)
		commandResult := RuntimeCommandResult{
			Stdout:          stdout,
			Stderr:          stderr,
			Output:          output,
			ExitCode:        r.commandExitCode,
			Success:         r.commandExitCode == 0,
			StdoutTruncated: r.commandTruncated,
			StderrTruncated: false,
			OutputTruncated: r.commandTruncated,
			Artifacts: RuntimeCommandArtifacts{
				Stdout:  filepath.Join(guestCellDir, "stdout.txt"),
				Stderr:  filepath.Join(guestCellDir, "stderr.txt"),
				Output:  filepath.Join(guestCellDir, "output.txt"),
				Request: filepath.Join(guestCellDir, "command-request.json"),
				Result:  filepath.Join(guestCellDir, "command-result.json"),
			},
		}
		if (r.commandTruncated || r.commandNoPayload) && session != nil {
			hostCellDir := filepath.Join(execution.HostSessionDir(session), "state", "cells", cellID)
			_ = os.MkdirAll(hostCellDir, 0o755)
			_ = os.WriteFile(filepath.Join(hostCellDir, "stdout.txt"), []byte(stdout+"full"), 0o644)
			_ = os.WriteFile(filepath.Join(hostCellDir, "stderr.txt"), []byte(stderr+"full"), 0o644)
			_ = os.WriteFile(filepath.Join(hostCellDir, "output.txt"), []byte(output+"full"), 0o644)
		}
		if session != nil {
			hostCellDir := filepath.Join(execution.HostSessionDir(session), "state", "cells", cellID)
			if err := os.MkdirAll(hostCellDir, 0o755); err != nil {
				return ExecResult{}, err
			}
			guestResult := commandResult
			guestResult.Stdout = commandResult.Stdout + fakeGuestCommandResultSentinel
			if err := execution.WriteJSONArtifact(filepath.Join(hostCellDir, "command-result.json"), guestResult); err != nil {
				return ExecResult{}, err
			}
		}
		payloadJSON, err := json.Marshal(commandResult)
		if err != nil {
			return ExecResult{}, err
		}
		payload := commandResultPrefix + string(payloadJSON)
		exitCode := r.commandExitCode
		if stream != nil {
			stream(ExecChunk{Text: stdout})
			if stderr != "" {
				stream(ExecChunk{Text: stderr, IsStderr: true})
			}
			if r.commandStreamHook != nil {
				r.commandStreamHook()
			}
		}
		if r.commandBlock != nil {
			<-r.commandBlock
		}
		if r.commandNoPayload {
			return ExecResult{
				Stdout:   stdout,
				Stderr:   stderr,
				Output:   output,
				ExitCode: exitCode,
				Success:  exitCode == 0,
			}, nil
		}
		return ExecResult{
			Stdout:   payload,
			Stderr:   "",
			Output:   output + payload,
			ExitCode: 0,
			Success:  true,
		}, nil
	}
	if spec.Command == "bash" || spec.Command == "node" || spec.Command == "python3" {
		r.commandSpecs = append(r.commandSpecs, spec)
		stdout := firstNonEmpty(r.commandStdout, "cell stdout\n")
		stderr := r.commandStderr
		output := firstNonEmpty(r.commandOutput, stdout+stderr)
		if stream != nil {
			stream(ExecChunk{Text: stdout})
			if stderr != "" {
				stream(ExecChunk{Text: stderr, IsStderr: true})
			}
		}
		exitCode := r.commandExitCode
		return ExecResult{
			Stdout:   stdout,
			Stderr:   stderr,
			Output:   output,
			ExitCode: exitCode,
			Success:  exitCode == 0,
		}, nil
	}
	if deadline, ok := ctx.Deadline(); ok {
		r.agentDeadlineDurations = append(r.agentDeadlineDurations, time.Until(deadline))
	}
	provider := "codex"
	for index, arg := range spec.Args {
		if arg == "--provider" && index+1 < len(spec.Args) {
			provider = strings.Trim(spec.Args[index+1], "'\"")
			break
		}
		marker := "--provider "
		position := strings.Index(arg, marker)
		if position < 0 {
			continue
		}
		remainder := strings.TrimSpace(arg[position+len(marker):])
		if remainder == "" {
			continue
		}
		provider = strings.Fields(remainder)[0]
		provider = strings.Trim(provider, "'\"")
		break
	}
	r.providers = append(r.providers, provider)
	r.agentSpecs = append(r.agentSpecs, spec)
	if stream != nil {
		stream(ExecChunk{Text: "loader agent transcript\n", IsStderr: true})
	}
	exitCode := r.agentExitCode
	if r.agentWaitForContext {
		<-ctx.Done()
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return ExecResult{
			Stdout:   stdout,
			Stderr:   stderr,
			Output:   output,
			ExitCode: execution.FirstNonZeroInt(exitCode, 1),
			Success:  false,
		}, ctx.Err()
	}
	if r.agentNoPayload {
		stdout := r.agentStdout
		stderr := r.agentStderr
		output := firstNonEmpty(r.agentOutput, stdout+stderr)
		return ExecResult{
			Stdout:   stdout,
			Stderr:   stderr,
			Output:   output,
			ExitCode: exitCode,
			Success:  exitCode == 0,
		}, nil
	}
	payload := agentResultPrefix + fmt.Sprintf(`{"provider":%q,"sessionId":"agent-runtime-session","stopReason":"completed","finalText":"loader agent transcript","transcript":"loader agent transcript"}`, provider)
	return ExecResult{
		Stdout:   payload,
		Stderr:   "loader agent transcript\n",
		Output:   "loader agent transcript\n" + payload,
		ExitCode: exitCode,
		Success:  exitCode == 0,
	}, nil
}

func isLoaderCommandExecSpec(spec ExecSpec) bool {
	if spec.Command != "sh" {
		return false
	}
	return strings.Contains(strings.Join(spec.Args, " "), "agent-compose-runtime exec")
}

func loaderCommandCellIDFromExecSpec(spec ExecSpec) string {
	args := strings.Join(spec.Args, " ")
	marker := "/data/state/cells/"
	index := strings.Index(args, marker)
	if index < 0 {
		return ""
	}
	remainder := args[index+len(marker):]
	remainder = strings.Trim(remainder, "'\" ")
	if slash := strings.Index(remainder, "/"); slash >= 0 {
		return remainder[:slash]
	}
	return remainder
}

func createLocalGitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v returned error: %v (%s)", args, err, strings.TrimSpace(string(output)))
		}
	}
	run("init")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("loader workspace\n"), 0o644); err != nil {
		t.Fatalf("os.WriteFile returned error: %v", err)
	}
	run("add", "README.md")
	cmd := exec.Command("git", "-c", "user.name=agent-compose Test", "-c", "user.email=agent-compose.invalid", "commit", "-m", "init")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git commit returned error: %v (%s)", err, strings.TrimSpace(string(output)))
	}
	return dir
}

func createTestLoader(t *testing.T, ctx context.Context, store *ConfigStore) Loader {
	t.Helper()
	loader, err := store.CreateLoader(ctx, Loader{
		Summary: domain.LoaderSummary{
			Name:    "Timer Loader",
			Runtime: domain.LoaderRuntimeScheduler,
			Enabled: true,
		},
		Script: "function main() {}",
	})
	if err != nil {
		t.Fatalf("CreateLoader returned error: %v", err)
	}
	return loader
}

type recordingLoaderEngine struct {
	requests []loaders.LoaderExecutionRequest
}

func (e *recordingLoaderEngine) Validate(context.Context, string, string) (loaders.LoaderValidationResult, error) {
	return loaders.LoaderValidationResult{}, nil
}

func (e *recordingLoaderEngine) Execute(ctx context.Context, request loaders.LoaderExecutionRequest, host loaders.LoaderHost) (loaders.LoaderExecutionResult, error) {
	e.requests = append(e.requests, request)
	if err := host.Log(ctx, "loader lifecycle", map[string]any{"step": "start"}); err != nil {
		return loaders.LoaderExecutionResult{}, err
	}
	if err := host.StateSet(ctx, "last", `{"value":1}`); err != nil {
		return loaders.LoaderExecutionResult{}, err
	}
	if err := host.StateSet(ctx, "temporary", `{"delete":true}`); err != nil {
		return loaders.LoaderExecutionResult{}, err
	}
	if err := host.StateDelete(ctx, "temporary"); err != nil {
		return loaders.LoaderExecutionResult{}, err
	}
	if value, ok, err := host.StateGet(ctx, "last"); err != nil || !ok || value != `{"value":1}` {
		return loaders.LoaderExecutionResult{}, fmt.Errorf("loader state read = %q/%t/%v", value, ok, err)
	}
	if llm, err := host.LLM(ctx, "summarize lifecycle", domain.LoaderLLMRequest{Model: "model-a"}); err != nil || llm.Text != "loader llm text" {
		return loaders.LoaderExecutionResult{}, fmt.Errorf("loader llm result = %#v/%v", llm, err)
	}
	if _, err := host.PublishEvent(ctx, "runtime.test.completed", `{"provider":"test-runtime","value":1}`); err != nil {
		return loaders.LoaderExecutionResult{}, err
	}
	return loaders.LoaderExecutionResult{ResultJSON: `{"ok":true}`}, nil
}

type scheduledRecordingLoaderEngine struct {
	requests chan loaders.LoaderExecutionRequest
}

func (e *scheduledRecordingLoaderEngine) Validate(context.Context, string, string) (loaders.LoaderValidationResult, error) {
	return loaders.LoaderValidationResult{}, nil
}

func (e *scheduledRecordingLoaderEngine) Execute(_ context.Context, request loaders.LoaderExecutionRequest, _ loaders.LoaderHost) (loaders.LoaderExecutionResult, error) {
	e.requests <- request
	return loaders.LoaderExecutionResult{ResultJSON: `{"scheduled":true}`}, nil
}
