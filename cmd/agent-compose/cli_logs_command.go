package main

import "github.com/spf13/cobra"

func newCLILogsCommand(cli *cliOptions) *cobra.Command {
	options := composeLogsOptions{}
	cmd := &cobra.Command{
		Use:   "logs [agent-or-id]",
		Short: "Print project run logs",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeLogsCommand(cmd, *cli, options, args)
		},
	}
	cmd.Flags().StringVar(&options.AgentName, "agent", "", "Filter logs by agent name")
	cmd.Flags().StringVar(&options.RunID, "run", "", "Filter logs by run id; cannot be combined with --sandbox or --event")
	cmd.Flags().StringVar(&options.SandboxID, "sandbox", "", "Filter logs by sandbox id; cannot be combined with --run or --event")
	cmd.Flags().StringVar(&options.EventID, "event", "", "Filter logs by event-bus event id (evt_...); cannot be combined with --run or --sandbox")
	cmd.Flags().BoolVar(&options.Follow, "follow", false, "Follow running run output")
	cmd.Flags().IntVarP(&options.TailLines, "tail", "n", -1, "Show the last N lines of run output")
	cmd.Flags().BoolVarP(&options.Timestamp, "timestamp", "t", false, "Prefix text log lines with a run-level timestamp")
	return cmd
}
