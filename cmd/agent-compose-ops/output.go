package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/chaitin/agent-compose/pkg/upgradeadvisor"
)

func writeJSON(out io.Writer, value any) error {
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func writeStatus(out io.Writer, report upgradeadvisor.StatusReport, location *time.Location) error {
	if _, err := fmt.Fprintf(out, "data schema: v%d\nnow:         %s\n\n", report.SchemaVersion, formatTime(report.Now, location)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "running scheduler runs: %d\n", len(report.RunningSchedulerRuns)); err != nil {
		return err
	}
	if len(report.RunningSchedulerRuns) > 0 {
		writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "STARTED\tELAPSED\tPROJECT/AGENT\tSCHEDULER\tRUN\tTRIGGER")
		for _, run := range report.RunningSchedulerRuns {
			_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
				formatShortTime(run.StartedAt, location),
				formatDurationMS(run.ElapsedMS),
				run.Project+"/"+run.Agent,
				shortSchedulerLabel(run.DisplayName, run.SchedulerID),
				shortID(run.RunID),
				firstNonEmptyText(run.TriggerID, run.TriggerKind),
			)
		}
		_ = writer.Flush()
	}
	if _, err := fmt.Fprintf(out, "\nrunning project runs: %d\n", len(report.RunningProjectRuns)); err != nil {
		return err
	}
	if len(report.RunningProjectRuns) > 0 {
		writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(writer, "STARTED\tELAPSED\tPROJECT\tSTATUS\tRUN")
		for _, run := range report.RunningProjectRuns {
			_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\n",
				formatShortTime(run.StartedAt, location),
				formatDurationMS(run.ElapsedMS),
				shortID(run.ProjectID),
				run.Status,
				shortID(run.RunID),
			)
		}
		_ = writer.Flush()
	}
	if len(report.SandboxCounts) > 0 {
		if _, err := fmt.Fprint(out, "\nsandboxes:\n"); err != nil {
			return err
		}
		for _, count := range report.SandboxCounts {
			if _, err := fmt.Fprintf(out, "  %-8s %d\n", count.Status, count.Count); err != nil {
				return err
			}
		}
	}
	if _, err := fmt.Fprintf(out, "\nrecent scheduler failures (24h): %d\n", len(report.RecentFailures)); err != nil {
		return err
	}
	for index, failure := range report.RecentFailures {
		if index >= 10 {
			break
		}
		if _, err := fmt.Fprintf(out, "  %s  %s  %s\n", formatShortTime(failure.StartedAt, location), shortID(failure.RunID), truncateForOutput(failure.Error, 160)); err != nil {
			return err
		}
	}
	if len(report.Anomalies) == 0 {
		_, err := fmt.Fprint(out, "\nanomalies: none\n")
		return err
	}
	if _, err := fmt.Fprint(out, "\nanomalies:\n"); err != nil {
		return err
	}
	for _, anomaly := range report.Anomalies {
		if _, err := fmt.Fprintf(out, "  [%s] %s: %s\n", anomaly.Severity, anomaly.Kind, anomaly.Message); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintln(out, "\nnote: a daemon restart interrupts running scheduler JS; running project runs survive when they were started behind the guest detach supervisor.")
	return err
}

func writeStats(out io.Writer, report upgradeadvisor.StatsReport, location *time.Location, showTriggers bool) error {
	if _, err := fmt.Fprintf(out, "window: %s .. %s (%d days)\nruns: %d  failures: %d  running now: %d\n\n",
		formatTime(report.Since, location), formatTime(report.Now, location), report.Days,
		report.Totals.Runs, report.Totals.Failures, report.Totals.Running); err != nil {
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "PROJECT/AGENT\tSCHEDULER\tRUNS\tFAIL\tRUNNING\tP50\tP90\tMAX\tBUSIEST HOURS\tLAST RUN")
	for _, scheduler := range report.Schedulers {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%d\t%s\t%s\t%s\t%s\t%s\n",
			scheduler.Project+"/"+scheduler.Agent,
			shortSchedulerLabel(scheduler.DisplayName, scheduler.SchedulerID),
			scheduler.Runs,
			scheduler.Failures,
			scheduler.Running,
			formatDurationMS(scheduler.P50MS),
			formatDurationMS(scheduler.P90MS),
			formatDurationMS(scheduler.MaxMS),
			busiestHours(scheduler.HourHistogram),
			formatShortTime(scheduler.LastRunAt, location),
		)
		if showTriggers {
			for _, trigger := range scheduler.Triggers {
				_, _ = fmt.Fprintf(writer, "  └ %s\t%s\t%d\t%d\t-\t%s\t%s\t%s\t%s\t%s\n",
					trigger.TriggerID,
					firstNonEmptyText(trigger.Source, trigger.Kind),
					trigger.Runs,
					trigger.Failures,
					formatDurationMS(trigger.P50MS),
					formatDurationMS(trigger.P90MS),
					formatDurationMS(trigger.MaxMS),
					busiestHours(trigger.HourHistogram),
					formatShortTime(trigger.LastRunAt, location),
				)
			}
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if len(report.Schedulers) == 0 {
		_, _ = fmt.Fprintln(out, "(no schedulers)")
	}
	return nil
}

func writeUpcoming(out io.Writer, report upgradeadvisor.UpcomingReport, location *time.Location) error {
	if _, err := fmt.Fprintf(out, "now: %s  horizon end: %s  timezone: %s\n\n",
		formatTime(report.Now, location), formatTime(report.HorizonEnd, location), report.Timezone); err != nil {
		return err
	}
	if len(report.Fires) == 0 {
		_, err := fmt.Fprintln(out, "no scheduled trigger fires in the horizon")
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "TIME\tPROJECT/AGENT\tSCHEDULER\tTRIGGER\tKIND\tSOURCE")
	for _, fire := range report.Fires {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%s\t%s\t%s\t%s\n",
			formatShortTime(fire.At, location),
			fire.Project+"/"+fire.Agent,
			shortSchedulerLabel(fire.DisplayName, fire.SchedulerID),
			fire.TriggerID,
			fire.Kind,
			fire.Source,
		)
	}
	return writer.Flush()
}

