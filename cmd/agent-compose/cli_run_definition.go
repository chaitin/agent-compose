package main

import "github.com/spf13/cobra"

func newCLIRunCommand(cli *cliOptions) *cobra.Command {
	options := composeRunOptions{}
	cmd := &cobra.Command{
		Use:   "run <agent>",
		Short: "Run a project agent",
		Args:  composeRunArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeRunCommand(cmd, *cli, options, args)
		},
	}
	cmd.Flags().StringVar(&options.Prompt, "prompt", "", "Prompt to send to the agent")
	cmd.Flags().StringVar(&options.Command, "command", "", "Bash command to execute in the agent sandbox")
	cmd.Flags().StringVar(&options.SandboxID, "sandbox", "", "Reuse an existing sandbox")
	cmd.Flags().StringVar(&options.Driver, "driver", "", "Runtime driver override for a new sandbox")
	cmd.Flags().BoolVar(&options.KeepRunning, "keep-running", false, "Keep the sandbox runtime running after completion")
	cmd.Flags().BoolVar(&options.Remove, "rm", false, "Remove a newly created sandbox after run completion")
	cmd.Flags().BoolVar(&options.Jupyter, "jupyter", false, "Enable Jupyter for this run")
	cmd.Flags().BoolVar(&options.JupyterExpose, "jupyter-expose", false, "Mark the Jupyter proxy endpoint for this run as user-accessible")
	cmd.Flags().BoolVarP(&options.Detach, "detach", "d", false, "Start the run in the daemon and return immediately")
	cmd.Flags().BoolVarP(&options.Interactive, "interactive", "i", false, "Reserved for future interactive runs")
	cmd.Flags().BoolVarP(&options.TTY, "tty", "t", false, "Allocate a TTY for interactive command runs")
	cmd.Flags().StringArrayVar(&options.Labels, "label", nil, "Attach a key=value label to this run (repeatable)")
	cmd.Flags().Lookup("prompt").NoOptDefVal = optionalRunModeFlagNoValue
	cmd.Flags().Lookup("command").NoOptDefVal = optionalRunModeFlagNoValue
	hideOptionalFlagNoValueInUsage(cmd, "prompt", "command")
	return cmd
}
