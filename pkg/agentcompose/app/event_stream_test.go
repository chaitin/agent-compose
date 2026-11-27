package app

import (
	"context"
	"testing"
	"time"

	"github.com/chaitin/agent-compose/pkg/eventstream"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

func testEventStreamBridge(t *testing.T) (*eventStreamBridge, *eventstream.Subscription) {
	t.Helper()
	cfg := eventstream.DefaultConfig()
	cfg.Enabled = true
	cfg.Heartbeat = time.Hour
	broker := eventstream.NewBroker(cfg)
	t.Cleanup(broker.Close)
	bridge := &eventStreamBridge{
		broker:          broker,
		sandboxProjects: make(map[string]string),
		sandboxVMStatus: make(map[string]string),
	}
	filter, err := eventstream.NewFilter(nil, nil, eventstream.SeverityInfo, true)
	if err != nil {
		t.Fatalf("NewFilter: %v", err)
	}
	sub, _, err := broker.Subscribe(filter, "")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	t.Cleanup(sub.Close)
	return bridge, sub
}

func receiveBridgeEvent(t *testing.T, sub *eventstream.Subscription) *eventstream.Event {
	t.Helper()
	select {
	case event, ok := <-sub.Events():
		if !ok {
			t.Fatal("subscription closed")
		}
		return event
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
		return nil
	}
}

func TestEventStreamBridgeRunStatusAndLog(t *testing.T) {
	bridge, sub := testEventStreamBridge(t)
	occurred := time.Unix(1700000000, 0).UTC()
	before := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "p1", SandboxID: "s1", Status: domain.ProjectRunStatusRunning}
	after := before
	after.Status = domain.ProjectRunStatusFailed
	after.Error = "boom"
	after.ErrorStack = "stack"
	after.UpdatedAt = occurred
	bridge.RunStatusChanged(context.Background(), before, after)

	event := receiveBridgeEvent(t, sub)
	if event.Type != eventstream.TypeRunStatus || event.ProjectID != "p1" || event.Severity != eventstream.SeverityError {
		t.Fatalf("run status event = %+v", event)
	}
	if event.RunStatus == nil || event.RunStatus.Run.RunID != "run-1" ||
		event.RunStatus.PreviousStatus != domain.ProjectRunStatusRunning || event.RunStatus.ErrorStack != "stack" {
		t.Fatalf("run status payload = %+v", event.RunStatus)
	}
	if got := bridge.sandboxProject("s1"); got != "p1" {
		t.Fatalf("cached sandbox project = %q", got)
	}

	bridge.PublishRunLog(runs.RunLogEvent{
		RunID: "run-1", ProjectID: "p1", SandboxID: "s1", Stream: "stdout",
		Data: "hello\n", Offset: 6, CreatedAt: occurred,
	})
	logEvent := receiveBridgeEvent(t, sub)
	if logEvent.Type != eventstream.TypeRunLog || logEvent.RunLog == nil || logEvent.RunLog.Data != "hello\n" ||
		logEvent.RunLog.Offset != 6 || logEvent.RunLog.Format != "text" {
		t.Fatalf("run log event = %+v", logEvent)
	}
}

func TestEventStreamBridgeSchedulerEvents(t *testing.T) {
	bridge, sub := testEventStreamBridge(t)
	created := time.Unix(1700000001, 0).UTC()
	bridge.SchedulerEventAdded(context.Background(), domain.SchedulerEvent{
		ID: "event-1", SchedulerID: "sch-1", Type: "scheduler.log", Level: "warn", Message: "careful", CreatedAt: created,
	}, "p1", "worker")
	event := receiveBridgeEvent(t, sub)
	if event.Type != eventstream.TypeSchedulerEvent || event.ProjectID != "p1" || event.Severity != eventstream.SeverityWarning {
		t.Fatalf("scheduler event = %+v", event)
	}
	if event.SchedulerEvent == nil || event.SchedulerEvent.Event.ID != "event-1" || event.SchedulerEvent.AgentName != "worker" {
		t.Fatalf("scheduler payload = %+v", event.SchedulerEvent)
	}

	bridge.SchedulerRunStatusChanged(context.Background(), domain.SchedulerRunStatusRunning, domain.SchedulerRunSummary{
		ID: "srun-1", SchedulerID: "sch-1", Status: domain.SchedulerRunStatusFailed, Error: "boom",
		StartedAt: created, CompletedAt: created.Add(time.Second),
	}, "p1", "worker")
	runEvent := receiveBridgeEvent(t, sub)
	if runEvent.Type != eventstream.TypeSchedulerRunStatus || runEvent.Severity != eventstream.SeverityError ||
		runEvent.SchedulerRunStatus == nil || runEvent.SchedulerRunStatus.PreviousStatus != domain.SchedulerRunStatusRunning {
		t.Fatalf("scheduler run event = %+v", runEvent)
	}
}

