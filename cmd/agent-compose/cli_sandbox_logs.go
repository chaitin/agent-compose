package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

type composeSandboxLogsOutput struct {
	SandboxID string                                `json:"sandbox_id"`
	Output    string                                `json:"output"`
	Events    []*agentcomposev2.SandboxHistoryEvent `json:"events,omitempty"`
}

func resolveProjectSandboxIDRef(ctx context.Context, client agentcomposev2connect.SandboxServiceClient, projectID, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox id is required")}
	}
	sandboxes, err := listAllSandboxes(ctx, client)
	if err != nil {
		return "", commandExitErrorForConnect(fmt.Errorf("resolve sandbox %s: %w", ref, err))
	}
	matches := make([]string, 0, 1)
	for _, sandbox := range sandboxes {
		if !protoSandboxBelongsToProject(sandbox, projectID) || !resourceIDMatchesRef(sandbox.GetSandboxId(), shortOpaqueID(sandbox.GetSandboxId()), ref) {
			continue
		}
		matches = append(matches, sandbox.GetSandboxId())
	}
	if len(matches) == 0 {
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox %q not found in current project", ref)}
	}
	if len(matches) > 1 {
		shortIDs := make([]string, 0, len(matches))
		for _, id := range matches {
			shortIDs = append(shortIDs, shortOpaqueID(id))
		}
		sort.Strings(shortIDs)
		return "", commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("sandbox ref %q is ambiguous in current project; matches: %s", ref, strings.Join(shortIDs, ", "))}
	}
	return matches[0], nil
}

func protoSandboxBelongsToProject(sandbox *agentcomposev2.Sandbox, projectID string) bool {
	projectID = strings.TrimSpace(projectID)
	tags := sessionTagsMap(sandbox.GetTags())
	if canonical := strings.TrimSpace(tags["project"]); canonical != "" {
		return canonical == projectID
	}
	return strings.TrimSpace(tags["project_id"]) == projectID
}

func writeSandboxHistoryLogs(cmd *cobra.Command, cli cliOptions, client agentcomposev2connect.SandboxServiceClient, projectID string, options composeLogsOptions) error {
	if options.Follow {
		return commandExitError{Code: exitCodeUnsupported, Err: fmt.Errorf("logs --follow is not yet supported for scheduler command sandbox history")}
	}
	sandboxID, err := resolveProjectSandboxIDRef(cmd.Context(), client, projectID, options.SandboxID)
	if err != nil {
		return err
	}
	history := &agentcomposev2.ListSandboxHistoryResponse{}
	total, err := collectAllPages("sandbox history", func(offset uint32) (int, uint32, error) {
		resp, err := client.ListSandboxHistory(cmd.Context(), connect.NewRequest(&agentcomposev2.ListSandboxHistoryRequest{SandboxId: sandboxID, Offset: offset, Limit: 500}))
		if err != nil {
			return 0, 0, commandExitErrorForConnect(fmt.Errorf("list sandbox %s history for project %s: %w", sandboxID, projectID, err))
		}
		history.LegacyHistory = resp.Msg.GetLegacyHistory()
		history.Cells = append(history.Cells, resp.Msg.GetCells()...)
		history.Events = append(history.Events, resp.Msg.GetEvents()...)
		return len(resp.Msg.GetCells()) + len(resp.Msg.GetEvents()), resp.Msg.GetTotal(), nil
	})
	if err != nil {
		return err
	}
	history.Total = total
	output := tailLogOutput(sandboxHistoryOutput(history.GetCells()), options.TailLines)
	if cli.JSON {
		data, err := json.MarshalIndent(composeSandboxLogsOutput{SandboxID: sandboxID, Output: output, Events: history.GetEvents()}, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	if output != "" {
		if _, err := fmt.Fprint(cmd.OutOrStdout(), output); err != nil {
			return err
		}
		if !strings.HasSuffix(output, "\n") {
			if _, err := fmt.Fprintln(cmd.OutOrStdout()); err != nil {
				return err
			}
		}
	}
	return nil
}

func sandboxHistoryOutput(cells []*agentcomposev2.SandboxHistoryCell) string {
	var output strings.Builder
	for _, cell := range cells {
		text := firstNonEmptyString(cell.GetOutput(), cell.GetStdout(), cell.GetStderr())
		if text == "" {
			continue
		}
		output.WriteString(text)
		if !strings.HasSuffix(text, "\n") {
			output.WriteByte('\n')
		}
	}
	return output.String()
}
