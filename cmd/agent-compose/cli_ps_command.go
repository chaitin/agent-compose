package main

import "github.com/spf13/cobra"

func newCLIPSCommand(cli *cliOptions) *cobra.Command {
	options := composePSOptions{}
	cmd := newCLISandboxListCommand(cli, "ps", "List project sandboxes", &options)
	return cmd
}

func newCLISandboxListCommand(cli *cliOptions, use, short string, options *composePSOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runComposePSCommand(cmd, *cli, *options)
		},
	}
	cmd.Flags().BoolVarP(&options.All, "all", "a", false, "Show current project sandboxes in all statuses")
	cmd.Flags().StringVar(&options.Status, "status", "", "Filter sandboxes by status: pending, running, stopped, failed, or deleting (comma-separated)")
	cmd.Flags().BoolVar(&options.Verbose, "verbose", false, "Show more sandbox details")
	cmd.Flags().StringArrayVar(&options.Labels, "label", nil, "Filter runs by label key=value (repeatable, ANDed)")
	return cmd
}
