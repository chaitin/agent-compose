package app

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/samber/do/v2"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/eventstream"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/runs"
	"github.com/chaitin/agent-compose/pkg/sandboxes"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

// eventStreamBridge adapts daemon domain notifications to the eventstream
// broker. It owns only in-memory attribution caches; sources have already
// persisted the state the event describes.
const maxLogEventDataBytes = 64 << 10

type eventStreamBridge struct {
	broker *eventstream.Broker

	mu              sync.Mutex
	sandboxProjects map[string]string
	sandboxVMStatus map[string]string
}

func NewEventStreamBroker(di do.Injector) (*eventstream.Broker, error) {
	cfg := do.MustInvoke[*appconfig.Config](di).EventStream
	broker := eventstream.NewBroker(eventstream.Config{
		Enabled:          cfg.Enabled,
		MaxSubscribers:   cfg.MaxSubscribers,
		SubscriberBuffer: cfg.SubscriberBuffer,
		ReplayEvents:     cfg.ReplayEvents,
		IdleTimeout:      cfg.IdleTimeout,
		Heartbeat:        cfg.Heartbeat,
		MaxBatchEvents:   cfg.MaxBatchEvents,
	})
	if cfg.Enabled {
		if root, err := do.Invoke[context.Context](di); err == nil && root != nil {
			go func() {
				<-root.Done()
				broker.Close()
			}()
		}
	}
	return broker, nil
}

func NewEventStreamBridge(di do.Injector) (*eventStreamBridge, error) {
	return &eventStreamBridge{
		broker:          do.MustInvoke[*eventstream.Broker](di),
		sandboxProjects: make(map[string]string),
		sandboxVMStatus: make(map[string]string),
	}, nil
}

var (
	_ runs.RunObserver    = (*eventStreamBridge)(nil)
	_ schedulers.Observer = (*eventStreamBridge)(nil)
)

func (b *eventStreamBridge) RunStatusChanged(_ context.Context, before, after domain.ProjectRunRecord) {
	if b == nil || b.broker == nil || !b.broker.Enabled() {
		return
	}
	b.rememberSandbox(after.ProjectID, after.SandboxID)
	b.broker.Publish(&eventstream.Event{
		Type:       eventstream.TypeRunStatus,
		ProjectID:  after.ProjectID,
		Severity:   runSeverity(after.Status),
		OccurredAt: firstTime(after.UpdatedAt, time.Now().UTC()),
		RunStatus: &eventstream.RunStatusEvent{
			Run:            after,
			PreviousStatus: before.Status,
			CleanupError:   after.CleanupError,
			ErrorStack:     after.ErrorStack,
		},
	})
}

func (b *eventStreamBridge) PublishRunLog(event runs.RunLogEvent) {
	if b == nil || b.broker == nil || !b.broker.Enabled() || event.Data == "" {
		return
	}
	b.rememberSandbox(event.ProjectID, event.SandboxID)
	data, truncated := truncateLogData(event.Data)
	b.broker.Publish(&eventstream.Event{
		Type:       eventstream.TypeRunLog,
		ProjectID:  event.ProjectID,
		Severity:   eventstream.SeverityInfo,
		OccurredAt: firstTime(event.CreatedAt, time.Now().UTC()),
		RunLog: &eventstream.RunLogEvent{
			RunID:     event.RunID,
			ProjectID: event.ProjectID,
			SandboxID: event.SandboxID,
			Offset:    event.Offset,
			Stream:    event.Stream,
			Data:      data,
			Format:    "text",
			Truncated: truncated,
		},
	})
}

func (b *eventStreamBridge) SchedulerRunStatusChanged(_ context.Context, previousStatus string, run domain.SchedulerRunSummary, projectID, agentName string) {
	if b == nil || b.broker == nil || !b.broker.Enabled() {
		return
	}
	b.broker.Publish(&eventstream.Event{
		Type:       eventstream.TypeSchedulerRunStatus,
		ProjectID:  projectID,
		Severity:   schedulerRunSeverity(run.Status),
		OccurredAt: firstTime(run.CompletedAt, run.StartedAt, time.Now().UTC()),
		SchedulerRunStatus: &eventstream.SchedulerRunStatusEvent{
			Run:            run,
			PreviousStatus: previousStatus,
			ProjectID:      projectID,
			AgentName:      agentName,
		},
	})
}

func (b *eventStreamBridge) SchedulerEventAdded(_ context.Context, event domain.SchedulerEvent, projectID, agentName string) {
	if b == nil || b.broker == nil || !b.broker.Enabled() {
		return
	}
	b.broker.Publish(&eventstream.Event{
		Type:       eventstream.TypeSchedulerEvent,
		ProjectID:  projectID,
		Severity:   eventstream.ParseSeverity(event.Level),
		OccurredAt: firstTime(event.CreatedAt, time.Now().UTC()),
		SchedulerEvent: &eventstream.SchedulerEvent{
			Event:     event,
			ProjectID: projectID,
			AgentName: agentName,
		},
	})
}

