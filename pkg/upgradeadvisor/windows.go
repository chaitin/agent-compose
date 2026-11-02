package upgradeadvisor

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// WindowsReport recommends restart windows based on historical activity and
// the expected schedule fires in the horizon.
type WindowsReport struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Now         time.Time         `json:"now"`
	HorizonEnd  time.Time         `json:"horizon_end"`
	Assumptions WindowAssumptions `json:"assumptions"`
	Candidates  []Candidate       `json:"candidates"`
	// Truncated reports that the scheduled-fire forecast hit its internal cap,
	// so late windows may under-count fires.
	Truncated bool `json:"truncated"`
}

// WindowAssumptions describes how the recommendations were derived.
type WindowAssumptions struct {
	Days            int     `json:"days"`
	BinMinutes      int     `json:"bin_minutes"`
	WindowMinutes   int     `json:"window_minutes"`
	DowntimeSeconds int     `json:"downtime_seconds"`
	HorizonMinutes  int     `json:"horizon_minutes"`
	Timezone        string  `json:"timezone"`
	AssumePause     bool    `json:"assume_pause"`
	ActivityWeeks   float64 `json:"activity_weeks"`
}

// Candidate is one recommended restart window.
type Candidate struct {
	Start                   time.Time `json:"start"`
	End                     time.Time `json:"end"`
	Score                   float64   `json:"score"`
	ExpectedInFlight        float64   `json:"expected_in_flight"`
	ExpectedCurrentInFlight float64   `json:"expected_current_in_flight"`
	HistoricalRunStarts     float64   `json:"historical_run_starts"`
	ScheduledFires          int       `json:"scheduled_fires"`
	CurrentRunning          int       `json:"current_running"`
	Reasons                 []string  `json:"reasons"`
}

// ForecastReport evaluates one operator-supplied time.
type ForecastReport struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Now         time.Time         `json:"now"`
	Assumptions WindowAssumptions `json:"assumptions"`
	Candidate   Candidate         `json:"candidate"`
}

type activityProfile struct {
	binMinutes  int
	binSeconds  float64
	weeks       float64
	busySeconds map[int]float64
	runStarts   map[int]float64
}

// Windows ranks candidate restart windows from now to the horizon.
func (a *Advisor) Windows(ctx context.Context) (WindowsReport, error) {
	ds, err := a.load(ctx)
	if err != nil {
		return WindowsReport{}, err
	}
	window := a.effectiveWindow()
	horizonEnd := ds.now.Add(a.opts.Horizon)
	report := WindowsReport{
		GeneratedAt: ds.now,
		Now:         ds.now,
		HorizonEnd:  horizonEnd,
		Assumptions: a.assumptions(),
		Candidates:  []Candidate{},
	}
	profile := buildActivityProfile(ds, a.opts)
	fires, firesTruncated := computeFires(ds, ds.now, horizonEnd)
	report.Truncated = firesTruncated
	p90ByScheduler, p90Global := p90FromDurations(durationsByScheduler(ds.runs))
	evaluator := newWindowEvaluator(a.opts, ds, profile, fires, p90Estimate{byScheduler: p90ByScheduler, global: p90Global})
	for start := alignUp(ds.now, a.opts.Bin); !start.Add(window).After(horizonEnd); start = start.Add(a.opts.Bin) {
		report.Candidates = append(report.Candidates, evaluator.evaluate(start, window))
	}
	report.Candidates = topNonOverlapping(report.Candidates, a.opts.Top)
	return report, nil
}

// Forecast evaluates a specific restart time.
func (a *Advisor) Forecast(ctx context.Context, at time.Time) (ForecastReport, error) {
	ds, err := a.load(ctx)
	if err != nil {
		return ForecastReport{}, err
	}
	at = at.UTC()
	if at.Before(ds.now.Add(-a.opts.Bin)) {
		return ForecastReport{}, fmt.Errorf("forecast time %s is in the past (now is %s)", at.Format(time.RFC3339), ds.now.Format(time.RFC3339))
	}
	window := a.effectiveWindow()
	profile := buildActivityProfile(ds, a.opts)
	fires, _ := computeFires(ds, ds.now, at.Add(window))
	p90ByScheduler, p90Global := p90FromDurations(durationsByScheduler(ds.runs))
	evaluator := newWindowEvaluator(a.opts, ds, profile, fires, p90Estimate{byScheduler: p90ByScheduler, global: p90Global})
	return ForecastReport{
		GeneratedAt: ds.now,
		Now:         ds.now,
		Assumptions: a.assumptions(),
		Candidate:   evaluator.evaluate(at, window),
	}, nil
}

