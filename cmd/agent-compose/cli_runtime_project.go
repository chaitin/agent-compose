package main

import (
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"strings"

	"connectrpc.com/connect"
)

type composeRuntimeProject struct {
	composePath string
	project     *agentcomposev2.Project
	spec        *compose.NormalizedProjectSpec
}

type composeRuntimeProjectSelection struct {
	composePath   string
	requestedName string
	ref           *agentcomposev2.ProjectRef
	localSpec     *compose.NormalizedProjectSpec
}

type composeRuntimeProjectLoadMode uint8

const (
	runtimeProjectIdentityOnly composeRuntimeProjectLoadMode = iota
	runtimeProjectWithState
)

func (p composeRuntimeProject) id() string {
	return strings.TrimSpace(p.project.GetSummary().GetProjectId())
}

func (p composeRuntimeProject) name() string {
	return strings.TrimSpace(p.project.GetSummary().GetName())
}

// resolveComposeRuntimeProject follows Docker Compose's project-or-name
// behavior: an explicit project name selects an already-applied project unless
// an explicit compose file was also supplied. Without either flag, the default
// compose file continues to identify the project.
func resolveComposeRuntimeProject(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, cli cliOptions, command string, loadMode composeRuntimeProjectLoadMode) (composeRuntimeProject, error) {
	selection, err := resolveComposeRuntimeProjectSelectionForCLI(cli)
	if err != nil {
		return composeRuntimeProject{}, err
	}
	if selection.localSpec != nil && loadMode == runtimeProjectIdentityOnly {
		return composeRuntimeProject{
			composePath: selection.composePath,
			project: &agentcomposev2.Project{Summary: &agentcomposev2.ProjectSummary{
				ProjectId:  selection.ref.GetProjectId(),
				Name:       selection.requestedName,
				SourcePath: selection.composePath,
			}},
			spec: selection.localSpec,
		}, nil
	}
	response, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project:     selection.ref,
		IncludeSpec: true,
	}))
	if err != nil {
		return composeRuntimeProject{}, commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", selection.requestedName, err), command, selection.requestedName, selection.composePath)
	}
	project := response.Msg.GetProject()
	if project == nil || strings.TrimSpace(project.GetSummary().GetProjectId()) == "" {
		return composeRuntimeProject{}, fmt.Errorf("get project %s: response did not include a project", selection.requestedName)
	}
	return composeRuntimeProject{
		composePath: selection.composePath,
		project:     project,
		spec:        normalizedRuntimeProjectSpec(project),
	}, nil
}

func resolveComposeRuntimeProjectRef(cli cliOptions) (string, string, *agentcomposev2.ProjectRef, error) {
	selection, err := resolveComposeRuntimeProjectSelectionForCLI(cli)
	if err != nil {
		return "", "", nil, err
	}
	return selection.composePath, selection.requestedName, selection.ref, nil
}

func resolveComposeRuntimeProjectSelectionForCLI(cli cliOptions) (composeRuntimeProjectSelection, error) {
	projectName := strings.TrimSpace(cli.ProjectName)
	if strings.TrimSpace(cli.ComposeFile) == "" && projectName != "" {
		return composeRuntimeProjectSelection{requestedName: projectName, ref: &agentcomposev2.ProjectRef{Name: projectName}}, nil
	}
	composePath, normalized, projectID, err := resolveComposeProject(cli)
	if err != nil {
		return composeRuntimeProjectSelection{}, err
	}
	return composeRuntimeProjectSelection{
		composePath:   composePath,
		requestedName: normalized.Name,
		ref:           &agentcomposev2.ProjectRef{ProjectId: projectID},
		localSpec:     normalized,
	}, nil
}

// normalizedRuntimeProjectSpec projects the persisted revision into the small
// compose model already used by CLI-side resource reference resolution. The
// daemon remains the source of truth; no local config is read here.
func normalizedRuntimeProjectSpec(project *agentcomposev2.Project) *compose.NormalizedProjectSpec {
	result := &compose.NormalizedProjectSpec{Name: project.GetSummary().GetName()}
	if spec := project.GetSpec(); spec != nil {
		result.Name = firstNonEmptyString(spec.GetName(), result.Name)
		result.Agents = make([]compose.NormalizedAgentSpec, 0, len(spec.GetAgents()))
		for _, agent := range spec.GetAgents() {
			result.Agents = append(result.Agents, normalizedRuntimeAgentSpec(agent))
		}
		return result
	}

	schedulers := make(map[string]*agentcomposev2.ProjectScheduler, len(project.GetSchedulers()))
	for _, scheduler := range project.GetSchedulers() {
		schedulers[scheduler.GetAgentName()] = scheduler
	}
	result.Agents = make([]compose.NormalizedAgentSpec, 0, len(project.GetAgents()))
	for _, agent := range project.GetAgents() {
		item := compose.NormalizedAgentSpec{
			Name:        agent.GetAgentName(),
			Enabled:     agent.GetEnabled(),
			DisplayName: agent.GetDisplayName(),
			Description: agent.GetDescription(),
			Provider:    agent.GetProvider(),
			Model:       agent.GetModel(),
			Image:       agent.GetImage(),
		}
		if scheduler := schedulers[item.Name]; scheduler != nil {
			item.Scheduler = &compose.NormalizedSchedulerSpec{
				Enabled:     scheduler.GetEnabled(),
				DisplayName: scheduler.GetDisplayName(),
				Description: scheduler.GetDescription(),
			}
		}
		result.Agents = append(result.Agents, item)
	}
	return result
}

func normalizedRuntimeAgentSpec(agent *agentcomposev2.AgentSpec) compose.NormalizedAgentSpec {
	result := compose.NormalizedAgentSpec{
		Name:         agent.GetName(),
		Enabled:      agent.GetEnabled(),
		DisplayName:  agent.GetDisplayName(),
		Description:  agent.GetDescription(),
		Provider:     agent.GetProvider(),
		Model:        agent.GetModel(),
		SystemPrompt: agent.GetSystemPrompt(),
		Image:        agent.GetImage(),
	}
	if scheduler := agent.GetScheduler(); scheduler != nil {
		result.Scheduler = &compose.NormalizedSchedulerSpec{
			Enabled:           scheduler.GetEnabled(),
			SandboxPolicy:     scheduler.GetSandboxPolicy(),
			ConcurrencyPolicy: scheduler.GetConcurrencyPolicy(),
			DisplayName:       scheduler.GetDisplayName(),
			Description:       scheduler.GetDescription(),
			Script:            scheduler.GetScript(),
			Triggers:          make([]compose.NormalizedTriggerSpec, 0, len(scheduler.GetTriggers())),
		}
		for _, trigger := range scheduler.GetTriggers() {
			item := compose.NormalizedTriggerSpec{
				Name:          trigger.GetName(),
				Kind:          trigger.GetKind(),
				Cron:          trigger.GetCron(),
				Interval:      trigger.GetInterval(),
				Timeout:       trigger.GetTimeout(),
				Prompt:        trigger.GetPrompt(),
				SandboxPolicy: trigger.GetSandboxPolicy(),
			}
			if event := trigger.GetEvent(); event != nil {
				item.Event = &compose.EventTriggerSpec{Topic: event.GetTopic()}
			}
			result.Scheduler.Triggers = append(result.Scheduler.Triggers, item)
		}
	}
	return result
}
