package schedulers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/chaitin/agent-compose/pkg/events"
	"github.com/chaitin/agent-compose/pkg/identity"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

type RunStore interface {
	CreateSchedulerRun(ctx context.Context, run domain.SchedulerRunSummary) error
	UpdateSchedulerRun(ctx context.Context, run domain.SchedulerRunSummary) error
	UpdateSchedulerLastError(ctx context.Context, schedulerID, lastError string) error
}

type RunHost interface {
	SchedulerHost
	CleanupCommandSessions(ctx context.Context)
}

type ExecutionKind string

const (
	ExecutionKindTrigger    ExecutionKind = "trigger"
	ExecutionKindInvocation ExecutionKind = "invocation"
)

type RuntimeExecutionContext struct {
	ID        string
	TriggerID string
	Kind      ExecutionKind
}

type RunHostFactory func(scheduler domain.Scheduler, execution RuntimeExecutionContext, triggerEvent TriggerEventMetadata) RunHost

type RunOptions struct {
	RetryWhenBusy  bool
	AlreadyEntered bool
}

// RunTriggerRequest bundles the scheduler, trigger, payload, source, and
// options identifying which run to prepare/execute, shared by Run and
// Prepare on both Controller and RunExecutor.
type RunTriggerRequest struct {
	Scheduler   domain.Scheduler
	Trigger     *domain.SchedulerTrigger
	PayloadJSON string
	Source      string
	Options     RunOptions
	// StartedByRequest marks a run started by an API request. It acts as that
	// request's trusted headers, read from the context, even when there are
	// none. Other runs (cron and event triggers) act as the Project's last
	// applier.
	StartedByRequest bool
}

type PreparedRun struct {
	Scheduler   domain.Scheduler
	Trigger     *domain.SchedulerTrigger
	Run         domain.SchedulerRunSummary
	PayloadJSON string
	// TrustedHeaders is the identity the run acts as: the headers of the
	// request that started it, or for runs without one (cron and event
	// triggers) the headers recorded by the Project's last apply.
	TrustedHeaders []domain.TrustedHeader
}

type RunExecutorDependencies struct {
	Store                      RunStore
	Engine                     SchedulerEngine
	HostFactory                RunHostFactory
	ArtifactsDir               func(schedulerID, runID string) string
	WriteArtifact              func(dir, name, content string) error
	EnterRun                   func(scheduler domain.Scheduler) bool
	LeaveRun                   func(schedulerID string)
	AddSchedulerEvent          func(ctx context.Context, event SchedulerEventInput) error
	UpdateTriggerEventDelivery func(ctx context.Context, run domain.SchedulerRunSummary)
	Notify                     func(reason string)
	Refresh                    func(ctx context.Context) error
	// ProjectApplyTrustedHeaders returns the trusted headers recorded by the
	// last apply of a Project. Nil leaves runs without a request of their own
	// without trusted headers.
	ProjectApplyTrustedHeaders func(ctx context.Context, projectID string) ([]domain.TrustedHeader, error)
}

type RunExecutor struct {
	deps RunExecutorDependencies
}

var ErrRunBusyForRetry = errors.New("scheduler is already running")

func NewRunExecutor(deps RunExecutorDependencies) *RunExecutor {
	return &RunExecutor{deps: deps}
}

func (e *RunExecutor) Run(ctx context.Context, req RunTriggerRequest, triggerEventAck ...func(context.Context) error) (domain.SchedulerRunSummary, error) {
	prepared, err := e.Prepare(ctx, req)
	if err != nil {
		return domain.SchedulerRunSummary{}, err
	}
	if len(triggerEventAck) > 0 && triggerEventAck[0] != nil {
		if err := triggerEventAck[0](ctx); err != nil {
			slog.Warn("failed to mark scheduler topic event published", "topic", req.Source, "error", err)
		}
	}
	return e.Execute(ctx, prepared)
}