func TestEventStreamBridgeSandboxAttributionAndCellOutput(t *testing.T) {
	bridge, sub := testEventStreamBridge(t)
	// Run status populates the sandbox-to-project cache used by sandbox events.
	bridge.RunStatusChanged(context.Background(), domain.ProjectRunRecord{}, domain.ProjectRunRecord{
		RunID: "run-1", ProjectID: "p1", SandboxID: "s1", Status: domain.ProjectRunStatusRunning,
	})
	receiveBridgeEvent(t, sub)

	summary := domain.SandboxSummary{ID: "s1", Title: "worker", Driver: "docker", VMStatus: domain.VMStatusRunning}
	bridge.PublishSandboxWatch(sandboxes.WatchEvent{
		SandboxID: "s1", EventType: sandboxes.WatchEventTypeSandboxUpdated, Sandbox: &summary,
	})
	statusEvent := receiveBridgeEvent(t, sub)
	if statusEvent.Type != eventstream.TypeSandboxStatus || statusEvent.ProjectID != "p1" ||
		statusEvent.SandboxStatus == nil || statusEvent.SandboxStatus.Summary.ID != "s1" {
		t.Fatalf("sandbox status event = %+v", statusEvent)
	}

	summary.VMStatus = domain.VMStatusFailed
	bridge.PublishSandboxWatch(sandboxes.WatchEvent{
		SandboxID: "s1", EventType: sandboxes.WatchEventTypeSandboxUpdated, Sandbox: &summary,
	})
	failedEvent := receiveBridgeEvent(t, sub)
	if failedEvent.Severity != eventstream.SeverityError || failedEvent.SandboxStatus.PreviousVMStatus != domain.VMStatusRunning {
		t.Fatalf("sandbox failure event = %+v", failedEvent)
	}

	bridge.PublishSandboxWatch(sandboxes.WatchEvent{
		SandboxID: "s1", EventType: sandboxes.WatchEventTypeEventAdded,
		Event: &domain.SandboxEvent{ID: "se-1", Type: "sandbox.test", Level: "info", Message: "hello"},
	})
	eventAdded := receiveBridgeEvent(t, sub)
	if eventAdded.Type != eventstream.TypeSandboxEvent || eventAdded.SandboxEvent == nil ||
		eventAdded.SandboxEvent.Event.ID != "se-1" || eventAdded.ProjectID != "p1" {
		t.Fatalf("sandbox event = %+v", eventAdded)
	}

	bridge.PublishSandboxWatch(sandboxes.WatchEvent{
		SandboxID: "s1", EventType: sandboxes.WatchEventTypeCellOutput, CellID: "cell-1",
		Chunk: "output", Stream: domain.StdioStdout,
	})
	cellEvent := receiveBridgeEvent(t, sub)
	if cellEvent.Type != eventstream.TypeSandboxCellOutput || cellEvent.ProjectID != "p1" ||
		cellEvent.SandboxCellOutput == nil || cellEvent.SandboxCellOutput.CellID != "cell-1" ||
		cellEvent.SandboxCellOutput.Data != "output" {
		t.Fatalf("cell output event = %+v", cellEvent)
	}
}

func TestEventStreamBridgeAttributionsFromSandboxTags(t *testing.T) {
	bridge, sub := testEventStreamBridge(t)
	summary := domain.SandboxSummary{
		ID: "s2", VMStatus: domain.VMStatusRunning,
		Tags: []domain.SandboxTag{{Name: "project", Value: "p2"}, {Name: "agent", Value: "worker"}},
	}
	bridge.PublishSandboxWatch(sandboxes.WatchEvent{
		SandboxID: "s2", EventType: sandboxes.WatchEventTypeSandboxUpdated, Sandbox: &summary,
	})
	event := receiveBridgeEvent(t, sub)
	if event.ProjectID != "p2" {
		t.Fatalf("sandbox event project = %q, want p2", event.ProjectID)
	}
}

func TestEventStreamBridgeStructuredRunAndSchedulerUpdates(t *testing.T) {
	bridge, sub := testEventStreamBridge(t)
	run := domain.ProjectRunRecord{RunID: "run-1", ProjectID: "p1", SandboxID: "s1"}
	bridge.RunEventsAppended(context.Background(), run, []domain.ProjectRunEventRecord{{
		ID: "event-1", RunID: "run-1", Sequence: 3, Kind: domain.ProjectRunEventKindAgentMessage, Text: "done",
	}})
	runEvent := receiveBridgeEvent(t, sub)
	if runEvent.Type != eventstream.TypeRunEvent || runEvent.ProjectID != "p1" ||
		runEvent.RunEvent == nil || runEvent.RunEvent.Event.Sequence != 3 || runEvent.RunEvent.Event.Text != "done" {
		t.Fatalf("structured run event = %+v", runEvent)
	}

	scheduler := domain.Scheduler{Summary: domain.SchedulerSummary{
		ID: "scheduler-1", ProjectID: "p1", AgentName: "worker", Enabled: false,
	}}
	bridge.SchedulerUpdated(context.Background(), scheduler, schedulers.SchedulerChange{Reason: schedulers.SchedulerChangeDisabled})
	schedulerEvent := receiveBridgeEvent(t, sub)
	if schedulerEvent.Type != eventstream.TypeSchedulerUpdate || schedulerEvent.SchedulerUpdate == nil ||
		schedulerEvent.SchedulerUpdate.SchedulerID != "scheduler-1" || schedulerEvent.SchedulerUpdate.Reason != schedulers.SchedulerChangeDisabled ||
		schedulerEvent.SchedulerUpdate.Enabled {
		t.Fatalf("scheduler update = %+v", schedulerEvent)
	}
}
