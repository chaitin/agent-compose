package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCLISchedulerCommand(cli *cliOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scheduler",
		Short: "Invoke, inspect, and operate project schedulers, trigger runs, logs, and triggers",
		Args:  cobra.NoArgs,
		RunE:  func(cmd *cobra.Command, _ []string) error { return cmd.Help() },
	}

	invokeOptions := composeSchedulerInvokeOptions{}
	invokeCmd := &cobra.Command{
		Use: "invoke <scheduler-ref>", Short: "Invoke a deployed scheduler script",
		Long: "Invoke a script-based scheduler in the foreground and wait for its result. This does not execute a named trigger or create scheduler trigger-run history.", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerInvokeCommand(cmd, *cli, invokeOptions, args[0])
		},
	}
	invokeCmd.Flags().StringVar(&invokeOptions.PayloadJSON, "payload", "", "JSON payload passed to the scheduler script")

	listOptions := composeSchedulerListOptions{}
	listCmd := &cobra.Command{
		Use: "ls [agent]", Short: "List project scheduler triggers", Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerListCommand(cmd, *cli, listOptions, args)
		},
	}
	listCmd.Flags().BoolVar(&listOptions.Verbose, "verbose", false, "Show full scheduler and trigger IDs")

	triggerOptions := composeSchedulerTriggerOptions{}
	triggerCmd := &cobra.Command{
		Use: "trigger <scheduler-ref> <trigger-ref>", Short: "Manually run a scheduler trigger", Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerTriggerCommand(cmd, *cli, triggerOptions, args[0], args[1])
		},
	}
	addComposeSchedulerExecutionFlags(triggerCmd, &triggerOptions)

	runsOptions := composeSchedulerRunsOptions{}
	runsCmd := &cobra.Command{
		Use: "runs [scheduler-ref]", Short: "List scheduler trigger runs",
		Long: "List outer trigger-run history for current project schedulers. Results are unbounded by default; use --limit to restrict the final count.", Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerRunsCommand(cmd, *cli, runsOptions, args)
		},
	}
	runsCmd.Flags().StringVar(&runsOptions.AgentName, "agent", "", "Filter by scheduler owner (deprecated)")
	_ = runsCmd.Flags().MarkHidden("agent")
	runsCmd.Flags().StringVar(&runsOptions.Trigger, "trigger", "", "Filter by trigger name or id")
	runsCmd.Flags().StringVar(&runsOptions.Status, "status", "", "Filter by run status")
	runsCmd.Flags().Uint32Var(&runsOptions.Limit, "limit", 0, "Maximum runs to show; 0 means all")

	logsOptions := composeSchedulerLogsOptions{}
	logsCmd := &cobra.Command{
		Use: "logs [run-ref]", Short: "Print scheduler trigger-run logs",
		Long: "Print outer structured events for all scheduler trigger runs by default. Use scheduler, trigger, or run filters to narrow the result. These logs do not include invocation logs or inner agent transcripts.", Args: cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerLogsCommand(cmd, *cli, logsOptions, args)
		},
	}
	logsCmd.Flags().StringVar(&logsOptions.SchedulerRef, "scheduler", "", "Filter by scheduler name or ID")
	logsCmd.Flags().StringVar(&logsOptions.AgentName, "agent", "", "Filter by scheduler owner (deprecated)")
	_ = logsCmd.Flags().MarkHidden("agent")
	logsCmd.Flags().StringVar(&logsOptions.Trigger, "trigger", "", "Filter by trigger name or id")
	logsCmd.Flags().StringVar(&logsOptions.RunID, "run", "", "Filter by scheduler run id")
	logsCmd.Flags().IntVarP(&logsOptions.Tail, "tail", "n", -1, "Show the last N log events")

	pruneOptions := composeSchedulerPruneOptions{}
	pruneCmd := &cobra.Command{
		Use: "prune", Short: "Prune scheduler trigger-run history",
		Long: "Prune terminal scheduler trigger runs and their outer events and artifacts. The command is a dry-run unless --force is specified.",
		Args: func(cmd *cobra.Command, args []string) error {
			if err := cobra.NoArgs(cmd, args); err != nil {
				return commandExitError{Code: exitCodeUsage, Err: err}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComposeSchedulerPruneCommand(cmd, *cli, pruneOptions)
		},
	}
	pruneCmd.Flags().StringVar(&pruneOptions.SchedulerRef, "scheduler", "", "Filter by scheduler name or ID")
	pruneCmd.Flags().StringVar(&pruneOptions.TriggerRef, "trigger", "", "Filter by current trigger name or exact historical trigger ID")
	pruneCmd.Flags().StringVar(&pruneOptions.Status, "status", "", "Filter by terminal run status, comma-separated")
	pruneCmd.Flags().StringVar(&pruneOptions.OlderThan, "older-than", "", "Only match runs older than a duration such as 7d or 24h")
	pruneCmd.Flags().BoolVar(&pruneOptions.Force, "force", false, "Actually remove matched scheduler trigger runs")

	stopOptions := composeSchedulerStopOptions{}
	stopCmd := &cobra.Command{
		Use: "stop <run>", Short: "Stop an active scheduler run", Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerStopCommand(cmd, *cli, stopOptions, args[0])
		},
	}
	stopCmd.Flags().StringVar(&stopOptions.Reason, "reason", "", "Reason recorded for the canceled run")

	inspectOptions := composeSchedulerInspectOptions{}
	inspectCmd := &cobra.Command{
		Use: "inspect <scheduler-or-trigger-or-run-ref>", Short: "Inspect a scheduler, trigger, or scheduler trigger run",
		Long: "Inspect one scheduler, trigger, or outer scheduler trigger run. Use --scheduler to disambiguate trigger references shared by multiple schedulers.",
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler inspect accepts one resource reference; use --scheduler <scheduler-ref> to scope a trigger lookup")}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerInspectCommand(cmd, *cli, inspectOptions, args[0])
		},
	}
	inspectCmd.Flags().StringVar(&inspectOptions.SchedulerRef, "scheduler", "", "Limit trigger lookup to a scheduler name or ID")
	cmd.AddCommand(listCmd, invokeCmd, triggerCmd, runsCmd, logsCmd, pruneCmd, stopCmd, inspectCmd)
	return cmd
}
