package main

import (
	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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

type composeSchedulerInvokeOptions struct {
	PayloadJSON string
}

type composeSchedulerLogsOptions struct {
	SchedulerRef string
	AgentName    string
	Trigger      string
	RunID        string
	Tail         int
}

type composeSchedulerInspectOptions struct {
	SchedulerRef string
}

func runComposeSchedulerTriggerCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerTriggerOptions, agentName, triggerRef string) error {
	return runComposeSchedulerTriggerV2Command(cmd, cli, options, agentName, triggerRef)
}

func runComposeSchedulerRunsCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerRunsOptions, args []string) error {
	if err := writeDeprecatedSchedulerAgentFlagWarning(cmd, "use the scheduler positional argument instead"); err != nil {
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
	if err := writeDeprecatedSchedulerAgentFlagWarning(cmd, "use --scheduler instead"); err != nil {
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
	schedulerRef := strings.TrimSpace(options.SchedulerRef)
	if schedulerRef != "" && strings.TrimSpace(options.AgentName) != "" {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler logs accepts either --scheduler or deprecated --agent, not both")}
	}
	if schedulerRef == "" {
		schedulerRef = strings.TrimSpace(options.AgentName)
	}
	if runRef != "" && (schedulerRef != "" || strings.TrimSpace(options.Trigger) != "") {
		return commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("scheduler logs run filters cannot be combined with --scheduler or --trigger")}
	}
	var selected *composeSchedulerRunItem
	agentName := ""
	triggerID := ""
	if runRef != "" {
		selected, err = getComposeSchedulerRun(cmd.Context(), clients, normalized, projectID, runRef)
		if err != nil {
			return err
		}
		agentName = selected.AgentName
		triggerID = selected.TriggerID
		runRef = selected.RunID
	} else if schedulerRef != "" {
		scheduler, resolveErr := resolveComposeScheduler(normalized, projectID, schedulerRef)
		if resolveErr != nil {
			return resolveErr
		}
		agentName = scheduler.AgentName
	}
	if triggerRef := strings.TrimSpace(options.Trigger); triggerRef != "" {
		resolvedTriggerID, resolveErr := resolveSchedulerTriggerIDForQuery(cmd.Context(), clients, normalized, projectID, agentName, triggerRef)
		if resolveErr != nil {
			if errors.Is(resolveErr, domain.ErrAmbiguous) && agentName == "" {
				resolveErr = fmt.Errorf("%w; use --scheduler <scheduler-ref> to disambiguate", resolveErr)
			}
			if isSchedulerResourceNotFound(resolveErr) || errors.Is(resolveErr, domain.ErrAmbiguous) {
				return commandExitError{Code: exitCodeUsage, Err: resolveErr}
			}
			return resolveErr
		}
		triggerID = resolvedTriggerID
	}
	events, err := listProjectSchedulerLogEvents(cmd.Context(), clients.project, projectID, agentName, triggerID, runRef, options.Tail)
	if err != nil {
		return err
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

func writeDeprecatedSchedulerAgentFlagWarning(cmd *cobra.Command, guidance string) error {
	if !cmd.Flags().Changed("agent") {
		return nil
	}
	_, err := fmt.Fprintf(cmd.ErrOrStderr(), "Warning: --agent is deprecated; %s\n", guidance)
	return err
}

func runComposeSchedulerInspectCommand(cmd *cobra.Command, cli cliOptions, options composeSchedulerInspectOptions, rawRef string) error {
	_, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return err
	}
	clients, err := newCLIServiceClients(cli)
	if err != nil {
		return err
	}
	output := composeSchedulerInspectOutput{Project: composeUpProjectOutput{ID: displayOpaqueID(projectID), Name: normalized.Name}}
	ref := strings.TrimSpace(rawRef)
	if schedulerRef := strings.TrimSpace(options.SchedulerRef); schedulerRef != "" {
		trigger, err := resolveComposeSchedulerTrigger(cmd.Context(), clients, normalized, projectID, schedulerRef, ref)
		if err != nil {
			return err
		}
		setSchedulerTriggerInspectOutput(&output, trigger)
	} else {
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
				if errors.Is(triggerErr, domain.ErrAmbiguous) {
					triggerErr = fmt.Errorf("%w; use --scheduler <scheduler-ref> to disambiguate", triggerErr)
				}
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
	return resolveSchedulerRuntimeRun(ctx, clients.project, normalized, projectID, runRef)
}

func normalizeComposeSchedulerTriggerOptions(options composeSchedulerTriggerOptions) (composeSchedulerTriggerOptions, error) {
	return normalizeComposeSchedulerExecutionOptions("scheduler trigger", options)
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
