package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"connectrpc.com/connect"

	"agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type composeSchedulerPruneOptions struct {
	SchedulerRef string
	TriggerRef   string
	Status       string
	OlderThan    string
	Force        bool
}

type composeSchedulerPruneStats struct {
	Runs              uint64 `json:"runs"`
	LoaderEvents      uint64 `json:"loader_events"`
	EventDeliveries   uint64 `json:"event_deliveries"`
	EventSandboxLinks uint64 `json:"event_sandbox_links"`
	ArtifactDirs      uint64 `json:"artifact_dirs"`
	ArtifactBytes     uint64 `json:"artifact_bytes"`
}

type composeSchedulerPruneResidue struct {
	LoaderID string `json:"loader_id"`
	RunID    string `json:"run_id"`
	Path     string `json:"path"`
	Error    string `json:"error"`
}

type composeSchedulerPruneOutput struct {
	RecordScope string                         `json:"record_scope"`
	DryRun      bool                           `json:"dry_run"`
	Project     composeUpProjectOutput         `json:"project"`
	Scheduler   string                         `json:"scheduler,omitempty"`
	TriggerID   string                         `json:"trigger_id,omitempty"`
	Statuses    []string                       `json:"statuses,omitempty"`
	OlderThan   string                         `json:"older_than,omitempty"`
	Matched     composeSchedulerPruneStats     `json:"matched"`
	Removed     composeSchedulerPruneStats     `json:"removed"`
	SkippedRuns uint64                         `json:"skipped_runs"`
	Residues    []composeSchedulerPruneResidue `json:"residues"`
	Warnings    []string                       `json:"warnings"`
}

func runComposeSchedulerPruneCommand(cmd interface {
	Context() context.Context
	OutOrStdout() io.Writer
}, cli cliOptions, options composeSchedulerPruneOptions) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	agentName := ""
	if strings.TrimSpace(options.SchedulerRef) != "" {
		scheduler, resolveErr := resolveComposeScheduler(normalized, projectID, options.SchedulerRef)
		if resolveErr != nil {
			return resolveErr
		}
		agentName = scheduler.AgentName
	}
	triggerID := ""
	if strings.TrimSpace(options.TriggerRef) != "" {
		triggerID, err = resolveSchedulerTriggerIDForQuery(cmd.Context(), clients, normalized, projectID, agentName, options.TriggerRef)
		if err != nil {
			if isSchedulerResourceNotFound(err) || errors.Is(err, model.ErrAmbiguous) {
				return commandExitError{Code: exitCodeUsage, Err: err}
			}
			return err
		}
	}
	statuses, statusNames, err := parseSchedulerRunPruneStatuses(options.Status)
	if err != nil {
		return err
	}
	olderThanSeconds, err := parseOlderThanSeconds(options.OlderThan)
	if err != nil {
		return commandExitError{Code: exitCodeUsage, Err: err}
	}
	response, err := clients.project.PruneSchedulerRuns(cmd.Context(), connect.NewRequest(&agentcomposev2.PruneSchedulerRunsRequest{
		Project:          &agentcomposev2.ProjectRef{ProjectId: projectID},
		AgentName:        agentName,
		TriggerId:        triggerID,
		Status:           statuses,
		OlderThanSeconds: olderThanSeconds,
		Force:            options.Force,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnimplemented {
			return commandExitError{Code: exitCodeUnsupported, Err: fmt.Errorf("daemon does not support scheduler run pruning; upgrade the daemon")}
		}
		return commandExitErrorForConnect(fmt.Errorf("prune scheduler runs: %w", err))
	}
	output := composeSchedulerPruneOutputFromResponse(response.Msg)
	output.Project = composeUpProjectOutput{ID: displayOpaqueID(projectID), Name: normalized.Name}
	output.Scheduler = agentName
	output.TriggerID = triggerID
	output.Statuses = statusNames
	output.OlderThan = strings.TrimSpace(options.OlderThan)
	if err := writeComposeSchedulerPruneOutput(cmd.OutOrStdout(), cli.JSON, output); err != nil {
		return err
	}
	if options.Force && (output.SkippedRuns > 0 || len(output.Residues) > 0) {
		return commandExitError{Code: exitCodeGeneral, Err: fmt.Errorf("scheduler prune completed with %d skipped run(s) and %d artifact residue(s)", output.SkippedRuns, len(output.Residues))}
	}
	return nil
}

func parseSchedulerRunPruneStatuses(value string) ([]agentcomposev2.SchedulerRunStatus, []string, error) {
	values := strings.Split(value, ",")
	statuses := make([]agentcomposev2.SchedulerRunStatus, 0, len(values))
	names := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if _, exists := seen[name]; exists {
			continue
		}
		var status agentcomposev2.SchedulerRunStatus
		switch name {
		case model.LoaderRunStatusSucceeded:
			status = agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SUCCEEDED
		case model.LoaderRunStatusFailed:
			status = agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_FAILED
		case model.LoaderRunStatusCanceled:
			status = agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_CANCELED
		case model.LoaderRunStatusSkipped:
			status = agentcomposev2.SchedulerRunStatus_SCHEDULER_RUN_STATUS_SKIPPED
		default:
			return nil, nil, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler prune --status must contain only succeeded, failed, canceled, or skipped")}
		}
		seen[name] = struct{}{}
		statuses = append(statuses, status)
		names = append(names, name)
	}
	sort.Strings(names)
	return statuses, names, nil
}

