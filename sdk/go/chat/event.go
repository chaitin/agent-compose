package chat

import (
	"encoding/json"
	"time"
)

// EventKind identifies an [Event] variant without a type switch.
type EventKind string

// Event kinds, mirroring the provider-neutral agent event model every runner
// maps its provider onto.
const (
	KindStepStart      EventKind = "step_start"
	KindStepEnd        EventKind = "step_end"
	KindTextDelta      EventKind = "text_delta"
	KindReasoningDelta EventKind = "reasoning_delta"
	KindToolCall       EventKind = "tool_call"
	KindToolResult     EventKind = "tool_result"
	KindTodo           EventKind = "todo"
	KindUsage          EventKind = "usage"
	KindRetry          EventKind = "retry"
	KindCompaction     EventKind = "compaction"
	KindError          EventKind = "error"
)

// Event is one observation from an agent working on a turn. The set of
// variants is closed; switch on the concrete type to handle them.
//
// A provider that structurally cannot report a kind emits no event of that
// kind at all, and an optional field a provider does not report stays nil.
// Neither is ever filled in with a zero value, so a caller can always tell
// "did not happen" from "this provider never reports it".
type Event interface {
	// Kind reports which variant this is.
	Kind() EventKind
	// At reports when the daemon recorded the event.
	At() time.Time
	isEvent()
}

// ToolKind categorizes what a tool does.
type ToolKind string

// Tool categories.
const (
	ToolRead    ToolKind = "read"
	ToolEdit    ToolKind = "edit"
	ToolDelete  ToolKind = "delete"
	ToolMove    ToolKind = "move"
	ToolSearch  ToolKind = "search"
	ToolExecute ToolKind = "execute"
	ToolThink   ToolKind = "think"
	ToolFetch   ToolKind = "fetch"
	ToolOther   ToolKind = "other"
)

// StopReason explains why a step ended.
type StopReason string

// Stop reasons.
const (
	StopEnd       StopReason = "stop"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopCancelled StopReason = "cancelled"
	StopError     StopReason = "error"
)

// ToolCallStatus is the lifecycle state of a tool call.
type ToolCallStatus string

// Tool call states.
const (
	ToolPending    ToolCallStatus = "pending"
	ToolInProgress ToolCallStatus = "in_progress"
	ToolCompleted  ToolCallStatus = "completed"
	ToolFailed     ToolCallStatus = "failed"
)

// UsageScope is the aggregation level a [UsageEvent] covers.
//
// Providers disagree: some report per turn, some per run, most per step.
// Records of differing scope must never be summed.
type UsageScope string

// Usage aggregation levels.
const (
	ScopeStep UsageScope = "step"
	ScopeTurn UsageScope = "turn"
	ScopeRun  UsageScope = "run"
)

// RetryReason explains why the provider retried.
type RetryReason string

// Retry reasons.
const (
	RetryRateLimit  RetryReason = "rate_limit"
	RetryOverloaded RetryReason = "overloaded"
	RetryNetwork    RetryReason = "network"
	RetryOther      RetryReason = "other"
)

// Severity grades an [ErrorEvent].
type Severity string

// Error severities.
const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
	SeverityFatal   Severity = "fatal"
)

// CompactionPhase marks the boundaries of a context compaction.
type CompactionPhase string

// Compaction phases.
const (
	CompactionStart CompactionPhase = "start"
	CompactionEnd   CompactionPhase = "end"
)

// FileChange is one path a tool call touched.
type FileChange struct {
	Path string `json:"path"`
	Kind string `json:"kind"`
}

// TodoItem is one entry of an agent's plan.
type TodoItem struct {
	Text      string `json:"text"`
	Completed bool   `json:"completed"`
}

type eventAt struct {
	Time time.Time `json:"-"`
}

func (e eventAt) At() time.Time { return e.Time }
func (eventAt) isEvent()        {}

// StepStartEvent reports that the agent began a step.
type StepStartEvent struct {
	eventAt
	Step *int `json:"step,omitempty"`
}

// Kind reports [KindStepStart].
func (*StepStartEvent) Kind() EventKind { return KindStepStart }

// StepEndEvent reports that the agent finished a step.
type StepEndEvent struct {
	eventAt
	Step *int `json:"step,omitempty"`
	// StopReason is empty when the provider does not report one.
	StopReason StopReason `json:"stopReason,omitempty"`
	// RawStopReason preserves the provider's own spelling.
	RawStopReason string `json:"rawStopReason,omitempty"`
}

// Kind reports [KindStepEnd].
func (*StepEndEvent) Kind() EventKind { return KindStepEnd }

// TextDeltaEvent carries a fragment of the agent's answer.
type TextDeltaEvent struct {
	eventAt
	Step       *int   `json:"step,omitempty"`
	BlockIndex *int   `json:"blockIndex,omitempty"`
	Text       string `json:"text"`
}

// Kind reports [KindTextDelta].
func (*TextDeltaEvent) Kind() EventKind { return KindTextDelta }

// ReasoningDeltaEvent carries a fragment of the agent's reasoning. It is not
// part of the answer and is absent for providers that do not expose it.
type ReasoningDeltaEvent struct {
	eventAt
	Step       *int   `json:"step,omitempty"`
	BlockIndex *int   `json:"blockIndex,omitempty"`
	Text       string `json:"text"`
}

// Kind reports [KindReasoningDelta].
func (*ReasoningDeltaEvent) Kind() EventKind { return KindReasoningDelta }

