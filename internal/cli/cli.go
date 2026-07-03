package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"agent-compose/pkg/agentcompose/projects"
	agentcompose "agent-compose/pkg/agentcompose/service"
	"agent-compose/pkg/compose"
	"agent-compose/pkg/config"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	"agent-compose/proto/agentcompose/v1/agentcomposev1connect"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
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

func runComposeConfigCommand(cmd *cobra.Command, cli cliOptions, options composeConfigOptions) error {
	_, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	if options.Quiet {
		return nil
	}

	var data []byte
	if cli.JSON {
		data, err = normalized.MarshalCanonicalJSON(true)
	} else {
		data, err = normalized.MarshalCanonicalYAML(true)
	}
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), data)
}

func runComposeUpCommand(cmd *cobra.Command, cli cliOptions) error {
	composePath, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return err
	}
	specHash, err := normalized.Hash()
	if err != nil {
		return fmt.Errorf("%s: hash normalized compose spec: %w", composePath, err)
	}
	clientConfig, err := ResolveClientConfig(cli.Host)
	if err != nil {
		return err
	}
	client := agentcomposev2connect.NewProjectServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	resp, err := client.ApplyProject(cmd.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: agentcompose.ProjectSpecResponse(normalized),
		Source: &agentcomposev2.ProjectSource{
			ComposePath: composePath,
			ProjectDir:  filepath.Dir(composePath),
		},
		ExpectedSpecHash: specHash,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("apply project %s: %w", normalized.Name, err))
	}
	msg := resp.Msg
	if len(msg.GetIssues()) > 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("apply project %s: %s", normalized.Name, formatProjectValidationIssues(msg.GetIssues()))}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(composeUpOutputFromResponse(msg), "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeComposeUpText(cmd.OutOrStdout(), msg)
}

func runComposeDownCommand(cmd *cobra.Command, cli cliOptions) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.project.RemoveProject(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("down project %s: %w", normalized.Name, err))
	}
	output := composeDownOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	} else if err := writeComposeDownText(cmd.OutOrStdout(), output); err != nil {
		return err
	}
	if output.FailedSessionStops > 0 {
		return commandExitError{
			Code: exitCodeGeneral,
			Err:  fmt.Errorf("down project %s completed with %d session stop failure(s)", normalized.Name, output.FailedSessionStops),
		}
	}
	return nil
}

func runComposeRunCommand(cmd *cobra.Command, cli cliOptions, options composeRunOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	agentName := strings.TrimSpace(args[0])
	prompt := strings.TrimSpace(options.Prompt)
	if prompt == "" && len(args) > 1 {
		prompt = strings.Join(args[1:], " ")
	}
	clientConfig, err := ResolveClientConfig(cli.Host)
	if err != nil {
		return err
	}
	cleanupPolicy := agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_STOP_ON_COMPLETION
	if options.KeepRunning {
		cleanupPolicy = agentcomposev2.RunSessionCleanupPolicy_RUN_SESSION_CLEANUP_POLICY_KEEP_RUNNING
	}
	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	stream, err := client.RunAgentStream(cmd.Context(), connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       agentName,
		Prompt:          prompt,
		Source:          agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
		SessionId:       strings.TrimSpace(options.SessionID),
		CleanupPolicy:   cleanupPolicy,
		ClientRequestId: manualRunClientRequestID(normalized.Name, agentName, prompt),
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", normalized.Name, agentName, err))
	}
	var completed *agentcomposev2.RunSummary
	for stream.Receive() {
		event := stream.Msg()
		switch event.GetEventType() {
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT:
			if cli.JSON {
				continue
			}
			target := cmd.OutOrStdout()
			if event.GetIsStderr() {
				target = cmd.ErrOrStderr()
			}
			if _, err := io.WriteString(target, event.GetChunk()); err != nil {
				return err
			}
		case agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED:
			completed = event.GetRun()
		}
	}
	if err := stream.Err(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("run project %s agent %s: %w", normalized.Name, agentName, err))
	}
	if completed == nil {
		return fmt.Errorf("run project %s agent %s: stream completed without terminal run", normalized.Name, agentName)
	}
	detail, err := getRunDetail(cmd.Context(), client, projectID, completed.GetRunId())
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", completed.GetRunId(), normalized.Name, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(composeRunOutputFromDetail(detail.Msg.GetRun()), "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if runSummaryFailed(completed) {
		return commandExitError{Code: runSummaryExitCode(completed), Err: fmt.Errorf("run %s for project %s agent %s failed: %s", completed.GetRunId(), normalized.Name, agentName, firstNonEmptyString(completed.GetError(), runStatusText(completed.GetStatus())))}
	}
	return nil
}

func runComposeLogsCommand(cmd *cobra.Command, cli cliOptions, options composeLogsOptions) error {
	if cli.JSON && options.Follow {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --json cannot be combined with --follow")}
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clientConfig, err := ResolveClientConfig(cli.Host)
	if err != nil {
		return err
	}
	client := agentcomposev2connect.NewRunServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	if strings.TrimSpace(options.RunID) != "" {
		run, err := getRunDetail(cmd.Context(), client, projectID, options.RunID)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", strings.TrimSpace(options.RunID), normalized.Name, err))
		}
		return writeLogsForRun(cmd.OutOrStdout(), run.Msg.GetRun(), cli.JSON)
	}
	return followOrPrintProjectLogs(cmd, cli, client, projectID, normalized.Name, options)
}

func runComposePSCommand(cmd *cobra.Command, cli cliOptions) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID},
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("get project %s: %w", normalized.Name, err))
	}
	output, err := composePSOutputFromProject(cmd.Context(), clients, project.Msg.GetProject())
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("build ps for project %s: %w", normalized.Name, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writePSText(cmd.OutOrStdout(), output)
}

