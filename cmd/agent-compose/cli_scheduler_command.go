package main

import (
	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

type composeSchedulerTriggerOptions struct {
	SandboxID     string
	Driver        string
	Prompt        string
	PayloadJSON   string
	KeepRunning   bool
	Remove        bool
	Jupyter       bool
	JupyterExpose bool
	Detach        bool
}

type composeSchedulerRunsOptions struct {
	AgentName string
	Trigger   string
	Status    string
	Limit     uint32
}

type composeSchedulerLogsOptions struct {
	AgentName string
	Trigger   string
	RunID     string
	Tail      int
}

func runComposeSchedulerTriggerCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerTriggerOptions, agentName, triggerRef string) error {
	return runComposeSchedulerTriggerV2Command(cmd, cli, options, agentName, triggerRef)
}

func runComposeSchedulerRunsCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerRunsOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	agentRef := options.AgentName
	if len(args) > 0 {
		if agentRef != "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler runs accepts either a scheduler argument or --agent, not both")}
		}
		agentRef = args[0]
	}
	runs, err := listComposeSchedulerRuns(cmd.Context(), clients, normalized, projectID, agentRef, options.Trigger, options.Status, options.Limit)
	if err != nil {
		return err
	}
	output := composeSchedulerRunsOutput{
		Project: composeUpProjectOutput{ID: displayOpaqueID(projectID), Name: normalized.Name},
		Runs:    runs,
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeSchedulerRunsText(cmd.OutOrStdout(), output)
}

func runComposeSchedulerLogsCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerLogsOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	runRef := strings.TrimSpace(options.RunID)
	if len(args) > 0 {
		if runRef != "" {
			return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler logs accepts either a run argument or --run, not both")}
		}
		runRef = args[0]
	}
	if options.Tail < -1 {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler logs --tail must be -1 or greater")}
	}
	if runRef != "" && (strings.TrimSpace(options.AgentName) != "" || strings.TrimSpace(options.Trigger) != "") {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler logs --agent and --trigger can only be used when selecting the latest run")}
	}
	var selected *composeSchedulerRunItem
	if runRef != "" {
		selected, err = getComposeSchedulerRun(cmd.Context(), clients, normalized, projectID, runRef)
		if err != nil {
			return err
		}
	} else {
		runs, listErr := listComposeSchedulerRuns(cmd.Context(), clients, normalized, projectID, options.AgentName, options.Trigger, "", 1)
		if listErr != nil {
			return listErr
		}
		if len(runs) > 0 {
			selected = &runs[0]
		}
	}
	if selected == nil {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("no scheduler runs found")}
	}
	events, err := listSchedulerRunEvents(cmd.Context(), clients, projectID, *selected)
	if err != nil {
		return commandExitErrorForConnect(fmt.Errorf("list scheduler run %s logs: %w", selected.RunID, err))
	}
	if options.Tail >= 0 && len(events) > options.Tail {
		events = events[len(events)-options.Tail:]
	}
	output := composeSchedulerLogsOutput{
		Project: composeUpProjectOutput{ID: displayOpaqueID(projectID), Name: normalized.Name},
		Run:     selected,
		Events:  events,
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeSchedulerLogsText(cmd.OutOrStdout(), output)
}

func runComposeSchedulerInspectCommand(cmd *cobra.Command, cli cliOptions, args []string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output := composeSchedulerInspectOutput{Project: composeUpProjectOutput{ID: displayOpaqueID(projectID), Name: normalized.Name}}
	if len(args) == 2 {
		trigger, err := resolveComposeSchedulerTrigger(cmd.Context(), clients, normalized, projectID, args[0], args[1])
		if err != nil {
			return err
		}
		setSchedulerTriggerInspectOutput(&output, trigger)
	} else {
		ref := strings.TrimSpace(args[0])
		if shouldResolveSchedulerRunRef(ref) {
			run, runErr := getComposeSchedulerRun(cmd.Context(), clients, normalized, projectID, ref)
			if runErr == nil {
				output.Resource = "run"
				output.AgentName = run.AgentName
				output.Run = run
			} else if !isSchedulerResourceNotFound(runErr) {
				return runErr
			}
		}
		if output.Resource == "" {
			scheduler, schedulerErr := resolveComposeScheduler(normalized, projectID, ref)
			if schedulerErr == nil {
				triggers, listErr := listComposeSchedulerTriggers(cmd.Context(), clients, normalized, projectID, scheduler.AgentName)
				if listErr != nil {
					return listErr
				}
				scheduler.TriggerCount = len(triggers)
				output.Resource = "scheduler"
				output.AgentName = scheduler.AgentName
				output.Scheduler = scheduler
			} else if !isSchedulerResourceNotFound(schedulerErr) {
				return schedulerErr
			}
		}
		if output.Resource == "" {
			triggers, listErr := listComposeSchedulerTriggers(cmd.Context(), clients, normalized, projectID, "")
			if listErr != nil {
				return listErr
			}
			trigger, triggerErr := resolveSchedulerTriggerFromItems(triggers, ref)
			if triggerErr != nil {
				return commandExitError{Code: exitCodeUsage, Err: triggerErr}
			}
			setSchedulerTriggerInspectOutput(&output, *trigger)
		}
	}
	if cli.JSON {
		data, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return err
		}
		return writeCommandOutput(cmd.OutOrStdout(), append(data, '\n'))
	}
	return writeSchedulerInspectText(cmd.OutOrStdout(), output)
}

