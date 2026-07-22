package loaders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	domain "agent-compose/pkg/model"
)

var errSchedulerRunTimedOut = errors.New("scheduler run timed out")

type SchedulerRunRequest struct {
	LoaderID    string
	TriggerID   string
	PayloadJSON string
	Timeout     time.Duration
}

type schedulerRunStore interface {
	GetLoaderRun(ctx context.Context, loaderID, runID string) (domain.LoaderRunSummary, error)
	ListLoaderRuns(ctx context.Context, loaderID string, limit int) ([]domain.LoaderRunSummary, error)
}

type schedulerRunSupervisorDependencies struct {
	RootCtx          context.Context
	Store            schedulerRunStore
	LoadLoaderForRun func(ctx context.Context, loaderID, triggerID string) (domain.Loader, *domain.LoaderTrigger, error)
	Prepare          func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions) (PreparedRun, error)
	Execute          func(ctx context.Context, prepared PreparedRun) (domain.LoaderRunSummary, error)
	RunTimeout       func(override time.Duration) time.Duration
}

type schedulerRunSupervisor struct {
	deps schedulerRunSupervisorDependencies

	mu     sync.Mutex
	active map[string]*activeSchedulerRun
}

type activeSchedulerRun struct {
	loaderID string
	cancel   context.CancelCauseFunc
	done     chan struct{}
	result   domain.LoaderRunSummary
	err      error
}

func newSchedulerRunSupervisor(deps schedulerRunSupervisorDependencies) *schedulerRunSupervisor {
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	return &schedulerRunSupervisor{
		deps:   deps,
		active: map[string]*activeSchedulerRun{},
	}
}

func (s *schedulerRunSupervisor) Run(ctx context.Context, request SchedulerRunRequest) (domain.LoaderRunSummary, error) {
	started, active, err := s.start(ctx, request)
	if err != nil || active == nil {
		return started, err
	}
	select {
	case <-active.done:
		return active.result, active.err
	case <-ctx.Done():
		active.cancel(context.Cause(ctx))
		return domain.LoaderRunSummary{}, ctx.Err()
	}
}

func (s *schedulerRunSupervisor) Start(ctx context.Context, request SchedulerRunRequest) (domain.LoaderRunSummary, error) {
	started, _, err := s.start(ctx, request)
	return started, err
}

func (s *schedulerRunSupervisor) start(ctx context.Context, request SchedulerRunRequest) (domain.LoaderRunSummary, *activeSchedulerRun, error) {
	request.LoaderID = strings.TrimSpace(request.LoaderID)
	request.TriggerID = strings.TrimSpace(request.TriggerID)
	if request.LoaderID == "" {
		return domain.LoaderRunSummary{}, nil, domain.ClassifyError(domain.ErrRequired, "scheduler loader id is required", nil)
	}
	if request.TriggerID == "" {
		return domain.LoaderRunSummary{}, nil, domain.ClassifyError(domain.ErrRequired, "scheduler trigger id is required", nil)
	}
	if err := context.Cause(s.deps.RootCtx); err != nil {
		return domain.LoaderRunSummary{}, nil, err
	}
	loader, trigger, err := s.deps.LoadLoaderForRun(ctx, request.LoaderID, request.TriggerID)
	if err != nil {
		return domain.LoaderRunSummary{}, nil, err
	}
	prepared, err := s.deps.Prepare(ctx, loader, trigger, request.PayloadJSON, "manual", RunOptions{})
	if err != nil {
		return domain.LoaderRunSummary{}, nil, err
	}
	if SchedulerRunStatusIsTerminal(prepared.Run.Status) {
		return prepared.Run, nil, nil
	}

	runCtx, cancel := context.WithCancelCause(s.deps.RootCtx)
	cleanup := func() { cancel(context.Canceled) }
	if timeout := s.runTimeout(request.Timeout); timeout > 0 {
		var timeoutCancel context.CancelFunc
		runCtx, timeoutCancel = context.WithTimeoutCause(runCtx, timeout, errSchedulerRunTimedOut)
		cleanup = func() {
			timeoutCancel()
			cancel(context.Canceled)
		}
	}
	active := &activeSchedulerRun{
		loaderID: request.LoaderID,
		cancel:   cancel,
		done:     make(chan struct{}),
	}
	s.register(prepared.Run.ID, active)
	go s.execute(runCtx, cleanup, prepared, active)
	return prepared.Run, active, nil
}

func (s *schedulerRunSupervisor) execute(ctx context.Context, cleanup func(), prepared PreparedRun, active *activeSchedulerRun) {
	defer cleanup()
	active.result, active.err = s.deps.Execute(ctx, prepared)
	close(active.done)
	s.unregister(prepared.Run.ID, active)
}

func (s *schedulerRunSupervisor) Get(ctx context.Context, loaderID, runID string) (domain.LoaderRunSummary, error) {
	if s.deps.Store == nil {
		return domain.LoaderRunSummary{}, fmt.Errorf("scheduler run store is unavailable")
	}
	return s.deps.Store.GetLoaderRun(ctx, strings.TrimSpace(loaderID), strings.TrimSpace(runID))
}

func (s *schedulerRunSupervisor) List(ctx context.Context, loaderID string, limit int) ([]domain.LoaderRunSummary, error) {
	if s.deps.Store == nil {
		return nil, fmt.Errorf("scheduler run store is unavailable")
	}
	return s.deps.Store.ListLoaderRuns(ctx, strings.TrimSpace(loaderID), limit)
}

func (s *schedulerRunSupervisor) Stop(ctx context.Context, loaderID, runID, reason string) (domain.LoaderRunSummary, bool, error) {
	loaderID = strings.TrimSpace(loaderID)
	runID = strings.TrimSpace(runID)
	active := s.lookup(loaderID, runID)
	if active == nil {
		current, err := s.Get(ctx, loaderID, runID)
		if err != nil || SchedulerRunStatusIsTerminal(current.Status) {
			return current, false, err
		}
		id := loaderID + "/" + runID
		return current, false, domain.ResourceError(domain.ErrFailedPrecondition, "scheduler run", id, fmt.Sprintf("scheduler run %s is not active in this process", id), nil)
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "stop requested"
	}
	active.cancel(errors.New(reason))
	select {
	case <-active.done:
		return active.result, true, active.err
	case <-ctx.Done():
		return domain.LoaderRunSummary{}, true, ctx.Err()
	}
}

func (s *schedulerRunSupervisor) register(runID string, active *activeSchedulerRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.active[strings.TrimSpace(runID)] = active
}

func (s *schedulerRunSupervisor) unregister(runID string, active *activeSchedulerRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runID = strings.TrimSpace(runID)
	if s.active[runID] == active {
		delete(s.active, runID)
	}
}

func (s *schedulerRunSupervisor) lookup(loaderID, runID string) *activeSchedulerRun {
	s.mu.Lock()
	defer s.mu.Unlock()
	active := s.active[runID]
	if active == nil || active.loaderID != loaderID {
		return nil
	}
	return active
}

func (s *schedulerRunSupervisor) runTimeout(override time.Duration) time.Duration {
	if s.deps.RunTimeout == nil {
		return override
	}
	return s.deps.RunTimeout(override)
}

func SchedulerRunStatusIsTerminal(status string) bool {
	switch domain.NormalizeLoaderRunStatus(status) {
	case domain.LoaderRunStatusSucceeded, domain.LoaderRunStatusFailed, domain.LoaderRunStatusCanceled, domain.LoaderRunStatusSkipped:
		return true
	default:
		return false
	}
}
