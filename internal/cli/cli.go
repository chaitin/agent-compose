package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"agent-compose/pkg/config"
)

type Runner func(context.Context) error

func Execute(ctx context.Context, out, errOut io.Writer, args []string, runDaemon Runner) int {
	cmd := NewRootCommand(out, errOut, runDaemon)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(errOut, err)
		return commandExitCode(err)
	}
	return 0
}

func NewRootCommand(out, errOut io.Writer, runDaemon Runner) *cobra.Command {
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
			_, err := fmt.Fprintln(cmd.OutOrStdout(), config.BuildVersion)
			return err
		},
	}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Query daemon status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			clientConfig, err := ResolveClientConfig(options.Host)
			if err != nil {
				return err
			}
			body, err := fetchDaemonVersion(cmd.Context(), clientConfig)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), string(body))
			return err
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

	upCmd := &cobra.Command{
		Use:   "up",
		Short: "Apply the current compose project to the daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeUpCommand(cmd, options)
		},
	}

	downCmd := &cobra.Command{
		Use:   "down",
		Short: "Stop project schedulers and running sessions",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeDownCommand(cmd, options)
		},
	}

	runOptions := composeRunOptions{}
	runCmd := &cobra.Command{
		Use:   "run <agent> [prompt...]",
		Short: "Run a project agent",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeRunCommand(cmd, options, runOptions, args)
		},
	}
	runCmd.Flags().StringVar(&runOptions.Prompt, "prompt", "", "Prompt to send to the agent")
	runCmd.Flags().StringVar(&runOptions.SessionID, "session-id", "", "Reuse an existing session")
	runCmd.Flags().BoolVar(&runOptions.KeepRunning, "keep-running", false, "Keep the session runtime running after completion")

	logsOptions := composeLogsOptions{}
	logsCmd := &cobra.Command{
		Use:   "logs",
		Short: "Print project run logs",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeLogsCommand(cmd, options, logsOptions)
		},
	}
	logsCmd.Flags().StringVar(&logsOptions.AgentName, "agent", "", "Filter logs by agent name")
	logsCmd.Flags().StringVar(&logsOptions.RunID, "run-id", "", "Filter logs by run id")
	logsCmd.Flags().StringVar(&logsOptions.SessionID, "session-id", "", "Filter logs by session id")
	logsCmd.Flags().BoolVar(&logsOptions.Follow, "follow", false, "Follow running run output")

	psCmd := &cobra.Command{
		Use:   "ps",
		Short: "List project agents and runtime state",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposePSCommand(cmd, options)
		},
	}

	execOptions := composeExecOptions{}
	execCmd := &cobra.Command{
		Use:   "exec [flags] <command> [args...]",
		Short: "Execute a command in a running project session",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeExecCommand(cmd, options, execOptions, args)
		},
	}
	execCmd.Flags().StringVar(&execOptions.AgentName, "agent", "", "Select a running session by agent")
	execCmd.Flags().StringVar(&execOptions.RunID, "run-id", "", "Execute in the session linked to a run")
	execCmd.Flags().StringVar(&execOptions.SessionID, "session-id", "", "Execute in a specific session")
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
		Use:   "pull <image>",
		Short: "Pull an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImagePullCommand(cmd, options, pullOptions, args[0])
		},
	}
	addImagePullFlags(pullCmd, &pullOptions)
	imagePullOptions := composeImagePullOptions{}
	imagePullCmd := &cobra.Command{
		Use:   "pull <image>",
		Short: "Pull an image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeImagePullCommand(cmd, options, imagePullOptions, args[0])
		},
	}
	addImagePullFlags(imagePullCmd, &imagePullOptions)

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
	imageCmd.AddCommand(imageLSCmd, imagePullCmd, imageRemoveCmd, imageInspectCmd)

	inspectCmd := &cobra.Command{
		Use:   "inspect <project|agent|run|session> [name-or-id]",
		Short: "Inspect project, agent, run, or session details",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runComposeInspectCommand(cmd, options, args)
		},
	}

	root.AddCommand(daemonCmd, versionCmd, statusCmd, configCmd, upCmd, downCmd, runCmd, logsCmd, psCmd, execCmd, imagesCmd, imageCmd, pullCmd, rmiCmd, inspectCmd)
	return root
}

type cliOptions struct {
	Host        string
	ComposeFile string
	ProjectName string
	JSON        bool
}

type ClientConfig struct {
	BaseURL       string
	SocketPath    string
	Source        string
	SourceValue   string
	UseUnixSocket bool
}

type composeConfigOptions struct {
	Quiet bool
}

type composeRunOptions struct {
	Prompt      string
	SessionID   string
	KeepRunning bool
}

type composeLogsOptions struct {
	AgentName string
	RunID     string
	SessionID string
	Follow    bool
}

type composeExecOptions struct {
	AgentName string
	RunID     string
	SessionID string
	Cwd       string
}

type composeImageListOptions struct {
	Query string
	All   bool
}

type composeImagePullOptions struct {
	Platform string
}

type composeImageRemoveOptions struct {
	Force         bool
	PruneChildren bool
}

func addImageListFlags(cmd *cobra.Command, options *composeImageListOptions) {
	cmd.Flags().StringVar(&options.Query, "query", "", "Filter images by reference")
	cmd.Flags().BoolVarP(&options.All, "all", "a", false, "Show all images")
}

func addImagePullFlags(cmd *cobra.Command, options *composeImagePullOptions) {
	cmd.Flags().StringVar(&options.Platform, "platform", "", "Pull platform as os/arch[/variant]")
}

func addImageRemoveFlags(cmd *cobra.Command, options *composeImageRemoveOptions) {
	cmd.Flags().BoolVar(&options.Force, "force", false, "Force image removal")
	cmd.Flags().BoolVar(&options.PruneChildren, "prune-children", false, "Remove untagged child images")
}
