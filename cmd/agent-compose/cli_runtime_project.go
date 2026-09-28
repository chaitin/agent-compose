package main

import (
	"context"
	"fmt"
	"github.com/chaitin/agent-compose/internal/projects"
	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"strings"

	"connectrpc.com/connect"
)

type composeRuntimeProject struct {
	composePath   string
	project       *agentcomposev2.Project
	spec          *compose.NormalizedProjectSpec
	summaryLoaded bool
}

type composeRuntimeProjectSelection struct {
	composePath   string
	requestedName string
	ref           *agentcomposev2.ProjectRef
	localSpec     *compose.NormalizedProjectSpec
}

func (p composeRuntimeProject) id() string {
	return strings.TrimSpace(p.project.GetSummary().GetProjectId())
}

func (p composeRuntimeProject) name() string {
	return strings.TrimSpace(p.project.GetSummary().GetName())
}

// resolveComposeRuntimeProject resolves an explicit project name as an
// already-applied project filter. Without one, the compose file identifies the
// project.
func resolveComposeRuntimeProject(ctx context.Context, client agentcomposev2connect.ProjectServiceClient, cli cliOptions, command string) (composeRuntimeProject, error) {
	selection, err := resolveComposeRuntimeProjectSelectionForCLI(cli)
	if err != nil {
		return composeRuntimeProject{}, err
	}
	response, err := client.GetProject(ctx, connect.NewRequest(&agentcomposev2.GetProjectRequest{
		Project:     selection.ref,
		IncludeSpec: true,
	}))
	if err != nil {
		if connect.CodeOf(err) == connect.CodeUnimplemented && selection.localSpec != nil {
			projectID, idErr := projects.StableProjectID(selection.requestedName, "")
			if idErr != nil {
				return composeRuntimeProject{}, idErr
			}
			return composeRuntimeProject{
				composePath: selection.composePath,
				project: &agentcomposev2.Project{Summary: &agentcomposev2.ProjectSummary{
					ProjectId:  projectID,
					Name:       selection.requestedName,
					SourcePath: selection.composePath,
				}},
				spec: selection.localSpec,
			}, nil
		}
		return composeRuntimeProject{}, commandExitErrorForComposeProject(fmt.Errorf("get project %s: %w", selection.requestedName, err), command, selection.requestedName, selection.composePath)
	}
	project := response.Msg.GetProject()
	if project == nil || strings.TrimSpace(project.GetSummary().GetProjectId()) == "" {
		return composeRuntimeProject{}, fmt.Errorf("get project %s: response did not include a project", selection.requestedName)
	}
	return composeRuntimeProject{
		composePath:   selection.composePath,
		project:       project,
		spec:          normalizedRuntimeProjectSpec(project),
		summaryLoaded: true,
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
	if projectName != "" {
		return composeRuntimeProjectSelection{requestedName: projectName, ref: &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_Name{Name: projectName}}}, nil
	}
	composePath, normalized, _, err := resolveComposeProject(cli)
	if err != nil {
		return composeRuntimeProjectSelection{}, err
	}
	return composeRuntimeProjectSelection{
		composePath:   composePath,
		requestedName: normalized.Name,
		ref:           &agentcomposev2.ProjectRef{Selector: &agentcomposev2.ProjectRef_Name{Name: normalized.Name}},
		localSpec:     normalized,
	}, nil
}

// normalizedRuntimeProjectSpec projects the persisted revision into the small
// compose model already used by CLI-side resource reference resolution. The
// daemon remains the source of truth; no local config is read here.
func normalizedRuntimeProjectSpec(project *agentcomposev2.Project) *compose.NormalizedProjectSpec {
	result := &compose.NormalizedProjectSpec{Name: project.GetSummary().GetName()}
	if spec := project.GetSpec(); spec != nil && (len(spec.GetAgents()) > 0 || len(project.GetAgents()) == 0) {
		result.Name = firstNonEmptyString(result.Name, spec.GetName())
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
			SandboxPolicy:     schedulerSandboxPolicyText(scheduler.GetSandboxPolicy()),
			ConcurrencyPolicy: schedulerConcurrencyPolicyText(scheduler.GetConcurrencyPolicy()),
			DisplayName:       scheduler.GetDisplayName(),
			Description:       scheduler.GetDescription(),
			Script:            scheduler.GetScript(),
			Triggers:          make([]compose.NormalizedTriggerSpec, 0, len(scheduler.GetTriggers())),
		}
		for _, trigger := range scheduler.GetTriggers() {
			item := compose.NormalizedTriggerSpec{
				Name:          trigger.GetName(),
				Kind:          triggerKindText(trigger.GetKind()),
				Cron:          trigger.GetCron(),
				Interval:      trigger.GetInterval(),
				Timeout:       trigger.GetTimeout(),
				Prompt:        trigger.GetPrompt(),
				IncludeEvent:  trigger.IncludeEvent,
				SandboxPolicy: schedulerSandboxPolicyText(trigger.GetSandboxPolicy()),
			}
			if event := trigger.GetEvent(); event != nil {
				item.Event = &compose.EventTriggerSpec{Topic: event.GetTopic()}
			}
			result.Scheduler.Triggers = append(result.Scheduler.Triggers, item)
		}
	}
	return result
}
