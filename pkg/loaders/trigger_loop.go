package loaders

import (
	"context"
	"log/slog"
	"time"

	domain "agent-compose/pkg/model"
)

type TriggerLoopStore interface {
	MarkLoaderTriggerFired(ctx context.Context, loaderID, triggerID string, lastFiredAt, nextFireAt time.Time) error
}

type TriggerLoopDependencies struct {
	RootCtx       context.Context
	Wake          <-chan struct{}
	Store         TriggerLoopStore
	Snapshot      func() map[string]domain.Loader
	ReplaceCached func(map[string]domain.Loader)
	Run           func(ctx context.Context, loader domain.Loader, trigger *domain.LoaderTrigger, payloadJSON, source string, options RunOptions, triggerEventAck ...func(context.Context) error) (domain.LoaderRunSummary, error)
	RunTimeout    func(time.Duration) time.Duration
}

type triggerLoop struct {
	deps TriggerLoopDependencies
}

func newTriggerLoop(deps TriggerLoopDependencies) *triggerLoop {
	if deps.RootCtx == nil {
		deps.RootCtx = context.Background()
	}
	return &triggerLoop{deps: deps}
}

func (s *triggerLoop) Loop() {
	for {
		jobs := s.CollectDue(time.Now().UTC())
		if len(jobs) > 0 {
			s.Dispatch(jobs)
			continue
		}

		nextFireAt, ok := s.NextFireAt()
		if !ok {
			select {
			case <-s.rootCtx().Done():
				return
			case <-s.deps.Wake:
				continue
			}
		}

		wait := time.Until(nextFireAt)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-s.rootCtx().Done():
			StopTimer(timer)
			return
		case <-s.deps.Wake:
			StopTimer(timer)
			continue
		case <-timer.C:
		}
	}
}

func (s *triggerLoop) Dispatch(jobs []ScheduledRun) {
	for _, job := range jobs {
		runCtx, cancel := context.WithTimeout(s.rootCtx(), s.runTimeout(0))
		go func(job ScheduledRun) {
			defer cancel()
			if _, err := s.deps.Run(runCtx, job.Loader, &job.Trigger, job.PayloadJSON, job.Source, RunOptions{}); err != nil {
				slog.Warn("loader scheduled run failed", "loader_id", job.Loader.Summary.ID, "trigger_id", job.Trigger.ID, "trigger_kind", job.Trigger.Kind, "error", err)
			}
		}(job)
	}
}

func (s *triggerLoop) NextFireAt() (time.Time, bool) {
	var nextFireAt time.Time
	for _, loader := range s.snapshot() {
		if !loader.Summary.Enabled {
			continue
		}
		for _, trigger := range loader.Triggers {
			if !trigger.Enabled || !domain.LoaderTriggerUsesSchedule(trigger.Kind) || trigger.NextFireAt.IsZero() {
				continue
			}
			if nextFireAt.IsZero() || trigger.NextFireAt.Before(nextFireAt) {
				nextFireAt = trigger.NextFireAt
			}
		}
	}
	if nextFireAt.IsZero() {
		return time.Time{}, false
	}
	return nextFireAt, true
}

func (s *triggerLoop) CollectDue(now time.Time) []ScheduledRun {
	scheduled, updatedLoaders, scheduleErrs := CollectDueScheduledRuns(s.snapshot(), now)
	if len(updatedLoaders) > 0 && s.deps.ReplaceCached != nil {
		s.deps.ReplaceCached(updatedLoaders)
	}
	for _, item := range scheduleErrs {
		slog.Warn("failed to compute next loader schedule", "loader_id", item.LoaderID, "trigger_id", item.TriggerID, "trigger_kind", item.TriggerKind, "error", item.Err)
	}
	for _, job := range scheduled {
		if s.deps.Store == nil {
			continue
		}
		if err := s.deps.Store.MarkLoaderTriggerFired(s.rootCtx(), job.Loader.Summary.ID, job.Trigger.ID, job.Trigger.LastFiredAt, job.Trigger.NextFireAt); err != nil {
			slog.Warn("failed to persist loader fire state", "loader_id", job.Loader.Summary.ID, "trigger_id", job.Trigger.ID, "trigger_kind", job.Trigger.Kind, "error", err)
		}
	}
	return scheduled
}

func StopTimer(timer *time.Timer) {
	if timer == nil {
		return
	}
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
}

func (s *triggerLoop) snapshot() map[string]domain.Loader {
	if s.deps.Snapshot == nil {
		return nil
	}
	return s.deps.Snapshot()
}

func (s *triggerLoop) rootCtx() context.Context {
	if s.deps.RootCtx == nil {
		return context.Background()
	}
	return s.deps.RootCtx
}

func (s *triggerLoop) runTimeout(override time.Duration) time.Duration {
	if s.deps.RunTimeout == nil {
		return 20 * time.Minute
	}
	return s.deps.RunTimeout(override)
}
