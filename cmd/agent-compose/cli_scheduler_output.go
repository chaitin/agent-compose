package main

import (
	"agent-compose/pkg/agentcompose/api"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

func setSchedulerTriggerInspectOutput(output *composeSchedulerInspectOutput, trigger composeSchedulerTriggerItem) {
	output.Resource = "trigger"
	output.Source = trigger.Source
	output.AgentName = trigger.AgentName
	output.Trigger = &trigger
	if trigger.Source == "declarative" && trigger.declarative != nil {
		output.Definition = api.TriggerYAMLShape(trigger.declarative)
	} else if trigger.registered != nil {
		output.Registered = trigger.registered
	}
}

func schedulerDisplayEventType(value string) string {
	return strings.Replace(strings.TrimSpace(value), "loader.", "scheduler.", 1)
}

func writeSchedulerListText(out io.Writer, output composeSchedulerListOutput, verbose bool) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	header := "SCHEDULER\tAGENT\tTRIGGER\tKIND\tSOURCE\tENABLED"
	if verbose {
		header = "SCHEDULER\tAGENT\tTRIGGER\tTRIGGER ID\tKIND\tSOURCE\tENABLED"
	}
	if _, err := fmt.Fprintln(tw, header); err != nil {
		return err
	}
	for _, trigger := range output.Triggers {
		name := firstNonEmptyString(trigger.Name, trigger.TriggerID)
		schedulerID := firstNonEmptyString(trigger.SchedulerShortID, shortOpaqueID(trigger.SchedulerID), "-")
		if verbose {
			schedulerID = firstNonEmptyString(trigger.SchedulerID, "-")
			if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%t\n",
				schedulerID, trigger.AgentName, name, firstNonEmptyString(trigger.TriggerID, "-"),
				trigger.Kind, trigger.Source, trigger.TriggerEnabled,
			); err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%t\n",
			schedulerID,
			trigger.AgentName,
			name,
			trigger.Kind,
			trigger.Source,
			trigger.TriggerEnabled,
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeSchedulerInspectText(out io.Writer, output composeSchedulerInspectOutput) error {
	if output.Resource == "scheduler" {
		data, err := yaml.Marshal(output.Scheduler)
		if err != nil {
			return err
		}
		return writeCommandOutput(out, data)
	}
	if output.Resource == "run" {
		data, err := yaml.Marshal(output.Run)
		if err != nil {
			return err
		}
		return writeCommandOutput(out, data)
	}
	var target map[string]any
	if output.Source == "declarative" {
		target = output.Definition
	} else {
		target = output.Registered
	}
	data, err := yaml.Marshal(target)
	if err != nil {
		return err
	}
	return writeCommandOutput(out, data)
}

func writeSchedulerRunsText(out io.Writer, output composeSchedulerRunsOutput) error {
	tw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "RUN ID\tAGENT\tTRIGGER\tSTATUS\tSANDBOXES\tSTARTED\tDURATION"); err != nil {
		return err
	}
	for _, run := range output.Runs {
		sandboxes := "-"
		if len(run.SandboxIDs) > 0 {
			shortIDs := make([]string, 0, len(run.SandboxIDs))
			for _, sandboxID := range run.SandboxIDs {
				shortIDs = append(shortIDs, shortOpaqueID(sandboxID))
			}
			sandboxes = strings.Join(shortIDs, ",")
		}
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			run.RunShortID,
			run.AgentName,
			firstNonEmptyString(run.TriggerID, "-"),
			run.Status,
			sandboxes,
			firstNonEmptyString(run.StartedAt, "-"),
			formatDurationMs(run.DurationMs),
		); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func writeSchedulerLogsText(out io.Writer, output composeSchedulerLogsOutput) error {
	for _, event := range output.Events {
		line := fmt.Sprintf("%s %s %s", event.CreatedAt, strings.ToUpper(firstNonEmptyString(event.Level, "info")), event.Type)
		line += " scheduler=" + firstNonEmptyString(event.AgentName, shortOpaqueID(event.SchedulerID), "-")
		line += " trigger=" + firstNonEmptyString(shortOpaqueID(event.TriggerID), "-")
		line += " run=" + firstNonEmptyString(shortOpaqueID(event.RunID), "-")
		if event.Message != "" {
			line += " " + event.Message
		}
		if event.SandboxID != "" {
			line += " sandbox=" + shortOpaqueID(event.SandboxID)
		}
		if _, err := fmt.Fprintln(out, strings.TrimSpace(line)); err != nil {
			return err
		}
	}
	return nil
}

type composeProjectSchedulerOutput struct {
	AgentName    string `json:"agent_name"`
	SchedulerID  string `json:"scheduler_id"`
	Enabled      bool   `json:"enabled"`
	TriggerCount uint32 `json:"trigger_count"`
}

type composeSchedulerListOutput struct {
	Project  composeUpProjectOutput        `json:"project"`
	Triggers []composeSchedulerTriggerItem `json:"triggers"`
}

type composeSchedulerInspectOutput struct {
	Project    composeUpProjectOutput       `json:"project"`
	Resource   string                       `json:"resource"`
	Source     string                       `json:"source"`
	AgentName  string                       `json:"agent_name"`
	Scheduler  *composeSchedulerItem        `json:"scheduler,omitempty"`
	Trigger    *composeSchedulerTriggerItem `json:"trigger,omitempty"`
	Run        *composeSchedulerRunItem     `json:"run,omitempty"`
	Definition map[string]any               `json:"definition,omitempty"`
	Registered map[string]any               `json:"registered,omitempty"`
}

type composeSchedulerRunsOutput struct {
	Project composeUpProjectOutput    `json:"project"`
	Runs    []composeSchedulerRunItem `json:"runs"`
}

type composeSchedulerLogsOutput struct {
	Project composeUpProjectOutput     `json:"project"`
	Run     *composeSchedulerRunItem   `json:"run,omitempty"`
	Events  []composeSchedulerLogEvent `json:"events"`
}

func composeProjectSchedulerOutputFromProto(scheduler *agentcomposev2.ProjectScheduler) composeProjectSchedulerOutput {
	return composeProjectSchedulerOutput{
		AgentName:    scheduler.GetAgentName(),
		SchedulerID:  displayOpaqueID(scheduler.GetSchedulerId()),
		Enabled:      scheduler.GetEnabled(),
		TriggerCount: scheduler.GetTriggerCount(),
	}
}