func (b *eventStreamBridge) PublishSandboxWatch(event sandboxes.WatchEvent) {
	if b == nil || b.broker == nil || !b.broker.Enabled() {
		return
	}
	switch event.EventType {
	case sandboxes.WatchEventTypeSandboxUpdated:
		if event.Sandbox == nil {
			return
		}
		projectID := b.sandboxProject(event.SandboxID)
		previous := b.setSandboxVMStatus(event.SandboxID, event.Sandbox.VMStatus)
		b.broker.Publish(&eventstream.Event{
			Type:       eventstream.TypeSandboxStatus,
			ProjectID:  projectID,
			Severity:   sandboxStatusSeverity(event.Sandbox.VMStatus),
			OccurredAt: firstTime(event.Sandbox.UpdatedAt, time.Now().UTC()),
			SandboxStatus: &eventstream.SandboxStatusEvent{
				Summary:          *event.Sandbox,
				PreviousVMStatus: previous,
			},
		})
	case sandboxes.WatchEventTypeEventAdded:
		if event.Event == nil {
			return
		}
		b.broker.Publish(&eventstream.Event{
			Type:       eventstream.TypeSandboxEvent,
			ProjectID:  b.sandboxProject(event.SandboxID),
			Severity:   eventstream.ParseSeverity(event.Event.Level),
			OccurredAt: firstTime(event.Event.CreatedAt, time.Now().UTC()),
			SandboxEvent: &eventstream.SandboxEvent{
				SandboxID: event.SandboxID,
				ProjectID: b.sandboxProject(event.SandboxID),
				Event:     *event.Event,
			},
		})
	case sandboxes.WatchEventTypeCellOutput:
		if strings.TrimSpace(event.Chunk) == "" {
			return
		}
		data, truncated := truncateLogData(event.Chunk)
		b.broker.Publish(&eventstream.Event{
			Type:       eventstream.TypeSandboxCellOutput,
			ProjectID:  b.sandboxProject(event.SandboxID),
			Severity:   eventstream.SeverityInfo,
			OccurredAt: time.Now().UTC(),
			SandboxCellOutput: &eventstream.SandboxCellOutputEvent{
				SandboxID: event.SandboxID,
				ProjectID: b.sandboxProject(event.SandboxID),
				CellID:    event.CellID,
				Stream:    string(event.Stream),
				Data:      data,
				Truncated: truncated,
			},
		})
	}
}

func truncateLogData(data string) (string, bool) {
	if len(data) <= maxLogEventDataBytes {
		return data, false
	}
	return strings.ToValidUTF8(data[:maxLogEventDataBytes], ""), true
}

func (b *eventStreamBridge) rememberSandbox(projectID, sandboxID string) {
	projectID, sandboxID = strings.TrimSpace(projectID), strings.TrimSpace(sandboxID)
	if projectID == "" || sandboxID == "" {
		return
	}
	b.mu.Lock()
	if len(b.sandboxProjects) > 8192 {
		b.sandboxProjects = make(map[string]string)
	}
	b.sandboxProjects[sandboxID] = projectID
	b.mu.Unlock()
}

func (b *eventStreamBridge) sandboxProject(sandboxID string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sandboxProjects[strings.TrimSpace(sandboxID)]
}

func (b *eventStreamBridge) setSandboxVMStatus(sandboxID, status string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	sandboxID = strings.TrimSpace(sandboxID)
	previous := b.sandboxVMStatus[sandboxID]
	if strings.TrimSpace(status) != "" {
		if len(b.sandboxVMStatus) > 8192 {
			b.sandboxVMStatus = make(map[string]string)
		}
		b.sandboxVMStatus[sandboxID] = status
	}
	return previous
}

func runSeverity(status string) eventstream.Severity {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case domain.ProjectRunStatusFailed:
		return eventstream.SeverityError
	case domain.ProjectRunStatusCanceled:
		return eventstream.SeverityWarning
	default:
		return eventstream.SeverityInfo
	}
}

func schedulerRunSeverity(status string) eventstream.Severity {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case domain.SchedulerRunStatusFailed:
		return eventstream.SeverityError
	case domain.SchedulerRunStatusCanceled, domain.SchedulerRunStatusSkipped:
		return eventstream.SeverityWarning
	default:
		return eventstream.SeverityInfo
	}
}

func sandboxStatusSeverity(status string) eventstream.Severity {
	if strings.EqualFold(strings.TrimSpace(status), domain.VMStatusFailed) {
		return eventstream.SeverityError
	}
	return eventstream.SeverityInfo
}

func firstTime(values ...time.Time) time.Time {
	for _, value := range values {
		if !value.IsZero() {
			return value
		}
	}
	return time.Time{}
}