func writeWindows(out io.Writer, report upgradeadvisor.WindowsReport, location *time.Location) error {
	if _, err := fmt.Fprintf(out, "now: %s  horizon end: %s  timezone: %s\n", formatTime(report.Now, location), formatTime(report.HorizonEnd, location), report.Assumptions.Timezone); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "assumptions: history=%dd bin=%dm window=%dm downtime=%ds assume_pause=%t\n\n",
		report.Assumptions.Days, report.Assumptions.BinMinutes, report.Assumptions.WindowMinutes,
		report.Assumptions.DowntimeSeconds, report.Assumptions.AssumePause); err != nil {
		return err
	}
	if len(report.Candidates) == 0 {
		_, err := fmt.Fprintln(out, "no candidate window fits the horizon; increase --horizon or reduce --window")
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(writer, "START\tEND\tSCORE\tIN-FLIGHT\tCURRENT\tFIRES\tRUNS/WK\tWHY")
	for _, candidate := range report.Candidates {
		_, _ = fmt.Fprintf(writer, "%s\t%s\t%.2f\t%.2f\t%.2f\t%d\t%.1f\t%s\n",
			formatShortTime(candidate.Start, location),
			formatShortTime(candidate.End, location),
			candidate.Score,
			candidate.ExpectedInFlight,
			candidate.ExpectedCurrentInFlight,
			candidate.ScheduledFires,
			candidate.HistoricalRunStarts,
			strings.Join(candidate.Reasons, "; "),
		)
	}
	return writer.Flush()
}

func writeForecast(out io.Writer, report upgradeadvisor.ForecastReport, location *time.Location) error {
	candidate := report.Candidate
	if _, err := fmt.Fprintf(out, "now: %s  proposed window: %s .. %s\n", formatTime(report.Now, location), formatShortTime(candidate.Start, location), formatShortTime(candidate.End, location)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "score=%.2f  expected historical in-flight=%.2f  expected current in-flight=%.2f\n",
		candidate.Score, candidate.ExpectedInFlight, candidate.ExpectedCurrentInFlight); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "scheduled fires in window=%d  historical run starts/week=%.1f  running now=%d\n\n",
		candidate.ScheduledFires, candidate.HistoricalRunStarts, candidate.CurrentRunning); err != nil {
		return err
	}
	for _, reason := range candidate.Reasons {
		if _, err := fmt.Fprintf(out, "- %s\n", reason); err != nil {
			return err
		}
	}
	return nil
}

func formatTime(value time.Time, location *time.Location) string {
	if value.IsZero() {
		return "—"
	}
	return value.In(location).Format("2006-01-02 15:04:05 MST")
}

func formatShortTime(value time.Time, location *time.Location) string {
	if value.IsZero() {
		return "—"
	}
	return value.In(location).Format("01-02 15:04")
}

func formatDurationMS(milliseconds int64) string {
	if milliseconds <= 0 {
		return "—"
	}
	duration := time.Duration(milliseconds) * time.Millisecond
	if duration < time.Second {
		return duration.Round(time.Millisecond).String()
	}
	return duration.Round(time.Second).String()
}

func busiestHours(histogram [24]int) string {
	type hourCount struct {
		hour  int
		count int
	}
	hours := make([]hourCount, 0, len(histogram))
	for hour, count := range histogram {
		if count > 0 {
			hours = append(hours, hourCount{hour: hour, count: count})
		}
	}
	for i := 0; i < len(hours); i++ {
		for j := i + 1; j < len(hours); j++ {
			if hours[j].count > hours[i].count || (hours[j].count == hours[i].count && hours[j].hour < hours[i].hour) {
				hours[i], hours[j] = hours[j], hours[i]
			}
		}
	}
	if len(hours) > 3 {
		hours = hours[:3]
	}
	labels := make([]string, 0, len(hours))
	for _, item := range hours {
		labels = append(labels, fmt.Sprintf("%02d", item.hour))
	}
	return strings.Join(labels, ",")
}

func shortSchedulerLabel(displayName, schedulerID string) string {
	if name := strings.TrimSpace(displayName); name != "" {
		return name
	}
	return shortID(schedulerID)
}

func shortID(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 12 {
		return value
	}
	return value[:12]
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return "—"
}

func truncateForOutput(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
