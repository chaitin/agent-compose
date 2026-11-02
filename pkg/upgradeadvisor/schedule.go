package upgradeadvisor

import (
	"context"
	"sort"
	"strings"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
)

// UpcomingReport lists the schedule-based trigger fires expected in the
// forecast horizon.
type UpcomingReport struct {
	GeneratedAt time.Time  `json:"generated_at"`
	Now         time.Time  `json:"now"`
	HorizonEnd  time.Time  `json:"horizon_end"`
	Timezone    string     `json:"timezone"`
	Fires       []FireInfo `json:"fires"`
}

// FireInfo is one expected trigger fire.
type FireInfo struct {
	At          time.Time `json:"at"`
	SchedulerID string    `json:"scheduler_id"`
	Project     string    `json:"project"`
	Agent       string    `json:"agent"`
	DisplayName string    `json:"display_name,omitempty"`
	TriggerID   string    `json:"trigger_id"`
	Kind        string    `json:"kind"`
	Source      string    `json:"source,omitempty"`
}

// Upcoming computes the next scheduled trigger fires within the horizon.
func (a *Advisor) Upcoming(ctx context.Context) (UpcomingReport, error) {
	ds, err := a.load(ctx)
	if err != nil {
		return UpcomingReport{}, err
	}
	fires := computeFires(ds, ds.now, ds.now.Add(a.opts.Horizon))
	if len(fires) > a.opts.Limit {
		fires = fires[:a.opts.Limit]
	}
	if fires == nil {
		fires = []FireInfo{}
	}
	return UpcomingReport{
		GeneratedAt: ds.now,
		Now:         ds.now,
		HorizonEnd:  ds.now.Add(a.opts.Horizon),
		Timezone:    a.opts.Location.String(),
		Fires:       fires,
	}, nil
}

// computeFires returns every scheduled fire in [from, until), sorted by time.
// Only cron, interval, and timeout triggers participate; event triggers are
// driven by their topic and are not predictable here.
func computeFires(ds *dataset, from, until time.Time) []FireInfo {
	from = from.UTC()
	until = until.UTC()
	var fires []FireInfo
	for _, trigger := range ds.triggers {
		if !trigger.Enabled || !isScheduleKind(trigger.Kind) {
			continue
		}
		info, _ := ds.scheduler(trigger.SchedulerID)
		for _, at := range triggerFires(trigger, from, until) {
			fires = append(fires, FireInfo{
				At:          at,
				SchedulerID: trigger.SchedulerID,
				Project:     info.Project,
				Agent:       info.Agent,
				DisplayName: info.DisplayName,
				TriggerID:   trigger.ID,
				Kind:        trigger.Kind,
				Source:      trigger.source(),
			})
		}
	}
	sort.Slice(fires, func(i, j int) bool {
		if fires[i].At.Equal(fires[j].At) {
			if fires[i].SchedulerID == fires[j].SchedulerID {
				return fires[i].TriggerID < fires[j].TriggerID
			}
			return fires[i].SchedulerID < fires[j].SchedulerID
		}
		return fires[i].At.Before(fires[j].At)
	})
	return fires
}

func triggerFires(trigger triggerInfo, from, until time.Time) []time.Time {
	if strings.EqualFold(strings.TrimSpace(trigger.Kind), domain.SchedulerTriggerKindTimeout) {
		return timeoutFires(trigger, from, until)
	}
	domainTrigger := trigger.domainTrigger()
	next := trigger.NextFireAt
	if next.IsZero() || !next.After(from) {
		computed, err := schedulers.SchedulerTriggerNextFireAt(from, domainTrigger, false)
		if err != nil || computed.IsZero() {
			return nil
		}
		next = computed
	}
	fires := make([]time.Time, 0, 8)
	for len(fires) < maxFiresPerTrigger && !next.IsZero() && !next.After(until) {
		fires = append(fires, next)
		computed, err := schedulers.SchedulerTriggerNextFireAt(next, domainTrigger, true)
		if err != nil || computed.IsZero() || !computed.After(next) {
			break
		}
		next = computed
	}
	return fires
}

// timeoutFires forecasts a one-shot trigger. A zero next-fire time means the
// timeout already fired (the scheduler clears it after the single fire), so
// inventing another fire from "now" would overstate the upgrade risk.
func timeoutFires(trigger triggerInfo, from, until time.Time) []time.Time {
	if trigger.NextFireAt.IsZero() {
		return nil
	}
	next := trigger.NextFireAt
	if !next.After(from) {
		next = from
	}
	if next.After(until) {
		return nil
	}
	return []time.Time{next}
}

func isScheduleKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case domain.SchedulerTriggerKindCron, domain.SchedulerTriggerKindInterval, domain.SchedulerTriggerKindTimeout:
		return true
	default:
		return false
	}
}
