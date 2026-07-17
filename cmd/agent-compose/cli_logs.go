package main

import (
	"agent-compose/pkg/compose"
	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"golang.org/x/text/width"
)

type composeLogsOptions struct {
	ResourceID string
	AgentName  string
	RunID      string
	SandboxID  string
	TailLines  int
	Follow     bool
	Timestamp  bool
}

func runComposeLogsCommand(cmd *cobra.Command, cli cliOptions, options composeLogsOptions, args []string) error {
	if cli.JSON && options.Follow {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --json cannot be combined with --follow")}
	}
	normalizedOptions, err := normalizeComposeLogsOptions(cmd, options, args)
	if err != nil {
		return err
	}
	if normalizedOptions.ResourceID != "" {
		return runComposeLogsForResourceID(cmd, cli, normalizedOptions)
	}
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	normalizedOptions, err = resolveComposeLogRefs(cmd.Context(), clients.run, clients.sandbox, normalized, projectID, normalizedOptions)
	if err != nil {
		return err
	}
	if strings.TrimSpace(normalizedOptions.RunID) != "" {
		run, err := getRunDetail(cmd.Context(), clients.run, projectID, normalizedOptions.RunID)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", strings.TrimSpace(normalizedOptions.RunID), normalized.Name, err))
		}
		if normalizedOptions.Follow {
			return followRunLogStream(cmd.Context(), cmd.OutOrStdout(), clients.run, projectID, run.Msg.GetRun().GetSummary(), normalizedOptions)
		}
		return writeLogsForRun(cmd.OutOrStdout(), run.Msg.GetRun(), cli.JSON, normalizedOptions)
	}
	return followOrPrintProjectLogs(cmd, cli, clients, projectID, normalized.Name, normalizedOptions)
}

func normalizeComposeLogsOptions(cmd *cobra.Command, options composeLogsOptions, args []string) (composeLogsOptions, error) {
	if len(args) > 0 {
		if cmd.Flags().Changed("agent") {
			return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs agent can be specified either positionally or with --agent, not both")}
		}
		if identity.IsIDPrefix(args[0]) {
			options.ResourceID = strings.TrimSpace(args[0])
		} else {
			options.AgentName = args[0]
		}
	}
	if options.TailLines < -1 {
		return options, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --tail must be -1 or greater")}
	}
	return options, nil
}

func resolveComposeLogRefs(ctx context.Context, runClient agentcomposev2connect.RunServiceClient, sandboxClient agentcomposev2connect.SandboxServiceClient, normalized *compose.NormalizedProjectSpec, projectID string, options composeLogsOptions) (composeLogsOptions, error) {
	if strings.TrimSpace(options.AgentName) != "" {
		agentName, err := resolveComposeAgentNameFromSpec(normalized, projectID, options.AgentName)
		if err != nil {
			return options, err
		}
		options.AgentName = agentName
	}
	if shouldResolveComposeLogResourceRef(options.RunID) {
		runID, err := resolveComposeRunIDRef(ctx, runClient, projectID, options.AgentName, options.RunID)
		if err != nil {
			return options, err
		}
		options.RunID = runID
	}
	if shouldResolveComposeLogResourceRef(options.SandboxID) {
		sandboxID, runErr := resolveComposeSandboxIDRefFromRuns(ctx, runClient, projectID, options.AgentName, options.SandboxID)
		if runErr != nil {
			var err error
			sandboxID, err = resolveProjectSandboxIDRef(ctx, sandboxClient, projectID, options.SandboxID)
			if err != nil {
				return options, err
			}
		}
		options.SandboxID = sandboxID
	}
	return options, nil
}

func shouldResolveComposeLogResourceRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	return identity.IsID(ref) || identity.IsIDPrefix(ref)
}

type composeLogsOutput struct {
	Runs []composeLogRunOutput `json:"runs"`
}

type composeLogRunOutput struct {
	AgentName  string `json:"agent_name,omitempty"`
	RunID      string `json:"run_id"`
	RunShortID string `json:"run_short_id,omitempty"`
	Time       string `json:"time,omitempty"`
	Prompt     string `json:"prompt,omitempty"`
	Content    string `json:"content"`
}

