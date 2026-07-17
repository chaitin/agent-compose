package main

import (
	"agent-compose/pkg/config"
	driverpkg "agent-compose/pkg/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"runtime"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type buildInfo struct {
	Version         string   `json:"version"`
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	CompiledDrivers []string `json:"compiled_drivers"`
}

func buildInfoForVersion(version string) buildInfo {
	return buildInfo{
		Version:         version,
		OS:              runtime.GOOS,
		Arch:            runtime.GOARCH,
		CompiledDrivers: driverpkg.CompiledRuntimeDrivers(),
	}
}

func currentBuildInfo() buildInfo {
	return buildInfoForVersion(config.BuildVersion)
}

func newRootCommand(out, errOut io.Writer, runDaemon daemonRunner) *cobra.Command {
	options := cliOptions{}
	root := &cobra.Command{
		Use:           "agent-compose",
		Short:         "agent-compose daemon and CLI",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetFlagErrorFunc(func(cmd *cobra.Command, err error) error {
		return commandExitError{Code: exitCodeUsage, Err: err}
	})
	root.CompletionOptions.DisableDefaultCmd = true

	root.PersistentFlags().StringVar(&options.Host, "host", "", "Daemon HTTP endpoint")
	root.PersistentFlags().StringVarP(&options.ComposeFile, "file", "f", "", "Path to agent-compose.yml")
	root.PersistentFlags().StringVar(&options.ProjectName, "project-name", "", "Override compose project name")
	root.PersistentFlags().BoolVar(&options.JSON, "json", false, "Print machine-readable JSON")

	daemonCmd := &cobra.Command{
		Use:   "daemon",
		Short: "Start the agent-compose daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDaemon(cmd.Context())
		},
	}

	versionCmd := &cobra.Command{
		Use:   "version",
		Short: "Print build version",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if options.JSON {
				data, err := json.MarshalIndent(currentBuildInfo(), "", "  ")
				if err != nil {
					return err
				}
				return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
			}
			_, err := fmt.Fprintln(cmd.OutOrStdout(), config.BuildVersion)
			return err
		},
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Query daemon status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientConfig, err := resolveCLIClientConfig(options.Host)
			if err != nil {
				return err
			}
			body, err := fetchDaemonVersion(cmd.Context(), clientConfig)
			if err != nil {
				return err
			}
			if options.JSON {
				_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
				return err
			}
			return writeDaemonStatusText(cmd.OutOrStdout(), body)
		},
	}

	configOptions := composeConfigOptions{}
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Validate and print normalized compose config",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeConfigCommand(cmd, options, configOptions)
		},
	}
	configCmd.Flags().BoolVar(&configOptions.Quiet, "quiet", false, "Only validate config")

	projectCmd := newCLIProjectCommand(&options)
	agentCmd := newCLIAgentCommand(&options)
	listCmd := newCLIAgentListCommand(&options)
	upCmd := newCLIProjectUpCommand(&options)
	downCmd := newCLIProjectDownCommand(&options)

	runOptions := composeRunOptions{}
	runCmd := &cobra.Command{
		Use:   "run <agent>",
		Short: "Run a project agent",
		Args:  composeRunArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeRunCommand(cmd, options, runOptions, args)
		},
	}
	runCmd.Flags().StringVar(&runOptions.Prompt, "prompt", "", "Prompt to send to the agent")
	runCmd.Flags().StringVar(&runOptions.Command, "command", "", "Bash command to execute in the agent sandbox")
	runCmd.Flags().StringVar(&runOptions.SandboxID, "sandbox", "", "Reuse an existing sandbox")
	runCmd.Flags().StringVar(&runOptions.Driver, "driver", "", "Runtime driver override for a new sandbox")
	runCmd.Flags().BoolVar(&runOptions.KeepRunning, "keep-running", false, "Keep the sandbox runtime running after completion")
	runCmd.Flags().BoolVar(&runOptions.Remove, "rm", false, "Remove the sandbox after a successful run")
	runCmd.Flags().BoolVar(&runOptions.Jupyter, "jupyter", false, "Enable Jupyter for this run")
	runCmd.Flags().BoolVar(&runOptions.JupyterExpose, "jupyter-expose", false, "Mark the Jupyter proxy endpoint for this run as user-accessible")
	runCmd.Flags().BoolVarP(&runOptions.Detach, "detach", "d", false, "Start the run in the daemon and return immediately")
	runCmd.Flags().BoolVarP(&runOptions.Interactive, "interactive", "i", false, "Reserved for future interactive runs")
	runCmd.Flags().BoolVarP(&runOptions.TTY, "tty", "t", false, "Allocate a TTY for interactive command runs")
	runCmd.Flags().Lookup("prompt").NoOptDefVal = optionalRunModeFlagNoValue
	runCmd.Flags().Lookup("command").NoOptDefVal = optionalRunModeFlagNoValue
	hideOptionalFlagNoValueInUsage(runCmd, "prompt", "command")

	schedulerTriggerOptions := composeSchedulerTriggerOptions{}
	schedulerRunsOptions := composeSchedulerRunsOptions{}
	schedulerLogsOptions := composeSchedulerLogsOptions{}
	schedulerCmd := &cobra.Command{
		Use:   "scheduler",
		Short: "Run, inspect, and operate project schedulers, runs, logs, and triggers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	schedulerRunOptions := composeSchedulerTriggerOptions{}
	schedulerRunCmd := &cobra.Command{
		Use:   "run <agent>",
		Short: "Run a scheduler main function",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerMainCommand(cmd, options, schedulerRunOptions, args[0])
		},
	}
	addComposeSchedulerExecutionFlags(schedulerRunCmd, &schedulerRunOptions)
	schedulerListOptions := composeSchedulerListOptions{}
	schedulerLSCmd := &cobra.Command{
		Use:   "ls [agent]",
		Short: "List project scheduler triggers",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerListCommand(cmd, options, schedulerListOptions, args)
		},
	}
	schedulerLSCmd.Flags().BoolVar(&schedulerListOptions.Verbose, "verbose", false, "Show full scheduler and trigger IDs")
	schedulerTriggerCmd := &cobra.Command{
		Use:   "trigger <agent> <trigger>",
		Short: "Manually run a scheduler trigger",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerTriggerCommand(cmd, options, schedulerTriggerOptions, args[0], args[1])
		},
	}
	addComposeSchedulerExecutionFlags(schedulerTriggerCmd, &schedulerTriggerOptions)
	schedulerRunsCmd := &cobra.Command{
		Use:   "runs [scheduler]",
		Short: "List project scheduler runs",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerRunsCommand(cmd, options, schedulerRunsOptions, args)
		},
	}
	schedulerRunsCmd.Flags().StringVar(&schedulerRunsOptions.AgentName, "agent", "", "Filter by agent name or id")
	schedulerRunsCmd.Flags().StringVar(&schedulerRunsOptions.Trigger, "trigger", "", "Filter by trigger name or id")
	schedulerRunsCmd.Flags().StringVar(&schedulerRunsOptions.Status, "status", "", "Filter by run status")
	schedulerRunsCmd.Flags().Uint32Var(&schedulerRunsOptions.Limit, "limit", 20, "Maximum runs to show")
	schedulerLogsCmd := &cobra.Command{
		Use:   "logs [run]",
		Short: "Print scheduler run logs",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerLogsCommand(cmd, options, schedulerLogsOptions, args)
		},
	}
	schedulerLogsCmd.Flags().StringVar(&schedulerLogsOptions.AgentName, "agent", "", "Filter by agent name or id")
	schedulerLogsCmd.Flags().StringVar(&schedulerLogsOptions.Trigger, "trigger", "", "Filter by trigger name or id")
	schedulerLogsCmd.Flags().StringVar(&schedulerLogsOptions.RunID, "run", "", "Filter by scheduler run id")
	schedulerLogsCmd.Flags().IntVarP(&schedulerLogsOptions.Tail, "tail", "n", -1, "Show the last N log events")
	schedulerStopOptions := composeSchedulerStopOptions{}
	schedulerStopCmd := &cobra.Command{
		Use:   "stop <run>",
		Short: "Stop an active scheduler run",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerStopCommand(cmd, options, schedulerStopOptions, args[0])
		},
	}
	schedulerStopCmd.Flags().StringVar(&schedulerStopOptions.Reason, "reason", "", "Reason recorded for the canceled run")
	schedulerInspectCmd := &cobra.Command{
		Use:   "inspect <name-or-id> [trigger]",
		Short: "Inspect a scheduler, trigger, or scheduler run",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSchedulerInspectCommand(cmd, options, args)
		},
	}
	schedulerCmd.AddCommand(schedulerLSCmd, schedulerRunCmd, schedulerTriggerCmd, schedulerRunsCmd, schedulerLogsCmd, schedulerStopCmd, schedulerInspectCmd)

	logsOptions := composeLogsOptions{}
	logsCmd := &cobra.Command{
		Use:   "logs [agent-or-id]",
		Short: "Print project run logs",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeLogsCommand(cmd, options, logsOptions, args)
		},
	}
	logsCmd.Flags().StringVar(&logsOptions.AgentName, "agent", "", "Filter logs by agent name")
	logsCmd.Flags().StringVar(&logsOptions.RunID, "run", "", "Filter logs by run id")
	logsCmd.Flags().StringVar(&logsOptions.SandboxID, "sandbox", "", "Filter logs by sandbox id")
	logsCmd.Flags().BoolVar(&logsOptions.Follow, "follow", false, "Follow running run output")
	logsCmd.Flags().IntVarP(&logsOptions.TailLines, "tail", "n", -1, "Show the last N lines of run output")
	logsCmd.Flags().BoolVarP(&logsOptions.Timestamp, "timestamp", "t", false, "Prefix text log lines with a run-level timestamp")

	psOptions := composePSOptions{}
	psCmd := &cobra.Command{
		Use:   "ps",
		Short: "List project sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePSCommand(cmd, options, psOptions)
		},
	}
	psCmd.Flags().BoolVarP(&psOptions.All, "all", "a", false, "Show current project sandboxes in all statuses")
	psCmd.Flags().StringVar(&psOptions.Status, "status", "", "Filter sandboxes by status, comma-separated")
	psCmd.Flags().BoolVar(&psOptions.Verbose, "verbose", false, "Show more sandbox details")

	statsCmd := &cobra.Command{
		Use:   "stats [sandbox]",
		Short: "Print sandbox resource stats",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeStatsCommand(cmd, options, args)
		},
	}

	stopCmd := &cobra.Command{
		Use:   "stop <sandbox> [<sandbox N>]",
		Short: "Stop one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "stop", "stopped", args)
		},
	}

	resumeCmd := &cobra.Command{
		Use:   "resume <sandbox> [<sandbox N>]",
		Short: "Resume one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "resume", "resumed", args)
		},
	}

	removeSandboxOptions := composeSandboxRemoveOptions{}
	rmCmd := &cobra.Command{
		Use:   "rm <sandbox> [<sandbox N>]",
		Short: "Remove one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxRemoveCommand(cmd, options, removeSandboxOptions, args)
		},
	}
	rmCmd.Flags().BoolVar(&removeSandboxOptions.Force, "force", false, "Force remove running sandboxes")

	sandboxPSOptions := composePSOptions{}
	sandboxLSCmd := &cobra.Command{
		Use:   "ls",
		Short: "List project sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePSCommand(cmd, options, sandboxPSOptions)
		},
	}
	sandboxLSCmd.Flags().BoolVarP(&sandboxPSOptions.All, "all", "a", false, "Show current project sandboxes in all statuses")
	sandboxLSCmd.Flags().StringVar(&sandboxPSOptions.Status, "status", "", "Filter sandboxes by status, comma-separated")
	sandboxLSCmd.Flags().BoolVar(&sandboxPSOptions.Verbose, "verbose", false, "Show more sandbox details")

	sandboxStopCmd := &cobra.Command{
		Use:   "stop <sandbox> [<sandbox N>]",
		Short: "Stop one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "stop", "stopped", args)
		},
	}

	sandboxResumeCmd := &cobra.Command{
		Use:   "resume <sandbox> [<sandbox N>]",
		Short: "Resume one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxActionCommand(cmd, options, "resume", "resumed", args)
		},
	}

	sandboxRemoveOptions := composeSandboxRemoveOptions{}
	sandboxRMCmd := &cobra.Command{
		Use:   "rm <sandbox> [<sandbox N>]",
		Short: "Remove one or more sandboxes",
		Args:  sandboxActionArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxRemoveCommand(cmd, options, sandboxRemoveOptions, args)
		},
	}
	sandboxRMCmd.Flags().BoolVar(&sandboxRemoveOptions.Force, "force", false, "Force remove running sandboxes")

	sandboxPruneOptions := composeSandboxPruneOptions{}
	sandboxPruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune stopped or failed sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeSandboxPruneCommand(cmd, options, sandboxPruneOptions)
		},
	}
	addSandboxPruneFlags(sandboxPruneCmd, &sandboxPruneOptions)

	sandboxCmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage project sandboxes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	sandboxCmd.AddCommand(sandboxLSCmd, sandboxStopCmd, sandboxResumeCmd, sandboxRMCmd, sandboxPruneCmd)

	execOptions := composeExecOptions{}
	execCmd := &cobra.Command{
		Use:   "exec <sandbox> (--command <shell-command> | --prompt <prompt> | -- <command> [args...])",
		Short: "Execute a command in a running sandbox",
		Args:  composeExecArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeExecCommand(cmd, options, execOptions, args)
		},
	}

	execCmd.Flags().StringVar(&execOptions.RunID, "run", "", "Deprecated target selection by run; use exec <sandbox>")
	execCmd.Flags().StringVar(&execOptions.Command, "command", "", "Shell command to execute in the sandbox")
	execCmd.Flags().StringVar(&execOptions.Prompt, "prompt", "", "Prompt the sandbox agent and attach to the response")
	execCmd.Flags().BoolVarP(&execOptions.Interactive, "interactive", "i", false, "Attach stdin to the sandbox command")
	execCmd.Flags().BoolVarP(&execOptions.TTY, "tty", "t", false, "Allocate a TTY for interactive exec")
	execCmd.Flags().StringVar(&execOptions.Cwd, "cwd", "", "Guest working directory")

	imageListOptions := composeImageListOptions{}
	imagesCmd := &cobra.Command{
		Use:   "images",
		Short: "List daemon images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImageListCommand(cmd, options, imageListOptions)
		},
	}
	addImageListFlags(imagesCmd, &imageListOptions)

	cacheCmd := &cobra.Command{
		Use:   "cache",
		Short: "Manage daemon runtime caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	cacheLSOptions := composeCacheFilterOptions{}
	cacheLSCmd := &cobra.Command{
		Use:   "ls",
		Short: "List daemon runtime caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCacheListCommand(cmd, options, cacheLSOptions)
		},
	}
	addCacheFilterFlags(cacheLSCmd, &cacheLSOptions)
	cacheInspectCmd := &cobra.Command{
		Use:   "inspect <cache-id>",
		Short: "Inspect a daemon runtime cache item",
		Args:  cacheInspectArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCacheInspectCommand(cmd, options, args[0])
		},
	}
	cachePruneOptions := composeCachePruneOptions{}
	cachePruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune daemon runtime caches",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCachePruneCommand(cmd, options, cachePruneOptions)
		},
	}
	addCachePruneFlags(cachePruneCmd, &cachePruneOptions)
	cacheRemoveOptions := composeCacheRemoveOptions{}
	cacheRemoveCmd := &cobra.Command{
		Use:   "rm <cache-id>",
		Short: "Remove a daemon runtime cache item",
		Args:  cacheRemoveArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeCacheRemoveCommand(cmd, options, cacheRemoveOptions, args[0])
		},
	}
	addCacheRemoveFlags(cacheRemoveCmd, &cacheRemoveOptions)
	cacheCmd.AddCommand(cacheLSCmd, cacheInspectCmd, cachePruneCmd, cacheRemoveCmd)

	volumeCmd := &cobra.Command{
		Use:   "volume",
		Short: "Manage daemon volumes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	volumeLSOptions := composeVolumeListOptions{}
	volumeLSCmd := &cobra.Command{
		Use:   "ls",
		Short: "List daemon volumes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeVolumeListCommand(cmd, options, volumeLSOptions)
		},
	}
	addVolumeListFlags(volumeLSCmd, &volumeLSOptions)
	volumeLSCmd.Flags().BoolVar(&volumeLSOptions.Verbose, "verbose", false, "Show the full project id")
	volumeCreateOptions := composeVolumeCreateOptions{}
	volumeCreateCmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a daemon volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeVolumeCreateCommand(cmd, options, volumeCreateOptions, args[0])
		},
	}
	addVolumeCreateFlags(volumeCreateCmd, &volumeCreateOptions)
	volumeInspectCmd := &cobra.Command{
		Use:   "inspect <name>",
		Short: "Inspect a daemon volume",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeVolumeInspectCommand(cmd, options, args[0])
		},
	}
	volumeRemoveOptions := composeVolumeRemoveOptions{}
	volumeRemoveCmd := &cobra.Command{
		Use:     "rm <name> [<name N>]",
		Aliases: []string{"remove"},
		Short:   "Remove one or more daemon volumes",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeVolumeRemoveCommand(cmd, options, volumeRemoveOptions, args)
		},
	}
	addVolumeRemoveFlags(volumeRemoveCmd, &volumeRemoveOptions)
	volumePruneOptions := composeVolumePruneOptions{}
	volumePruneCmd := &cobra.Command{
		Use:   "prune",
		Short: "Prune unused daemon volumes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeVolumePruneCommand(cmd, options, volumePruneOptions)
		},
	}
	addVolumePruneFlags(volumePruneCmd, &volumePruneOptions)
	volumeCmd.AddCommand(volumeLSCmd, volumeCreateCmd, volumeInspectCmd, volumeRemoveCmd, volumePruneCmd)

	imageCmd := &cobra.Command{
		Use:   "image",
		Short: "Manage daemon images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return cmd.Help()
		},
	}
	imageLSOptions := composeImageListOptions{}
	imageLSCmd := &cobra.Command{
		Use:   "ls",
		Short: "List daemon images",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImageListCommand(cmd, options, imageLSOptions)
		},
	}
	addImageListFlags(imageLSCmd, &imageLSOptions)

	pullOptions := composeImagePullOptions{}
	pullCmd := &cobra.Command{
		Use:   "pull [image]",
		Short: "Pull an image or all project images",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePullCommand(cmd, options, pullOptions, args)
		},
	}
	addImagePullFlags(pullCmd, &pullOptions)
	imagePullOptions := composeImagePullOptions{}
	imagePullCmd := &cobra.Command{
		Use:   "pull [image]",
		Short: "Pull an image or all project images",
		Args:  cobra.RangeArgs(0, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePullCommand(cmd, options, imagePullOptions, args)
		},
	}
	addImagePullFlags(imagePullCmd, &imagePullOptions)

	buildOptions := composeImageBuildOptions{}
	buildCmd := &cobra.Command{
		Use:   "build [agent...]",
		Short: "Build project agent images",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeBuildCommand(cmd, options, buildOptions, args)
		},
	}
	addImageBuildFlags(buildCmd, &buildOptions)
	imageBuildOptions := composeImageBuildOptions{}
	imageBuildCmd := &cobra.Command{
		Use:   "build [agent...]",
		Short: "Build project agent images",
		Args:  cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeBuildCommand(cmd, options, imageBuildOptions, args)
		},
	}
	addImageBuildFlags(imageBuildCmd, &imageBuildOptions)

	removeOptions := composeImageRemoveOptions{}
	rmiCmd := &cobra.Command{
		Use:   "rmi <image>",
		Short: "Remove an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImageRemoveCommand(cmd, options, removeOptions, args[0])
		},
	}
	addImageRemoveFlags(rmiCmd, &removeOptions)
	imageRemoveOptions := composeImageRemoveOptions{}
	imageRemoveCmd := &cobra.Command{
		Use:   "rm <image>",
		Short: "Remove an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImageRemoveCommand(cmd, options, imageRemoveOptions, args[0])
		},
	}
	addImageRemoveFlags(imageRemoveCmd, &imageRemoveOptions)

	imageInspectCmd := &cobra.Command{
		Use:   "inspect <image>",
		Short: "Inspect an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImageInspectCommand(cmd, options, args[0])
		},
	}
	imageCmd.AddCommand(imageLSCmd, imagePullCmd, imageBuildCmd, imageRemoveCmd, imageInspectCmd)

	inspectCmd := &cobra.Command{
		Use:   "inspect <id>|<project|agent|run|sandbox|image|cache|volume> [name-or-id]",
		Short: "Inspect project, agent, run, sandbox, image, cache, or volume details",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeInspectCommand(cmd, options, args)
		},
	}

	authCmd := newCLIAuthCommand(&options)
	root.AddCommand(daemonCmd, versionCmd, statusCmd, authCmd, configCmd, projectCmd, agentCmd, listCmd, upCmd, downCmd, runCmd, schedulerCmd, logsCmd, psCmd, statsCmd, sandboxCmd, stopCmd, resumeCmd, rmCmd, execCmd, imagesCmd, cacheCmd, volumeCmd, imageCmd, pullCmd, buildCmd, rmiCmd, inspectCmd)
	return root
}

