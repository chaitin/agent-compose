package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

type composeSandboxPruneOptions struct {
	Status         string
	Agent          string
	Driver         string
	OlderThan      string
	IncludeOrphans bool
	Force          bool
}

func addSandboxPruneFlags(cmd *cobra.Command, options *composeSandboxPruneOptions) {
	cmd.Flags().StringVar(&options.Status, "status", "", "Filter sandboxes by status, comma-separated")
	cmd.Flags().StringVar(&options.Agent, "agent", "", "Filter sandboxes by agent name")
	cmd.Flags().StringVar(&options.Driver, "driver", "", "Filter sandboxes by driver: docker, boxlite, or microsandbox")
	cmd.Flags().StringVar(&options.OlderThan, "older-than", "", "Only match sandboxes older than a duration such as 7d or 24h")
	cmd.Flags().BoolVar(&options.IncludeOrphans, "include-orphans", false, "Include daemon-wide runtime residues without sandbox records")
	cmd.Flags().BoolVar(&options.Force, "force", false, "Actually remove matched sandboxes")
}

func runComposeSandboxPruneCommand(cmd *cobra.Command, cli cliOptions, options composeSandboxPruneOptions) error {
	statusFilter, err := sandboxPruneStatusFilter(options.Status)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	driverFilter, err := sandboxPruneDriverFilterValue(options.Driver)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	options.Driver = driverFilter
	var olderThanSeconds uint64
	if strings.TrimSpace(options.OlderThan) != "" {
		olderThanSeconds, err = parseOlderThanSeconds(options.OlderThan)
		if err != nil {
			return commandExitError{Code: exitCodeUsage, Err: err}
		}
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	runtimeProject, err := resolveComposeRuntimeProject(cmd.Context(), clients.project, cli, "sandbox prune", runtimeProjectIdentityOnly)
	if err != nil {
		return err
	}
	projectID := runtimeProject.id()
	statuses := make([]string, 0, len(statusFilter))
	for status := range statusFilter {
		statuses = append(statuses, strings.ToUpper(status))
	}
	sort.Strings(statuses)
	resp, err := clients.sandbox.PruneSandboxes(cmd.Context(), connect.NewRequest(&agentcomposev2.PruneSandboxesRequest{
		ProjectId: projectID, Status: statuses, AgentName: strings.TrimSpace(options.Agent), Driver: options.Driver,
		OlderThanSeconds: olderThanSeconds, IncludeOrphans: options.IncludeOrphans, Force: options.Force,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			if options.IncludeOrphans {
				return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("sandbox prune --include-orphans requires a daemon with PruneSandboxes support")}
			}
			return runLegacyComposeSandboxPrune(cmd, cli, options, clients, runtimeProject, statusFilter, olderThanSeconds)
		}
		return commandExitErrorForConnect(fmt.Errorf("prune sandboxes: %w", err))
	}
	output := composeSandboxPruneOutputFromResponse(resp.Msg)
	if err := writeSandboxPruneOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	if options.Force && len(output.Skipped) > 0 {
		return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("sandbox prune skipped %d sandbox(es)", len(output.Skipped))}
	}
	return nil
}

func runLegacyComposeSandboxPrune(cmd *cobra.Command, cli cliOptions, options composeSandboxPruneOptions, clients cliServiceClients, runtimeProject composeRuntimeProject, statusFilter map[string]bool, olderThanSeconds uint64) error {
	project, err := clients.project.GetProject(cmd.Context(), connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project: &agentcomposev2.ProjectRef{ProjectId: runtimeProject.id()},
	}))
	if err != nil {
		return commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", runtimeProject.name(), err), "sandbox prune", runtimeProject.name(), runtimeProject.composePath)
	}
	psOutput, err := composePSOutputFromProject(cmd.Context(), clients, project.Msg.GetProject(), composePSOptions{All: true})
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("build sandbox prune candidates for project %s: %w", runtimeProject.name(), err))
	}
	output := composeSandboxPruneDryRunOutput(psOutput.Sandboxes, statusFilter, options, olderThanSeconds)
	if options.Force {
		output.DryRun = false
		for _, sandbox := range output.Matched {
			removeID := firstNonEmptyString(sandbox.RawID, sandbox.SandboxID)
			if err := removeSandbox(cmd.Context(), clients.sandbox, removeID, false); err != nil {
				output.Skipped = append(output.Skipped, composeSandboxPruneSkipped{SandboxID: sandbox.SandboxID, Agent: sandbox.Agent, Status: sandbox.Status, Driver: sandbox.Driver, UpdatedAt: firstNonEmptyString(sandbox.UpdatedAt, sandbox.CreatedAt), Reason: fmt.Sprintf("remove failed: %s", err)})
				continue
			}
			output.Removed = append(output.Removed, sandbox.SandboxID)
		}
	}
	if err := writeSandboxPruneOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	if options.Force && len(output.Skipped) > 0 {
		return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("sandbox prune skipped %d sandbox(es)", len(output.Skipped))}
	}
	return nil
}