// ToolCallEvent reports a tool the agent invoked. Correlate it with its
// [ToolResultEvent] through ID.
type ToolCallEvent struct {
	eventAt
	Step            *int            `json:"step,omitempty"`
	ParentToolUseID string          `json:"parentToolUseId,omitempty"`
	ID              string          `json:"id"`
	Name            string          `json:"name"`
	ToolKind        ToolKind        `json:"toolKind"`
	Status          ToolCallStatus  `json:"status"`
	Input           json.RawMessage `json:"input,omitempty"`
	// Command is present when ToolKind is [ToolExecute].
	Command  string `json:"command,omitempty"`
	ExitCode *int32 `json:"exitCode,omitempty"`
	// Changes is present when the call applied a patch.
	Changes []FileChange `json:"changes,omitempty"`
}

// Kind reports [KindToolCall].
func (*ToolCallEvent) Kind() EventKind { return KindToolCall }

// ToolResultEvent reports what a tool returned.
type ToolResultEvent struct {
	eventAt
	Step            *int   `json:"step,omitempty"`
	ParentToolUseID string `json:"parentToolUseId,omitempty"`
	ID              string `json:"id"`
	OK              bool   `json:"ok"`
	Output          string `json:"output,omitempty"`
	Error           string `json:"error,omitempty"`
}

// Kind reports [KindToolResult].
func (*ToolResultEvent) Kind() EventKind { return KindToolResult }

// TodoEvent reports the agent's current plan.
type TodoEvent struct {
	eventAt
	Items []TodoItem `json:"items"`
}

// Kind reports [KindTodo].
func (*TodoEvent) Kind() EventKind { return KindTodo }

// UsageEvent reports token accounting.
//
// Sum only records sharing a Scope. InputTokens always excludes cached tokens,
// and OutputTokens always includes reasoning tokens.
type UsageEvent struct {
	eventAt
	Step  *int       `json:"step,omitempty"`
	Scope UsageScope `json:"scope"`
	Model string     `json:"model,omitempty"`
	// InputTokens always excludes cached tokens.
	InputTokens int64 `json:"inputTokens"`
	// OutputTokens includes reasoning tokens.
	OutputTokens     int64    `json:"outputTokens"`
	ReasoningTokens  *int64   `json:"reasoningTokens,omitempty"`
	CachedTokens     *int64   `json:"cachedTokens,omitempty"`
	CacheWriteTokens *int64   `json:"cacheWriteTokens,omitempty"`
	CostUSD          *float64 `json:"costUsd,omitempty"`
}

// Kind reports [KindUsage].
func (*UsageEvent) Kind() EventKind { return KindUsage }

// RetryEvent reports that the provider retried a request.
type RetryEvent struct {
	eventAt
	Reason      RetryReason `json:"reason"`
	Attempt     int         `json:"attempt"`
	MaxAttempts *int        `json:"maxAttempts,omitempty"`
	Message     string      `json:"message,omitempty"`
}

// Kind reports [KindRetry].
func (*RetryEvent) Kind() EventKind { return KindRetry }

// CompactionEvent marks the agent compacting its context.
type CompactionEvent struct {
	eventAt
	Phase CompactionPhase `json:"phase"`
}

// Kind reports [KindCompaction].
func (*CompactionEvent) Kind() EventKind { return KindCompaction }

// ErrorEvent reports a problem the agent hit. A [SeverityFatal] event ends the
// turn; the others do not.
type ErrorEvent struct {
	eventAt
	Severity  Severity `json:"severity"`
	Code      string   `json:"code,omitempty"`
	Retryable *bool    `json:"retryable,omitempty"`
	Message   string   `json:"message"`
}

// Kind reports [KindError].
func (*ErrorEvent) Kind() EventKind { return KindError }

// decodeEvent maps one agent event frame onto its variant. An unrecognized
// kind yields a nil Event and no error: the daemon may add kinds this build
// does not know, and dropping them is preferable to failing the turn.
func decodeEvent(name string, payload []byte, at time.Time) (Event, error) {
	if len(payload) == 0 {
		payload = []byte("{}")
	}
	var target Event
	switch EventKind(name) {
	case KindStepStart:
		target = &StepStartEvent{}
	case KindStepEnd:
		target = &StepEndEvent{}
	case KindTextDelta:
		target = &TextDeltaEvent{}
	case KindReasoningDelta:
		target = &ReasoningDeltaEvent{}
	case KindToolCall:
		target = &ToolCallEvent{}
	case KindToolResult:
		target = &ToolResultEvent{}
	case KindTodo:
		target = &TodoEvent{}
	case KindUsage:
		target = &UsageEvent{}
	case KindRetry:
		target = &RetryEvent{}
	case KindCompaction:
		target = &CompactionEvent{}
	case KindError:
		target = &ErrorEvent{}
	default:
		return nil, nil
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return nil, err
	}
	setEventTime(target, at)
	return target, nil
}

func setEventTime(event Event, at time.Time) {
	switch typed := event.(type) {
	case *StepStartEvent:
		typed.Time = at
	case *StepEndEvent:
		typed.Time = at
	case *TextDeltaEvent:
		typed.Time = at
	case *ReasoningDeltaEvent:
		typed.Time = at
	case *ToolCallEvent:
		typed.Time = at
	case *ToolResultEvent:
		typed.Time = at
	case *TodoEvent:
		typed.Time = at
	case *UsageEvent:
		typed.Time = at
	case *RetryEvent:
		typed.Time = at
	case *CompactionEvent:
		typed.Time = at
	case *ErrorEvent:
		typed.Time = at
	}
}