type cliOptions struct {
	Host        string
	ComposeFile string
	ProjectName string
	JSON        bool
}

func hideOptionalFlagNoValueInUsage(cmd *cobra.Command, flagNames ...string) {
	usageFunc := cmd.UsageFunc()
	cmd.SetUsageFunc(func(c *cobra.Command) error {
		return withHiddenOptionalFlagNoValue(c, flagNames, func() error {
			return usageFunc(c)
		})
	})
}

func withHiddenOptionalFlagNoValue(cmd *cobra.Command, flagNames []string, fn func() error) error {
	type flagRestore struct {
		name        string
		noOptDefVal string
	}
	var restores []flagRestore
	for _, name := range flagNames {
		flag := cmd.Flags().Lookup(name)
		if flag == nil || flag.NoOptDefVal != optionalRunModeFlagNoValue {
			continue
		}
		restores = append(restores, flagRestore{name: name, noOptDefVal: flag.NoOptDefVal})
		flag.NoOptDefVal = ""
	}
	defer func() {
		for _, restore := range restores {
			if flag := cmd.Flags().Lookup(restore.name); flag != nil {
				flag.NoOptDefVal = restore.noOptDefVal
			}
		}
	}()
	return fn()
}

type commandExitError struct {
	Code int
	Err  error
}

func (e commandExitError) Error() string {
	if e.Err == nil {
		return ""
	}
	return e.Err.Error()
}

func (e commandExitError) Unwrap() error {
	return e.Err
}

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr commandExitError
	if errors.As(err, &exitErr) && exitErr.Code > 0 {
		return exitErr.Code
	}
	if isSchedulerResourceNotFound(err) {
		return exitCodeUsage
	}
	return exitCodeGeneral
}

func commandExitErrorForConnect(err error) error {
	if isAttachHTTP2TransportMismatch(err) {
		return commandExitError{
			Code: exitCodeUnavailable,
			Err:  fmt.Errorf("%w; attach RPCs require HTTP/2 h2c, restart the agent-compose daemon with a matching build or connect directly without an HTTP/1 proxy", err),
		}
	}
	switch connect.CodeOf(err) {
	case connect.CodeUnimplemented:
		return commandExitError{Code: exitCodeUnsupported, Err: err}
	case connect.CodeUnavailable:
		return commandExitError{Code: exitCodeUnavailable, Err: err}
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeNotFound:
		return commandExitError{Code: exitCodeUsage, Err: err}
	default:
		return err
	}
}
