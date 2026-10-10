package adapters

import (
	"bytes"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/chaitin/agent-compose/pkg/driver"
)

func captureDefaultSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestReportMeasuredLowerLayerIsolationFactsLogsObservedFailure(t *testing.T) {
	logs := captureDefaultSlog(t)

	// An empty fact set is the common case and must not log anything.
	reportMeasuredLowerLayerIsolationFacts("sandbox-1", "docker", driver.ExecSecurityFacts{})
	if logs.Len() != 0 {
		t.Fatalf("logs = %q, want nothing for an empty fact set", logs.String())
	}

	reportMeasuredLowerLayerIsolationFacts("sandbox-1", "docker", driver.ExecSecurityFacts{SeccompUnavailable: 2})
	message := logs.String()
	for _, want := range []string{"process.seccomp", "lower_layer_unavailable", "sandbox_id=sandbox-1", "driver=docker", "2 time(s)"} {
		if !strings.Contains(message, want) {
			t.Fatalf("logs = %q, want %q", message, want)
		}
	}
}

// TestTrackedRuntimeInteractionReportsLowerLayerFactsOnce pins the production
// wiring: the tracked wrapper is the completion point of every interactive run,
// and a repeated Wait must not duplicate the report.
func TestTrackedRuntimeInteractionReportsLowerLayerFactsOnce(t *testing.T) {
	logs := captureDefaultSlog(t)
	interaction := &trackedRuntimeInteraction{
		RuntimeInteraction: staticRuntimeInteraction{facts: driver.ExecSecurityFacts{NoNewPrivilegesUnavailable: 1}},
		finish:             func() {},
		done:               make(chan struct{}),
		sandboxID:          "sandbox-7",
		driverName:         "microsandbox",
	}
	if _, err := interaction.Wait(); err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if _, err := interaction.Wait(); err != nil {
		t.Fatalf("second Wait() error = %v", err)
	}
	message := logs.String()
	if !strings.Contains(message, "process.no_new_privs") || !strings.Contains(message, "sandbox_id=sandbox-7") {
		t.Fatalf("logs = %q, want the reported no_new_privileges fact", message)
	}
	if got := strings.Count(message, "sandbox lower layer reported an isolation failure"); got != 1 {
		t.Fatalf("report count = %d, want 1 even after two Wait calls", got)
	}
}

type staticRuntimeInteraction struct {
	facts driver.ExecSecurityFacts
}

func (staticRuntimeInteraction) Send(driver.RuntimeInputFrame) error { return nil }
func (staticRuntimeInteraction) CloseSend() error                    { return nil }
func (staticRuntimeInteraction) Recv() (driver.RuntimeOutputFrame, error) {
	return driver.RuntimeOutputFrame{}, io.EOF
}
func (i staticRuntimeInteraction) Wait() (driver.RuntimeResult, error) {
	return driver.RuntimeResult{SecurityFacts: i.facts.Pointer()}, nil
}
