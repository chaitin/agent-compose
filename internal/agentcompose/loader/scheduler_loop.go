package loader

import (
	"context"
	"time"
)

type ScheduledRun struct {
	Loader      Definition
	Trigger     Trigger
	PayloadJSON string
	Source      string
}

type ScheduleLoopHost interface {
	Context() context.Context
	Wake() <-chan struct{}
	CollectDueScheduledRuns(now time.Time) []ScheduledRun
	DispatchScheduledRuns(jobs []ScheduledRun)
	NextScheduledFireAt() (time.Time, bool)
}

func RunScheduleLoop(host ScheduleLoopHost) {
	if host == nil {
		return
	}
	for {
		jobs := host.CollectDueScheduledRuns(time.Now().UTC())
		if len(jobs) > 0 {
			host.DispatchScheduledRuns(jobs)
			continue
		}

		nextFireAt, ok := host.NextScheduledFireAt()
		if !ok {
			select {
			case <-host.Context().Done():
				return
			case <-host.Wake():
				continue
			}
		}

		wait := time.Until(nextFireAt)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-host.Context().Done():
			stopTimer(timer)
			return
		case <-host.Wake():
			stopTimer(timer)
			continue
		case <-timer.C:
		}
	}
}

func stopTimer(timer *time.Timer) {
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
