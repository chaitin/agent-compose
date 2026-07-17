package main

import (
	"agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/compose"
	driverpkg "agent-compose/pkg/driver"
	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

const optionalRunModeFlagNoValue = "\x00agent-compose-run-mode"

type composeRunOptions struct {
	Prompt        string
	Command       string
	SandboxID     string
	Driver        string
	KeepRunning   bool
	Remove        bool
	Jupyter       bool
	JupyterExpose bool
	Detach        bool
	Interactive   bool
	TTY           bool
}

func composeRunArgs(_ *cobra.Command, args []string) error {
	if len(args) < 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires an agent")}
	}
	return nil
}

func runComposeConfigCommand(cmd *cobra.Command, cli cliOptions, options composeConfigOptions) error {
	_, normalized, err := loadResolvedNormalizedCompose(cmd.Context(), cli)
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

func runComposeListProjectsCommand(cmd *cobra.Command, cli cliOptions, options composeListProjectsOptions) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output, err := listProjects(cmd.Context(), clients.project, options)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list projects: %w", err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeProjectListText(cmd.OutOrStdout(), output.Projects, options.Verbose)
}

func runComposeUpCommand(cmd *cobra.Command, cli cliOptions) error {
	composePath, normalized, err := loadResolvedNormalizedCompose(cmd.Context(), cli)
	if err != nil {
		return err
	}
	specHash, err := normalized.Hash()
	if err != nil {
		return fmt.Errorf("%s: hash normalized compose spec: %w", composePath, err)
	}
	clientConfig, err := resolveCLIClientConfig(cli.Host)
	if err != nil {
		return err
	}
	client := agentcomposev2connect.NewProjectServiceClient(newDaemonHTTPClient(clientConfig), clientConfig.BaseURL)
	protoSpec, err := api.ProjectSpecToProtoChecked(normalized)
	if err != nil {
		return fmt.Errorf("%s: serialize normalized compose spec: %w", composePath, err)
	}
	resp, err := client.ApplyProject(cmd.Context(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: protoSpec,
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
	return writeComposeUpText(cmd.OutOrStdout(), composeDisplayChangesFromProjectChanges(msg.GetChanges(), normalized, msg.GetProject().GetSummary().GetProjectId()))
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
	} else if err := writeComposeDownText(cmd.OutOrStdout(), composeDownDisplayChanges(resp.Msg, normalized)); err != nil {
		return err
	}
	if output.FailedSandboxStops > 0 {
		return commandExitError{
			Code: exitCodeGeneral,
			Err:  fmt.Errorf("down project %s completed with %d sandbox stop failure(s)", normalized.Name, output.FailedSandboxStops),
		}
	}
	return nil
}

func runComposeStatsCommand(cmd *cobra.Command, cli cliOptions, args []string) error {
	if len(args) > 0 {
		return runComposeSingleStatsCommand(cmd, cli, args[0])
	}
	composePath, normalized, projectID, err := resolveComposeProject(cli)
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
		return commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", normalized.Name, err), "stats", normalized.Name, composePath)
	}
	output, err := composeProjectStatsOutputFromProject(cmd.Context(), clients, project.Msg.GetProject())
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("build stats for project %s: %w", normalized.Name, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeStatsText(cmd.OutOrStdout(), output.Stats)
}

func runComposeSingleStatsCommand(cmd *cobra.Command, cli cliOptions, sandboxID string) error {
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	sandboxID = strings.TrimSpace(sandboxID)
	if sandboxID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("stats requires non-empty sandbox")}
	}
	sandboxID, err = resolveComposeSandboxRefForCommand(cmd.Context(), cli, clients, sandboxID)
	if err != nil {
		return err
	}
	output, err := composeStatsOutputForSandbox(cmd.Context(), clients.sandbox, sandboxID)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("get sandbox %s stats: %w", sandboxID, err))
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeStatsText(cmd.OutOrStdout(), []composeStatsOutput{output})
}

