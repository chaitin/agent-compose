package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	agentcomposev2connect "github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"github.com/spf13/cobra"
)

const eventLogPrefix = "evt_"

// validateEventLogTarget rejects values that are not full event-bus event
// ids. Prefix matching is intentionally unsupported for --event.
func validateEventLogTarget(eventID string) error {
	if !strings.HasPrefix(eventID, eventLogPrefix) || strings.TrimSpace(strings.TrimPrefix(eventID, eventLogPrefix)) == "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("logs --event requires a full event id starting with %s; prefix matching is not supported", eventLogPrefix)}
	}
	return nil
}

// resolveEventLogSchedulerRunIDs fetches GET /api/events/{id}/trace and
// returns the delivery scheduler run ids in delivery order, deduplicated.
func resolveEventLogSchedulerRunIDs(ctx context.Context, httpClient *http.Client, baseURL, eventID string) ([]string, error) {
	eventID = strings.TrimSpace(eventID)
	endpoint := strings.TrimSuffix(baseURL, "/") + "/api/events/" + url.PathEscape(eventID) + "/trace"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("build event trace request for %s: %w", eventID, err)}
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, commandExitError{Code: exitCodeUnavailable, Err: fmt.Errorf("get event %s trace: %w", eventID, err)}
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(response.Body)
	if readErr != nil {
		return nil, fmt.Errorf("read event %s trace response: %w", eventID, readErr)
	}
	switch {
	case response.StatusCode == http.StatusNotFound:
		return nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("event %s not found", eventID)}
	case response.StatusCode >= 300:
		return nil, commandExitError{Code: exitCodeUnavailable, Err: fmt.Errorf("get event %s trace: unexpected status %s", eventID, response.Status)}
	}
	var trace struct {
		Runs []struct {
			Delivery struct {
				RunID string `json:"run_id"`
			} `json:"delivery"`
		} `json:"runs"`
	}
	if err := json.Unmarshal(body, &trace); err != nil {
		return nil, commandExitError{Code: exitCodeUnavailable, Err: fmt.Errorf("parse event %s trace response: %w", eventID, err)}
	}
	runIDs := make([]string, 0, len(trace.Runs))
	seen := make(map[string]struct{}, len(trace.Runs))
	for _, run := range trace.Runs {
		runID := strings.TrimSpace(run.Delivery.RunID)
		if runID == "" {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		runIDs = append(runIDs, runID)
	}
	return runIDs, nil
}

// resolveEventLogRuns expands scheduler run ids into the project's agent runs
// that recorded them, preserving scheduler run order.
func resolveEventLogRuns(ctx context.Context, client agentcomposev2connect.RunServiceClient, projectID string, schedulerRunIDs []string) ([]*agentcomposev2.RunSummary, error) {
	runs := make([]*agentcomposev2.RunSummary, 0, len(schedulerRunIDs))
	seen := make(map[string]struct{}, len(schedulerRunIDs))
	for _, schedulerRunID := range schedulerRunIDs {
		resp, err := client.ListRuns(ctx, connect.NewRequest(&agentcomposev2.ListRunsRequest{
			ProjectId:      strings.TrimSpace(projectID),
			SchedulerRunId: schedulerRunID,
			Limit:          200,
		}))
		if err != nil {
			return nil, err
		}
		for _, run := range resp.Msg.GetRuns() {
			runID := run.GetRunId()
			if _, ok := seen[runID]; ok {
				continue
			}
			seen[runID] = struct{}{}
			runs = append(runs, run)
		}
	}
	return runs, nil
}

// listEventLogRuns resolves the --event target to the agent runs to replay.
// An event without recorded deliveries, or whose deliveries produced no agent
// runs, returns an empty slice.
func listEventLogRuns(ctx context.Context, httpClient *http.Client, baseURL string, client agentcomposev2connect.RunServiceClient, projectID, eventID string) ([]*agentcomposev2.RunSummary, error) {
	schedulerRunIDs, err := resolveEventLogSchedulerRunIDs(ctx, httpClient, baseURL, eventID)
	if err != nil {
		return nil, err
	}
	if len(schedulerRunIDs) == 0 {
		return nil, nil
	}
	return resolveEventLogRuns(ctx, client, projectID, schedulerRunIDs)
}

// eventLogReplayRuns narrows resolved runs to the requested agent, if any.
func eventLogReplayRuns(runs []*agentcomposev2.RunSummary, agentName string) []*agentcomposev2.RunSummary {
	agentName = strings.TrimSpace(agentName)
	if agentName == "" {
		return runs
	}
	narrowed := make([]*agentcomposev2.RunSummary, 0, len(runs))
	for _, run := range runs {
		if run.GetAgentName() == agentName {
			narrowed = append(narrowed, run)
		}
	}
	return narrowed
}

// runComposeLogsForEvent replays logs for the agent runs produced by an event.
// Event traces are served by the daemon's REST endpoints rather than a Connect
// procedure, so the daemon HTTP client is rebuilt alongside the RPC clients.
func runComposeLogsForEvent(cmd *cobra.Command, cli cliOptions, clients cliServiceClients, projectID, projectName string, options composeLogsOptions) error {
	clientConfig, err := resolveCLIServiceClientConfig(cli)
	if err != nil {
		return err
	}
	httpClient := newDaemonHTTPClient(clientConfig)
	runs, err := listEventLogRuns(cmd.Context(), httpClient, clientConfig.BaseURL, clients.run, projectID, options.EventID)
	if err != nil {
		var exitErr commandExitError
		if errors.As(err, &exitErr) {
			return err
		}
		return commandExitErrorForConnect(fmt.Errorf("list logs for event %s for project %s: %w", options.EventID, projectName, err))
	}
	runs = eventLogReplayRuns(runs, options.AgentName)
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
