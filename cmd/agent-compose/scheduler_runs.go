package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type composeSchedulerStopOptions struct {
	Reason string
}

type composeSchedulerRunOutput struct {
	ID                 string `json:"id"`
	ShortID            string `json:"short_id"`
	ProjectID          string `json:"project_id"`
	ProjectName        string `json:"project_name,omitempty"`
	AgentName          string `json:"agent_name"`
	SchedulerID        string `json:"scheduler_id"`
	SchedulerShortID   string `json:"scheduler_short_id,omitempty"`
	TriggerID          string `json:"trigger_id,omitempty"`
	TriggerShortID     string `json:"trigger_short_id,omitempty"`
	TriggerKind        string `json:"trigger_kind,omitempty"`
	TriggerSource      string `json:"trigger_source,omitempty"`
	Status             string `json:"status"`
	StartedAt          string `json:"started_at,omitempty"`
	CompletedAt        string `json:"completed_at,omitempty"`
	DurationMs         int64  `json:"duration_ms,omitempty"`
	Error              string `json:"error,omitempty"`
	ResultJSON         string `json:"result_json,omitempty"`
	PayloadJSON        string `json:"payload_json,omitempty"`
	SourceScriptSHA256 string `json:"source_script_sha256,omitempty"`
	ArtifactsDir       string `json:"artifacts_dir,omitempty"`
	InspectCommand     string `json:"inspect_command,omitempty"`
	StopCommand        string `json:"stop_command,omitempty"`
}

type composeSchedulerStopOutput struct {
	Run           composeSchedulerRunOutput `json:"run"`
	StopRequested bool                      `json:"stop_requested"`
}

type composeSchedulerInvocationOutput struct {
	Scheduler  string   `json:"scheduler"`
	Status     string   `json:"status"`
	DurationMs int64    `json:"duration_ms"`
	ResultJSON string   `json:"result_json,omitempty"`
	Warnings   []string `json:"warnings,omitempty"`
}

func addComposeSchedulerExecutionFlags(cmd *cobra.Command, options *composeSchedulerTriggerOptions) {
	cmd.Flags().StringVar(&options.PayloadJSON, "payload", "", "JSON payload passed to the trigger callback")
	cmd.Flags().BoolVarP(&options.Detach, "detach", "d", false, "Start the scheduler run and return immediately")
}

func runComposeSchedulerInvokeCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerInvokeOptions, schedulerRef string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	scheduler, err := resolveComposeScheduler(normalized, projectID, schedulerRef)
	if err != nil {
		return err
	}
	agent, ok := composeRunAgentSpec(normalized, scheduler.AgentName)
	if !ok || agent.Scheduler == nil || !agent.Scheduler.HasScript() {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler %q does not define a script; use scheduler trigger to execute one of its triggers", scheduler.AgentName)}
	}
	payloadJSON := strings.TrimSpace(options.PayloadJSON)
	if payloadJSON != "" {
		payloadJSON, err = domain.NormalizeJSONDocument(payloadJSON)
		if err != nil {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler invoke --payload: %w", err)}
		}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	response, err := clients.project.InvokeScheduler(cmd.Context(), connect.NewRequest(&agentcomposev2.InvokeSchedulerRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, AgentName: scheduler.AgentName, PayloadJson: payloadJSON,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("invoke scheduler %s in project %s: %w", scheduler.AgentName, normalized.Name, err))
	}
	output := composeSchedulerInvocationOutput{
		Scheduler: scheduler.AgentName, Status: "succeeded", DurationMs: response.Msg.GetDurationMs(),
		ResultJSON: response.Msg.GetResultJson(), Warnings: append([]string(nil), response.Msg.GetWarnings()...),
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Scheduler: %s\nStatus: %s\nDuration: %s\n", output.Scheduler, output.Status, formatDurationMs(output.DurationMs)); err != nil {
		return err
	}
	if output.ResultJSON != "" {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "Result: %s\n", output.ResultJSON); err != nil {
			return err
		}
	}
	for _, warning := range output.Warnings {
		if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: %s\n", warning); err != nil {
			return err
		}
	}
	return nil
}

