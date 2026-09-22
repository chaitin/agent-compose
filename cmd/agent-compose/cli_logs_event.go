package main

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/spf13/cobra"
)

const eventLogPrefix = "evt_"

// eventLogScopeCap mirrors the daemon-resolved event scope limit (see
// ListRunsRequest.event_id); the CLI only uses it in the truncation warning.
const eventLogScopeCap = 1000

// validateEventLogTarget rejects values that are not full event-bus event
// ids. Prefix matching is intentionally unsupported for --event.
func validateEventLogTarget(eventID string) error {
	if !strings.HasPrefix(eventID, eventLogPrefix) || strings.TrimSpace(strings.TrimPrefix(eventID, eventLogPrefix)) == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --event requires a full event id starting with %s; prefix matching is not supported", eventLogPrefix)}
	}
	return nil
}

// runComposeLogsForEvent lists the agent runs recorded against an event via
// the daemon-side ListRuns event_id filter and replays their logs.
func runComposeLogsForEvent(cmd *cobra.Command, cli cliOptions, clients cliServiceClients, projectID, projectName string, options composeLogsOptions) error {
	// The daemon resolves the event scope server-side; the CLI only pages
	// through the result with the same offset+total loop as other list views.
	runs := make([]*agentcomposev2.RunSummary, 0, 200)
	eventScopeTruncated := false
	offset := uint32(0)
	for {
		resp, err := clients.run.ListRuns(cmd.Context(), connect.NewRequest(&agentcomposev2.ListRunsRequest{
			ProjectId: strings.TrimSpace(projectID),
			AgentName: strings.TrimSpace(options.AgentName),
			EventId:   strings.TrimSpace(options.EventID),
			Offset:    offset,
			Limit:     200,
		}))
		if err != nil {
			return commandExitErrorForConnect(fmt.Errorf("list logs for event %s for project %s: %w", options.EventID, projectName, err))
		}
		if resp.Msg.GetEventScopeTruncated() {
			eventScopeTruncated = true
		}
		runs = append(runs, resp.Msg.GetRuns()...)
		offset += uint32(len(resp.Msg.GetRuns()))
		if len(resp.Msg.GetRuns()) == 0 || offset >= resp.Msg.GetTotal() {
			break
		}
	}
	if eventScopeTruncated {
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: event %s has more associated events than the daemon resolves (cap %d); runs recorded only against events beyond the cap are missing from this output\n", options.EventID, eventLogScopeCap)
		if err != nil {
			return err
		}
	}
	if len(runs) == 0 {
		if cli.JSON {
			data, err := json.MarshalIndent(composeLogsOutput{}, "", "  ")
			if err != nil {
				return err
			}
			return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
		}
		_, err := fmt.Fprintf(cmd.ErrOrStderr(), "No runs are associated with event %s\n", options.EventID)
		return err
	}
	// ListRuns returns newest first; replay oldest first like the other logs paths.
	sort.SliceStable(runs, func(i, j int) bool { return logRunSummaryLess(runs[i], runs[j]) })
	if cli.JSON {
		output := composeLogsOutput{Runs: make([]composeLogRunOutput, 0, len(runs))}
		for _, summary := range runs {
			detail, err := getRunDetail(cmd.Context(), clients.run, projectID, summary.GetRunId())
			if err != nil {
				return commandExitErrorForConnect(fmt.Errorf("get run %s for project %s: %w", summary.GetRunId(), projectName, err))
			}
			output.Runs = append(output.Runs, composeLogRunOutputFromDetail(detail.Msg.GetRun(), options))
		}
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	for _, summary := range runs {
		if err := followRunLogStream(cmd.Context(), cmd.OutOrStdout(), clients.run, projectID, summary, options); err != nil {
			return err
		}
	}
	return nil
}
