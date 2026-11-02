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
	GeneratedAt time.Time `json:"generated_at"`
	Now         time.Time `json:"now"`
	HorizonEnd  time.Time `json:"horizon_end"`
	Timezone    string    `json:"timezone"`
	// TotalFires is the number of fires found in the horizon before the
	// report limit is applied.
	TotalFires int `json:"total_fires"`
	// Truncated reports that Fires was limited (by --limit or the internal
	// forecast cap) and does not list every expected fire.
	Truncated bool       `json:"truncated"`
	Fires     []FireInfo `json:"fires"`
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
	fires, truncated := computeFires(ds, ds.now, ds.now.Add(a.opts.Horizon))
	total := len(fires)
	if len(fires) > a.opts.Limit {
		fires = fires[:a.opts.Limit]
		truncated = true
	}
	if fires == nil {
		fires = []FireInfo{}
	}
	return UpcomingReport{
		GeneratedAt: ds.now,
		Now:         ds.now,
		HorizonEnd:  ds.now.Add(a.opts.Horizon),
		Timezone:    a.opts.Location.String(),
		TotalFires:  total,
		Truncated:   truncated,
		Fires:       fires,
	}, nil
}

// computeFires returns the scheduled fires in [from, until), sorted by time,
// and whether the internal cap cut the forecast short. Only triggers that are
// enabled on an enabled scheduler participate: a disabled scheduler's leftover
// trigger rows are historical configuration, not expected activity. cron,
// interval, and timeout triggers participate; event triggers are driven by
// their topic and are not predictable here.
func computeFires(ds *dataset, from, until time.Time) ([]FireInfo, bool) {
	from = from.UTC()
	until = until.UTC()
	var fires []FireInfo
	truncated := false
	for _, trigger := range ds.triggers {
		if !trigger.Enabled || !isScheduleKind(trigger.Kind) {
			continue
		}
		info, ok := ds.scheduler(trigger.SchedulerID)
		if !ok || !info.Enabled {
			continue
		}
		times, triggerTruncated := triggerFires(trigger, from, until)
		if triggerTruncated {
			truncated = true
		}
		for _, at := range times {
			if len(fires) >= maxFiresTotal {
				truncated = true
				break
			}
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
		if len(fires) >= maxFiresTotal {
			break
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
	return fires, truncated
}

func triggerFires(trigger triggerInfo, from, until time.Time) ([]time.Time, bool) {
	if strings.EqualFold(strings.TrimSpace(trigger.Kind), domain.SchedulerTriggerKindTimeout) {
		return timeoutFires(trigger, from, until), false
	}
	domainTrigger := trigger.domainTrigger()
	next := trigger.NextFireAt
	if next.IsZero() || !next.After(from) {
		computed, err := schedulers.SchedulerTriggerNextFireAt(from, domainTrigger, false)
		if err != nil || computed.IsZero() {
			return nil, false
		}
		next = computed
	}
	fires := make([]time.Time, 0, 8)
	for !next.IsZero() && !next.After(until) {
		if len(fires) >= maxFiresPerTrigger {
			return fires, true
		}
		fires = append(fires, next)
		computed, err := schedulers.SchedulerTriggerNextFireAt(next, domainTrigger, true)
		if err != nil || computed.IsZero() || !computed.After(next) {
			break
		}
		next = computed
	}
	return fires, false
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
