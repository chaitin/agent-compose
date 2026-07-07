package loaders

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestLoaderScanFunctionsAndStoredTimeEdges(t *testing.T) {
	at := time.Date(2026, 7, 1, 2, 3, 4, 0, time.UTC)
	summary, err := ScanLoaderSummary(assignScanValues(
		"loader-1", "Loader", "desc", "scheduler", "workspace-1", "agent-1", "docker", "guest", "codex", "reuse", "wait", `["cap-a"]`,
		" project-1 ", int64(7), " agent-a ", " scheduler-1 ", 1, "", at.Format(time.RFC3339), []byte("1700000000"), 2, 3, 4, "2026-07-01T02:03:04.000Z",
	))
	if err != nil {
		t.Fatalf("ScanLoaderSummary returned error: %v", err)
	}
	if summary.ID != "loader-1" || !summary.Enabled || summary.ManagedProjectID != "project-1" || summary.ManagedAgentName != "agent-a" || summary.TriggerCount != 2 || summary.CreatedAt.IsZero() || summary.UpdatedAt.IsZero() || summary.LatestRunAt.IsZero() {
		t.Fatalf("summary = %#v", summary)
	}

	loader, err := ScanLoader(assignScanValues(
		"loader-1", "Loader", "desc", "scheduler", "script", "workspace-1", "agent-1", "docker", "guest", "codex", "reuse", "wait", `["cap-a"]`, `[]`, `[]`,
		" project-1 ", int64(7), " agent-a ", " scheduler-1 ", 1, "", int64(1700000000), float64(1700000000000),
	))
	if err != nil {
		t.Fatalf("ScanLoader returned error: %v", err)
	}
	if loader.Summary.ID != "loader-1" || !loader.Summary.Enabled || loader.Summary.UpdatedAt.IsZero() {
		t.Fatalf("loader = %#v", loader)
	}
	if _, err := ScanLoader(assignScanValues(
		"loader-1", "Loader", "desc", "scheduler", "script", "", "", "", "", "", "", "", "", `{bad`, `[]`,
		"", int64(0), "", "", 1, "", nil, nil,
	)); err == nil {
		t.Fatal("ScanLoader returned nil for invalid env JSON")
	}

	trigger, err := ScanLoaderTrigger(assignScanValues("loader-1", "trigger-1", "interval", "topic", int64(1000), 1, 1, `{}`, "1700000000", []byte("2026-07-01T02:03:04Z")))
	if err != nil {
		t.Fatalf("ScanLoaderTrigger returned error: %v", err)
	}
	if trigger.ID != "trigger-1" || !trigger.Enabled || !trigger.AutoID || trigger.NextFireAt.IsZero() || trigger.LastFiredAt.IsZero() {
		t.Fatalf("trigger = %#v", trigger)
	}
	run, err := ScanLoaderRun(assignScanValues("loader-1", "run-1", "trigger-1", "interval", "manual", "succeeded", int(1700000000), "2026-07-01T02:03:04Z", int64(10), "", `{}`, `{}`, "hash", "/tmp/artifacts"))
	if err != nil {
		t.Fatalf("ScanLoaderRun returned error: %v", err)
	}
	if run.ID != "run-1" || run.StartedAt.IsZero() || run.CompletedAt.IsZero() {
		t.Fatalf("run = %#v", run)
	}
	event, err := ScanLoaderEvent(assignScanValues("loader-1", "event-1", "run-1", "trigger-1", "type", "info", "message", `{}`, "session-1", "cell-1", "agent-session", []byte("1700000000")))
	if err != nil {
		t.Fatalf("ScanLoaderEvent returned error: %v", err)
	}
	if event.ID != "event-1" || event.CreatedAt.IsZero() {
		t.Fatalf("event = %#v", event)
	}
	binding, err := ScanLoaderBinding(assignScanValues("loader-1", "session-1", "2026-07-01T02:03:04.000Z", nil))
	if err != nil {
		t.Fatalf("ScanLoaderBinding returned error: %v", err)
	}
	if binding.LoaderID != "loader-1" || binding.CreatedAt.IsZero() || !binding.UpdatedAt.IsZero() {
		t.Fatalf("binding = %#v", binding)
	}

	if _, err := ScanLoaderSummary(func(dest ...any) error { return errors.New("scan failed") }); err == nil {
		t.Fatal("ScanLoaderSummary returned nil for scan error")
	}
	if got := parseStoredLoaderTriggerTime(struct{}{}); !got.IsZero() {
		t.Fatalf("default trigger time = %v, want zero", got)
	}
	if got := parseStoredLoaderTriggerTime("not-time"); !got.IsZero() {
		t.Fatalf("invalid trigger time = %v, want zero", got)
	}
	if got := parseStoredTime("not-time"); !got.IsZero() {
		t.Fatalf("invalid stored time = %v, want zero", got)
	}
	if got := parseStoredUnixTimeAuto(0); !got.IsZero() {
		t.Fatalf("zero unix time = %v, want zero", got)
	}
}

func assignScanValues(values ...any) func(dest ...any) error {
	return func(dest ...any) error {
		if len(dest) != len(values) {
			return errors.New("scan destination count mismatch")
		}
		for i := range dest {
			target := reflect.ValueOf(dest[i]).Elem()
			if values[i] == nil {
				target.Set(reflect.Zero(target.Type()))
				continue
			}
			target.Set(reflect.ValueOf(values[i]))
		}
		return nil
	}
}
