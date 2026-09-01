package schedulers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

var errSchedulerRunTimedOut = errors.New("scheduler run timed out")

type SchedulerRunRequest struct {
	SchedulerID string
	TriggerID   string
	PayloadJSON string
	Timeout     time.Duration
}

type schedulerRunStore interface {
	GetSchedulerRun(ctx context.Context, schedulerID, runID string) (domain.SchedulerRunSummary, error)
	ListSchedulerRuns(ctx context.Context, schedulerID string, limit int) ([]domain.SchedulerRunSummary, error)
}

type schedulerRunSupervisorDependencies struct {
	RootCtx             context.Context
	Store               schedulerRunStore
	LoadSchedulerForRun func(ctx context.Context, schedulerID, triggerID string) (domain.Scheduler, *domain.SchedulerTrigger, error)
	Prepare             func(ctx context.Context, req RunTriggerRequest) (PreparedRun, error)
	Execute             func(ctx context.Context, prepared PreparedRun) (domain.SchedulerRunSummary, error)
	RunTimeout          func(override time.Duration) time.Duration
}

// SchedulerRunSupervisor owns scheduler-run execution, cancellation, and lookup.
type SchedulerRunSupervisor struct {
	deps schedulerRunSupervisorDependencies

	mu     sync.Mutex
	active map[SchedulerRunKey]*activeSchedulerRun
}

type activeSchedulerRun struct {
	cancel context.CancelCauseFunc
	done   chan struct{}
	result domain.SchedulerRunSummary
	err    error
}

func newSchedulerRunSupervisor(deps schedulerRunSupervisorDependencies) *SchedulerRunSupervisor {
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	return &SchedulerRunSupervisor{
		deps:   deps,
		active: map[SchedulerRunKey]*activeSchedulerRun{},
	}
}

// runTrigger prepares and executes a scheduler trigger while keeping the run
// visible to lifecycle operations such as StopSchedulerRun.
func (s *SchedulerRunSupervisor) runTrigger(ctx context.Context, request RunTriggerRequest, triggerEventAck ...func(context.Context) error) (domain.SchedulerRunSummary, error) {
	prepared, err := s.deps.Prepare(ctx, request)
	if err != nil {
		return domain.SchedulerRunSummary{}, err
	}
	if len(triggerEventAck) > 0 && triggerEventAck[0] != nil {
		if err := triggerEventAck[0](ctx); err != nil {
			slog.Warn("failed to mark scheduler topic event published", "topic", request.Source, "error", err)
		}
	}
	return s.runPrepared(ctx, prepared)
}

// runPrepared executes an already-persisted scheduler run under supervisor
// ownership. Event delivery uses this after preparing all matched webhook runs.
func (s *SchedulerRunSupervisor) runPrepared(ctx context.Context, prepared PreparedRun) (domain.SchedulerRunSummary, error) {
	if SchedulerRunStatusIsTerminal(prepared.Run.Status) {
		return prepared.Run, nil
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	active := &activeSchedulerRun{cancel: cancel, done: make(chan struct{})}
	s.register(prepared.Run.SchedulerID, prepared.Run.ID, active)
	s.execute(runCtx, func() { cancel(context.Canceled) }, prepared, active)
	return active.result, active.err
}

func (s *SchedulerRunSupervisor) RunScheduler(ctx context.Context, request SchedulerRunRequest) (domain.SchedulerRunSummary, error) {
	started, active, err := s.start(ctx, request)
	if err != nil || active == nil {
		return started, err
	}
	select {
	case <-active.done:
		return active.result, active.err
	case <-ctx.Done():
		active.cancel(context.Cause(ctx))
		return domain.SchedulerRunSummary{}, ctx.Err()
	}
}

func (s *SchedulerRunSupervisor) StartSchedulerRun(ctx context.Context, request SchedulerRunRequest) (domain.SchedulerRunSummary, error) {
	started, _, err := s.start(ctx, request)
	return started, err
}

func (s *SchedulerRunSupervisor) start(ctx context.Context, request SchedulerRunRequest) (domain.SchedulerRunSummary, *activeSchedulerRun, error) {
	request.SchedulerID = strings.TrimSpace(request.SchedulerID)
	request.TriggerID = strings.TrimSpace(request.TriggerID)
	if request.SchedulerID == "" {
		return domain.SchedulerRunSummary{}, nil, domain.ClassifyError(domain.ErrRequired, "scheduler id is required", nil)
	}
	if request.TriggerID == "" {
		return domain.SchedulerRunSummary{}, nil, domain.ClassifyError(domain.ErrRequired, "scheduler trigger id is required", nil)
	}
	if err := context.Cause(s.deps.RootCtx); err != nil {
		return domain.SchedulerRunSummary{}, nil, err
	}
	scheduler, trigger, err := s.deps.LoadSchedulerForRun(ctx, request.SchedulerID, request.TriggerID)
	if err != nil {
		return domain.SchedulerRunSummary{}, nil, err
	}
	prepared, err := s.deps.Prepare(ctx, RunTriggerRequest{Scheduler: scheduler, Trigger: trigger, PayloadJSON: request.PayloadJSON, Source: "manual"})
	if err != nil {
		return domain.SchedulerRunSummary{}, nil, err
	}
	if SchedulerRunStatusIsTerminal(prepared.Run.Status) {
		return prepared.Run, nil, nil
	}

	runCtx, cancel := context.WithCancelCause(s.deps.RootCtx)
	cleanup := func() { cancel(context.Canceled) }
	if timeout := effectiveSchedulerRunTimeout(scheduler, request.Timeout, s.deps.RunTimeout); timeout > 0 {
		var timeoutCancel context.CancelFunc
		runCtx, timeoutCancel = context.WithTimeoutCause(runCtx, timeout, errSchedulerRunTimedOut)
		cleanup = func() {
			timeoutCancel()
			cancel(context.Canceled)
		}
	}
	active := &activeSchedulerRun{cancel: cancel, done: make(chan struct{})}
	s.register(request.SchedulerID, prepared.Run.ID, active)
	go s.execute(runCtx, cleanup, prepared, active)
	return prepared.Run, active, nil
}

func (s *SchedulerRunSupervisor) execute(ctx context.Context, cleanup func(), prepared PreparedRun, active *activeSchedulerRun) {
	defer cleanup()
	active.result, active.err = s.deps.Execute(ctx, prepared)
	close(active.done)
	s.unregister(prepared.Run.SchedulerID, prepared.Run.ID, active)
}

func (s *SchedulerRunSupervisor) GetSchedulerRun(ctx context.Context, schedulerID, runID string) (domain.SchedulerRunSummary, error) {
	if s.deps.Store == nil {
		return domain.SchedulerRunSummary{}, fmt.Errorf("scheduler run store is unavailable")
	}
	return s.deps.Store.GetSchedulerRun(ctx, strings.TrimSpace(schedulerID), strings.TrimSpace(runID))
}

func (s *SchedulerRunSupervisor) ListSchedulerRuns(ctx context.Context, schedulerID string, limit int) ([]domain.SchedulerRunSummary, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("scheduler run store is unavailable")
	}
	return s.deps.Store.ListSchedulerRuns(ctx, strings.TrimSpace(schedulerID), limit)
}

