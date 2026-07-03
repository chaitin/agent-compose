package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	"agent-compose/pkg/agentcompose/projects"
	agentcompose "agent-compose/pkg/agentcompose/service"
	"agent-compose/pkg/compose"
	agentcomposev1 "agent-compose/proto/agentcompose/v1"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func runComposeConfigCommand(cmd *cobra.Command, cli options, options composeConfigOptions) error {
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

func runComposeUpCommand(cmd *cobra.Command, cli options) error {
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

func runComposeDownCommand(cmd *cobra.Command, cli options) error {
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

func runComposeRunCommand(cmd *cobra.Command, cli options, options composeRunOptions, args []string) error {
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

func runComposeLogsCommand(cmd *cobra.Command, cli options, options composeLogsOptions) error {
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

func runComposePSCommand(cmd *cobra.Command, cli options) error {
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

func runComposeExecCommand(cmd *cobra.Command, cli options, options composeExecOptions, args []string) error {
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

func runComposeImageListCommand(cmd *cobra.Command, cli options, options composeImageListOptions) error {
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

func runComposeImagePullCommand(cmd *cobra.Command, cli options, options composeImagePullOptions, imageRef string) error {
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

func runComposeImageRemoveCommand(cmd *cobra.Command, cli options, options composeImageRemoveOptions, imageRef string) error {
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

func runComposeImageInspectCommand(cmd *cobra.Command, cli options, imageRef string) error {
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

func runComposeInspectCommand(cmd *cobra.Command, cli options, args []string) error {
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

func resolveComposeProject(cli options) (string, *compose.NormalizedProjectSpec, string, error) {
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

func loadNormalizedCompose(cli options) (string, *compose.NormalizedProjectSpec, error) {
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