func (e *RunExecutor) Prepare(ctx context.Context, req RunTriggerRequest) (PreparedRun, error) {
	scheduler, trigger, source, options := req.Scheduler, req.Trigger, req.Source, req.Options
	payloadJSON, err := domain.NormalizeJSONDocument(req.PayloadJSON)
	if err != nil {
		if options.AlreadyEntered {
			e.leaveRun(scheduler.Summary.ID)
		}
		return PreparedRun{}, err
	}
	trustedHeaders, err := e.runTrustedHeaders(ctx, scheduler, req.StartedByRequest)
	if err != nil {
		if options.AlreadyEntered {
			e.leaveRun(scheduler.Summary.ID)
		}
		return PreparedRun{}, err
	}
	now := time.Now().UTC()
	run := domain.SchedulerRunSummary{
		ID:               identity.NewRandomID(identity.ResourceRun),
		SchedulerID:      scheduler.Summary.ID,
		TriggerSource:    strings.TrimSpace(source),
		Status:           domain.SchedulerRunStatusRunning,
		StartedAt:        now,
		PayloadJSON:      payloadJSON,
		SourceScriptHash: SourceSHA(scheduler.Script),
		ArtifactsDir:     e.artifactsDir(scheduler.Summary.ID, ""),
	}
	if trigger != nil {
		run.TriggerID = trigger.ID
		run.TriggerKind = trigger.Kind
	}
	run.ArtifactsDir = e.artifactsDir(scheduler.Summary.ID, run.ID)

	entered := options.AlreadyEntered
	if !entered && !e.enterRun(scheduler) {
		if options.RetryWhenBusy {
			return PreparedRun{}, ErrRunBusyForRetry
		}
		if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
			return PreparedRun{}, fmt.Errorf("create scheduler run artifacts dir: %w", err)
		}
		_ = e.writeArtifact(run.ArtifactsDir, "payload.json", payloadJSON)
		run.Status = domain.SchedulerRunStatusSkipped
		run.CompletedAt = now
		run.Error = "scheduler is already running"
		if err := e.deps.Store.CreateSchedulerRun(ctx, run); err != nil {
			return PreparedRun{}, err
		}
		e.updateTriggerEventDelivery(ctx, run)
		e.notify("scheduler_run_updated")
		_ = e.deps.Store.UpdateSchedulerLastError(ctx, scheduler.Summary.ID, run.Error)
		_ = e.addSchedulerEvent(ctx, schedulerRunEventInput{
			SchedulerID: scheduler.Summary.ID,
			RunID:       run.ID,
			TriggerID:   run.TriggerID,
			EventType:   "scheduler.run.skipped",
			Level:       "warn",
			Message:     run.Error,
		})
		_ = e.writeArtifact(run.ArtifactsDir, "error.txt", run.Error)
		return PreparedRun{Scheduler: scheduler, Trigger: trigger, Run: run, PayloadJSON: payloadJSON, TrustedHeaders: trustedHeaders}, nil
	}

	if err := os.MkdirAll(run.ArtifactsDir, 0o755); err != nil {
		e.leaveRun(scheduler.Summary.ID)
		return PreparedRun{}, fmt.Errorf("create scheduler run artifacts dir: %w", err)
	}
	_ = e.writeArtifact(run.ArtifactsDir, "payload.json", payloadJSON)

	if err := e.deps.Store.CreateSchedulerRun(ctx, run); err != nil {
		e.leaveRun(scheduler.Summary.ID)
		return PreparedRun{}, err
	}
	e.updateTriggerEventDelivery(ctx, run)
	e.notify("scheduler_run_updated")
	_ = e.addSchedulerEvent(ctx, schedulerRunEventInput{
		SchedulerID: scheduler.Summary.ID,
		RunID:       run.ID,
		TriggerID:   run.TriggerID,
		EventType:   "scheduler.run.started",
		Level:       "info",
		Message:     "scheduler run started",
		Payload:     map[string]any{"source": run.TriggerSource},
	})
	return PreparedRun{Scheduler: scheduler, Trigger: trigger, Run: run, PayloadJSON: payloadJSON, TrustedHeaders: trustedHeaders}, nil
}

// runTrustedHeaders picks the identity a run acts as. A run started by a
// request acts as that request, and one without trusted headers stays without
// them rather than borrowing the applier's identity. Only runs with no request
// of their own act as the last applier of the scheduler's Project.
func (e *RunExecutor) runTrustedHeaders(ctx context.Context, scheduler domain.Scheduler, startedByRequest bool) ([]domain.TrustedHeader, error) {
	if startedByRequest {
		return domain.TrustedHeadersFromContext(ctx), nil
	}
	projectID := strings.TrimSpace(scheduler.Summary.ProjectID)
	if projectID == "" || e.deps.ProjectApplyTrustedHeaders == nil {
		return nil, nil
	}
	headers, err := e.deps.ProjectApplyTrustedHeaders(ctx, projectID)
	if err != nil {
		return nil, fmt.Errorf("load scheduler %s apply trusted headers: %w", scheduler.Summary.ID, err)
	}
	return headers, nil
}