func composeSandboxPruneOutputFromResponse(resp *agentcomposev2.PruneSandboxesResponse) composeSandboxPruneOutput {
	output := composeSandboxPruneOutput{DryRun: resp.GetDryRun(), Removed: displayOpaqueIDs(resp.GetRemoved()), Warnings: append([]string(nil), resp.GetWarnings()...)}
	for _, item := range resp.GetMatched() {
		output.Matched = append(output.Matched, composePSSandboxOutput{
			SandboxID: displayOpaqueID(firstNonEmptyString(item.GetSandboxId(), item.GetRuntimeId())),
			RawID:     item.GetSandboxId(), SandboxShortID: shortOpaqueID(firstNonEmptyString(item.GetSandboxId(), item.GetRuntimeId())),
			Agent: item.GetAgentName(), Status: strings.ToLower(item.GetStatus()), Driver: item.GetDriver(),
			UpdatedAt: formatProtoTimestamp(item.GetUpdatedAt()), Kind: sandboxPruneCandidateKindText(item.GetKind()), RuntimeID: item.GetRuntimeId(),
		})
	}
	for _, item := range resp.GetSkipped() {
		output.Skipped = append(output.Skipped, composeSandboxPruneSkipped{
			SandboxID: displayOpaqueID(firstNonEmptyString(item.GetSandboxId(), item.GetRuntimeId())), Agent: item.GetAgentName(),
			Status: strings.ToLower(item.GetStatus()), Driver: item.GetDriver(), UpdatedAt: formatProtoTimestamp(item.GetUpdatedAt()),
			Kind: sandboxPruneCandidateKindText(item.GetKind()), RuntimeID: item.GetRuntimeId(), Reason: strings.Join(item.GetBlockedReasons(), "; "),
		})
	}
	return output
}

func sandboxPruneCandidateKindText(kind agentcomposev2.SandboxPruneCandidateKind) string {
	if kind == agentcomposev2.SandboxPruneCandidateKind_SANDBOX_PRUNE_CANDIDATE_KIND_RUNTIME_RESIDUE {
		return "runtime-residue"
	}
	return "sandbox-record"
}

func composeSandboxPruneDryRunOutput(sandboxes []composePSSandboxOutput, statusFilter map[string]bool, options composeSandboxPruneOptions, olderThanSeconds uint64) composeSandboxPruneOutput {
	output := composeSandboxPruneOutput{
		DryRun:  true,
		Matched: []composePSSandboxOutput{},
		Removed: []string{},
		Skipped: []composeSandboxPruneSkipped{},
	}
	agentFilter := strings.ToLower(strings.TrimSpace(options.Agent))
	driverFilter := strings.ToLower(strings.TrimSpace(options.Driver))
	var cutoff time.Time
	if olderThanSeconds > 0 {
		cutoff = time.Now().UTC().Add(-time.Duration(olderThanSeconds) * time.Second)
	}
	for _, sandbox := range sandboxes {
		status := strings.ToLower(strings.TrimSpace(sandbox.Status))
		if !statusFilter[status] {
			continue
		}
		if agentFilter != "" && strings.ToLower(strings.TrimSpace(sandbox.Agent)) != agentFilter {
			continue
		}
		if driverFilter != "" && strings.ToLower(strings.TrimSpace(sandbox.Driver)) != driverFilter {
			continue
		}
		if !cutoff.IsZero() {
			timestamp, _, err := sandboxPruneTimestamp(sandbox)
			if err != nil {
				output.Warnings = append(output.Warnings, fmt.Sprintf("sandbox %s skipped: %s", sandbox.SandboxID, err))
				continue
			}
			if timestamp.After(cutoff) {
				continue
			}
		}
		output.Matched = append(output.Matched, sandbox)
	}
	return output
}

func sandboxPruneStatusFilter(value string) (map[string]bool, error) {
	result := map[string]bool{}
	for _, item := range strings.Split(value, ",") {
		status := strings.ToLower(strings.TrimSpace(item))
		if status == "" {
			continue
		}
		if status == "running" || status == "pending" {
			return nil, fmt.Errorf("sandbox prune cannot target %s sandboxes; use `agent-compose sandbox rm --force <sandbox>` for running sandboxes", status)
		}
		result[status] = true
	}
	if len(result) == 0 {
		result["stopped"] = true
		result["failed"] = true
	}
	return result, nil
}