func runComposeRunCommand(cmd *cobra.Command, cli cliOptions, options composeRunOptions, args []string) error {
	normalizedOptions, err := normalizeComposeRunOptions(cmd, options)
	if err != nil {
		return err
	}
	promptFlagChanged := cmd.Flags().Changed("prompt")
	commandFlagChanged := cmd.Flags().Changed("command")
	prompt := normalizeOptionalRunModeValue(normalizedOptions.Prompt)
	commandText := normalizeOptionalRunModeValue(normalizedOptions.Command)
	if promptFlagChanged && normalizedOptions.Prompt == optionalRunModeFlagNoValue && len(args) > 1 {
		prompt = strings.TrimSpace(args[1])
		args = append(args[:1], args[2:]...)
	}
	if commandFlagChanged && normalizedOptions.Command == optionalRunModeFlagNoValue && len(args) > 1 {
		commandText = strings.TrimSpace(args[1])
		args = append(args[:1], args[2:]...)
	}
	if normalizedOptions.Interactive && len(args) > 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -i/--interactive does not accept additional positional arguments")}
	}
	if len(args) > 1 {
		if promptFlagChanged {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run with --prompt does not accept additional positional arguments")}
		}
		if commandFlagChanged {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run with --command does not accept additional positional arguments")}
		}
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run does not accept positional trigger arguments; use scheduler trigger <agent> <trigger>")}
	}
	if len(args) == 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires an agent")}
	}
	if normalizedOptions.Detach && normalizedOptions.Interactive {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -d/--detach cannot be combined with -i/--interactive")}
	}
	if normalizedOptions.TTY && !normalizedOptions.Interactive {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -t/--tty requires -i/--interactive")}
	}
	if normalizedOptions.Interactive && cli.JSON {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -i/--interactive cannot be combined with --json")}
	}
	if normalizedOptions.Interactive && promptFlagChanged == commandFlagChanged {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -i/--interactive requires exactly one of --prompt or --command")}
	}
	if normalizedOptions.Interactive && normalizedOptions.TTY && !commandFlagChanged && !promptFlagChanged {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run -t/--tty requires --prompt or --command")}
	}
	if normalizedOptions.Interactive && normalizedOptions.TTY && strings.TrimSpace(commandText) == "" {
		if commandFlagChanged {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --command -it requires a non-empty command")}
		}
		if strings.TrimSpace(prompt) == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --prompt -it requires a non-empty prompt")}
		}
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	agentName, err := resolveComposeAgentNameFromSpec(normalized, projectID, args[0])
	if err != nil {
		return err
	}
	if normalizedOptions.Interactive && promptFlagChanged {
		if err := validateInteractivePromptProvider(normalized, agentName, normalizedOptions.TTY); err != nil {
			return err
		}
	}
	if !normalizedOptions.Interactive && cmd.Flags().Changed("command") && commandText == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --command requires a non-empty command")}
	}
	if !normalizedOptions.Interactive && cmd.Flags().Changed("prompt") && prompt == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --prompt requires a non-empty prompt")}
	}
	modeCount := 0
	if !normalizedOptions.Interactive {
		for _, value := range []string{prompt, commandText} {
			if value != "" {
				modeCount++
			}
		}
	}
	if modeCount > 1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires only one of --prompt or --command")}
	}
	if !normalizedOptions.Interactive && modeCount == 0 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run requires --prompt or --command")}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	if strings.TrimSpace(normalizedOptions.SandboxID) != "" {
		sandboxID, err := resolveComposeSandboxRefForCommand(cmd.Context(), cli, clients, normalizedOptions.SandboxID)
		if err != nil {
			return err
		}
		normalizedOptions.SandboxID = sandboxID
	}
	cleanupPolicy := agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION
	if normalizedOptions.KeepRunning {
		cleanupPolicy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING
	} else if normalizedOptions.Remove {
		cleanupPolicy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION
	}
	client := clients.run
	var jupyter *agentcomposev2.RunJupyterSpec
	if normalizedOptions.Jupyter || normalizedOptions.JupyterExpose {
		jupyter = &agentcomposev2.RunJupyterSpec{
			Enabled: normalizedOptions.Jupyter || normalizedOptions.JupyterExpose,
			Expose:  normalizedOptions.JupyterExpose,
		}
	}
	runReq := &agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       agentName,
		Prompt:          prompt,
		Command:         commandText,
		Source:          agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
		SandboxId:       strings.TrimSpace(normalizedOptions.SandboxID),
		Driver:          strings.TrimSpace(normalizedOptions.Driver),
		CleanupPolicy:   cleanupPolicy,
		ClientRequestId: manualRunClientRequestID(normalized.Name, agentName, firstNonEmptyString(prompt, commandText)),
		Jupyter:         jupyter,
	}
	if normalizedOptions.Detach {
		return startDetachedRun(cmd, cli, normalized.Name, client, runReq)
	}
	if normalizedOptions.Interactive {
		if normalizedOptions.TTY {
			attachClient, err := newCLIRunAttachServiceClient(cli)
			if err != nil {
				return err
			}
			if promptFlagChanged {
				runReq.Prompt = prompt
				runReq.Command = ""
				return runComposeRunPromptAttachCommand(cmd, normalized.Name, connectRunAttachClient{client: attachClient}, runReq)
			}
			runReq.Prompt = ""
			runReq.Command = commandText
			return runComposeRunAttachCommand(cmd, normalized.Name, connectRunAttachClient{client: attachClient}, runReq, normalizedOptions)
		}
		runReq.Prompt = ""
		runReq.Command = ""
		runReq.CleanupPolicy = agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING
		return runInteractiveComposeRun(cmd, normalizedOptions, normalized.Name, client, clients.sandbox, runReq, promptFlagChanged, prompt, commandText)
	}
	return executeComposeRunRequest(cmd, cli, normalized.Name, projectID, client, runReq, normalizedOptions.Detach)
}

