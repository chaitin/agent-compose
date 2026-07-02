package agentcompose

import (
	"slices"
	"time"

	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func projectResponse(project ProjectRecord, spec *agentcomposev2.ProjectSpec, agents []ProjectAgentRecord, schedulers []ProjectSchedulerRecord) *agentcomposev2.Project {
	return &agentcomposev2.Project{
		Summary:    projectSummaryResponse(project, agents, schedulers),
		Spec:       spec,
		Agents:     projectAgentResponses(agents),
		Schedulers: projectSchedulerResponses(schedulers),
	}
}

func projectSummaryResponse(project ProjectRecord, agents []ProjectAgentRecord, schedulers []ProjectSchedulerRecord) *agentcomposev2.ProjectSummary {
	return &agentcomposev2.ProjectSummary{
		ProjectId:       project.ID,
		Name:            project.Name,
		SourcePath:      project.SourcePath,
		CurrentRevision: uint64(project.CurrentRevision),
		SpecHash:        project.SpecHash,
		AgentCount:      uint32(len(agents)),
		SchedulerCount:  uint32(len(schedulers)),
		CreatedAt:       formatProjectTime(project.CreatedAt),
		UpdatedAt:       formatProjectTime(project.UpdatedAt),
		RemovedAt:       formatProjectTime(project.RemovedAt),
	}
}

func projectRevisionResponse(revision ProjectRevisionRecord, spec *agentcomposev2.ProjectSpec) *agentcomposev2.ProjectRevision {
	return &agentcomposev2.ProjectRevision{
		ProjectId: revision.ProjectID,
		Revision:  uint64(revision.Revision),
		SpecHash:  revision.SpecHash,
		Spec:      spec,
		CreatedAt: formatProjectTime(revision.CreatedAt),
	}
}

func projectAgentResponses(agents []ProjectAgentRecord) []*agentcomposev2.ProjectAgent {
	items := make([]*agentcomposev2.ProjectAgent, 0, len(agents))
	for _, agent := range agents {
		items = append(items, &agentcomposev2.ProjectAgent{
			ProjectId:        agent.ProjectID,
			AgentName:        agent.AgentName,
			ManagedAgentId:   agent.ManagedAgentID,
			Provider:         agent.Provider,
			Model:            agent.Model,
			Image:            agent.Image,
			Driver:           agent.Driver,
			SchedulerEnabled: agent.SchedulerEnabled,
		})
	}
	return items
}

func projectSchedulerResponses(schedulers []ProjectSchedulerRecord) []*agentcomposev2.ProjectScheduler {
	items := make([]*agentcomposev2.ProjectScheduler, 0, len(schedulers))
	for _, scheduler := range schedulers {
		items = append(items, &agentcomposev2.ProjectScheduler{
			ProjectId:       scheduler.ProjectID,
			AgentName:       scheduler.AgentName,
			SchedulerId:     scheduler.SchedulerID,
			ManagedLoaderId: scheduler.ManagedLoaderID,
			Enabled:         scheduler.Enabled,
			TriggerCount:    uint32(scheduler.TriggerCount),
		})
	}
	return items
}

// ProjectSpecResponse converts a normalized compose spec into the v2 ProjectSpec API shape.
func ProjectSpecResponse(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	if spec == nil {
		return nil
	}
	return &agentcomposev2.ProjectSpec{
		Name:      spec.Name,
		Variables: envVarResponses(spec.Variables),
		Workspace: workspaceResponse(spec.Workspace),
		Agents:    agentSpecResponses(spec.Agents),
		Network:   networkResponse(spec.Network),
	}
}

func agentSpecResponses(agents []compose.NormalizedAgentSpec) []*agentcomposev2.AgentSpec {
	items := make([]*agentcomposev2.AgentSpec, 0, len(agents))
	for _, agent := range agents {
		items = append(items, &agentcomposev2.AgentSpec{
			Name:         agent.Name,
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Driver:       driverResponse(agent.Driver),
			Env:          envVarResponses(agent.Env),
			CapsetIds:    normalizeCapsetIDs(agent.CapsetIDs),
			Workspace:    workspaceResponse(agent.Workspace),
			Scheduler:    schedulerResponse(agent.Scheduler),
		})
	}
	return items
}

func envVarResponses(values map[string]compose.EnvVarSpec) []*agentcomposev2.EnvVarSpec {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]*agentcomposev2.EnvVarSpec, 0, len(values))
	for _, name := range names {
		value := values[name]
		items = append(items, &agentcomposev2.EnvVarSpec{Name: name, Value: value.Value, Secret: value.Secret})
	}
	return items
}

func workspaceResponse(workspace *compose.WorkspaceSpec) *agentcomposev2.WorkspaceSpec {
	if workspace == nil {
		return nil
	}
	return &agentcomposev2.WorkspaceSpec{
		Provider: workspace.Provider,
		Url:      workspace.URL,
		Branch:   workspace.Branch,
		Path:     workspace.Path,
	}
}

func networkResponse(network *compose.NetworkSpec) *agentcomposev2.NetworkSpec {
	if network == nil {
		return nil
	}
	return &agentcomposev2.NetworkSpec{Mode: network.Mode}
}

func driverResponse(driver *compose.NormalizedDriverSpec) *agentcomposev2.DriverSpec {
	if driver == nil {
		return nil
	}
	result := &agentcomposev2.DriverSpec{Name: driver.Name}
	switch driver.Name {
	case compose.DriverBoxlite:
		result.Boxlite = &agentcomposev2.BoxliteDriverSpec{}
		if driver.Boxlite != nil {
			result.Boxlite.Kernel = driver.Boxlite.Kernel
			result.Boxlite.Rootfs = driver.Boxlite.Rootfs
		}
	case compose.DriverDocker:
		result.Docker = &agentcomposev2.DockerDriverSpec{}
		if driver.Docker != nil {
			result.Docker.Host = driver.Docker.Host
		}
	case compose.DriverMicrosandbox:
		result.Microsandbox = &agentcomposev2.MicrosandboxDriverSpec{}
		if driver.Microsandbox != nil {
			result.Microsandbox.Profile = driver.Microsandbox.Profile
		}
	}
	return result
}

func schedulerResponse(scheduler *compose.NormalizedSchedulerSpec) *agentcomposev2.SchedulerSpec {
	if scheduler == nil {
		return nil
	}
	triggers := make([]*agentcomposev2.TriggerSpec, 0, len(scheduler.Triggers))
	for _, trigger := range scheduler.Triggers {
		triggers = append(triggers, triggerResponse(trigger))
	}
	return &agentcomposev2.SchedulerSpec{
		Enabled:  scheduler.Enabled,
		Triggers: triggers,
		Script:   scheduler.Script,
	}
}

func triggerResponse(trigger compose.NormalizedTriggerSpec) *agentcomposev2.TriggerSpec {
	result := &agentcomposev2.TriggerSpec{
		Name:   trigger.Name,
		Kind:   trigger.Kind,
		Prompt: trigger.Prompt,
	}
	switch trigger.Kind {
	case "cron":
		result.Cron = trigger.Cron
	case "interval":
		result.Interval = trigger.Interval
	case "timeout":
		result.Timeout = trigger.Timeout
	case "event":
		result.Event = &agentcomposev2.EventTriggerSpec{}
		if trigger.Event != nil {
			result.Event.Topic = trigger.Event.Topic
		}
	}
	return result
}

func formatProjectTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