func (e *RunExecutor) Execute(ctx context.Context, prepared PreparedRun) (domain.SchedulerRunSummary, error) {
	if prepared.Run.Status == domain.SchedulerRunStatusSkipped {
		return prepared.Run, nil
	}
	defer e.leaveRun(prepared.Scheduler.Summary.ID)
	ctx = events.WithPublisherProject(ctx, prepared.Scheduler.Summary.ProjectID)
	ctx = domain.NewContextWithTrustedHeaders(ctx, prepared.TrustedHeaders)
	run := prepared.Run
	host := e.deps.HostFactory(prepared.Scheduler, RuntimeExecutionContext{
		ID:        run.ID,
		TriggerID: run.TriggerID,
		Kind:      ExecutionKindTrigger,
	}, ParseTriggerEventMetadata(prepared.PayloadJSON))
	maxAsyncLLM, maxAsyncAgent := AsyncConcurrencyFromSchedulerEnv(prepared.Scheduler.EnvItems)
	execution, execErr := e.deps.Engine.Execute(ctx, SchedulerExecutionRequest{
		Runtime:                  prepared.Scheduler.Summary.Runtime,
		Script:                   prepared.Scheduler.Script,
		Trigger:                  prepared.Trigger,
		PayloadJSON:              prepared.PayloadJSON,
		MaxAsyncLLMConcurrency:   maxAsyncLLM,
		MaxAsyncAgentConcurrency: maxAsyncAgent,
	}, host)

	writeCtx := context.WithoutCancel(ctx)
	if host != nil {
		host.CleanupCommandSessions(writeCtx)
	}
	for _, warning := range execution.Warnings {
		_ = e.addSchedulerEvent(writeCtx, schedulerRunEventInput{
			SchedulerID: prepared.Scheduler.Summary.ID,
			RunID:       run.ID,
			TriggerID:   run.TriggerID,
			EventType:   "scheduler.deprecated_alias.warning",
			Level:       "warning",
			Message:     warning,
		})
	}

	completedAt := time.Now().UTC()
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	if cancelCause := context.Cause(ctx); cancelCause != nil {
		run.Status = domain.SchedulerRunStatusCanceled
		run.Error = cancelCause.Error()
		_ = e.writeArtifact(run.ArtifactsDir, "error.txt", run.Error)
		_ = e.deps.Store.UpdateSchedulerLastError(writeCtx, prepared.Scheduler.Summary.ID, run.Error)
		_ = e.addSchedulerEvent(writeCtx, schedulerRunEventInput{
			SchedulerID: prepared.Scheduler.Summary.ID,
			RunID:       run.ID,
			TriggerID:   run.TriggerID,
			EventType:   "scheduler.run.canceled",
			Level:       "warn",
			Message:     run.Error,
		})
	} else if execErr != nil {
		run.Status = domain.SchedulerRunStatusFailed
		run.Error = execErr.Error()
		_ = e.writeArtifact(run.ArtifactsDir, "error.txt", run.Error)
		_ = e.deps.Store.UpdateSchedulerLastError(writeCtx, prepared.Scheduler.Summary.ID, run.Error)
		_ = e.addSchedulerEvent(writeCtx, schedulerRunEventInput{
			SchedulerID: prepared.Scheduler.Summary.ID,
			RunID:       run.ID,
			TriggerID:   run.TriggerID,
			EventType:   "scheduler.run.failed",
			Level:       "error",
			Message:     run.Error,
		})
	} else {
		run.Status = domain.SchedulerRunStatusSucceeded
		run.ResultJSON = execution.ResultJSON
		if execution.ResultJSON != "" {
			_ = e.writeArtifact(run.ArtifactsDir, "result.json", execution.ResultJSON)
		}
		_ = e.deps.Store.UpdateSchedulerLastError(writeCtx, prepared.Scheduler.Summary.ID, "")
		_ = e.addSchedulerEvent(writeCtx, schedulerRunEventInput{
			SchedulerID: prepared.Scheduler.Summary.ID,
			RunID:       run.ID,
			TriggerID:   run.TriggerID,
			EventType:   "scheduler.run.completed",
			Level:       "info",
			Message:     "scheduler run completed",
			Payload:     map[string]any{"resultJson": execution.ResultJSON},
		})
	}
	if err := e.deps.Store.UpdateSchedulerRun(writeCtx, run); err != nil {
		return domain.SchedulerRunSummary{}, err
	}
	e.updateTriggerEventDelivery(writeCtx, run)
	e.notify("scheduler_run_updated")
	if e.deps.Refresh != nil {
		if err := e.deps.Refresh(writeCtx); err != nil {
			slog.Warn("failed to refresh schedulers after run", "scheduler_id", prepared.Scheduler.Summary.ID, "error", err)
		}
	}
	return run, nil
}