func runComposeSchedulerTriggerV2Command(cmd *cobra.Command, cli cliOptions, options composeSchedulerTriggerOptions, agentRef, triggerRef string) error {
	options, err := prepareComposeSchedulerExecutionOptions(cmd, "scheduler trigger", options)
	if err != nil {
		return err
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	trigger, err := resolveComposeSchedulerTrigger(cmd.Context(), clients, normalized, projectID, agentRef, triggerRef)
	if err != nil {
		return err
	}
	triggerID := firstNonEmptyString(trigger.RawTriggerID, trigger.TriggerID)
	return executeComposeSchedulerRun(cmd, cli, normalized.Name, projectID, trigger.AgentName, triggerID, options, clients.project)
}

func executeComposeSchedulerRun(cmd *cobra.Command, cli cliOptions, projectName, projectID, agentName, triggerID string, options composeSchedulerTriggerOptions, client agentcomposev2connect.ProjectServiceClient) error {
	project := &agentcomposev2.ProjectRef{ProjectId: projectID}
	var run *agentcomposev2.SchedulerRun
	if options.Detach {
		response, err := client.StartSchedulerRun(cmd.Context(), connect.NewRequest(&agentcomposev2.StartSchedulerRunRequest{
			Project: project, AgentName: agentName, TriggerId: triggerID, PayloadJson: options.PayloadJSON,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("start scheduler run for project %s agent %s: %w", projectName, agentName, err))
		}
		run = response.Msg.GetRun()
	} else {
		response, err := client.RunScheduler(cmd.Context(), connect.NewRequest(&agentcomposev2.RunSchedulerRequest{
			Project: project, AgentName: agentName, TriggerId: triggerID, PayloadJson: options.PayloadJSON,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("run scheduler for project %s agent %s: %w", projectName, agentName, err))
		}
		run = response.Msg.GetRun()
	}
	if run == nil {
		return fmt.Errorf("scheduler run for project %s agent %s: response did not include a run", projectName, agentName)
	}
	output := composeSchedulerRunOutputFromProto(run, projectName)
	if options.Detach {
		output.InspectCommand = schedulerRunCLICommand(cli, "inspect", "run", run.GetRunId())
		output.StopCommand = schedulerRunCLICommand(cli, "scheduler", "stop", run.GetRunId())
	}
	if err := writeComposeSchedulerRunOutput(cmd.OutOrStdout(), output, cli.JSON, options.Detach); err != nil {
		return err
	}
	if options.Detach {
		return nil
	}
	return composeSchedulerRunCompletionError(projectName, output)
}

func runComposeSchedulerStopCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerStopOptions, runID string) error {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler stop requires a run id")}
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	response, err := clients.project.StopSchedulerRun(cmd.Context(), connect.NewRequest(&agentcomposev2.StopSchedulerRunRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, RunId: runID, Reason: strings.TrimSpace(options.Reason),
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("stop scheduler run %s in project %s: %w", runID, normalized.Name, err))
	}
	run := response.Msg.GetRun()
	if run == nil {
		return fmt.Errorf("stop scheduler run %s in project %s: response did not include a run", runID, normalized.Name)
	}
	output := composeSchedulerStopOutput{Run: composeSchedulerRunOutputFromProto(run, normalized.Name), StopRequested: response.Msg.GetStopRequested()}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	if err := writeSchedulerRunText(cmd.OutOrStdout(), output.Run); err != nil {
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "Stop requested: %t\n", output.StopRequested)
	return err
}

func inspectComposeRunOutput(ctx context.Context, clients cliServiceClients, projectID, projectName, target string) (any, error) {
	runID, err := resolveComposeRunIDRef(ctx, clients.run, projectID, "", target)
	if err != nil {
		return nil, err
	}
	run, err := getRunDetail(ctx, clients.run, projectID, runID)
	if err == nil {
		return composeRunOutputFromDetail(run.Msg.GetRun()), nil
	}
	if connect.CodeOf(err) != connect.CodeNotFound {
		return nil, commandExitErrorForConnect(fmt.Errorf("inspect run %s in project %s: %w", runID, projectName, err))
	}
	schedulerRun, schedulerErr := clients.project.GetSchedulerRun(ctx, connect.NewRequest(&agentcomposev2.GetSchedulerRunRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: projectID}, RunId: runID,
	}))
	if schedulerErr != nil {
		return nil, commandExitErrorForConnect(fmt.Errorf("inspect run %s in project %s: %w", runID, projectName, schedulerErr))
	}
	if schedulerRun.Msg.GetRun() == nil {
		return nil, fmt.Errorf("inspect scheduler run %s in project %s: response did not include a run", runID, projectName)
	}
	return composeSchedulerRunOutputFromProto(schedulerRun.Msg.GetRun(), projectName), nil
}

func prepareComposeSchedulerExecutionOptions(cmd *cobra.Command, command string, options composeSchedulerTriggerOptions) (composeSchedulerTriggerOptions, error) {
	if err := rejectChangedSchedulerExecutionFlags(cmd, command); err != nil {
		return options, err
	}
	return normalizeComposeSchedulerExecutionOptions(command, options)
}

func rejectChangedSchedulerExecutionFlags(cmd *cobra.Command, command string) error {
	for _, flagName := range []string{"prompt", "sandbox", "driver", "keep-running", "rm", "jupyter", "jupyter-expose"} {
		flag := cmd.Flags().Lookup(flagName)
		if flag == nil || !flag.Changed {
			continue
		}
		return unsupportedSchedulerExecutionFlagError(command, flagName)
	}
	return nil
}