func followOrPrintProjectLogs(cmd *cobra.Command, cli cliOptions, clients cliServiceClients, projectID, projectName string, options composeLogsOptions) error {
	client := clients.run
	if options.Follow && !cli.JSON {
		runs, err := listLogRuns(cmd.Context(), client, projectID, options)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("list logs for project %s: %w", projectName, err))
		}
		if len(runs) == 0 && options.SandboxID != "" {
			return writeSandboxHistoryLogs(cmd, cli, clients.sandbox, projectID, options)
		}
		for _, summary := range runs {
			if err := followRunLogStream(cmd.Context(), cmd.OutOrStdout(), client, projectID, summary, options); err != nil {
				return err
			}
		}
		return nil
	}
	printed := map[string]runLogPrintState{}
	for {
		runs, err := listLogRuns(cmd.Context(), client, projectID, options)
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("list logs for project %s: %w", projectName, err))
		}
		if len(runs) == 0 {
			if options.SandboxID != "" {
				return writeSandboxHistoryLogs(cmd, cli, clients.sandbox, projectID, options)
			}
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
		sortLogRunDetails(details)
		if cli.JSON {
			output := composeLogsOutput{Runs: make([]composeLogRunOutput, 0, len(details))}
			for _, detail := range details {
				output.Runs = append(output.Runs, composeLogRunOutputFromDetail(detail, options))
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
		} else if err := writeLogDetails(cmd.OutOrStdout(), details, printed, options); err != nil {
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
		SandboxId: strings.TrimSpace(options.SandboxID),
		Limit:     20,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetRuns(), nil
}

func listLogRunRefCandidates(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID, agentName string) ([]*agentcomposev2.RunSummary, error) {
	resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
		ProjectId: strings.TrimSpace(projectID),
		AgentName: strings.TrimSpace(agentName),
		Limit:     200,
	}))
	if err != nil {
		return nil, err
	}
	return resp.Msg.GetRuns(), nil
}

func followRunLogStream(ctx context.Context, out io.Writer, client agentcomposev2connect.RunServiceClient, projectID string, summary *agentcomposev2.RunSummary, options composeLogsOptions) error {
	if summary == nil {
		return nil
	}
	detailResp, err := getRunDetail(ctx, client, projectID, summary.GetRunId())
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", summary.GetRunId(), projectID, err))
	}
	detailRun := detailResp.Msg.GetRun()
	displaySummary := summary
	if detailSummary := detailRun.GetSummary(); detailSummary != nil {
		displaySummary = detailSummary
	}
	prompt := runLogPrompt(detailRun)
	promptPrinted := false
	refreshPrompt := func() error {
		if promptPrinted || prompt != "" {
			return nil
		}
		detailResp, err := getRunDetail(ctx, client, projectID, summary.GetRunId())
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", summary.GetRunId(), projectID, err))
		}
		detailRun = detailResp.Msg.GetRun()
		if detailSummary := detailRun.GetSummary(); detailSummary != nil {
			displaySummary = detailSummary
		}
		prompt = runLogPrompt(detailRun)
		return nil
	}
	printPrompt := func() error {
		if prompt == "" || promptPrinted {
			return nil
		}
		if err := writePrefixedRunOutput(out, displaySummary, runLogConversationText(out, displaySummary, prompt, "", options.Timestamp), options.Timestamp); err != nil {
			return err
		}
		promptPrinted = true
		return nil
	}
	if err := printPrompt(); err != nil {
		return err
	}
	tailLines := uint32(0)
	if options.TailLines > 0 {
		tailLines = uint32(options.TailLines)
	}
	stream, err := client.FollowRunLogs(ctx, connect.NewRequest(&agentcomposev2.FollowRunLogsRequest{
		ProjectId: strings.TrimSpace(projectID),
		RunId:     summary.GetRunId(),
		TailLines: tailLines,
		Follow:    true,
	}))
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("follow run %s logs: %w", summary.GetRunId(), err))
	}
	assistantStarted := false
	for stream.Receive() {
		chunk := stream.Msg()
		if chunk.GetData() != "" {
			if !assistantStarted && !promptPrinted && prompt == "" {
				if err := refreshPrompt(); err != nil {
					return err
				}
				if err := printPrompt(); err != nil {
					return err
				}
			}
			if !assistantStarted {
				if err := writePrefixedRunOutput(out, displaySummary, runLogAssistantSeparator(out, displaySummary, options.Timestamp), options.Timestamp); err != nil {
					return err
				}
				assistantStarted = true
			}
			if err := writePrefixedRunOutput(out, displaySummary, chunk.GetData(), options.Timestamp); err != nil {
				return err
			}
		}
		if chunk.GetIsFinal() {
			if !assistantStarted && !promptPrinted && prompt == "" {
				if err := refreshPrompt(); err != nil {
					return err
				}
				if err := printPrompt(); err != nil {
					return err
				}
			}
			return nil
		}
	}
	if err := stream.Err(); err != nil {
		return commandExitErrorForConnect(fmt.Errorf("follow run %s logs: %w", summary.GetRunId(), err))
	}
	return nil
}