func runComposeExecCommand(cmd *cobra.Command, cli cliOptions, options composeExecOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	req := &agentcomposev2.ExecRequest{
		Command: &agentcomposev2.ExecCommand{Command: args[0], Args: append([]string(nil), args[1:]...)},
		Cwd:     strings.TrimSpace(options.Cwd),
	}
	switch {
	case strings.TrimSpace(options.SessionID) != "":
		req.Target = &agentcomposev2.ExecRequest_SessionId{SessionId: strings.TrimSpace(options.SessionID)}
	case strings.TrimSpace(options.RunID) != "":
		req.Target = &agentcomposev2.ExecRequest_RunId{RunId: strings.TrimSpace(options.RunID)}
	default:
		req.Target = &agentcomposev2.ExecRequest_Selector{Selector: &agentcomposev2.ExecSessionSelector{
			ProjectId:   projectID,
			ProjectName: normalized.Name,
			AgentName:   strings.TrimSpace(options.AgentName),
		}}
	}
	stream, err := clients.exec.ExecStream(cmd.Context(), connect.NewRequest(req))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s: %w", normalized.Name, err))
	}
	var result *agentcomposev2.ExecResult
	for stream.Receive() {
		event := stream.Msg()
		switch event.GetEventType() {
		case agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT:
			if cli.JSON {
				continue
			}
			target := cmd.OutOrStdout()
			if event.GetIsStderr() {
				target = cmd.ErrOrStderr()
			}
			if _, err := io.WriteString(target, event.GetChunk()); err != nil {
				return err
			}
		case agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED:
			result = event.GetResult()
		}
	}
	if err := stream.Err(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("exec project %s: %w", normalized.Name, err))
	}
	if result == nil {
		return fmt.Errorf("exec project %s: stream completed without result", normalized.Name)
	}
	if cli.JSON {
		data, err := json.MarshalIndent(composeExecOutputFromResult(result), "", "  ")
		if err != nil {
			return err
		}
		if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
			return err
		}
	}
	if !result.GetSuccess() {
		return commandExitError{Code: execResultExitCode(result), Err: fmt.Errorf("exec %s in session %s failed: %s", result.GetExecId(), result.GetSessionId(), firstNonEmptyString(result.GetError(), result.GetStderr(), result.GetOutput(), "command failed"))}
	}
	return nil
}

func runComposeImageListCommand(cmd *cobra.Command, cli cliOptions, options composeImageListOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.ListImages(cmd.Context(), connect.NewRequest(&agentcomposev2.ListImagesRequest{
		Query: strings.TrimSpace(options.Query),
		All:   options.All,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list images: %w", err))
	}
	output := composeImageListOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeImagesText(cmd.OutOrStdout(), output.Images)
}

func runComposeImagePullCommand(cmd *cobra.Command, cli cliOptions, options composeImagePullOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	platform, err := parseImagePlatform(options.Platform)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	resp, err := clients.image.PullImage(cmd.Context(), connect.NewRequest(&agentcomposev2.PullImageRequest{
		ImageRef: strings.TrimSpace(imageRef),
		Platform: platform,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("pull image %s: %w", strings.TrimSpace(imageRef), err))
	}
	output := composeImagePullOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Pulled %s\nResolved: %s\n", output.ImageRef, firstNonEmptyString(output.ResolvedRef, "-"))
	return err
}

func runComposeImageRemoveCommand(cmd *cobra.Command, cli cliOptions, options composeImageRemoveOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.RemoveImage(cmd.Context(), connect.NewRequest(&agentcomposev2.RemoveImageRequest{
		ImageRef:      strings.TrimSpace(imageRef),
		Force:         options.Force,
		PruneChildren: options.PruneChildren,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("remove image %s: %w", strings.TrimSpace(imageRef), err))
	}
	output := composeImageRemoveOutputFromResponse(resp.Msg)
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, ref := range output.UntaggedRefs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Untagged: %s\n", ref); err != nil {
			return err
		}
	}
	for _, id := range output.DeletedIDs {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Deleted: %s\n", id); err != nil {
			return err
		}
	}
	if len(output.UntaggedRefs) == 0 && len(output.DeletedIDs) == 0 {
		_, err = fmt.Fprintf(cmd.OutOrStdout(), "Removed: %s\n", output.ImageRef)
		return err
	}
	return nil
}

func runComposeImageInspectCommand(cmd *cobra.Command, cli cliOptions, imageRef string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	resp, err := clients.image.InspectImage(cmd.Context(), connect.NewRequest(&agentcomposev2.InspectImageRequest{
		ImageRef: strings.TrimSpace(imageRef),
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("inspect image %s: %w", strings.TrimSpace(imageRef), err))
	}
	output := composeImageInspectOutputFromResponse(resp.Msg)
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
}

func runComposeInspectCommand(cmd *cobra.Command, cli cliOptions, args []string) error {
	kind := strings.ToLower(strings.TrimSpace(args[0]))
	target := ""
	if len(args) > 1 {
		target = strings.TrimSpace(args[1])
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	var output any
	switch kind {
	case "project":
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
			Project:     &agentcomposev2.ProjectRef{ProjectId: projectID},
			IncludeSpec: true,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect project %s: %w", normalized.Name, err))
		}
		output = composeProjectOutputFromProject(project.Msg.GetProject())
	case "agent":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect agent requires an agent name")}
		}
		project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
			Project:     &agentcomposev2.ProjectRef{ProjectId: projectID},
			IncludeSpec: true,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect agent %s in project %s: %w", target, normalized.Name, err))
		}
		agent, err := composeAgentInspectOutputFor(cmd.Context(), clients, project.Msg.GetProject(), target)
		if err != nil {
			return err
		}
		output = agent
	case "run":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect run requires a run id")}
		}
		run, err := getRunDetail(cmd.Context(), clients.run, projectID, target)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect run %s in project %s: %w", target, normalized.Name, err))
		}
		output = composeRunOutputFromDetail(run.Msg.GetRun())
	case "session":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect session requires a session id")}
		}
		session, err := clients.session.GetSession(cmd.Context(), connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: target}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect session %s: %w", target, err))
		}
		output = composeSessionOutputFromSummary(session.Msg.GetSession().GetSummary())
	default:
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("unsupported inspect target %q", kind)}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	data, err := json.MarshalIndent(output, "", "  ")
	if err != nil {
		return err
	}
	return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
}

func resolveComposeProject(cli cliOptions) (string, *compose.NormalizedProjectSpec, string, error) {
	composePath, normalized, err := loadNormalizedCompose(cli)
	if err != nil {
		return "", nil, "", err
	}
	project, err := projects.NewRecordFromSpec(normalized, composePath)
	if err != nil {
		return "", nil, "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s: resolve project %s: %w", composePath, normalized.Name, err)}
	}
	return composePath, normalized, project.ID, nil
}

func loadNormalizedCompose(cli cliOptions) (string, *compose.NormalizedProjectSpec, error) {
	composePath, err := resolveComposePath(cli.ComposeFile)
	if err != nil {
		return "", nil, err
	}
	spec, err := compose.ParseFile(composePath)
	if err != nil {
		return "", nil, commandExitError{Code: exitCodeUsage, Err: err}
	}
	if projectName := strings.TrimSpace(cli.ProjectName); projectName != "" {
		spec.Name = projectName
	}
	normalized, err := compose.Normalize(spec, compose.NormalizeOptions{ComposePath: composePath})
	if err != nil {
		return "", nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s: %w", composePath, err)}
	}
	return composePath, normalized, nil
}

