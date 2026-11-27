// Package eventstream owns the daemon's opt-in observation-event broker. It
// is a leaf package: sources publish immutable events, the API layer maps them
// to proto, and nothing here imports transport or storage code.
package eventstream

import (
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// Event type names are part of the public API. Add new values only when the
// daemon can actually emit them; clients use them for filtering.
const (
	TypeRunStatus          = "run.status"
	TypeRunEvent           = "run.event"
	TypeRunLog             = "run.log"
	TypeSchedulerRunStatus = "scheduler.run.status"
	TypeSchedulerEvent     = "scheduler.event"
	TypeSchedulerUpdate    = "scheduler.updated"
	TypeSandboxStatus      = "sandbox.status"
	TypeSandboxEvent       = "sandbox.event"
	TypeSandboxCellOutput  = "sandbox.cell.output"
	TypeSandboxRemoved     = "sandbox.removed"
)

// eventTypes is the stable order reported by GetEventStreamCapabilities.
var eventTypes = []string{
	TypeRunStatus,
	TypeRunEvent,
	TypeRunLog,
	TypeSchedulerRunStatus,
	TypeSchedulerEvent,
	TypeSchedulerUpdate,
	TypeSandboxStatus,
	TypeSandboxEvent,
	TypeSandboxCellOutput,
	TypeSandboxRemoved,
}

// EventTypes returns the stable list of event types this daemon can emit.
func EventTypes() []string {
	return append([]string(nil), eventTypes...)
}

// IsKnownType reports whether an exact event type is supported. Prefix
// patterns are validated separately because they may match several types.
func IsKnownType(eventType string) bool {
	for _, known := range eventTypes {
		if eventType == known {
			return true
		}
	}
	return false
}

// IsLogType reports whether an event is high-volume log output. Log events
// never enter the replay ring. An explicit type selects them directly;
// IncludeLogs gates logs matched by a wildcard or an empty type list.
func IsLogType(eventType string) bool {
	return eventType == TypeRunLog || eventType == TypeSandboxCellOutput
}

// Severity classifies an event for filtering. Info is the lowest level.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityError
)

// ParseSeverity maps the daemon's persisted level strings to a severity.
// Unknown or empty levels are informational, matching how scheduler and
// sandbox events are displayed.
func ParseSeverity(level string) Severity {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "error", "fatal":
		return SeverityError
	case "warn", "warning":
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

// Event is one immutable observation. Exactly one payload pointer is set;
// sources build a fresh value and never mutate it after Publish returns.
type Event struct {
	ID         string
	Cursor     string
	Sequence   uint64
	OccurredAt time.Time
	Type       string
	ProjectID  string
	Severity   Severity

	RunStatus          *RunStatusEvent
	RunEvent           *RunEvent
	RunLog             *RunLogEvent
	SchedulerRunStatus *SchedulerRunStatusEvent
	SchedulerEvent     *SchedulerEvent
	SchedulerUpdate    *SchedulerUpdateEvent
	SandboxStatus      *SandboxStatusEvent
	SandboxEvent       *SandboxEvent
	SandboxCellOutput  *SandboxCellOutputEvent
	SandboxRemoved     *SandboxRemovedEvent
}

// RunStatusEvent carries the current run snapshot so a consumer can apply it
// idempotently. PreviousStatus is empty when the source has no prior state.
type RunStatusEvent struct {
	Run            domain.ProjectRunRecord
	PreviousStatus string
	CleanupError   string
	ErrorStack     string
}

// RunEvent is one persisted structured run event (user message, agent
// message, agent activity, or status). It mirrors ListRunEvents so a
// subscriber does not need to poll that API.
type RunEvent struct {
	Event     domain.ProjectRunEventRecord
	ProjectID string
}

// RunLogEvent is one run log chunk. Offset is the end offset of the chunk in
// the run log file and can be passed to FollowRunLogs as start_offset.
type RunLogEvent struct {
	RunID     string
	ProjectID string
	SandboxID string
	Offset    uint64
	Stream    string
	Data      string
	Format    string
	EntryType string
}

// SchedulerRunStatusEvent carries a scheduler run snapshot.
type SchedulerRunStatusEvent struct {
	Run            domain.SchedulerRunSummary
	PreviousStatus string
	ProjectID      string
	AgentName      string
}

// SchedulerEvent carries one persisted scheduler event plus the ownership
// fields the event row does not store.
type SchedulerEvent struct {
	Event     domain.SchedulerEvent
	ProjectID string
	AgentName string
}

// SandboxRemovedEvent is a tombstone published after a sandbox is fully
// removed. Consumers remove the sandbox object instead of leaving it in
// DELETING forever.
type SandboxRemovedEvent struct {
	SandboxID string
	ProjectID string
}

// SchedulerUpdateEvent reports an enable/disable, trigger enable/disable, or
// scheduler configuration change. Consumers can call GetScheduler for details.
type SchedulerUpdateEvent struct {
	SchedulerID    string
	ProjectID      string
	AgentName      string
	Enabled        bool
	TriggerID      string
	TriggerEnabled bool
	Reason         string
}

// SandboxStatusEvent carries a sandbox summary snapshot. Error is best effort;
// detailed failure text usually arrives in a SandboxEvent.
type SandboxStatusEvent struct {
	Summary          domain.SandboxSummary
	PreviousVMStatus string
	Error            string
}

// SandboxEvent carries one persisted sandbox event.
type SandboxEvent struct {
	SandboxID string
	ProjectID string
	Event     domain.SandboxEvent
}

// SandboxCellOutputEvent is one stdout/stderr chunk from a sandbox cell.
type SandboxCellOutputEvent struct {
	SandboxID string
	ProjectID string
	CellID    string
	Stream    string
	Data      string
}
