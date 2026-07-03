package loaders

import (
	"time"

	domain "agent-compose/pkg/model"
)

type ScheduledRun struct {
	Loader      domain.Loader
	Trigger     domain.LoaderTrigger
	PayloadJSON string
	Source      string
}

type ScheduleError struct {
	LoaderID    string
	TriggerID   string
	TriggerKind string
	Err         error
}

func CollectDueScheduledRuns(items map[string]domain.Loader, now time.Time) ([]ScheduledRun, map[string]domain.Loader, []ScheduleError) {
	jobs := make([]ScheduledRun, 0)
	updatedLoaders := make(map[string]domain.Loader)
	var errs []ScheduleError
	for id, loader := range items {
		if !loader.Summary.Enabled {
			continue
		}
		updated := false
		for index := range loader.Triggers {
			trigger := &loader.Triggers[index]
			if !trigger.Enabled || !domain.LoaderTriggerUsesSchedule(trigger.Kind) || trigger.NextFireAt.IsZero() || trigger.NextFireAt.After(now) {
				continue
			}
			nextFireAt, err := LoaderTriggerNextFireAt(now, *trigger, true)
			if err != nil {
				errs = append(errs, ScheduleError{
					LoaderID:    loader.Summary.ID,
					TriggerID:   trigger.ID,
					TriggerKind: trigger.Kind,
					Err:         err,
				})
				continue
			}
			trigger.LastFiredAt = now
			trigger.NextFireAt = nextFireAt
			jobs = append(jobs, ScheduledRun{
				Loader:      CloneLoader(loader),
				Trigger:     *trigger,
				PayloadJSON: "",
				Source:      LoaderTriggerSource(*trigger),
			})
			updated = true
		}
		if updated {
			updatedLoaders[id] = CloneLoader(loader)
		}
	}
	return jobs, updatedLoaders, errs
}

func CloneLoader(item domain.Loader) domain.Loader {
	cloned := item
	if item.Triggers != nil {
		cloned.Triggers = append([]domain.LoaderTrigger(nil), item.Triggers...)
	}
	if item.EnvItems != nil {
		cloned.EnvItems = append([]domain.SessionEnvVar(nil), item.EnvItems...)
	}
	return cloned
}
