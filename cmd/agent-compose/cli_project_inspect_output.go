package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/chaitin/agent-compose/pkg/agentcompose/api"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type composeProjectOutput struct {
	DeclaredConfig json.RawMessage                 `json:"declared_config"`
	Runtime        json.RawMessage                 `json:"runtime"`
	OmittedScripts []composeOmittedScript          `json:"omitted_scripts,omitempty"`
	Project        composeUpProjectOutput          `json:"project"`
	Agents         []composeProjectAgentOutput     `json:"agents"`
	Schedulers     []composeProjectSchedulerOutput `json:"schedulers"`
}

type composeAgentInspectOutput struct {
	DeclaredConfig   json.RawMessage                 `json:"declared_config"`
	Runtime          json.RawMessage                 `json:"runtime"`
	OmittedScripts   []composeOmittedScript          `json:"omitted_scripts,omitempty"`
	Project          composeUpProjectOutput          `json:"project"`
	Agent            composeProjectAgentOutput       `json:"agent"`
	Schedulers       []composeProjectSchedulerOutput `json:"schedulers"`
	LatestRun        *composeRunOutput               `json:"latest_run,omitempty"`
	RunningSandboxes []composeSandboxOutput          `json:"running_sandboxes,omitempty"`
}

func writeComposeProjectInspectOutput(cmd *cobra.Command, project *agentcomposev2.Project) error {
	output := composeProjectOutputFromProject(project)
	spec, err := marshalInspectProto(api.RedactProjectSpecSecrets(project.GetSpec()))
	if err != nil {
		return err
	}
	output.DeclaredConfig = spec
	// The revision is declaration data; the other Project fields describe the
	// daemon's current view. Clone before correcting the legacy agent flag.
	runtime := &agentcomposev2.Project{Summary: project.GetSummary(), Schedulers: project.GetSchedulers()}
	for _, agent := range project.GetAgents() {
		runtime.Agents = append(runtime.Agents, inspectRuntimeAgent(agent, project.GetSchedulers()))
	}
	output.Runtime, err = marshalInspectProto(runtime)
	if err != nil {
		return err
	}
	return writeComposeInspectOutput(cmd, output)
}

func inspectRuntimeAgent(agent *agentcomposev2.ProjectAgent, schedulers []*agentcomposev2.ProjectScheduler) *agentcomposev2.ProjectAgent {
	if agent == nil {
		return nil
	}
	result := proto.Clone(agent).(*agentcomposev2.ProjectAgent)
	// ProjectAgent.scheduler_enabled is persisted at apply time. The scheduler
	// record reflects subsequent enable/disable operations.
	for _, scheduler := range schedulers {
		if scheduler.GetAgentName() == agent.GetAgentName() {
			result.SchedulerEnabled = scheduler.GetEnabled()
			break
		}
	}
	return result
}

func marshalInspectProto(message proto.Message) (json.RawMessage, error) {
	if message == nil || !message.ProtoReflect().IsValid() {
		return json.RawMessage("null"), nil
	}
	data, err := (protojson.MarshalOptions{UseProtoNames: true, EmitDefaultValues: true}).Marshal(message)
	if err != nil {
		return nil, fmt.Errorf("encode inspect details: %w", err)
	}
	return data, nil
}

func composeAgentInspectOutputFor(ctx context.Context, clients cliServiceClients, project *agentcomposev2.Project, agentName string) (composeAgentInspectOutput, error) {
	var found *agentcomposev2.ProjectAgent
	for _, agent := range project.GetAgents() {
		if agent.GetAgentName() == agentName {
			found = agent
			break
		}
	}
	if found == nil {
		return composeAgentInspectOutput{}, commandExitError{Code: exitCodeUsage, Err: fmt.Errorf("agent %s not found in project %s", agentName, project.GetSummary().GetName())}
	}
	output := composeAgentInspectOutput{
		Project: composeProjectSummaryOutput(project.GetSummary()),
		Agent:   composeProjectAgentOutputFromProto(found),
	}
	for _, scheduler := range project.GetSchedulers() {
		if scheduler.GetAgentName() == agentName {
			output.Schedulers = append(output.Schedulers, composeProjectSchedulerOutputFromProto(scheduler))
		}
	}
	if latest, err := latestRunOutput(ctx, clients.run, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list latest run for agent %s: %w", agentName, err))
	} else {
		output.LatestRun = latest
	}
	if session, err := firstRunningSandboxOutput(ctx, clients, project.GetSummary().GetProjectId(), agentName); err != nil {
		return composeAgentInspectOutput{}, commandExitErrorForConnect(fmt.Errorf("list running sandbox for agent %s: %w", agentName, err))
	} else if session != nil {
		output.RunningSandboxes = append(output.RunningSandboxes, *session)
	}
	spec := api.RedactProjectSpecSecrets(project.GetSpec())
	for _, agent := range spec.GetAgents() {
		if agent.GetName() == agentName {
			data, err := marshalInspectProto(agent)
			if err != nil {
				return composeAgentInspectOutput{}, err
			}
			output.DeclaredConfig = data
			break
		}
	}
	runtime, err := marshalInspectProto(inspectRuntimeAgent(found, project.GetSchedulers()))
	if err != nil {
		return composeAgentInspectOutput{}, err
	}
	output.Runtime = runtime
	return output, nil
}