func (a *Advisor) assumptions() WindowAssumptions {
	return WindowAssumptions{
		Days:            a.opts.Days,
		BinMinutes:      int(a.opts.Bin / time.Minute),
		WindowMinutes:   int(a.effectiveWindow() / time.Minute),
		DowntimeSeconds: int(a.opts.Downtime / time.Second),
		HorizonMinutes:  int(a.opts.Horizon / time.Minute),
		Timezone:        a.opts.Location.String(),
		AssumePause:     a.opts.AssumePause,
		ActivityWeeks:   activityWeeks(a.opts.Days),
	}
}

func (a *Advisor) effectiveWindow() time.Duration {
	window := a.opts.Window
	if a.opts.Downtime > window {
		window = a.opts.Downtime
	}
	bins := (window + a.opts.Bin - 1) / a.opts.Bin
	if bins <= 0 {
		bins = 1
	}
	return time.Duration(int64(bins) * int64(a.opts.Bin))
}

func activityWeeks(days int) float64 {
	if days <= 0 {
		return 1
	}
	weeks := float64(days) / 7
	if weeks < 1 {
		return 1
	}
	return weeks
}

func alignUp(t time.Time, bin time.Duration) time.Time {
	truncated := t.Truncate(bin)
	if truncated.Before(t) {
		return truncated.Add(bin)
	}
	return truncated
}

func binKey(t time.Time, loc *time.Location, binMinutes int) int {
	local := t.In(loc)
	minutes := int(local.Weekday())*1440 + local.Hour()*60 + local.Minute()
	return minutes / binMinutes
}

func buildActivityProfile(ds *dataset, opts Options) activityProfile {
	binMinutes := int(opts.Bin / time.Minute)
	profile := activityProfile{
		binMinutes:  binMinutes,
		binSeconds:  opts.Bin.Seconds(),
		weeks:       activityWeeks(opts.Days),
		busySeconds: map[int]float64{},
		runStarts:   map[int]float64{},
	}
	for _, run := range ds.runs {
		millis := runDurationMillis(run.StartedAt, run.CompletedAt, run.DurationMS)
		if millis <= 0 || run.StartedAt.IsZero() {
			continue
		}
		start := run.StartedAt.UTC()
		end := start.Add(time.Duration(millis) * time.Millisecond)
		startedBin := start.Truncate(opts.Bin)
		for binStart := startedBin; binStart.Before(end); binStart = binStart.Add(opts.Bin) {
			overlapStart := binStart
			if start.After(overlapStart) {
				overlapStart = start
			}
			overlapEnd := binStart.Add(opts.Bin)
			if end.Before(overlapEnd) {
				overlapEnd = end
			}
			if !overlapEnd.After(overlapStart) {
				continue
			}
			profile.busySeconds[binKey(binStart, opts.Location, binMinutes)] += overlapEnd.Sub(overlapStart).Seconds()
		}
		profile.runStarts[binKey(start, opts.Location, binMinutes)]++
	}
	return profile
}

type p90Estimate struct {
	byScheduler map[string]int64
	global      int64
}

type windowEvaluator struct {
	opts    Options
	ds      *dataset
	profile activityProfile
	fires   []FireInfo
	p90     p90Estimate
}

func newWindowEvaluator(options Options, ds *dataset, profile activityProfile, fires []FireInfo, p90 p90Estimate) windowEvaluator {
	return windowEvaluator{opts: options, ds: ds, profile: profile, fires: fires, p90: p90}
}

