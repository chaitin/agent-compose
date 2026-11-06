package upgradeadvisor

import (
	"context"
	"sort"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// StatsReport summarizes scheduler history for the requested window.
type StatsReport struct {
	GeneratedAt time.Time           `json:"generated_at"`
	Now         time.Time           `json:"now"`
	Since       time.Time           `json:"since"`
	Days        int                 `json:"days"`
	Totals      StatsTotals         `json:"totals"`
	Schedulers  []SchedulerActivity `json:"schedulers"`
}

// StatsTotals aggregates all schedulers.
type StatsTotals struct {
	Runs     int `json:"runs"`
	Failures int `json:"failures"`
	Running  int `json:"running"`
}

// SchedulerActivity is the aggregated history for one scheduler.
type SchedulerActivity struct {
	SchedulerID      string            `json:"scheduler_id"`
	ProjectID        string            `json:"project_id"`
	Project          string            `json:"project"`
	Agent            string            `json:"agent"`
	DisplayName      string            `json:"display_name,omitempty"`
	Enabled          bool              `json:"enabled"`
	Runs             int               `json:"runs"`
	Failures         int               `json:"failures"`
	Running          int               `json:"running"`
	P50MS            int64             `json:"p50_ms"`
	P90MS            int64             `json:"p90_ms"`
	MaxMS            int64             `json:"max_ms"`
	HourHistogram    [24]int           `json:"hour_histogram"`
	WeekdayHistogram [7]int            `json:"weekday_histogram"`
	LastRunAt        time.Time         `json:"last_run_at,omitempty"`
	NextFireAt       time.Time         `json:"next_fire_at,omitempty"`
	Triggers         []TriggerActivity `json:"triggers"`
}

// TriggerActivity is the aggregated history for one scheduler trigger.
type TriggerActivity struct {
	TriggerID        string    `json:"trigger_id"`
	Kind             string    `json:"kind"`
	Topic            string    `json:"topic,omitempty"`
	Source           string    `json:"source,omitempty"`
	Enabled          bool      `json:"enabled"`
	Runs             int       `json:"runs"`
	Failures         int       `json:"failures"`
	P50MS            int64     `json:"p50_ms"`
	P90MS            int64     `json:"p90_ms"`
	MaxMS            int64     `json:"max_ms"`
	HourHistogram    [24]int   `json:"hour_histogram"`
	WeekdayHistogram [7]int    `json:"weekday_histogram"`
	LastRunAt        time.Time `json:"last_run_at,omitempty"`
	NextFireAt       time.Time `json:"next_fire_at,omitempty"`
	LastFiredAt      time.Time `json:"last_fired_at,omitempty"`
}

type activityAccumulator struct {
	activity  SchedulerActivity
	durations []int64
	triggers  map[string]*triggerAccumulator
}

type triggerAccumulator struct {
	activity  TriggerActivity
	durations []int64
}

// Stats aggregates run history, durations, failure counts, and local-time
// histograms for every scheduler and trigger.
func (a *Advisor) Stats(ctx context.Context) (StatsReport, error) {
	ds, err := a.load(ctx)
	if err != nil {
		return StatsReport{}, err
	}
	accumulators := make(map[string]*activityAccumulator, len(ds.schedulerOrder))
	for _, id := range ds.schedulerOrder {
		info, ok := ds.scheduler(id)
		if !ok {
			continue
		}
		accumulators[id] = &activityAccumulator{
			activity: SchedulerActivity{
				SchedulerID: info.ID,
				ProjectID:   info.ProjectID,
				Project:     info.Project,
				Agent:       info.Agent,
				DisplayName: info.DisplayName,
				Enabled:     info.Enabled,
			},
			triggers: map[string]*triggerAccumulator{},
		}
	}
	for _, run := range ds.runs {
		accumulator, ok := accumulators[run.SchedulerID]
		if !ok {
			continue
		}
		accumulateSchedulerRun(&accumulator.activity, &accumulator.durations, run, ds.loc)
		trigger := accumulator.triggers[run.TriggerID]
		if trigger == nil {
			trigger = &triggerAccumulator{activity: TriggerActivity{
				TriggerID: run.TriggerID,
				Kind:      run.TriggerKind,
				Source:    run.TriggerSource,
			}}
			accumulator.triggers[run.TriggerID] = trigger
		}
		accumulateTrigger(&trigger.activity, &trigger.durations, run, ds.loc)
		trigger.activity.Source = firstNonEmpty(trigger.activity.Source, run.TriggerSource)
	}
	for _, trigger := range ds.triggers {
		accumulator, ok := accumulators[trigger.SchedulerID]
		if !ok {
			continue
		}
		item := accumulator.triggers[trigger.ID]
		if item == nil {
			item = &triggerAccumulator{activity: TriggerActivity{TriggerID: trigger.ID}}
			accumulator.triggers[trigger.ID] = item
		}
		item.activity.Kind = firstNonEmpty(item.activity.Kind, trigger.Kind)
		item.activity.Topic = trigger.Topic
		item.activity.Source = trigger.source()
		item.activity.Enabled = trigger.Enabled
		item.activity.NextFireAt = trigger.NextFireAt
		item.activity.LastFiredAt = trigger.LastFiredAt
	}
	// Running counts come from the live status query, not the history window.
	for _, run := range ds.runningScheduler {
		if accumulator, ok := accumulators[run.SchedulerID]; ok {
			accumulator.activity.Running++
		}
	}

	report := StatsReport{
		GeneratedAt: ds.now,
		Now:         ds.now,
		Since:       ds.now.Add(-time.Duration(a.opts.Days) * 24 * time.Hour),
		Days:        a.opts.Days,
		Schedulers:  []SchedulerActivity{},
	}
	for _, id := range ds.schedulerOrder {
		accumulator := accumulators[id]
		finalizeActivity(&accumulator.activity, accumulator.durations)
		for _, item := range accumulator.triggers {
			finalizeTrigger(&item.activity, item.durations)
			accumulator.activity.Triggers = append(accumulator.activity.Triggers, item.activity)
		}
		sort.Slice(accumulator.activity.Triggers, func(i, j int) bool {
			return accumulator.activity.Triggers[i].TriggerID < accumulator.activity.Triggers[j].TriggerID
		})
		report.Totals.Runs += accumulator.activity.Runs
		report.Totals.Failures += accumulator.activity.Failures
		report.Totals.Running += accumulator.activity.Running
		report.Schedulers = append(report.Schedulers, accumulator.activity)
	}
	return report, nil
}

func accumulateSchedulerRun(activity *SchedulerActivity, durations *[]int64, run runRecord, loc *time.Location) {
	activity.Runs++
	if run.Status == domain.SchedulerRunStatusFailed {
		activity.Failures++
	}
	if !run.StartedAt.IsZero() {
		local := run.StartedAt.In(loc)
		activity.HourHistogram[local.Hour()]++
		activity.WeekdayHistogram[int(local.Weekday())]++
		if run.StartedAt.After(activity.LastRunAt) {
			activity.LastRunAt = run.StartedAt
		}
	}
	if millis := runDurationMillis(run.StartedAt, run.CompletedAt, run.DurationMS); millis > 0 {
		*durations = append(*durations, millis)
	}
}

func accumulateTrigger(activity *TriggerActivity, durations *[]int64, run runRecord, loc *time.Location) {
	activity.Runs++
	if run.Status == domain.SchedulerRunStatusFailed {
		activity.Failures++
	}
	if !run.StartedAt.IsZero() {
		local := run.StartedAt.In(loc)
		activity.HourHistogram[local.Hour()]++
		activity.WeekdayHistogram[int(local.Weekday())]++
		if run.StartedAt.After(activity.LastRunAt) {
			activity.LastRunAt = run.StartedAt
		}
	}
	if millis := runDurationMillis(run.StartedAt, run.CompletedAt, run.DurationMS); millis > 0 {
		*durations = append(*durations, millis)
	}
}

func finalizeActivity(activity *SchedulerActivity, durations []int64) {
	sorted := sortedMillis(durations)
	activity.P50MS = percentileMillis(sorted, 0.50)
	activity.P90MS = percentileMillis(sorted, 0.90)
	if len(sorted) > 0 {
		activity.MaxMS = sorted[len(sorted)-1]
	}
}

func finalizeTrigger(activity *TriggerActivity, durations []int64) {
	sorted := sortedMillis(durations)
	activity.P50MS = percentileMillis(sorted, 0.50)
	activity.P90MS = percentileMillis(sorted, 0.90)
	if len(sorted) > 0 {
		activity.MaxMS = sorted[len(sorted)-1]
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// durationsByScheduler returns the completed-run durations grouped by
// scheduler, used to estimate whether a currently running run is likely to
// outlive a candidate window.
func durationsByScheduler(runs []runRecord) map[string][]int64 {
	result := map[string][]int64{}
	for _, run := range runs {
		millis := runDurationMillis(run.StartedAt, run.CompletedAt, run.DurationMS)
		if millis <= 0 {
			continue
		}
		result[run.SchedulerID] = append(result[run.SchedulerID], millis)
	}
	return result
}

func p90FromDurations(durations map[string][]int64) (map[string]int64, int64) {
	perScheduler := map[string]int64{}
	var all []int64
	for schedulerID, values := range durations {
		sorted := sortedMillis(values)
		perScheduler[schedulerID] = percentileMillis(sorted, 0.90)
		all = append(all, values...)
	}
	sortedAll := sortedMillis(all)
	return perScheduler, percentileMillis(sortedAll, 0.90)
}