func resolveComposePath(pathFlag string) (string, error) {
	pathFlag = strings.TrimSpace(pathFlag)
	if pathFlag != "" {
		abs, err := filepath.Abs(pathFlag)
		if err != nil {
			return pathFlag, fmt.Errorf("resolve --file %q: %w", pathFlag, err)
		}
		return abs, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("find agent-compose.yml: %w", err)
	}
	return filepath.Join(wd, "agent-compose.yml"), nil
}

func writeCommandOutput(out io.Writer, data []byte) error {
	if _, err := out.Write(data); err != nil {
		return err
	}
	if len(data) == 0 || data[len(data)-1] == '\n' {
		return nil
	}
	_, err := fmt.Fprintln(out)
	return err
}

type composeUpOutput struct {
	Project   composeUpProjectOutput  `json:"project"`
	Revision  composeUpRevisionOutput `json:"revision"`
	Applied   bool                    `json:"applied"`
	Unchanged bool                    `json:"unchanged"`
	Changes   []composeUpChangeOutput `json:"changes"`
}

type composeDownOutput struct {
	Project            composeUpProjectOutput  `json:"project"`
	Status             string                  `json:"status"`
	FailedSessionStops uint32                  `json:"failed_session_stops"`
	Changes            []composeUpChangeOutput `json:"changes"`
}

type composeUpProjectOutput struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	SourcePath      string `json:"source_path"`
	CurrentRevision uint64 `json:"current_revision"`
	SpecHash        string `json:"spec_hash"`
	AgentCount      uint32 `json:"agent_count"`
	SchedulerCount  uint32 `json:"scheduler_count"`
}

type composeUpRevisionOutput struct {
	Revision uint64 `json:"revision"`
	SpecHash string `json:"spec_hash"`
}

type composeUpChangeOutput struct {
	Action       string `json:"action"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Name         string `json:"name"`
	Message      string `json:"message,omitempty"`
}

type composeRunOutput struct {
	RunID        string `json:"run_id"`
	ProjectID    string `json:"project_id"`
	ProjectName  string `json:"project_name"`
	AgentName    string `json:"agent_name"`
	Source       string `json:"source"`
	Status       string `json:"status"`
	SessionID    string `json:"session_id"`
	ExitCode     int32  `json:"exit_code"`
	Error        string `json:"error,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	DurationMs   int64  `json:"duration_ms,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Output       string `json:"output,omitempty"`
	ResultJSON   string `json:"result_json,omitempty"`
	LogsPath     string `json:"logs_path,omitempty"`
	ArtifactsDir string `json:"artifacts_dir,omitempty"`
	CleanupError string `json:"cleanup_error,omitempty"`
	Driver       string `json:"driver,omitempty"`
	ImageRef     string `json:"image_ref,omitempty"`
}

type composeLogsOutput struct {
	Runs []composeRunOutput `json:"runs"`
}

type cliServiceClients struct {
	project agentcomposev2connect.ProjectServiceClient
	run     agentcomposev2connect.RunServiceClient
	exec    agentcomposev2connect.ExecServiceClient
	image   agentcomposev2connect.ImageServiceClient
	session agentcomposev1connect.SessionServiceClient
}

type composePSOutput struct {
	Project composeUpProjectOutput `json:"project"`
	Agents  []composePSAgentOutput `json:"agents"`
}

type composePSAgentOutput struct {
	AgentName         string                `json:"agent_name"`
	ManagedAgentID    string                `json:"managed_agent_id"`
	SchedulerEnabled  bool                  `json:"scheduler_enabled"`
	SchedulerID       string                `json:"scheduler_id,omitempty"`
	SchedulerTriggers uint32                `json:"scheduler_triggers"`
	LatestRun         *composeRunOutput     `json:"latest_run,omitempty"`
	RunningSession    *composeSessionOutput `json:"running_session,omitempty"`
	Driver            string                `json:"driver,omitempty"`
	Image             string                `json:"image,omitempty"`
}

type composeProjectOutput struct {
	Project    composeUpProjectOutput          `json:"project"`
	Agents     []composeProjectAgentOutput     `json:"agents"`
	Schedulers []composeProjectSchedulerOutput `json:"schedulers"`
}

type composeProjectAgentOutput struct {
	AgentName        string `json:"agent_name"`
	ManagedAgentID   string `json:"managed_agent_id"`
	Provider         string `json:"provider,omitempty"`
	Model            string `json:"model,omitempty"`
	Image            string `json:"image,omitempty"`
	Driver           string `json:"driver,omitempty"`
	SchedulerEnabled bool   `json:"scheduler_enabled"`
}

type composeProjectSchedulerOutput struct {
	AgentName       string `json:"agent_name"`
	SchedulerID     string `json:"scheduler_id"`
	ManagedLoaderID string `json:"managed_loader_id"`
	Enabled         bool   `json:"enabled"`
	TriggerCount    uint32 `json:"trigger_count"`
}

type composeAgentInspectOutput struct {
	Project         composeUpProjectOutput          `json:"project"`
	Agent           composeProjectAgentOutput       `json:"agent"`
	Schedulers      []composeProjectSchedulerOutput `json:"schedulers"`
	LatestRun       *composeRunOutput               `json:"latest_run,omitempty"`
	RunningSessions []composeSessionOutput          `json:"running_sessions,omitempty"`
}

type composeSessionOutput struct {
	SessionID     string            `json:"session_id"`
	Title         string            `json:"title,omitempty"`
	Driver        string            `json:"driver,omitempty"`
	VMStatus      string            `json:"vm_status,omitempty"`
	WorkspacePath string            `json:"workspace_path,omitempty"`
	ProxyPath     string            `json:"proxy_path,omitempty"`
	GuestImage    string            `json:"guest_image,omitempty"`
	TriggerSource string            `json:"trigger_source,omitempty"`
	CreatedAt     string            `json:"created_at,omitempty"`
	UpdatedAt     string            `json:"updated_at,omitempty"`
	CellCount     uint32            `json:"cell_count"`
	EventCount    uint32            `json:"event_count"`
	Tags          map[string]string `json:"tags,omitempty"`
}

type composeExecOutput struct {
	ExecID    string   `json:"exec_id"`
	SessionID string   `json:"session_id"`
	RunID     string   `json:"run_id,omitempty"`
	Command   string   `json:"command"`
	Args      []string `json:"args,omitempty"`
	Cwd       string   `json:"cwd,omitempty"`
	ExitCode  int32    `json:"exit_code"`
	Success   bool     `json:"success"`
	Stdout    string   `json:"stdout,omitempty"`
	Stderr    string   `json:"stderr,omitempty"`
	Output    string   `json:"output,omitempty"`
	Error     string   `json:"error,omitempty"`
}