type schedulerResourceNotFoundError struct {
	kind string
	ref  string
}

func (e schedulerResourceNotFoundError) Error() string {
	return fmt.Sprintf("scheduler %s %q not found", e.kind, e.ref)
}

func isSchedulerResourceNotFound(err error) bool {
	var target schedulerResourceNotFoundError
	return errors.As(err, &target)
}

func getComposeSchedulerRun(ctx context.Context, clients cliServiceClients, normalized *compose.NormalizedProjectSpec, projectID, runRef string) (*composeSchedulerRunItem, error) {
	runID, err := resolveSchedulerRunID(ctx, clients.resource, projectID, runRef)
	if err != nil {
		if !isSchedulerResourceNotFound(err) {
			return nil, err
		}
		return resolveSchedulerRuntimeRun(ctx, clients.project, normalized, projectID, runRef)
	}
	resp, err := clients.run.GetRun(ctx, connect.NewRequest(&agentcomposev2.GetRunRequest{ProjectId: projectID, RunId: runID}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeNotFound {
			return resolveSchedulerRuntimeRun(ctx, clients.project, normalized, projectID, runRef)
		}
		return nil, commandExitErrorForConnect(fmt.Errorf("get scheduler run %s: %w", runRef, err))
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary == nil || strings.TrimSpace(summary.GetSchedulerId()) == "" {
		return resolveSchedulerRuntimeRun(ctx, clients.project, normalized, projectID, runRef)
	}
	loaderID, idErr := domain.StableManagedLoaderID(projectID, summary.GetAgentName(), "")
	if idErr != nil {
		return nil, idErr
	}
	item := schedulerRunItem(summary.GetAgentName(), summary.GetSchedulerId(), loaderID, summary)
	item.ResultJSON = resp.Msg.GetRun().GetResultJson()
	item.ArtifactsDir = resp.Msg.GetRun().GetArtifactsDir()
	return &item, nil
}

func isLegacySchedulerRunID(runID string) bool {
	runID = strings.TrimSpace(runID)
	parsed, err := uuid.Parse(runID)
	return err == nil && parsed.String() == runID
}

func normalizeComposeSchedulerTriggerOptions(options composeSchedulerTriggerOptions) (composeSchedulerTriggerOptions, error) {
	return normalizeComposeSchedulerExecutionOptions("scheduler trigger", options)
}

func mergeSchedulerRuntimeRuns(current, legacy []composeSchedulerRunItem) []composeSchedulerRunItem {
	byID := make(map[string]int, len(current))
	for index := range current {
		byID[current[index].RunID] = index
	}
	for _, run := range legacy {
		index, ok := byID[run.RunID]
		if !ok {
			byID[run.RunID] = len(current)
			current = append(current, run)
			continue
		}
		current[index].SandboxIDs = appendUniqueStrings(current[index].SandboxIDs, run.SandboxIDs...)
	}
	return current
}

func (b *composeDisplayChangeBuilder) addTriggerChanges(action, id, agentName, message string, spec *compose.NormalizedProjectSpec) {
	triggerRefs := composeTriggerRefsForAgent(spec, agentName)
	if len(triggerRefs) == 0 {
		b.add(composeDisplayChangeOutput{
			Action:       action,
			ResourceType: "trigger",
			ID:           id,
			Name:         agentName,
			Owner:        agentName,
			Message:      message,
		})
		return
	}
	for _, triggerRef := range triggerRefs {
		triggerID := id
		if stableID, err := domain.StableManagedTriggerID(b.projectID, agentName, "", triggerRef.name, triggerRef.index); err == nil {
			triggerID = shortOpaqueID(stableID)
		}
		b.add(composeDisplayChangeOutput{
			Action:       action,
			ResourceType: "trigger",
			ID:           triggerID,
			Name:         triggerRef.name,
			Owner:        agentName,
			Message:      message,
		})
	}
}

type composeTriggerRef struct {
	name  string
	index int
}

func composeTriggerRefsForAgent(spec *compose.NormalizedProjectSpec, agentName string) []composeTriggerRef {
	if spec == nil {
		return nil
	}
	for _, agent := range spec.Agents {
		if agent.Name != agentName || agent.Scheduler == nil {
			continue
		}
		refs := make([]composeTriggerRef, 0, len(agent.Scheduler.Triggers))
		for index, trigger := range agent.Scheduler.Triggers {
			if strings.TrimSpace(trigger.Name) == "" {
				continue
			}
			refs = append(refs, composeTriggerRef{name: trigger.Name, index: index})
		}
		return refs
	}
	return nil
}