func (s *SchedulerRunSupervisor) StopSchedulerRun(ctx context.Context, schedulerID, runID, reason string) (domain.SchedulerRunSummary, bool, error) {
	active, current, requested, err := s.requestSchedulerRunStop(ctx, schedulerID, runID, reason)
	if err != nil || !requested {
		return current, requested, err
	}
	select {
	case <-active.done:
		return active.result, true, active.err
	case <-ctx.Done():
		return domain.SchedulerRunSummary{}, true, ctx.Err()
	}
}

// RequestSchedulerRunStop requests cancellation without waiting for the run to
// reach a terminal state.
func (s *SchedulerRunSupervisor) RequestSchedulerRunStop(ctx context.Context, schedulerID, runID, reason string) (bool, error) {
	_, _, requested, err := s.requestSchedulerRunStop(ctx, schedulerID, runID, reason)
	return requested, err
}

func (s *SchedulerRunSupervisor) requestSchedulerRunStop(ctx context.Context, schedulerID, runID, reason string) (*activeSchedulerRun, domain.SchedulerRunSummary, bool, error) {
	schedulerID = strings.TrimSpace(schedulerID)
	runID = strings.TrimSpace(runID)
	active := s.lookup(schedulerID, runID)
	if active == nil {
		current, err := s.GetSchedulerRun(ctx, schedulerID, runID)
		if err != nil || SchedulerRunStatusIsTerminal(current.Status) {
			return nil, current, false, err
		}
		id := schedulerID + "/" + runID
		return nil, current, false, domain.ResourceError(domain.ErrFailedPrecondition, "scheduler run", id, fmt.Sprintf("scheduler run %s is not active in this process", id), nil)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "stop requested"
	}
	active.cancel(errors.New(reason))
	return active, domain.SchedulerRunSummary{}, true, nil
}

func (s *SchedulerRunSupervisor) register(schedulerID, runID string, active *activeSchedulerRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[schedulerRunKey(schedulerID, runID)] = active
}

func (s *SchedulerRunSupervisor) unregister(schedulerID, runID string, active *activeSchedulerRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := schedulerRunKey(schedulerID, runID)
	if s.active[key] == active {
		delete(s.active, key)
	}
}

func (s *SchedulerRunSupervisor) lookup(schedulerID, runID string) *activeSchedulerRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[schedulerRunKey(schedulerID, runID)]
}

func schedulerRunKey(schedulerID, runID string) SchedulerRunKey {
	return SchedulerRunKey{SchedulerID: strings.TrimSpace(schedulerID), RunID: strings.TrimSpace(runID)}
}

func SchedulerRunStatusIsTerminal(status string) bool {
	switch NormalizeRunStatus(status) {
	case domain.SchedulerRunStatusSucceeded, domain.SchedulerRunStatusFailed, domain.SchedulerRunStatusCanceled, domain.SchedulerRunStatusSkipped:
		return true
	default:
		return false
	}
}