type composeImageListOutput struct {
	Images      []composeImageOutput    `json:"images"`
	TotalCount  uint32                  `json:"total_count"`
	HasMore     bool                    `json:"has_more"`
	NextOffset  uint32                  `json:"next_offset"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImageInspectOutput struct {
	Image       composeImageOutput      `json:"image"`
	StoreStatus composeImageStoreOutput `json:"store_status"`
}

type composeImagePullOutput struct {
	ImageRef    string                     `json:"image_ref"`
	ResolvedRef string                     `json:"resolved_ref,omitempty"`
	Status      string                     `json:"status"`
	Image       composeImageOutput         `json:"image"`
	Progress    []composeImageProgressItem `json:"progress,omitempty"`
	Warnings    []string                   `json:"warnings,omitempty"`
}

type composeImageRemoveOutput struct {
	ImageRef     string   `json:"image_ref"`
	UntaggedRefs []string `json:"untagged_refs,omitempty"`
	DeletedIDs   []string `json:"deleted_ids,omitempty"`
	Warnings     []string `json:"warnings,omitempty"`
}

type composeImageOutput struct {
	ImageID            string            `json:"image_id"`
	ImageRef           string            `json:"image_ref"`
	ResolvedRef        string            `json:"resolved_ref,omitempty"`
	RepoTags           []string          `json:"repo_tags,omitempty"`
	RepoDigests        []string          `json:"repo_digests,omitempty"`
	Store              string            `json:"store"`
	AvailabilityStatus string            `json:"availability_status"`
	Platform           string            `json:"platform,omitempty"`
	SizeBytes          uint64            `json:"size_bytes"`
	VirtualSizeBytes   uint64            `json:"virtual_size_bytes"`
	CreatedAt          string            `json:"created_at,omitempty"`
	InspectedAt        string            `json:"inspected_at,omitempty"`
	Dangling           bool              `json:"dangling"`
	ContainerCount     uint64            `json:"container_count"`
	Labels             map[string]string `json:"labels,omitempty"`
}

type composeImageStoreOutput struct {
	Store     string `json:"store"`
	Available bool   `json:"available"`
	Endpoint  string `json:"endpoint,omitempty"`
	Error     string `json:"error,omitempty"`
}

type composeImageProgressItem struct {
	ID           string `json:"id,omitempty"`
	Status       string `json:"status,omitempty"`
	Progress     string `json:"progress,omitempty"`
	CurrentBytes uint64 `json:"current_bytes,omitempty"`
	TotalBytes   uint64 `json:"total_bytes,omitempty"`
}

func composeUpOutputFromResponse(resp *agentcomposev2.ApplyProjectResponse) composeUpOutput {
	summary := resp.GetProject().GetSummary()
	revision := resp.GetRevision()
	changes := make([]composeUpChangeOutput, 0, len(resp.GetChanges()))
	for _, change := range resp.GetChanges() {
		changes = append(changes, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ResourceID:   change.GetResourceId(),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return composeUpOutput{
		Project: composeUpProjectOutput{
			ID:              summary.GetProjectId(),
			Name:            summary.GetName(),
			SourcePath:      summary.GetSourcePath(),
			CurrentRevision: summary.GetCurrentRevision(),
			SpecHash:        summary.GetSpecHash(),
			AgentCount:      summary.GetAgentCount(),
			SchedulerCount:  summary.GetSchedulerCount(),
		},
		Revision: composeUpRevisionOutput{
			Revision: revision.GetRevision(),
			SpecHash: revision.GetSpecHash(),
		},
		Applied:   resp.GetApplied(),
		Unchanged: resp.GetUnchanged(),
		Changes:   changes,
	}
}

func composeDownOutputFromResponse(resp *agentcomposev2.RemoveProjectResponse) composeDownOutput {
	changes := composeChangeOutputs(resp.GetChanges())
	failedSessionStops := countProjectDownFailedSessionStops(resp.GetChanges())
	status := "down"
	if len(changes) == 0 {
		status = "unchanged"
	}
	if failedSessionStops > 0 {
		status = "partial-failure"
	}
	return composeDownOutput{
		Project:            composeProjectSummaryOutput(resp.GetProject().GetSummary()),
		Status:             status,
		FailedSessionStops: uint32(failedSessionStops),
		Changes:            changes,
	}
}

func composeChangeOutputs(changes []*agentcomposev2.ProjectChange) []composeUpChangeOutput {
	output := make([]composeUpChangeOutput, 0, len(changes))
	for _, change := range changes {
		output = append(output, composeUpChangeOutput{
			Action:       projectChangeActionText(change.GetAction()),
			ResourceType: change.GetResourceType(),
			ResourceID:   change.GetResourceId(),
			Name:         change.GetName(),
			Message:      change.GetMessage(),
		})
	}
	return output
}

func writeComposeUpText(out io.Writer, resp *agentcomposev2.ApplyProjectResponse) error {
	summary := resp.GetProject().GetSummary()
	revision := resp.GetRevision()
	status := "applied"
	if resp.GetUnchanged() {
		status = "unchanged"
	} else if !resp.GetApplied() {
		status = "not-applied"
	}
	if _, err := fmt.Fprintf(out, "Project: %s\nID: %s\nRevision: %d\nSpec: %s\nStatus: %s\nAgents: %d\nSchedulers: %d\n\n",
		summary.GetName(),
		summary.GetProjectId(),
		revision.GetRevision(),
		revision.GetSpecHash(),
		status,
		summary.GetAgentCount(),
		summary.GetSchedulerCount(),
	); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ACTION\tTYPE\tNAME\tID"); err != nil {
		return err
	}
	for _, change := range resp.GetChanges() {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n",
			projectChangeActionText(change.GetAction()),
			change.GetResourceType(),
			change.GetName(),
			change.GetResourceId(),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeComposeDownText(out io.Writer, output composeDownOutput) error {
	if _, err := fmt.Fprintf(out, "Project: %s\nID: %s\nStatus: %s\nFailed session stops: %d\n\n",
		output.Project.Name,
		output.Project.ID,
		output.Status,
		output.FailedSessionStops,
	); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ACTION\tTYPE\tNAME\tID\tMESSAGE"); err != nil {
		return err
	}
	for _, change := range output.Changes {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n",
			change.Action,
			change.ResourceType,
			change.Name,
			change.ResourceID,
			change.Message,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func countProjectDownFailedSessionStops(changes []*agentcomposev2.ProjectChange) int {
	count := 0
	for _, change := range changes {
		if change.GetAction() == agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED &&
			change.GetResourceType() == "session" &&
			strings.TrimSpace(change.GetMessage()) != "" {
			count++
		}
	}
	return count
}

func projectChangeActionText(action agentcomposev2.ProjectChangeAction) string {
	switch action {
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED:
		return "created"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED:
		return "updated"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_REMOVED:
		return "removed"
	case agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED:
		return "unchanged"
	default:
		return "unspecified"
	}
}

func formatProjectValidationIssues(issues []*agentcomposev2.ProjectValidationIssue) string {
	parts := make([]string, 0, len(issues))
	for _, issue := range issues {
		if issue.GetPath() == "" {
			parts = append(parts, issue.GetMessage())
			continue
		}
		parts = append(parts, fmt.Sprintf("%s: %s", issue.GetPath(), issue.GetMessage()))
	}
	return strings.Join(parts, "; ")
}

func newCLIServiceClients(cli cliOptions) (cliServiceClients, error) {
	clientConfig, err := ResolveClientConfig(cli.Host)
	if err != nil {
		return cliServiceClients{}, err
	}
	httpClient := newDaemonHTTPClient(clientConfig)
	return cliServiceClients{
		project: agentcomposev2connect.NewProjectServiceClient(httpClient, clientConfig.BaseURL),
		run:     agentcomposev2connect.NewRunServiceClient(httpClient, clientConfig.BaseURL),
		exec:    agentcomposev2connect.NewExecServiceClient(httpClient, clientConfig.BaseURL),
		image:   agentcomposev2connect.NewImageServiceClient(httpClient, clientConfig.BaseURL),
		session: agentcomposev1connect.NewSessionServiceClient(httpClient, clientConfig.BaseURL),
	}, nil
}

func composePSOutputFromProject(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project) (composePSOutput, error) {
	output := composePSOutput{Project: composeProjectSummaryOutput(project.GetSummary())}
	schedulers := schedulersByAgent(project.GetSchedulers())
	for _, agent := range project.GetAgents() {
		item := composePSAgentOutput{
			AgentName:        agent.GetAgentName(),
			ManagedAgentID:   agent.GetManagedAgentId(),
			SchedulerEnabled: agent.GetSchedulerEnabled(),
			Driver:           agent.GetDriver(),
			Image:            agent.GetImage(),
		}
		if scheduler := schedulers[agent.GetAgentName()]; scheduler != nil {
			item.SchedulerID = scheduler.GetSchedulerId()
			item.SchedulerTriggers = scheduler.GetTriggerCount()
			item.SchedulerEnabled = scheduler.GetEnabled()
		}
		if latest, err := latestRunOutput(ctx, clients.run, project.GetSummary().GetProjectId(), agent.GetAgentName()); err != nil {
			return composePSOutput{}, err
		} else {
			item.LatestRun = latest
			if latest != nil {
				if latest.Driver != "" {
					item.Driver = latest.Driver
				}
				if latest.ImageRef != "" {
					item.Image = latest.ImageRef
				}
			}
		}
		if session, err := firstRunningSessionOutput(ctx, clients, project.GetSummary().GetProjectId(), agent.GetAgentName()); err != nil {
			return composePSOutput{}, err
		} else {
			item.RunningSession = session
		}
		output.Agents = append(output.Agents, item)
	}
	return output, nil
}

func latestRunOutput(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, agentName string) (*composeRunOutput, error) {
	resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: agentName,
		Limit:     1,
	}))
	if err != nil {
		return nil, err
	}
	if len(resp.Msg.GetRuns()) == 0 {
		return nil, nil
	}
	detail, err := getRunDetail(ctx, client, projectID, resp.Msg.GetRuns()[0].GetRunId())
	if err != nil {
		return nil, err
	}
	output := composeRunOutputFromDetail(detail.Msg.GetRun())
	return &output, nil
}

func firstRunningSessionOutput(ctx context.Context, clients cliServiceClients, projectID, agentName string) (*composeSessionOutput, error) {
	resp, err := clients.run.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: agentName,
		Limit:     20,
	}))
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, run := range resp.Msg.GetRuns() {
		sessionID := strings.TrimSpace(run.GetSessionId())
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		session, err := clients.session.GetSession(ctx, connect.NewRequest(&agentcomposev1.SessionIDRequest{SessionId: sessionID}))
		if err != nil {
			continue
		}
		summary := session.Msg.GetSession().GetSummary()
		if strings.EqualFold(summary.GetVmStatus(), "running") {
			output := composeSessionOutputFromSummary(summary)
			return &output, nil
		}
	}
	return nil, nil
}

func writePSText(out io.Writer, output composePSOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "AGENT\tSCHEDULER\tLATEST RUN\tRUN STATUS\tSESSION\tDRIVER\tIMAGE"); err != nil {
		return err
	}
	for _, agent := range output.Agents {
		latestRunID := "-"
		latestStatus := "-"
		if agent.LatestRun != nil {
			latestRunID = agent.LatestRun.RunID
			latestStatus = agent.LatestRun.Status
		}
		sessionID := "-"
		if agent.RunningSession != nil {
			sessionID = agent.RunningSession.SessionID
		}
		scheduler := "disabled"
		if agent.SchedulerEnabled {
			scheduler = "enabled"
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			agent.AgentName,
			scheduler,
			latestRunID,
			latestStatus,
			sessionID,
			firstNonEmptyString(agent.Driver, "-"),
			firstNonEmptyString(agent.Image, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func schedulersByAgent(items []*agentcomposev2.ProjectScheduler) map[string]*agentcomposev2.ProjectScheduler {
	result := make(map[string]*agentcomposev2.ProjectScheduler, len(items))
	for _, item := range items {
		result[item.GetAgentName()] = item
	}
	return result
}

func composeProjectOutputFromProject(project *agentcomposev2.Project) composeProjectOutput {
	output := composeProjectOutput{Project: composeProjectSummaryOutput(project.GetSummary())}
	for _, agent := range project.GetAgents() {
		output.Agents = append(output.Agents, composeProjectAgentOutputFromProto(agent))
	}
	for _, scheduler := range project.GetSchedulers() {
		output.Schedulers = append(output.Schedulers, composeProjectSchedulerOutputFromProto(scheduler))
	}
	return output
}

func composeAgentInspectOutputFor(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project, agentName string) (composeAgentInspectOutput, error) {
	var found *agentcomposev2.ProjectAgent
	for _, agent := range project.GetAgents() {
		if agent.GetAgentName() == agentName {
			found = agent
			break
		}
	}
	if found == nil {
		return composeAgentInspectOutput{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %s not found in project %s", agentName, project.GetSummary().GetName())}
	}
	output := composeAgentInspectOutput{
		Project: composeProjectSummaryOutput(project.GetSummary()),
		Agent:   composeProjectAgentOutputFromProto(found),
	}
	for _, scheduler := range project.GetSchedulers() {
		if scheduler.GetAgentName() == agentName {
			output.Schedulers = append(output.Schedulers, composeProjectSchedulerOutputFromProto(scheduler))
		}
	}
	if latest, err := latestRunOutput(ctx, clients.run, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list latest run for agent %s: %w", agentName, err))
	} else {
		output.LatestRun = latest
	}
	if session, err := firstRunningSessionOutput(ctx, clients, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list running session for agent %s: %w", agentName, err))
	} else if session != nil {
		output.RunningSessions = append(output.RunningSessions, *session)
	}
	return output, nil
}

func composeProjectSummaryOutput(summary *agentcomposev2.ProjectSummary) composeUpProjectOutput {
	return composeUpProjectOutput{
		ID:              summary.GetProjectId(),
		Name:            summary.GetName(),
		SourcePath:      summary.GetSourcePath(),
		CurrentRevision: summary.GetCurrentRevision(),
		SpecHash:        summary.GetSpecHash(),
		AgentCount:      summary.GetAgentCount(),
		SchedulerCount:  summary.GetSchedulerCount(),
	}
}

func composeProjectAgentOutputFromProto(agent *agentcomposev2.ProjectAgent) composeProjectAgentOutput {
	return composeProjectAgentOutput{
		AgentName:        agent.GetAgentName(),
		ManagedAgentID:   agent.GetManagedAgentId(),
		Provider:         agent.GetProvider(),
		Model:            agent.GetModel(),
		Image:            agent.GetImage(),
		Driver:           agent.GetDriver(),
		SchedulerEnabled: agent.GetSchedulerEnabled(),
	}
}

func composeProjectSchedulerOutputFromProto(scheduler *agentcomposev2.ProjectScheduler) composeProjectSchedulerOutput {
	return composeProjectSchedulerOutput{
		AgentName:       scheduler.GetAgentName(),
		SchedulerID:     scheduler.GetSchedulerId(),
		ManagedLoaderID: scheduler.GetManagedLoaderId(),
		Enabled:         scheduler.GetEnabled(),
		TriggerCount:    scheduler.GetTriggerCount(),
	}
}

func composeRunOutputFromDetail(run *agentcomposev2.RunDetail) composeRunOutput {
	summary := run.GetSummary()
	return composeRunOutput{
		RunID:        summary.GetRunId(),
		ProjectID:    summary.GetProjectId(),
		ProjectName:  summary.GetProjectName(),
		AgentName:    summary.GetAgentName(),
		Source:       runSourceText(summary.GetSource()),
		Status:       runStatusText(summary.GetStatus()),
		SessionID:    summary.GetSessionId(),
		ExitCode:     summary.GetExitCode(),
		Error:        summary.GetError(),
		StartedAt:    summary.GetStartedAt(),
		CompletedAt:  summary.GetCompletedAt(),
		DurationMs:   summary.GetDurationMs(),
		Prompt:       run.GetPrompt(),
		Output:       run.GetOutput(),
		ResultJSON:   run.GetResultJson(),
		LogsPath:     run.GetLogsPath(),
		ArtifactsDir: run.GetArtifactsDir(),
		CleanupError: run.GetCleanupError(),
		Driver:       run.GetDriver(),
		ImageRef:     run.GetImageRef(),
	}
}

func composeExecOutputFromResult(result *agentcomposev2.ExecResult) composeExecOutput {
	return composeExecOutput{
		ExecID:    result.GetExecId(),
		SessionID: result.GetSessionId(),
		RunID:     result.GetRunId(),
		Command:   result.GetCommand().GetCommand(),
		Args:      append([]string(nil), result.GetCommand().GetArgs()...),
		Cwd:       result.GetCwd(),
		ExitCode:  result.GetExitCode(),
		Success:   result.GetSuccess(),
		Stdout:    result.GetStdout(),
		Stderr:    result.GetStderr(),
		Output:    result.GetOutput(),
		Error:     result.GetError(),
	}
}

func composeImageListOutputFromResponse(resp *agentcomposev2.ListImagesResponse) composeImageListOutput {
	output := composeImageListOutput{
		Images:      make([]composeImageOutput, 0, len(resp.GetImages())),
		TotalCount:  resp.GetTotalCount(),
		HasMore:     resp.GetHasMore(),
		NextOffset:  resp.GetNextOffset(),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
	for _, image := range resp.GetImages() {
		output.Images = append(output.Images, composeImageOutputFromProto(image))
	}
	return output
}

func composeImagePullOutputFromResponse(resp *agentcomposev2.PullImageResponse) composeImagePullOutput {
	output := composeImagePullOutput{
		ImageRef:    firstNonEmptyString(resp.GetImage().GetImageRef(), resp.GetResolvedRef()),
		ResolvedRef: resp.GetResolvedRef(),
		Status:      imageOperationStatusText(resp.GetStatus()),
		Image:       composeImageOutputFromProto(resp.GetImage()),
		Warnings:    append([]string(nil), resp.GetWarnings()...),
		Progress:    make([]composeImageProgressItem, 0, len(resp.GetProgress())),
	}
	for _, item := range resp.GetProgress() {
		output.Progress = append(output.Progress, composeImageProgressItem{
			ID:           item.GetId(),
			Status:       item.GetStatus(),
			Progress:     item.GetProgress(),
			CurrentBytes: item.GetCurrentBytes(),
			TotalBytes:   item.GetTotalBytes(),
		})
	}
	return output
}

func composeImageInspectOutputFromResponse(resp *agentcomposev2.InspectImageResponse) composeImageInspectOutput {
	return composeImageInspectOutput{
		Image:       composeImageOutputFromProto(resp.GetImage()),
		StoreStatus: composeImageStoreOutputFromProto(resp.GetStoreStatus()),
	}
}

func composeImageRemoveOutputFromResponse(resp *agentcomposev2.RemoveImageResponse) composeImageRemoveOutput {
	return composeImageRemoveOutput{
		ImageRef:     resp.GetImageRef(),
		UntaggedRefs: append([]string(nil), resp.GetUntaggedRefs()...),
		DeletedIDs:   append([]string(nil), resp.GetDeletedIds()...),
		Warnings:     append([]string(nil), resp.GetWarnings()...),
	}
}

func composeImageOutputFromProto(image *agentcomposev2.Image) composeImageOutput {
	if image == nil {
		return composeImageOutput{}
	}
	return composeImageOutput{
		ImageID:            image.GetImageId(),
		ImageRef:           image.GetImageRef(),
		ResolvedRef:        image.GetResolvedRef(),
		RepoTags:           append([]string(nil), image.GetRepoTags()...),
		RepoDigests:        append([]string(nil), image.GetRepoDigests()...),
		Store:              imageStoreText(image.GetStore()),
		AvailabilityStatus: imageAvailabilityStatusText(image.GetAvailabilityStatus()),
		Platform:           imagePlatformText(image.GetPlatform()),
		SizeBytes:          image.GetSizeBytes(),
		VirtualSizeBytes:   image.GetVirtualSizeBytes(),
		CreatedAt:          image.GetCreatedAt(),
		InspectedAt:        image.GetInspectedAt(),
		Dangling:           image.GetDangling(),
		ContainerCount:     image.GetContainerCount(),
		Labels:             cloneStringMapForCLI(image.GetLabels()),
	}
}

func composeImageStoreOutputFromProto(status *agentcomposev2.ImageStoreStatus) composeImageStoreOutput {
	if status == nil {
		return composeImageStoreOutput{}
	}
	return composeImageStoreOutput{
		Store:     imageStoreText(status.GetStore()),
		Available: status.GetAvailable(),
		Endpoint:  status.GetEndpoint(),
		Error:     status.GetError(),
	}
}

func writeImagesText(out io.Writer, images []composeImageOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "IMAGE ID\tREF\tSTATUS\tSIZE\tCREATED"); err != nil {
		return err
	}
	for _, image := range images {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%s\n",
			shortImageID(image.ImageID),
			firstNonEmptyString(image.ImageRef, image.ResolvedRef, "-"),
			firstNonEmptyString(image.AvailabilityStatus, "-"),
			image.SizeBytes,
			firstNonEmptyString(image.CreatedAt, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func composeSessionOutputFromSummary(summary *agentcomposev1.SessionSummary) composeSessionOutput {
	tags := make(map[string]string, len(summary.GetTags()))
	for _, tag := range summary.GetTags() {
		name := strings.TrimSpace(tag.GetName())
		if name == "" {
			continue
		}
		tags[name] = tag.GetValue()
	}
	if len(tags) == 0 {
		tags = nil
	}
	return composeSessionOutput{
		SessionID:     summary.GetSessionId(),
		Title:         summary.GetTitle(),
		Driver:        summary.GetDriver(),
		VMStatus:      strings.ToLower(strings.TrimSpace(summary.GetVmStatus())),
		WorkspacePath: summary.GetWorkspacePath(),
		ProxyPath:     summary.GetProxyPath(),
		GuestImage:    summary.GetGuestImage(),
		TriggerSource: summary.GetTriggerSource(),
		CreatedAt:     summary.GetCreatedAt(),
		UpdatedAt:     summary.GetUpdatedAt(),
		CellCount:     summary.GetCellCount(),
		EventCount:    summary.GetEventCount(),
		Tags:          tags,
	}
}

func followOrPrintProjectLogs(cmd *cobra.Command, cli cliOptions, client agentcomposev2connect.RunServiceClient, projectID, projectName string, options composeLogsOptions) error {
	printed := map[string]int{}
	for {
		runs, err := listLogRuns(cmd.Context(), client, projectID, options)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("list logs for project %s: %w", projectName, err))
		}
		if len(runs) == 0 {
			if cli.JSON {
				data, err := json.MarshalIndent(composeLogsOutput{}, "", "  ")
				if err != nil {
					return err
				}
				return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
			}
			if !options.Follow {
				return nil
			}
		}
		details := make([]*agentcomposev2.RunDetail, 0, len(runs))
		anyRunning := false
		for _, summary := range runs {
			detail, err := getRunDetail(cmd.Context(), client, projectID, summary.GetRunId())
			if err != nil {
				return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", summary.GetRunId(), projectName, err))
			}
			details = append(details, detail.Msg.GetRun())
			if !runSummaryTerminal(detail.Msg.GetRun().GetSummary()) {
				anyRunning = true
			}
		}
		if cli.JSON {
			output := composeLogsOutput{Runs: make([]composeRunOutput, 0, len(details))}
			for _, detail := range details {
				output.Runs = append(output.Runs, composeRunOutputFromDetail(detail))
			}
			data, err := json.MarshalIndent(output, "", "  ")
			if err != nil {
				return err
			}
			if err := writeCommandOutput(cmd.OutOrStdout(), append(data, '\n')); err != nil {
				return err
			}
			if !options.Follow || !anyRunning {
				return nil
			}
		} else if err := writeLogDetails(cmd.OutOrStdout(), details, printed, options.Follow); err != nil {
			return err
		}
		if !options.Follow || !anyRunning {
			return nil
		}
		select {
		case <-cmd.Context().Done():
			return cmd.Context().Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

func listLogRuns(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID string, options composeLogsOptions) ([]*agentcomposev2.RunSummary, error) {
	resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: projectID,
		AgentName: strings.TrimSpace(options.AgentName),
		SessionId: strings.TrimSpace(options.SessionID),
		Limit:     20,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetRuns(), nil
}

func getRunDetail(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, runID string) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	return client.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{
		ProjectId: strings.TrimSpace(projectID),
		RunId:     strings.TrimSpace(runID),
	}))
}

func writeLogsForRun(out io.Writer, run *agentcomposev2.RunDetail, asJSON bool) error {
	if asJSON {
		data, err := json.MarshalIndent(composeLogsOutput{Runs: []composeRunOutput{composeRunOutputFromDetail(run)}}, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	return writeCommandOutput(out, []byte(run.GetOutput()))
}

func writeLogDetails(out io.Writer, details []*agentcomposev2.RunDetail, printed map[string]int, incremental bool) error {
	multiple := len(details) > 1
	for _, detail := range details {
		summary := detail.GetSummary()
		output := detail.GetOutput()
		start := 0
		if incremental {
			start = printed[summary.GetRunId()]
			if start > len(output) {
				start = 0
			}
		}
		if start == len(output) {
			continue
		}
		if multiple && !incremental {
			if _, err := fmt.Fprintf(out, "==> run %s agent %s session %s <==\n", summary.GetRunId(), summary.GetAgentName(), summary.GetSessionId()); err != nil {
				return err
			}
		}
		if err := writeCommandOutput(out, []byte(output[start:])); err != nil {
			return err
		}
		printed[summary.GetRunId()] = len(output)
	}
	return nil
}

func manualRunClientRequestID(projectName, agentName, prompt string) string {
	value := strings.TrimSpace(projectName) + "|" + strings.TrimSpace(agentName) + "|" + strings.TrimSpace(prompt) + "|" + time.Now().UTC().Format(time.RFC3339Nano)
	return value
}

func runSummaryFailed(run *agentcomposev2.RunSummary) bool {
	switch run.GetStatus() {
	case agentcomposev2.RunStatus_RUN_STATUS_FAILED, agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return true
	default:
		return false
	}
}

func runSummaryTerminal(run *agentcomposev2.RunSummary) bool {
	switch run.GetStatus() {
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, agentcomposev2.RunStatus_RUN_STATUS_FAILED, agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return true
	default:
		return false
	}
}

func runSummaryExitCode(run *agentcomposev2.RunSummary) int {
	if code := int(run.GetExitCode()); code > 0 && code < 126 {
		return code
	}
	return exitCodeGeneral
}

func execResultExitCode(result *agentcomposev2.ExecResult) int {
	if code := int(result.GetExitCode()); code > 0 && code < 126 {
		return code
	}
	return exitCodeGeneral
}

func parseImagePlatform(value string) (*agentcomposev2.ImagePlatform, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return nil, fmt.Errorf("invalid --platform %q: expected os/arch[/variant]", value)
	}
	platform := &agentcomposev2.ImagePlatform{
		Os:           strings.TrimSpace(parts[0]),
		Architecture: strings.TrimSpace(parts[1]),
	}
	if len(parts) == 3 {
		platform.Variant = strings.TrimSpace(parts[2])
	}
	return platform, nil
}

func imageStoreText(store agentcomposev2.ImageStoreKind) string {
	switch store {
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON:
		return "docker"
	case agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE:
		return "oci-cache"
	default:
		return "unspecified"
	}
}

func imageAvailabilityStatusText(status agentcomposev2.ImageAvailabilityStatus) string {
	switch status {
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE:
		return "available"
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_MISSING:
		return "missing"
	case agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_ERROR:
		return "error"
	default:
		return "unspecified"
	}
}

func imageOperationStatusText(status agentcomposev2.ImageOperationStatus) string {
	switch status {
	case agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED:
		return "succeeded"
	case agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_FAILED:
		return "failed"
	default:
		return "unspecified"
	}
}

func imagePlatformText(platform *agentcomposev2.ImagePlatform) string {
	if platform == nil {
		return ""
	}
	parts := []string{strings.TrimSpace(platform.GetOs()), strings.TrimSpace(platform.GetArchitecture())}
	if strings.TrimSpace(platform.GetVariant()) != "" {
		parts = append(parts, strings.TrimSpace(platform.GetVariant()))
	}
	if parts[0] == "" || parts[1] == "" {
		return strings.Trim(strings.Join(parts, "/"), "/")
	}
	return strings.Join(parts, "/")
}

func shortImageID(id string) string {
	id = strings.TrimPrefix(strings.TrimSpace(id), "sha256:")
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func cloneStringMapForCLI(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func runStatusText(status agentcomposev2.RunStatus) string {
	switch status {
	case agentcomposev2.RunStatus_RUN_STATUS_PENDING:
		return "pending"
	case agentcomposev2.RunStatus_RUN_STATUS_RUNNING:
		return "running"
	case agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED:
		return "succeeded"
	case agentcomposev2.RunStatus_RUN_STATUS_FAILED:
		return "failed"
	case agentcomposev2.RunStatus_RUN_STATUS_CANCELED:
		return "canceled"
	default:
		return "unspecified"
	}
}

func runSourceText(source agentcomposev2.RunSource) string {
	switch source {
	case agentcomposev2.RunSource_RUN_SOURCE_MANUAL:
		return "manual"
	case agentcomposev2.RunSource_RUN_SOURCE_SCHEDULER:
		return "scheduler"
	case agentcomposev2.RunSource_RUN_SOURCE_API:
		return "api"
	default:
		return "unspecified"
	}
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
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

const (
	exitCodeGeneral     = 1
	exitCodeUsage       = 2
	exitCodeUnavailable = 3
)

func commandExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr commandExitError
	if errors.As(err, &exitErr) && exitErr.Code > 0 {
		return exitErr.Code
	}
	return exitCodeGeneral
}

func commandExitErrorForConnect(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeUnavailable:
		return commandExitError{Code: exitCodeUnavailable, Err: err}
	case connect.CodeInvalidArgument, connect.CodeFailedPrecondition, connect.CodeNotFound:
		return commandExitError{Code: exitCodeUsage, Err: err}
	default:
		return err
	}
}

func ResolveClientConfig(hostFlag string) (ClientConfig, error) {
	hostFlag = strings.TrimSpace(hostFlag)
	if hostFlag != "" {
		baseURL, err := normalizeCLIHost("--host", hostFlag)
		if err != nil {
			return ClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
		}
		return ClientConfig{
			BaseURL:     baseURL,
			Source:      "--host",
			SourceValue: hostFlag,
		}, nil
	}

	if envHost := strings.TrimSpace(os.Getenv("AGENT_COMPOSE_HOST")); envHost != "" {
		baseURL, err := normalizeCLIHost("AGENT_COMPOSE_HOST", envHost)
		if err != nil {
			return ClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
		}
		return ClientConfig{
			BaseURL:     baseURL,
			Source:      "AGENT_COMPOSE_HOST",
			SourceValue: envHost,
		}, nil
	}

	socketPath, err := resolveAgentComposeSocketForCLI(os.Getenv("AGENT_COMPOSE_SOCKET"))
	if err != nil {
		return ClientConfig{}, commandExitError{Code: exitCodeUsage, Err: err}
	}
	return ClientConfig{
		BaseURL:       "http://agent-compose",
		SocketPath:    socketPath,
		Source:        "AGENT_COMPOSE_SOCKET",
		SourceValue:   socketPath,
		UseUnixSocket: true,
	}, nil
}

func normalizeCLIHost(name, value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid %s %q: %w", name, value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid %s %q: scheme must be http or https", name, value)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid %s %q: host is required", name, value)
	}
	return strings.TrimRight(value, "/"), nil
}

func resolveAgentComposeSocketForCLI(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		if runtimeDir := strings.TrimSpace(os.Getenv("XDG_RUNTIME_DIR")); runtimeDir != "" {
			value = filepath.Join(runtimeDir, "agent-compose.sock")
		} else {
			value = filepath.Join(os.TempDir(), fmt.Sprintf("agent-compose-%d.sock", os.Getuid()))
		}
	}
	if value == "" {
		return "", fmt.Errorf("AGENT_COMPOSE_SOCKET is empty")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("invalid AGENT_COMPOSE_SOCKET %q: path contains NUL byte", value)
	}
	resolved, err := filepath.Abs(value)
	if err != nil {
		return value, nil
	}
	return resolved, nil
}

func fetchDaemonVersion(ctx context.Context, clientConfig ClientConfig) ([]byte, error) {
	client := newDaemonHTTPClient(clientConfig)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, clientConfig.BaseURL+"/api/version", nil)
	if err != nil {
		return nil, fmt.Errorf("create daemon request for %s %q: %w", clientConfig.Source, clientConfig.SourceValue, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, commandExitError{Code: exitCodeUnavailable, Err: fmt.Errorf("connect daemon via %s %q: %w", clientConfig.Source, clientConfig.SourceValue, err)}
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read daemon response from %s %q: %w", clientConfig.Source, clientConfig.SourceValue, err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("daemon via %s %q returned HTTP %d: %s", clientConfig.Source, clientConfig.SourceValue, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

func newDaemonHTTPClient(clientConfig ClientConfig) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if clientConfig.UseUnixSocket {
		socketPath := clientConfig.SocketPath
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		}
	}
	return &http.Client{
		Transport: transport,
		Timeout:   10 * time.Minute,
	}
}