func (e windowEvaluator) evaluate(start time.Time, window time.Duration) Candidate {
	end := start.Add(window)
	bins := int(window / e.opts.Bin)
	historicalInFlight := 0.0
	historicalStarts := 0.0
	for index := 0; index < bins; index++ {
		binStart := start.Add(time.Duration(index) * e.opts.Bin)
		key := binKey(binStart, e.opts.Location, e.profile.binMinutes)
		historicalInFlight += e.profile.busySeconds[key] / (e.profile.binSeconds * e.profile.weeks)
		historicalStarts += e.profile.runStarts[key] / e.profile.weeks
	}
	if bins > 0 {
		historicalInFlight /= float64(bins)
	}
	scheduledFires := countFiresInWindow(e.fires, start, end)
	currentInFlight := expectedCurrentInFlight(e.ds, e.p90.byScheduler, e.p90.global, start)
	score := historicalInFlight + currentInFlight
	if !e.opts.AssumePause {
		score += float64(scheduledFires) * 0.25
	}
	candidate := Candidate{
		Start:                   start,
		End:                     end,
		Score:                   score,
		ExpectedInFlight:        historicalInFlight,
		ExpectedCurrentInFlight: currentInFlight,
		HistoricalRunStarts:     historicalStarts,
		ScheduledFires:          scheduledFires,
		CurrentRunning:          len(e.ds.runningScheduler),
	}
	candidate.Reasons = candidateReasons(candidate, e.opts.AssumePause)
	return candidate
}

func countFiresInWindow(fires []FireInfo, start, end time.Time) int {
	count := 0
	for _, fire := range fires {
		if !fire.At.Before(start) && fire.At.Before(end) {
			count++
		}
	}
	return count
}

func expectedCurrentInFlight(ds *dataset, p90ByScheduler map[string]int64, p90Global int64, start time.Time) float64 {
	total := 0.0
	delta := start.Sub(ds.now)
	if delta < 0 {
		delta = 0
	}
	for _, run := range ds.runningScheduler {
		horizon := p90ByScheduler[run.SchedulerID]
		if horizon <= 0 {
			horizon = p90Global
		}
		if horizon <= 0 {
			total++
			continue
		}
		elapsed := ds.now.Sub(run.StartedAt)
		if elapsed < 0 {
			elapsed = 0
		}
		if elapsed >= time.Duration(horizon)*time.Millisecond {
			total++
			continue
		}
		remaining := time.Duration(horizon)*time.Millisecond - elapsed
		if delta >= remaining {
			continue
		}
		total += 1 - float64(delta)/float64(remaining)
	}
	return total
}

func candidateReasons(candidate Candidate, assumePause bool) []string {
	var reasons []string
	if candidate.ExpectedCurrentInFlight < 0.05 && candidate.ExpectedInFlight < 0.05 {
		reasons = append(reasons, "no scheduler run is expected to be in flight")
	}
	if candidate.ScheduledFires == 0 {
		reasons = append(reasons, "no scheduled trigger fires during the window")
	} else if assumePause {
		reasons = append(reasons, fmt.Sprintf("%d scheduled trigger fire(s) would be deferred until after the upgrade", candidate.ScheduledFires))
	} else {
		reasons = append(reasons, fmt.Sprintf("%d scheduled trigger fire(s) fall inside the window", candidate.ScheduledFires))
	}
	if candidate.CurrentRunning > 0 {
		reasons = append(reasons, fmt.Sprintf("%d scheduler run(s) are running now", candidate.CurrentRunning))
	}
	if candidate.ExpectedInFlight >= 0.5 {
		reasons = append(reasons, "historical activity is elevated at this time of week")
	}
	return reasons
}

func topNonOverlapping(candidates []Candidate, top int) []Candidate {
	sorted := append([]Candidate(nil), candidates...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Score == sorted[j].Score {
			return sorted[i].Start.Before(sorted[j].Start)
		}
		return sorted[i].Score < sorted[j].Score
	})
	selected := make([]Candidate, 0, top)
	for _, candidate := range sorted {
		overlaps := false
		for _, existing := range selected {
			if candidate.Start.Before(existing.End) && existing.Start.Before(candidate.End) {
				overlaps = true
				break
			}
		}
		if overlaps {
			continue
		}
		selected = append(selected, candidate)
		if len(selected) >= top {
			break
		}
	}
	return selected
}