func normalizeComposeSchedulerExecutionOptions(command string, options composeSchedulerTriggerOptions) (composeSchedulerTriggerOptions, error) {
	options.SandboxID = strings.TrimSpace(options.SandboxID)
	options.Driver = strings.TrimSpace(options.Driver)
	options.Prompt = strings.TrimSpace(options.Prompt)
	options.PayloadJSON = strings.TrimSpace(options.PayloadJSON)
	for _, unsupported := range []struct {
		name string
		used bool
	}{
		{name: "prompt", used: options.Prompt != ""},
		{name: "sandbox", used: options.SandboxID != ""},
		{name: "driver", used: options.Driver != ""},
		{name: "keep-running", used: options.KeepRunning},
		{name: "rm", used: options.Remove},
		{name: "jupyter", used: options.Jupyter},
		{name: "jupyter-expose", used: options.JupyterExpose},
	} {
		if unsupported.used {
			return options, unsupportedSchedulerExecutionFlagError(command, unsupported.name)
		}
	}
	if options.PayloadJSON != "" {
		payloadJSON, err := domain.NormalizeJSONDocument(options.PayloadJSON)
		if err != nil {
			return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s --payload: %w", command, err)}
		}
		options.PayloadJSON = payloadJSON
	}
	return options, nil
}

func unsupportedSchedulerExecutionFlagError(command, flagName string) error {
	return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("%s --%s is unsupported for complete scheduler runs", command, flagName)}
}

func composeSchedulerRunOutputFromProto(run *agentcomposev2.SchedulerRun, projectName string) composeSchedulerRunOutput {
	return composeSchedulerRunOutput{
		ID:                 displayOpaqueID(run.GetRunId()),
		ShortID:            shortOpaqueID(run.GetRunId()),
		ProjectID:          displayOpaqueID(run.GetProjectId()),
		ProjectName:        projectName,
		AgentName:          run.GetAgentName(),
		SchedulerID:        displayOpaqueID(run.GetSchedulerId()),
		SchedulerShortID:   shortOpaqueID(run.GetSchedulerId()),
		TriggerID:          displayOpaqueID(run.GetTriggerId()),
		TriggerShortID:     shortOpaqueID(run.GetTriggerId()),
		TriggerKind:        run.GetTriggerKind(),
		TriggerSource:      run.GetTriggerSource(),
		Status:             schedulerRunStatusText(run.GetStatus()),
		StartedAt:          formatProtoTimestamp(run.GetStartedAt()),
		CompletedAt:        formatProtoTimestamp(run.GetCompletedAt()),
		DurationMs:         run.GetDurationMs(),
		Error:              run.GetError(),
		ResultJSON:         run.GetResultJson(),
		PayloadJSON:        run.GetPayloadJson(),
		SourceScriptSHA256: run.GetSourceScriptSha256(),
		ArtifactsDir:       run.GetArtifactsDir(),
	}
}

func writeComposeSchedulerRunOutput(out io.Writer, output composeSchedulerRunOutput, asJSON, detached bool) error {
	if asJSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	if detached {
		return writeDetachedSchedulerRunText(out, output)
	}
	return writeSchedulerRunText(out, output)
}

func writeSchedulerRunText(out io.Writer, run composeSchedulerRunOutput) error {
	trigger := firstNonEmptyString(run.TriggerID, "main")
	if _, err := fmt.Fprintf(out, "Run: %s\nAgent: %s\nTrigger: %s\nStatus: %s\n", run.ID, run.AgentName, trigger, run.Status); err != nil {
		return err
	}
	if run.ResultJSON != "" {
		if _, err := fmt.Fprintf(out, "Result: %s\n", run.ResultJSON); err != nil {
			return err
		}
	}
	if run.Error != "" {
		if _, err := fmt.Fprintf(out, "Error: %s\n", run.Error); err != nil {
			return err
		}
	}
	return nil
}

func writeDetachedSchedulerRunText(out io.Writer, run composeSchedulerRunOutput) error {
	_, err := fmt.Fprintf(out, "Run: %s\nStatus: %s\nInspect: %s\nStop: %s\n", run.ID, run.Status, run.InspectCommand, run.StopCommand)
	return err
}

func composeSchedulerRunCompletionError(projectName string, run composeSchedulerRunOutput) error {
	if run.Status == "succeeded" {
		return nil
	}
	message := firstNonEmptyString(run.Error, run.Status)
	return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("scheduler run %s for project %s agent %s %s: %s", run.ID, projectName, run.AgentName, run.Status, message)}
}

func schedulerRunStatusText(status agentcomposev2.SchedulerRunStatus) string {
	switch status {
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_RUNNING:
		return "running"
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED:
		return "succeeded"
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED:
		return "failed"
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED:
		return "canceled"
	case agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED:
		return "skipped"
	default:
		return "unspecified"
	}
}

func schedulerRunCLICommand(cli cliOptions, args ...string) string {
	parts := []string{"agent-compose"}
	if value := strings.TrimSpace(cli.Host); value != "" {
		parts = append(parts, "--host", value)
	}
	if value := strings.TrimSpace(cli.ComposeFile); value != "" {
		parts = append(parts, "--file", value)
	}
	if value := strings.TrimSpace(cli.ProjectName); value != "" {
		parts = append(parts, "--project-name", value)
	}
	parts = append(parts, args...)
	for index, part := range parts {
		parts[index] = shellQuoteCLIArg(part)
	}
	return strings.Join(parts, " ")
}
