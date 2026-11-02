package upgradeadvisor

import (
	"context"
	"fmt"
	"sort"
	"time"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// StatusReport is the live snapshot of work that an upgrade would interrupt.
type StatusReport struct {
	GeneratedAt          time.Time          `json:"generated_at"`
	Now                  time.Time          `json:"now"`
	SchemaVersion        int64              `json:"schema_version"`
	RunningSchedulerRuns []SchedulerRunInfo `json:"running_scheduler_runs"`
	RunningProjectRuns   []ProjectRunInfo   `json:"running_project_runs"`
	SandboxCounts        []SandboxCount     `json:"sandbox_counts"`
	RecentFailures       []FailureInfo      `json:"recent_failures"`
	Anomalies            []Anomaly          `json:"anomalies"`
}

// SchedulerRunInfo identifies one running scheduler run.
type SchedulerRunInfo struct {
	SchedulerID   string    `json:"scheduler_id"`
	Project       string    `json:"project"`
	Agent         string    `json:"agent"`
	DisplayName   string    `json:"display_name,omitempty"`
	RunID         string    `json:"run_id"`
	TriggerID     string    `json:"trigger_id,omitempty"`
	TriggerKind   string    `json:"trigger_kind,omitempty"`
	TriggerSource string    `json:"trigger_source,omitempty"`
	Status        string    `json:"status"`
	StartedAt     time.Time `json:"started_at"`
	ElapsedMS     int64     `json:"elapsed_ms"`
}

// ProjectRunInfo identifies one running or pending project run.
type ProjectRunInfo struct {
	RunID     string    `json:"run_id"`
	ProjectID string    `json:"project_id"`
	Source    string    `json:"source,omitempty"`
	Status    string    `json:"status"`
	StartedAt time.Time `json:"started_at,omitempty"`
	ElapsedMS int64     `json:"elapsed_ms"`
}

// SandboxCount is one sandbox lifecycle state and how many sandboxes are in it.
type SandboxCount struct {
	Status string `json:"status"`
	Count  int    `json:"count"`
}

// FailureInfo is a recent scheduler run failure.
type FailureInfo struct {
	SchedulerID string    `json:"scheduler_id"`
	RunID       string    `json:"run_id"`
	TriggerID   string    `json:"trigger_id,omitempty"`
	TriggerKind string    `json:"trigger_kind,omitempty"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	Error       string    `json:"error,omitempty"`
}

// Anomaly is a suspicious observation that may affect the upgrade.
type Anomaly struct {
	Severity    string `json:"severity"`
	Kind        string `json:"kind"`
	Message     string `json:"message"`
	SchedulerID string `json:"scheduler_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	SandboxID   string `json:"sandbox_id,omitempty"`
}

// Status reports the current in-flight scheduler and project work plus
// anomalies that an operator should inspect before restarting the daemon.
func (a *Advisor) Status(ctx context.Context) (StatusReport, error) {
	ds, err := a.load(ctx)
	if err != nil {
		return StatusReport{}, err
	}
	report := StatusReport{
		GeneratedAt:          ds.now,
		Now:                  ds.now,
		SchemaVersion:        ds.schemaVersion,
		RunningSchedulerRuns: []SchedulerRunInfo{},
		RunningProjectRuns:   []ProjectRunInfo{},
		SandboxCounts:        []SandboxCount{},
		RecentFailures:       []FailureInfo{},
		Anomalies:            []Anomaly{},
	}
	for _, run := range ds.runningScheduler {
		info, _ := ds.scheduler(run.SchedulerID)
		elapsed := int64(0)
		if !run.StartedAt.IsZero() {
			elapsed = ds.now.Sub(run.StartedAt).Milliseconds()
			if elapsed < 0 {
				elapsed = 0
			}
		}
		report.RunningSchedulerRuns = append(report.RunningSchedulerRuns, SchedulerRunInfo{
			SchedulerID:   run.SchedulerID,
			Project:       info.Project,
			Agent:         info.Agent,
			DisplayName:   info.DisplayName,
			RunID:         run.RunID,
			TriggerID:     run.TriggerID,
			TriggerKind:   run.TriggerKind,
			TriggerSource: run.TriggerSource,
			Status:        run.Status,
			StartedAt:     run.StartedAt,
			ElapsedMS:     elapsed,
		})
	}
	sort.Slice(report.RunningSchedulerRuns, func(i, j int) bool {
		return report.RunningSchedulerRuns[i].StartedAt.Before(report.RunningSchedulerRuns[j].StartedAt)
	})
	for _, run := range ds.projectRuns {
		elapsed := int64(0)
		if !run.StartedAt.IsZero() {
			elapsed = ds.now.Sub(run.StartedAt).Milliseconds()
			if elapsed < 0 {
				elapsed = 0
			}
		}
		report.RunningProjectRuns = append(report.RunningProjectRuns, ProjectRunInfo{
			RunID:     run.RunID,
			ProjectID: run.ProjectID,
			Source:    run.Source,
			Status:    run.Status,
			StartedAt: run.StartedAt,
			ElapsedMS: elapsed,
		})
	}
	sort.Slice(report.RunningProjectRuns, func(i, j int) bool {
		return report.RunningProjectRuns[i].StartedAt.Before(report.RunningProjectRuns[j].StartedAt)
	})
	for status, count := range ds.sandboxCounts {
		report.SandboxCounts = append(report.SandboxCounts, SandboxCount{Status: status, Count: count})
	}
	sort.Slice(report.SandboxCounts, func(i, j int) bool {
		return report.SandboxCounts[i].Status < report.SandboxCounts[j].Status
	})
	for _, run := range ds.recentFailures {
		report.RecentFailures = append(report.RecentFailures, FailureInfo{
			SchedulerID: run.SchedulerID,
			RunID:       run.RunID,
			TriggerID:   run.TriggerID,
			TriggerKind: run.TriggerKind,
			Status:      run.Status,
			StartedAt:   run.StartedAt,
			Error:       truncate(run.Error, 300),
		})
	}
	report.Anomalies = a.detectAnomalies(ds)
	return report, nil
}

func (a *Advisor) detectAnomalies(ds *dataset) []Anomaly {
	anomalies := []Anomaly{}
	for _, run := range ds.runningScheduler {
		if !run.StartedAt.IsZero() && ds.now.Sub(run.StartedAt) > a.opts.StuckAfter {
			anomalies = append(anomalies, Anomaly{
				Severity:    "warning",
				Kind:        "scheduler_run_long_running",
				Message:     fmt.Sprintf("scheduler run has been running for %s (threshold %s)", ds.now.Sub(run.StartedAt).Round(time.Second), a.opts.StuckAfter),
				SchedulerID: run.SchedulerID,
				RunID:       run.RunID,
			})
		}
	}
	for _, trigger := range ds.triggers {
		if !trigger.Enabled {
			continue
		}
		kind := trigger.Kind
		if kind == domain.SchedulerTriggerKindCron || kind == domain.SchedulerTriggerKindInterval {
			if trigger.NextFireAt.IsZero() {
				anomalies = append(anomalies, Anomaly{
					Severity:    "warning",
					Kind:        "scheduler_trigger_without_next_fire",
					Message:     fmt.Sprintf("enabled %s trigger has no next fire time; the scheduler may not be ticking", kind),
					SchedulerID: trigger.SchedulerID,
				})
				continue
			}
			grace := 5 * time.Minute
			if kind == domain.SchedulerTriggerKindInterval && trigger.IntervalMS > 0 {
				interval := time.Duration(trigger.IntervalMS) * time.Millisecond
				if interval*2 > grace {
					grace = interval * 2
				}
			}
			if ds.now.Sub(trigger.NextFireAt) > grace {
				anomalies = append(anomalies, Anomaly{
					Severity:    "warning",
					Kind:        "scheduler_trigger_overdue",
					Message:     fmt.Sprintf("enabled %s trigger is overdue by %s", kind, ds.now.Sub(trigger.NextFireAt).Round(time.Second)),
					SchedulerID: trigger.SchedulerID,
				})
			}
		}
	}
	for _, sandbox := range ds.pendingSandboxes {
		anomalies = append(anomalies, Anomaly{
			Severity:  "warning",
			Kind:      "sandbox_creation_stuck",
			Message:   fmt.Sprintf("sandbox has been PENDING for %s", ds.now.Sub(sandbox.CreatedAt).Round(time.Second)),
			SandboxID: sandbox.ID,
		})
		if len(anomalies) >= 50 {
			break
		}
	}
	if len(ds.recentFailures) > 0 {
		anomalies = append(anomalies, Anomaly{
			Severity: "info",
			Kind:     "scheduler_failures_recent",
			Message:  fmt.Sprintf("%d scheduler run(s) failed in the last 24h", len(ds.recentFailures)),
		})
	}
	return anomalies
}

func truncate(value string, limit int) string {
	if limit <= 0 {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