func (e *RunExecutor) Abort(ctx context.Context, prepared PreparedRun, reason string) {
	if prepared.Run.Status == domain.SchedulerRunStatusSkipped {
		return
	}
	defer e.leaveRun(prepared.Scheduler.Summary.ID)
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "scheduler run aborted before execution"
	}
	run := prepared.Run
	completedAt := time.Now().UTC()
	run.Status = domain.SchedulerRunStatusFailed
	run.CompletedAt = completedAt
	run.DurationMs = completedAt.Sub(run.StartedAt).Milliseconds()
	run.Error = reason
	_ = e.writeArtifact(run.ArtifactsDir, "error.txt", run.Error)
	_ = e.deps.Store.UpdateSchedulerLastError(ctx, prepared.Scheduler.Summary.ID, run.Error)
	_ = e.addSchedulerEvent(ctx, schedulerRunEventInput{
		SchedulerID: prepared.Scheduler.Summary.ID,
		RunID:       run.ID,
		TriggerID:   run.TriggerID,
		EventType:   "scheduler.run.failed",
		Level:       "error",
		Message:     run.Error,
	})
	if err := e.deps.Store.UpdateSchedulerRun(ctx, run); err != nil {
		slog.Warn("failed to abort prepared scheduler run", "scheduler_id", prepared.Scheduler.Summary.ID, "run_id", run.ID, "error", err)
	}
	e.updateTriggerEventDelivery(ctx, run)
	e.notify("scheduler_run_updated")
}

func (e *RunExecutor) artifactsDir(schedulerID, runID string) string {
	if e.deps.ArtifactsDir == nil {
		return ""
	}
	return e.deps.ArtifactsDir(schedulerID, runID)
}

func (e *RunExecutor) writeArtifact(dir, name, content string) error {
	if e.deps.WriteArtifact == nil {
		return nil
	}
	return e.deps.WriteArtifact(dir, name, content)
}

func (e *RunExecutor) enterRun(scheduler domain.Scheduler) bool {
	if e.deps.EnterRun == nil {
		return true
	}
	return e.deps.EnterRun(scheduler)
}

func (e *RunExecutor) leaveRun(schedulerID string) {
	if e.deps.LeaveRun != nil {
		e.deps.LeaveRun(schedulerID)
	}
}

// schedulerRunEventInput groups a single run's scheduler event, including the
// run identity, for the RunExecutor event-recording helper.
type schedulerRunEventInput struct {
	SchedulerID string
	RunID       string
	TriggerID   string
	EventType   string
	Level       string
	Message     string
	Payload     any
}

func (e *RunExecutor) addSchedulerEvent(ctx context.Context, event schedulerRunEventInput) error {
	if e.deps.AddSchedulerEvent == nil {
		return nil
	}
	return e.deps.AddSchedulerEvent(ctx, SchedulerEventInput{
		SchedulerID: event.SchedulerID, RunID: event.RunID, TriggerID: event.TriggerID,
		EventType: event.EventType, Level: event.Level, Message: event.Message, Payload: event.Payload,
	})
}

func (e *RunExecutor) updateTriggerEventDelivery(ctx context.Context, run domain.SchedulerRunSummary) {
	if e.deps.UpdateTriggerEventDelivery != nil {
		e.deps.UpdateTriggerEventDelivery(ctx, run)
	}
}

func (e *RunExecutor) notify(reason string) {
	if e.deps.Notify != nil {
		e.deps.Notify(reason)
	}
}