func composeRunCompletionError(projectName, agentName string, completed *agentcomposev2.RunSummary, detail *agentcomposev2.RunDetail) error {
	cleanupErr := runDetailCleanupError(detail)
	if runSummaryFailed(completed) {
		message := fmt.Sprintf("run %s for project %s agent %s failed: %s", completed.GetRunId(), projectName, agentName, firstNonEmptyString(completed.GetError(), runStatusText(completed.GetStatus())))
		if cleanupErr != "" {
			message += fmt.Sprintf("; cleanup warning: %s", cleanupErr)
		}
		return commandExitError{Code: runSummaryExitCode(completed), Err: fmt.Errorf("%s", message)}
	}
	if cleanupErr != "" {
		return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("run %s for project %s agent %s succeeded but sandbox cleanup failed: %s", completed.GetRunId(), projectName, agentName, cleanupErr)}
	}
	return nil
}

func runDetailCleanupError(detail *agentcomposev2.RunDetail) string {
	if detail == nil {
		return ""
	}
	return strings.TrimSpace(detail.GetCleanupError())
}

func writeRunWarnings(out io.Writer, warnings []string) error {
	for _, warning := range appendUniqueStrings(nil, warnings...) {
		if _, err := fmt.Fprintf(out, "warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

func (o *terminalStreamOutput) Write(transcript *agentcomposev2.TranscriptEvent, chunk string, stream agentcomposev2.StdioStream) error {
	text, stream := transcriptOrChunkText(transcript, chunk, stream)
	if text == "" {
		return nil
	}
	target := &o.stdout
	if stream == agentcomposev2.StdioStream_STDIO_STREAM_STDERR {
		target = &o.stderr
	}
	target.wrote = true
	target.lastByte = text[len(text)-1]
	_, err := io.WriteString(target.writer, text)
	return err
}

func (o *terminalStreamOutput) Finish() error {
	if err := o.stdout.Finish(); err != nil {
		return err
	}
	return o.stderr.Finish()
}

func (w *terminalStreamWriter) Finish() error {
	if !w.wrote || w.lastByte == '\n' {
		return nil
	}
	_, err := io.WriteString(w.writer, "\n")
	return err
}

func normalizeComposeRunOptions(cmd *cobra.Command, options composeRunOptions) (composeRunOptions, error) {
	options.SandboxID = strings.TrimSpace(options.SandboxID)
	options.Driver = strings.TrimSpace(options.Driver)
	if options.Driver != "" {
		driver, err := driverpkg.ResolveSandboxRuntimeDriver(options.Driver, "")
		if err != nil {
			return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --driver: %w", err)}
		}
		options.Driver = driver
	}
	if options.SandboxID != "" && options.Driver != "" {
		return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run --driver cannot be combined with --sandbox")}
	}
	return options, nil
}

func composeRunAgentSpec(normalized *compose.NormalizedProjectSpec, agentName string) (compose.NormalizedAgentSpec, bool) {
	agentName = strings.TrimSpace(agentName)
	if normalized == nil {
		return compose.NormalizedAgentSpec{}, false
	}
	for _, agent := range normalized.Agents {
		if strings.TrimSpace(agent.Name) == agentName {
			return agent, true
		}
	}
	return compose.NormalizedAgentSpec{}, false
}

func normalizeOptionalRunModeValue(value string) string {
	if value == optionalRunModeFlagNoValue {
		return ""
	}
	return strings.TrimSpace(value)
}

func runComposePSCommand(cmd *cobra.Command, cli cliOptions, options composePSOptions) error {
	composePath, normalized, projectID, err := resolveComposeProject(cli)
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
		return commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", normalized.Name, err), "ps", normalized.Name, composePath)
	}
	output, err := composePSOutputFromProject(cmd.Context(), clients, project.Msg.GetProject(), options)
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
	return writePSText(cmd.OutOrStdout(), output, options.Verbose)
}

func runComposeInspectCommand(cmd *cobra.Command, cli cliOptions, args []string) error {
	kind := strings.ToLower(strings.TrimSpace(args[0]))
	if len(args) == 1 && identity.IsIDPrefix(kind) {
		return runComposeIDInspectCommand(cmd, cli, kind)
	}
	target := ""
	if len(args) > 1 {
		target = strings.TrimSpace(args[1])
	}
	if kind == "image" {
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect image requires an image reference")}
		}
		return runComposeImageInspectCommand(cmd, cli, target)
	}
	if kind == "cache" {
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect cache requires a cache id")}
		}
		return runComposeCacheInspectCommand(cmd, cli, target)
	}
	if kind == "volume" {
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect volume requires a volume name")}
		}
		return runComposeVolumeInspectCommand(cmd, cli, target)
	}
	composePath, normalized, projectID, err := resolveComposeProject(cli)
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
			return commandExitErrorForComposeProject(fmt.Errorf("inspect project %s: %w", normalized.Name, err), "inspect project", normalized.Name, composePath)
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
			return commandExitErrorForComposeProject(fmt.Errorf("inspect agent %s in project %s: %w", target, normalized.Name, err), "inspect agent", normalized.Name, composePath)
		}
		agentName, err := resolveComposeAgentNameFromProject(project.Msg.GetProject(), target)
		if err != nil {
			return err
		}
		agent, err := composeAgentInspectOutputFor(cmd.Context(), clients, project.Msg.GetProject(), agentName)
		if err != nil {
			return err
		}
		output = agent
	case "run":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect run requires a run id")}
		}
		output, err = inspectComposeRunOutput(cmd.Context(), clients, projectID, normalized.Name, target)
		if err != nil {
			return err
		}
	case "sandbox":
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect sandbox requires a sandbox")}
		}
		target, err = resolveComposeSandboxRefWithProject(cmd.Context(), clients, projectID, target)
		if err != nil {
			return err
		}
		output, err = composeSandboxInspectOutputFor(cmd.Context(), clients, target)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect sandbox %s: %w", target, err))
		}
	case "session":

		if err := writeDeprecatedWarning(cmd.ErrOrStderr(), "agent-compose inspect session", "agent-compose inspect sandbox"); err != nil {
			return err
		}
		if target == "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("inspect session requires a sandbox")}
		}
		target, err = resolveComposeSandboxRefWithProject(cmd.Context(), clients, projectID, target)
		if err != nil {
			return err
		}
		output, err = composeSandboxInspectOutputFor(cmd.Context(), clients, target)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("inspect sandbox %s: %w", target, err))
		}
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

