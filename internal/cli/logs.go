package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func followOrPrintProjectLogs(cmd *cobra.Command, cli options, client agentcomposev2connect.RunServiceClient, projectID, projectName string, options composeLogsOptions) error {
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
