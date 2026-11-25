package schedulers

import (
	"errors"
	"reflect"
	"testing"
)

func TestSchedulerScanFunctionsAndStoredTimeEdges(t *testing.T) {
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