func composeSchedulerPruneOutputFromResponse(response *agentcomposev2.PruneSchedulerRunsResponse) composeSchedulerPruneOutput {
	output := composeSchedulerPruneOutput{
		RecordScope: "trigger_runs",
		DryRun:      response.GetDryRun(),
		Matched:     composeSchedulerPruneStatsFromProto(response.GetMatched()),
		Removed:     composeSchedulerPruneStatsFromProto(response.GetRemoved()),
		SkippedRuns: response.GetSkippedRuns(),
		Residues:    make([]composeSchedulerPruneResidue, 0, len(response.GetResidues())),
		Warnings:    append([]string(nil), response.GetWarnings()...),
	}
	for _, residue := range response.GetResidues() {
		output.Residues = append(output.Residues, composeSchedulerPruneResidue{
			LoaderID: displayOpaqueID(residue.GetLoaderId()),
			RunID:    displayOpaqueID(residue.GetRunId()),
			Path:     residue.GetPath(),
			Error:    residue.GetError(),
		})
	}
	return output
}

func composeSchedulerPruneStatsFromProto(stats *agentcomposev2.SchedulerRunPruneStats) composeSchedulerPruneStats {
	if stats == nil {
		return composeSchedulerPruneStats{}
	}
	return composeSchedulerPruneStats{
		Runs:              stats.GetRuns(),
		LoaderEvents:      stats.GetLoaderEvents(),
		EventDeliveries:   stats.GetEventDeliveries(),
		EventSandboxLinks: stats.GetEventSandboxLinks(),
		ArtifactDirs:      stats.GetArtifactDirs(),
		ArtifactBytes:     stats.GetArtifactBytes(),
	}
}

func writeComposeSchedulerPruneOutput(out io.Writer, asJSON bool, output composeSchedulerPruneOutput) error {
	if asJSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(out, append(data, '\n'))
	}
	removable := output.Matched.Runs - min(output.Matched.Runs, output.SkippedRuns)
	if output.DryRun {
		if _, err := fmt.Fprintf(out, "Dry-run: %d scheduler trigger run(s) matched; %d would be removed, %d skipped.\n", output.Matched.Runs, removable, output.SkippedRuns); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(out, "Removed %d scheduler trigger run(s); %d matched, %d skipped.\n", output.Removed.Runs, output.Matched.Runs, output.SkippedRuns); err != nil {
		return err
	}
	related := output.Matched
	label := "Matched related"
	if !output.DryRun {
		related = output.Removed
		label = "Removed related"
	}
	if _, err := fmt.Fprintf(out, "%s: %d loader event(s), %d event delivery row(s), %d event sandbox link(s), %d artifact dir(s), %d artifact byte(s).\n",
		label, related.LoaderEvents, related.EventDeliveries, related.EventSandboxLinks, related.ArtifactDirs, related.ArtifactBytes); err != nil {
		return err
	}
	if output.DryRun && removable > 0 {
		if _, err := fmt.Fprintln(out, "Use --force to remove matched scheduler trigger-run history."); err != nil {
			return err
		}
	}
	for _, residue := range output.Residues {
		if _, err := fmt.Fprintf(out, "Residue: run=%s path=%s error=%s\n", residue.RunID, residue.Path, residue.Error); err != nil {
			return err
		}
	}
	return writeStringListSection(out, "Warnings", output.Warnings)
}
