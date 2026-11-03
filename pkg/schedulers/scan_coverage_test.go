package schedulers

import (
	"errors"
	"reflect"
	"testing"
)

func TestSchedulerScanFunctionsAndStoredTimeEdges(t *testing.T) {
	trigger, err := ScanSchedulerTrigger(assignScanValues("scheduler-1", "trigger-1", "interval", "topic", int64(1000), 1, 1, `{}`, "1700000000", []byte("2026-07-01T02:03:04Z")))
	if err != nil {
		t.Fatalf("ScanSchedulerTrigger returned error: %v", err)
	}
	if trigger.ID != "trigger-1" || !trigger.Enabled || !trigger.AutoID || trigger.NextFireAt.IsZero() || trigger.LastFiredAt.IsZero() {
		t.Fatalf("trigger = %#v", trigger)
	}
	run, err := ScanSchedulerRun(assignScanValues("scheduler-1", "run-1", "trigger-1", "interval", "manual", "succeeded", int(1700000000), "2026-07-01T02:03:04Z", int64(10), "", `{}`, `{}`, "hash", "/tmp/artifacts"))
	if err != nil {
		t.Fatalf("ScanSchedulerRun returned error: %v", err)
	}
	if run.ID != "run-1" || run.StartedAt.IsZero() || run.CompletedAt.IsZero() {
		t.Fatalf("run = %#v", run)
	}
	event, err := ScanSchedulerEvent(assignScanValues("scheduler-1", "event-1", "run-1", "trigger-1", "type", "info", "message", `{}`, "session-1", "cell-1", "agent-session", []byte("1700000000")))
	if err != nil {
		t.Fatalf("ScanSchedulerEvent returned error: %v", err)
	}
	if event.ID != "event-1" || event.CreatedAt.IsZero() {
		t.Fatalf("event = %#v", event)
	}
	binding, err := ScanSchedulerBinding(assignScanValues("scheduler-1", "trigger-1", "session-1", "sha256:config", "sha256:base", "2026-07-01T02:03:04.000Z", nil))
	if err != nil {
		t.Fatalf("ScanSchedulerBinding returned error: %v", err)
	}
	if binding.SchedulerID != "scheduler-1" || binding.SandboxConfigHash != "sha256:config" || binding.SchedulerConfigHash != "sha256:base" || binding.CreatedAt.IsZero() || !binding.UpdatedAt.IsZero() {
		t.Fatalf("binding = %#v", binding)
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
