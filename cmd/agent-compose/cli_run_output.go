package main

import (
	"context"
	"encoding/json"
	"fmt"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"io"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

func startDetachedRun(cmd *cobra.Command, cli cliOptions, projectName string, client agentcomposev2connect.RunServiceClient, req *agentcomposev2.RunAgentRequest) error {
	resp, err := client.StartAgentRun(cmd.Context(), connect.NewRequest(&agentcomposev2.StartAgentRunRequest{Run: req}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("start run project %s agent %s: %w", projectName, req.GetAgentName(), err))
	}
	run := resp.Msg.GetRun()
	if run == nil {
		return fmt.Errorf("start run project %s agent %s: response did not include run summary", projectName, req.GetAgentName())
	}
	warnings := appendUniqueStrings(append([]string(nil), resp.Msg.GetWarnings()...), run.GetWarnings()...)
	logsCommand := detachedRunLogsCommand(cli, run.GetRunId())
	jupyter := composeRunJupyterOutput{}
	if runJupyterRequested(req) {
		var resolveErr error
		jupyter, run, resolveErr = resolveDetachedRunJupyterOutput(cmd.Context(), cli, client, run)
		if resolveErr != nil {
			warnings = appendUniqueStrings(warnings, resolveErr.Error())
		}
	}
	if cli.JSON {
		output := composeRunOutputFromSummary(run, projectName, logsCommand)
		output.Warnings = warnings
		output.JupyterURL = jupyter.URL
		output.JupyterPath = jupyter.Path
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	if err := writeRunWarnings(cmd.ErrOrStderr(), warnings); err != nil {
		return err
	}
	return writeDetachedRunText(cmd.OutOrStdout(), run, logsCommand, jupyter)
}

func writeDetachedRunText(out io.Writer, run *agentcomposev2.RunSummary, logsCommand string, jupyter composeRunJupyterOutput) error {
	if _, err := fmt.Fprintf(out, "Run: %s\nSandbox: %s\nStatus: %s\nLogs: %s\n",
		firstNonEmptyString(displayOpaqueID(run.GetRunId()), "-"),
		firstNonEmptyString(displayOpaqueID(run.GetSandboxId()), "-"),
		runStatusText(run.GetStatus()),
		logsCommand,
	); err != nil {
		return err
	}
	return writeJupyterRunText(out, jupyter)
}

type composeRunOutput struct {
	ID             string   `json:"id"`
	ShortID        string   `json:"short_id"`
	ProjectID      string   `json:"project_id"`
	ProjectName    string   `json:"project_name"`
	AgentName      string   `json:"agent_name"`
	Source         string   `json:"source"`
	Status         string   `json:"status"`
	SandboxID      string   `json:"sandbox_id,omitempty"`
	SandboxShortID string   `json:"sandbox_short_id,omitempty"`
	ExitCode       int32    `json:"exit_code"`
	Error          string   `json:"error,omitempty"`
	StartedAt      string   `json:"started_at,omitempty"`
	CompletedAt    string   `json:"completed_at,omitempty"`
	DurationMs     int64    `json:"duration_ms,omitempty"`
	Prompt         string   `json:"prompt,omitempty"`
	Output         string   `json:"output,omitempty"`
	ResultJSON     string   `json:"result_json,omitempty"`
	LogsPath       string   `json:"logs_path,omitempty"`
	ArtifactsDir   string   `json:"artifacts_dir,omitempty"`
	CleanupError   string   `json:"cleanup_error,omitempty"`
	Driver         string   `json:"driver,omitempty"`
	ImageRef       string   `json:"image_ref,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	// Labels is only populated from a RunDetail. Summary-derived output omits it
	// because RunSummary carries no labels, so an absent field means "not
	// reported here", never "this run has no labels".
	Labels      map[string]string `json:"labels,omitempty"`
	LogsCommand string            `json:"logs_command,omitempty"`
	JupyterURL  string            `json:"jupyter_url,omitempty"`
	JupyterPath string            `json:"jupyter_path,omitempty"`
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

func composeRunOutputFromDetail(run *agentcomposev2.RunDetail) composeRunOutput {
	return composeRunOutputFromDetailWithOptions(run, composeLogsOptions{TailLines: -1})
}

func composeRunOutputFromSummary(run *agentcomposev2.RunSummary, projectName, logsCommand string) composeRunOutput {
	sandboxID := runSummarySandboxID(run)
	return composeRunOutput{
		ID:             displayOpaqueID(run.GetRunId()),
		ShortID:        shortOpaqueID(run.GetRunId()),
		ProjectID:      displayOpaqueID(run.GetProjectId()),
		ProjectName:    firstNonEmptyString(run.GetProjectName(), projectName),
		AgentName:      run.GetAgentName(),
		Source:         runSourceText(run.GetSource()),
		Status:         runStatusText(run.GetStatus()),
		SandboxID:      displayOpaqueID(sandboxID),
		SandboxShortID: shortOpaqueID(sandboxID),
		ExitCode:       run.GetExitCode(),
		Error:          run.GetError(),
		StartedAt:      formatProtoTimestamp(run.GetStartedAt()),
		CompletedAt:    formatProtoTimestamp(run.GetCompletedAt()),
		DurationMs:     run.GetDurationMs(),
		Warnings:       appendUniqueStrings(nil, run.GetWarnings()...),
		LogsCommand:    logsCommand,
	}
}

func composeRunOutputFromDetailWithOptions(run *agentcomposev2.RunDetail, options composeLogsOptions) composeRunOutput {
	summary := run.GetSummary()
	sandboxID := runSummarySandboxID(summary)
	return composeRunOutput{
		ID:             displayOpaqueID(summary.GetRunId()),
		ShortID:        shortOpaqueID(summary.GetRunId()),
		ProjectID:      displayOpaqueID(summary.GetProjectId()),
		ProjectName:    summary.GetProjectName(),
		AgentName:      summary.GetAgentName(),
		Source:         runSourceText(summary.GetSource()),
		Status:         runStatusText(summary.GetStatus()),
		SandboxID:      displayOpaqueID(sandboxID),
		SandboxShortID: shortOpaqueID(sandboxID),
		ExitCode:       summary.GetExitCode(),
		Error:          summary.GetError(),
		StartedAt:      formatProtoTimestamp(summary.GetStartedAt()),
		CompletedAt:    formatProtoTimestamp(summary.GetCompletedAt()),
		DurationMs:     summary.GetDurationMs(),
		Prompt:         run.GetPrompt(),
		Output:         tailLogOutput(run.GetOutput(), options.TailLines),
		ResultJSON:     run.GetResultJson(),
		LogsPath:       run.GetLogsPath(),
		ArtifactsDir:   run.GetArtifactsDir(),
		CleanupError:   run.GetCleanupError(),
		Driver:         run.GetDriver(),
		ImageRef:       run.GetImageRef(),
		Warnings:       appendUniqueStrings(append([]string(nil), summary.GetWarnings()...), run.GetWarnings()...),
		Labels:         run.GetLabels(),
	}
}

func writePrefixedRunOutput(out io.Writer, summary *agentcomposev2.RunSummary, output string, timestamp bool) error {
	return writePrefixedRunOutputWithTimestamp(out, summary, output, timestamp, runLogTimestamp(summary))
}

func writePrefixedRunOutputWithTimestamp(out io.Writer, summary *agentcomposev2.RunSummary, output string, timestamp bool, timestampValue string) error {
	writer := newPrefixedRunOutputStreamWriter(out, timestamp)
	if err := writer.Write(summary, output, timestampValue); err != nil {
		return err
	}
	return writer.Finish()
}

type prefixedRunOutputStreamWriter struct {
	out       io.Writer
	timestamp bool
	lineOpen  bool
}

func newPrefixedRunOutputStreamWriter(out io.Writer, timestamp bool) *prefixedRunOutputStreamWriter {
	return &prefixedRunOutputStreamWriter{out: out, timestamp: timestamp}
}

func (w *prefixedRunOutputStreamWriter) Write(summary *agentcomposev2.RunSummary, output, timestampValue string) error {
	for len(output) > 0 {
		if !w.lineOpen {
			if err := w.writePrefix(summary, timestampValue); err != nil {
				return err
			}
		}
		chunk := output
		rest := ""
		if idx := strings.IndexByte(output, '\n'); idx >= 0 {
			chunk = output[:idx+1]
			rest = output[idx+1:]
		}
		if _, err := io.WriteString(w.out, chunk); err != nil {
			return err
		}
		w.lineOpen = !strings.HasSuffix(chunk, "\n")
		output = rest
	}
	return nil
}

func (w *prefixedRunOutputStreamWriter) Finish() error {
	if !w.lineOpen {
		return nil
	}
	w.lineOpen = false
	_, err := fmt.Fprintln(w.out)
	return err
}

func (w *prefixedRunOutputStreamWriter) writePrefix(summary *agentcomposev2.RunSummary, timestampValue string) error {
	prefix := runLogPrefix(summary)
	if w.timestamp {
		if runTime := formatComposeLogTimestamp(timestampValue); runTime != "" {
			_, err := fmt.Fprintf(w.out, "%s [%s]| ", prefix, runTime)
			return err
		}
	}
	_, err := fmt.Fprintf(w.out, "%s | ", prefix)
	return err
}

func runSummaryExitCode(run *agentcomposev2.RunSummary) int {
	if code := int(run.GetExitCode()); code > 0 && code < 126 {
		return code
	}
	return exitCodeGeneral
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