func writeLogsForRun(out io.Writer, run *agentcomposev2.RunDetail, asJSON bool, options composeLogsOptions) error {
	if asJSON {
		data, err := json.MarshalIndent(composeLogsOutput{Runs: []composeLogRunOutput{composeLogRunOutputFromDetail(run, options)}}, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	return writeLogDetails(out, []*agentcomposev2.RunDetail{run}, map[string]runLogPrintState{}, options)
}

func composeLogRunOutputFromDetail(run *agentcomposev2.RunDetail, options composeLogsOptions) composeLogRunOutput {
	summary := run.GetSummary()
	return composeLogRunOutput{
		AgentName:  summary.GetAgentName(),
		RunID:      displayOpaqueID(summary.GetRunId()),
		RunShortID: shortOpaqueID(summary.GetRunId()),
		Time:       formatComposeLogTimestamp(runLogTimestamp(summary)),
		Prompt:     runLogPrompt(run),
		Content:    tailLogOutput(run.GetOutput(), options.TailLines),
	}
}

type runLogPrintState struct {
	outputPos     int
	promptPrinted bool
}

func writeLogDetails(out io.Writer, details []*agentcomposev2.RunDetail, printed map[string]runLogPrintState, options composeLogsOptions) error {
	for _, detail := range details {
		summary := detail.GetSummary()
		runID := summary.GetRunId()
		output := detail.GetOutput()
		prompt := runLogPrompt(detail)
		state := printed[runID]
		start := 0
		if options.Follow {
			start = state.outputPos
			if start > len(output) {
				start = 0
			}
		}
		promptChunk := ""
		if prompt != "" && !state.promptPrinted {
			promptChunk = prompt
		}
		if start == len(output) && promptChunk == "" {
			continue
		}
		chunk := output[start:]
		if options.TailLines >= 0 && (!options.Follow || start == 0) {
			chunk = tailLogOutput(chunk, options.TailLines)
		}
		text := runLogConversationText(out, summary, promptChunk, chunk, options.Timestamp)
		if err := writePrefixedRunOutput(out, summary, text, options.Timestamp); err != nil {
			return err
		}
		state.outputPos = len(output)
		state.promptPrinted = state.promptPrinted || promptChunk != ""
		printed[runID] = state
	}
	return nil
}

func runLogPrompt(run *agentcomposev2.RunDetail) string {
	if run == nil {
		return ""
	}
	if prompt := run.GetPrompt(); strings.TrimSpace(prompt) != "" {
		return prompt
	}
	var result struct {
		Mode    string `json:"mode"`
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(run.GetResultJson()), &result); err != nil {
		return ""
	}
	if strings.TrimSpace(result.Mode) == "command" && strings.TrimSpace(result.Command) != "" {
		return result.Command
	}
	return ""
}

func runLogConversationText(out io.Writer, summary *agentcomposev2.RunSummary, prompt, output string, timestamp bool) string {
	var builder strings.Builder
	if prompt != "" {
		builder.WriteString(runLogUserSeparator(out, summary, timestamp))
		builder.WriteString(prompt)
		if !strings.HasSuffix(prompt, "\n") {
			builder.WriteString("\n")
		}
	}
	if output != "" {
		builder.WriteString(runLogAssistantSeparator(out, summary, timestamp))
		builder.WriteString(output)
		if !strings.HasSuffix(output, "\n") {
			builder.WriteString("\n")
		}
	}
	return builder.String()
}

func runLogUserSeparator(out io.Writer, summary *agentcomposev2.RunSummary, timestamp bool) string {
	return runLogSeparator(out, summary, timestamp, '>')
}

func runLogAssistantSeparator(out io.Writer, summary *agentcomposev2.RunSummary, timestamp bool) string {
	return runLogSeparator(out, summary, timestamp, '<')
}

func runLogSeparator(out io.Writer, summary *agentcomposev2.RunSummary, timestamp bool, marker rune) string {
	width := runLogSeparatorWidth(out, summary, timestamp)
	if width < 8 {
		width = 8
	}
	return strings.Repeat(string(marker), width) + "\n"
}

func runLogSeparatorWidth(out io.Writer, summary *agentcomposev2.RunSummary, timestamp bool) int {
	width := terminalOutputWidth(out)
	if width <= 0 {
		width = 80
	}
	prefixWidth := runLogLinePrefixWidth(summary, runLogTimestamp(summary), timestamp)
	if width > prefixWidth {
		return width - prefixWidth
	}
	return width
}

func terminalOutputWidth(out io.Writer) int {
	file, ok := out.(*os.File)
	if !ok {
		return 80
	}
	width := terminalFileWidth(file)
	if width <= 0 {
		return 80
	}
	return width
}

func runLogLinePrefixWidth(summary *agentcomposev2.RunSummary, timestampValue string, timestamp bool) int {
	prefixWidth := displayStringWidth(runLogPrefix(summary))
	runTime := ""
	if timestamp {
		runTime = formatComposeLogTimestamp(timestampValue)
	}
	if runTime != "" {
		return prefixWidth + displayStringWidth(runTime) + len(" []| ")
	}
	return prefixWidth + len(" | ")
}

func displayStringWidth(value string) int {
	total := 0
	for _, r := range value {
		switch {
		case r == '\n' || r == '\r':
			continue
		case r == '\t':
			total++
		case unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || unicode.Is(unicode.Cf, r):
			continue
		default:
			switch width.LookupRune(r).Kind() {
			case width.EastAsianFullwidth, width.EastAsianWide:
				total += 2
			default:
				total++
			}
		}
	}
	return total
}

func runLogPrefix(summary *agentcomposev2.RunSummary) string {
	runID := strings.TrimSpace(summary.GetRunId())
	agentName := strings.TrimSpace(summary.GetAgentName())
	if agentName == "" {
		return firstNonEmptyString(shortOpaqueID(runID), "-")
	}
	if runID == "" {
		return agentName + "-run-"
	}
	return agentName + "-run-" + shortRunID(runID)
}

func tailLogOutput(output string, lines int) string {
	if lines < 0 || output == "" {
		return output
	}
	if lines == 0 {
		return ""
	}
	trimmed := strings.TrimSuffix(output, "\n")
	if trimmed == "" {
		return output
	}
	parts := strings.Split(trimmed, "\n")
	if len(parts) <= lines {
		return output
	}
	result := strings.Join(parts[len(parts)-lines:], "\n")
	if strings.HasSuffix(output, "\n") {
		result += "\n"
	}
	return result
}

func runLogTimestamp(summary *agentcomposev2.RunSummary) string {
	return firstNonEmptyString(summary.GetCompletedAt(), summary.GetUpdatedAt(), summary.GetStartedAt())
}

func runLogSortTimestamp(summary *agentcomposev2.RunSummary) string {
	return firstNonEmptyString(summary.GetStartedAt(), summary.GetCreatedAt(), summary.GetUpdatedAt(), summary.GetCompletedAt())
}

func sortLogRunDetails(details []*agentcomposev2.RunDetail) {
	sort.SliceStable(details, func(i, j int) bool {
		return logRunSummaryLess(details[i].GetSummary(), details[j].GetSummary())
	})
}

func logRunSummaryLess(left, right *agentcomposev2.RunSummary) bool {
	leftTime, leftOK := parseComposeLogSortTimestamp(runLogSortTimestamp(left))
	rightTime, rightOK := parseComposeLogSortTimestamp(runLogSortTimestamp(right))
	switch {
	case leftOK && rightOK && !leftTime.Equal(rightTime):
		return leftTime.Before(rightTime)
	case leftOK != rightOK:
		return leftOK
	}
	if agent := strings.Compare(left.GetAgentName(), right.GetAgentName()); agent != 0 {
		return agent < 0
	}
	return strings.Compare(left.GetRunId(), right.GetRunId()) < 0
}

func parseComposeLogSortTimestamp(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}

func formatComposeLogTimestamp(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return value
	}
	return parsed.UTC().Format("2006-01-02T15:04:05.000Z")
}

func detachedRunLogsCommand(cli cliOptions, runID string) string {
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
	parts = append(parts, "logs", "--run", strings.TrimSpace(runID), "--follow")
	for i, part := range parts {
		parts[i] = shellQuoteCLIArg(part)
	}
	return strings.Join(parts, " ")
}