func sandboxPruneDriverFilterValue(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "":
		return "", nil
	case "docker", "boxlite", "microsandbox":
		return strings.ToLower(strings.TrimSpace(value)), nil
	default:
		return "", fmt.Errorf("invalid --driver %q: expected docker, boxlite, or microsandbox", value)
	}
}

func sandboxPruneTimestamp(sandbox composePSSandboxOutput) (time.Time, string, error) {
	source := "updated_at"
	value := strings.TrimSpace(sandbox.UpdatedAt)
	if value == "" {
		source = "created_at"
		value = strings.TrimSpace(sandbox.CreatedAt)
	}
	if value == "" {
		return time.Time{}, source, fmt.Errorf("missing updated_at and created_at")
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, source, fmt.Errorf("invalid %s %q", source, value)
	}
	return parsed.UTC(), source, nil
}

type composeSandboxPruneOutput struct {
	DryRun   bool                         `json:"dry_run"`
	Matched  []composePSSandboxOutput     `json:"matched"`
	Removed  []string                     `json:"removed"`
	Skipped  []composeSandboxPruneSkipped `json:"skipped"`
	Warnings []string                     `json:"warnings,omitempty"`
}

type composeSandboxPruneSkipped struct {
	Kind      string `json:"kind,omitempty"`
	RuntimeID string `json:"runtime_id,omitempty"`
	SandboxID string `json:"sandbox_id"`
	Agent     string `json:"agent,omitempty"`
	Status    string `json:"status,omitempty"`
	Driver    string `json:"driver,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
	Reason    string `json:"reason"`
}

func writeSandboxPruneOutput(out io.Writer, asJSON bool, output composeSandboxPruneOutput) error {
	if asJSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	if output.DryRun {
		if _, err := fmt.Fprintf(out, "Dry-run: %d matched, %d skipped, %d would be removed.\n", len(output.Matched), len(output.Skipped), len(output.Matched)); err != nil {
			return err
		}
		if len(output.Matched) > 0 {
			if _, err := fmt.Fprintln(out, "Use --force to remove matched sandboxes."); err != nil {
				return err
			}
		}
	} else {
		if _, err := fmt.Fprintf(out, "Removed %d sandbox(es); %d matched, %d skipped.\n", len(output.Removed), len(output.Matched), len(output.Skipped)); err != nil {
			return err
		}
	}
	if len(output.Removed) > 0 {
		if err := writeStringListSection(out, "Removed", output.Removed); err != nil {
			return err
		}
	}
	if len(output.Matched) > 0 {
		if _, err := fmt.Fprintln(out, "Matched:"); err != nil {
			return err
		}
		reason := "matched"
		if output.DryRun {
			reason = "would remove"
		}
		if err := writeSandboxPruneMatchedTable(out, output.Matched, reason); err != nil {
			return err
		}
	}
	if len(output.Skipped) > 0 {
		if _, err := fmt.Fprintln(out, "Skipped:"); err != nil {
			return err
		}
		if err := writeSandboxPruneSkippedTable(out, output.Skipped); err != nil {
			return err
		}
	}
	return writeStringListSection(out, "Warnings", output.Warnings)
}

func writeSandboxPruneMatchedTable(out io.Writer, sandboxes []composePSSandboxOutput, reason string) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SANDBOX\tAGENT\tSTATUS\tDRIVER\tUPDATED\tREASON"); err != nil {
		return err
	}
	for _, sandbox := range sandboxes {
		updated := firstNonEmptyString(sandbox.UpdatedAt, sandbox.CreatedAt)
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(sandbox.SandboxShortID, shortOpaqueID(sandbox.SandboxID), "-"),
			firstNonEmptyString(sandbox.Agent, "-"),
			firstNonEmptyString(sandbox.Status, "-"),
			firstNonEmptyString(sandbox.Driver, "-"),
			firstNonEmptyString(updated, "-"),
			reason,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeSandboxPruneSkippedTable(out io.Writer, skipped []composeSandboxPruneSkipped) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "SANDBOX\tAGENT\tSTATUS\tDRIVER\tUPDATED\tREASON"); err != nil {
		return err
	}
	for _, item := range skipped {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			firstNonEmptyString(item.SandboxID, "-"),
			firstNonEmptyString(item.Agent, "-"),
			firstNonEmptyString(item.Status, "-"),
			firstNonEmptyString(item.Driver, "-"),
			firstNonEmptyString(item.UpdatedAt, "-"),
			firstNonEmptyString(item.Reason, "-"),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}
