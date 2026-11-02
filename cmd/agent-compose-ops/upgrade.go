package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/chaitin/agent-compose/pkg/upgradeadvisor"
)

func newStatusCommand(options *cliOptions) *cobra.Command {
	var stuckAfter time.Duration
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show running scheduler/project work, sandboxes, and anomalies",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			advisor, closeAdvisor, err := options.openAdvisor(upgradeadvisor.Options{StuckAfter: stuckAfter})
			if err != nil {
				return err
			}
			defer closeAdvisor()
			report, err := advisor.Status(cmd.Context())
			if err != nil {
				return fmt.Errorf("read daemon status: %w", err)
			}
			if options.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			location, err := options.location()
			if err != nil {
				return err
			}
			return writeStatus(cmd.OutOrStdout(), report, location)
		},
	}
	cmd.Flags().DurationVar(&stuckAfter, "stuck-after", upgradeadvisor.DefaultStuckAfter, "Flag scheduler runs that have been running longer than this")
	return cmd
}

func newStatsCommand(options *cliOptions) *cobra.Command {
	var days int
	var showTriggers bool
	cmd := &cobra.Command{
		Use:   "stats",
		Short: "Summarize scheduler run history, durations, failures, and busy hours",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			advisor, closeAdvisor, err := options.openAdvisor(upgradeadvisor.Options{Days: days})
			if err != nil {
				return err
			}
			defer closeAdvisor()
			report, err := advisor.Stats(cmd.Context())
			if err != nil {
				return fmt.Errorf("read scheduler history: %w", err)
			}
			if options.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			location, err := options.location()
			if err != nil {
				return err
			}
			return writeStats(cmd.OutOrStdout(), report, location, showTriggers)
		},
	}
	cmd.Flags().IntVar(&days, "days", upgradeadvisor.DefaultDays, "How many days of history to analyze")
	cmd.Flags().BoolVar(&showTriggers, "triggers", false, "Include the per-trigger breakdown")
	return cmd
}

func newUpcomingCommand(options *cliOptions) *cobra.Command {
	var horizon time.Duration
	var limit int
	cmd := &cobra.Command{
		Use:   "upcoming",
		Short: "List scheduled trigger fires in the future",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			advisor, closeAdvisor, err := options.openAdvisor(upgradeadvisor.Options{Horizon: horizon, Limit: limit})
			if err != nil {
				return err
			}
			defer closeAdvisor()
			report, err := advisor.Upcoming(cmd.Context())
			if err != nil {
				return fmt.Errorf("forecast scheduled triggers: %w", err)
			}
			if options.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			location, err := options.location()
			if err != nil {
				return err
			}
			return writeUpcoming(cmd.OutOrStdout(), report, location)
		},
	}
	cmd.Flags().DurationVar(&horizon, "horizon", upgradeadvisor.DefaultHorizon, "How far ahead to look")
	cmd.Flags().IntVar(&limit, "limit", upgradeadvisor.DefaultLimit, "Maximum number of fires to print")
	return cmd
}

func newWindowsCommand(options *cliOptions) *cobra.Command {
	var days int
	var horizon time.Duration
	var bin time.Duration
	var window time.Duration
	var downtime time.Duration
	var top int
	var assumePause bool
	cmd := &cobra.Command{
		Use:   "windows",
		Short: "Recommend daemon restart windows based on history and upcoming fires",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			advisor, closeAdvisor, err := options.openAdvisor(upgradeadvisor.Options{
				Days: days, Horizon: horizon, Bin: bin, Window: window, Downtime: downtime,
				Top: top, AssumePause: assumePause,
			})
			if err != nil {
				return err
			}
			defer closeAdvisor()
			report, err := advisor.Windows(cmd.Context())
			if err != nil {
				return fmt.Errorf("recommend upgrade windows: %w", err)
			}
			if options.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			location, err := options.location()
			if err != nil {
				return err
			}
			return writeWindows(cmd.OutOrStdout(), report, location)
		},
	}
	cmd.Flags().IntVar(&days, "days", upgradeadvisor.DefaultDays, "How many days of history to analyze")
	cmd.Flags().DurationVar(&horizon, "horizon", upgradeadvisor.DefaultHorizon, "How far ahead to recommend windows")
	cmd.Flags().DurationVar(&bin, "bin", upgradeadvisor.DefaultBin, "Activity histogram resolution")
	cmd.Flags().DurationVar(&window, "window", upgradeadvisor.DefaultWindow, "Minimum candidate window length")
	cmd.Flags().DurationVar(&downtime, "downtime", upgradeadvisor.DefaultDowntime, "Estimated daemon restart duration")
	cmd.Flags().IntVar(&top, "top", upgradeadvisor.DefaultTop, "How many candidate windows to print")
	cmd.Flags().BoolVar(&assumePause, "assume-pause", true, "Assume new trigger fires will be deferred by upgrade prepare; set false to also penalize fires inside the window")
	return cmd
}

func newForecastCommand(options *cliOptions) *cobra.Command {
	var at string
	var bin time.Duration
	var window time.Duration
	var downtime time.Duration
	var assumePause bool
	cmd := &cobra.Command{
		Use:   "forecast",
		Short: "Evaluate one proposed restart time",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			location, err := options.location()
			if err != nil {
				return err
			}
			forecastAt, err := parseForecastTime(at, location)
			if err != nil {
				return err
			}
			advisor, closeAdvisor, err := options.openAdvisor(upgradeadvisor.Options{
				Bin: bin, Window: window, Downtime: downtime, AssumePause: assumePause,
			})
			if err != nil {
				return err
			}
			defer closeAdvisor()
			report, err := advisor.Forecast(cmd.Context(), forecastAt)
			if err != nil {
				return fmt.Errorf("forecast upgrade time: %w", err)
			}
			if options.JSON {
				return writeJSON(cmd.OutOrStdout(), report)
			}
			return writeForecast(cmd.OutOrStdout(), report, location)
		},
	}
	cmd.Flags().StringVar(&at, "at", "", "Proposed restart time (RFC3339, or local '2006-01-02 15:04')")
	cmd.Flags().DurationVar(&bin, "bin", upgradeadvisor.DefaultBin, "Activity histogram resolution")
	cmd.Flags().DurationVar(&window, "window", upgradeadvisor.DefaultWindow, "Minimum candidate window length")
	cmd.Flags().DurationVar(&downtime, "downtime", upgradeadvisor.DefaultDowntime, "Estimated daemon restart duration")
	cmd.Flags().BoolVar(&assumePause, "assume-pause", true, "Assume new trigger fires will be deferred by upgrade prepare")
	_ = cmd.MarkFlagRequired("at")
	return cmd
}

func parseForecastTime(value string, location *time.Location) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, fmt.Errorf("--at is required")
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed, nil
	}
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.ParseInLocation(layout, value, location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("parse --at %q: expected RFC3339 or %q", value, "2006-01-02 15:04")
}