func listProjectRuns(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID string) ([]*agentcomposev2.RunSummary, error) {
	var result []*agentcomposev2.RunSummary
	var offset uint32
	const limit uint32 = 100
	for {
		resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
			ProjectId: projectID,
			Offset:    offset,
			Limit:     limit,
		}))
		if err != nil {
			return nil, err
		}
		runs := resp.Msg.GetRuns()
		result = append(result, runs...)
		if uint32(len(runs)) < limit {
			break
		}
		offset += limit
	}
	return result, nil
}

func runSortTime(run *agentcomposev2.RunSummary) string {
	return firstNonEmptyString(run.GetUpdatedAt(), run.GetCreatedAt(), run.GetStartedAt(), run.GetCompletedAt())
}

func resolveComposeRunIDRef(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, agentName, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run id is required")}
	}
	if identity.IsID(ref) {
		return ref, nil
	}
	if !shouldResolveComposeLogResourceRef(ref) {
		return ref, nil
	}
	runs, err := listLogRunRefCandidates(ctx, client, projectID, agentName)
	if err != nil {
		return "", commandExitErrorForConnect(fmt.Errorf("resolve run %s: %w", ref, err))
	}
	var matches []*agentcomposev2.RunSummary
	for _, run := range runs {
		if resourceIDMatchesRef(run.GetRunId(), shortOpaqueID(run.GetRunId()), ref) {
			matches = append(matches, run)
		}
	}
	if len(matches) == 0 {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run %q not found in current project", ref)}
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for _, match := range matches {
			ids = append(ids, shortOpaqueID(match.GetRunId()))
		}
		sort.Strings(ids)
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("run ref %q is ambiguous in current project; matches: %s", ref, strings.Join(ids, ", "))}
	}
	return matches[0].GetRunId(), nil
}

func getRunDetail(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, runID string) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	return client.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{
		ProjectId: strings.TrimSpace(projectID),
		RunId:     strings.TrimSpace(runID),
	}))
}

func shortRunID(runID string) string {
	return shortOpaqueID(strings.TrimPrefix(strings.TrimSpace(runID), "run-"))
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
