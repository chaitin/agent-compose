package api

import (
	"slices"
	"strings"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func ProjectSpecToProto(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	result, err := ProjectSpecToProtoChecked(spec)
	if err != nil {
		return nil
	}
	return result
}

func ProjectSpecToProtoChecked(spec *compose.NormalizedProjectSpec) (*agentcomposev2.ProjectSpec, error) {
	if spec == nil {
		return nil, nil
	}
	if err := spec.ValidateResolvedScriptURLs(); err != nil {
		return nil, err
	}
	return &agentcomposev2.ProjectSpec{
		Name:           spec.Name,
		Variables:      EnvVarSpecsToProto(spec.Variables),
		Workspaces:     NamedWorkspaceSpecsToProto(spec.Workspaces),
		Agents:         AgentSpecsToProto(spec.Agents),
		Volumes:        ProjectVolumeSpecsToProto(spec.Volumes),
		McpServers:     MCPServerSpecsToProto(spec.MCPServers),
		OctobusServers: OctoBusServerSpecsToProto(spec.OctoBusServers),
	}, nil
}

func NamedWorkspaceSpecsToProto(workspaces map[string]compose.WorkspaceSpec) []*agentcomposev2.NamedWorkspaceSpec {
	keys := make([]string, 0, len(workspaces))
	for key := range workspaces {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	items := make([]*agentcomposev2.NamedWorkspaceSpec, 0, len(keys))
	for _, key := range keys {
		workspace := workspaces[key]
		workspace.Name = ""
		items = append(items, &agentcomposev2.NamedWorkspaceSpec{
			Name:      key,
			Workspace: WorkspaceSpecToProto(&workspace),
		})
	}
	return items
}

func AgentSpecsToProto(agents []compose.NormalizedAgentSpec) []*agentcomposev2.AgentSpec {
	items := make([]*agentcomposev2.AgentSpec, 0, len(agents))
	for _, agent := range agents {
		items = append(items, &agentcomposev2.AgentSpec{
			Name:             agent.Name,
			DisplayName:      agent.DisplayName,
			Description:      agent.Description,
			InputSchemaJson:  jsonSchemaString(agent.InputSchema),
			OutputSchemaJson: jsonSchemaString(agent.OutputSchema),
			Provider:         agent.Provider,
			Model:            agent.Model,
			SystemPrompt:     agent.SystemPrompt,
			Image:            agent.Image,
			Build:            BuildSpecToProto(agent.Build),
			Driver:           DriverSpecToProto(agent.Driver),
			Env:              EnvVarSpecsToProto(agent.Env),
			CapsetIds:        capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
			Skills:           SkillSpecsToProto(agent.Skills),
			Workspace:        WorkspaceSpecToProto(agent.Workspace),
			Sandbox:          SandboxSpecToProto(agent.Sandbox),
			Scheduler:        SchedulerSpecToProto(agent.Scheduler),
			Jupyter:          JupyterSpecToProto(agent.Jupyter),
			Volumes:          VolumeMountSpecsToProto(agent.Volumes),
			McpServers:       MCPServerSpecsToProto(agent.MCPServers),
			Enabled:          &agent.Enabled,
		})
	}
	return items
}

func jsonSchemaString(schema *compose.JSONSchema) string {
	if schema == nil {
		return ""
	}
	return string(*schema)
}

func SandboxSpecToProto(sandbox *compose.NormalizedSandboxSpec) *agentcomposev2.SandboxSpec {
	if sandbox == nil {
		return nil
	}
	return &agentcomposev2.SandboxSpec{StoppedRuntimePolicy: sandbox.StoppedRuntimePolicy}
}

func MCPServerSpecsToProto(values map[string]compose.NormalizedMCPServerSpec) []*agentcomposev2.MCPServerSpec {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	items := make([]*agentcomposev2.MCPServerSpec, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		items = append(items, &agentcomposev2.MCPServerSpec{
			Name:      key,
			Type:      value.Type,
			Transport: value.Transport,
			Command:   value.Command,
			Args:      append([]string(nil), value.Args...),
			Env:       EnvVarSpecsToProto(value.Env),
			Url:       value.URL,
			Headers:   EnvVarSpecsToProto(value.Headers),
		})
	}
	return items
}

func SkillSpecsToProto(skills []compose.NormalizedSkillSpec) []*agentcomposev2.SkillSpec {
	items := make([]*agentcomposev2.SkillSpec, 0, len(skills))
	for _, skill := range skills {
		items = append(items, &agentcomposev2.SkillSpec{
			Name:     skill.Name,
			Provider: skill.Provider,
			Url:      skill.URL,
			Path:     skill.Path,
			Ref:      skill.Ref,
			Format:   skill.Format,
			Username: skill.Username,
			Password: skill.Password,
			Token:    skill.Token,
		})
	}
	return items
}

func ProjectVolumeSpecsToProto(volumes map[string]compose.NormalizedVolumeSpec) []*agentcomposev2.ProjectVolumeSpec {
	keys := make([]string, 0, len(volumes))
	for key := range volumes {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	items := make([]*agentcomposev2.ProjectVolumeSpec, 0, len(keys))
	for _, key := range keys {
		volume := volumes[key]
		items = append(items, &agentcomposev2.ProjectVolumeSpec{
			Key:      key,
			Name:     volume.Name,
			Driver:   volume.Driver,
			External: volume.External,
			Labels:   cloneProjectStringMap(volume.Labels),
			Options:  cloneProjectStringMap(volume.Options),
		})
	}
	return items
}

func VolumeMountSpecsToProto(volumes []compose.NormalizedVolumeMountSpec) []*agentcomposev2.VolumeMountSpec {
	items := make([]*agentcomposev2.VolumeMountSpec, 0, len(volumes))
	for _, volume := range volumes {
		items = append(items, &agentcomposev2.VolumeMountSpec{
			Type:     volumeMountTypeToProto(volume.Type),
			Source:   volume.Source,
			Target:   volume.Target,
			ReadOnly: volume.ReadOnly,
		})
	}
	return items
}

func BuildSpecToProto(build *compose.NormalizedBuildSpec) *agentcomposev2.BuildSpec {
	if build == nil {
		return nil
	}
	return &agentcomposev2.BuildSpec{
		Context:    build.Context,
		Dockerfile: build.Dockerfile,
		Target:     build.Target,
		Args:       cloneProjectStringMap(build.Args),
		Platforms:  append([]string(nil), build.Platforms...),
		Tags:       append([]string(nil), build.Tags...),
		NoCache:    build.NoCache,
		Pull:       build.Pull,
	}
}

func JupyterSpecToProto(jupyter *compose.JupyterSpec) *agentcomposev2.JupyterSpec {
	if jupyter == nil {
		return nil
	}
	return &agentcomposev2.JupyterSpec{
		Enabled:   jupyter.Enabled,
		GuestPort: uint32(jupyter.GuestPort),
	}
}

func EnvVarSpecsToProto(values map[string]compose.EnvVarSpec) []*agentcomposev2.EnvVarSpec {
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

func WorkspaceSpecToProto(workspace *compose.WorkspaceSpec) *agentcomposev2.WorkspaceSpec {
	if workspace == nil {
		return nil
	}
	return &agentcomposev2.WorkspaceSpec{
		Name:     workspace.Name,
		Provider: workspace.Provider,
		Url:      workspace.URL,
		Ref:      workspace.Ref,
		Path:     workspace.Path,
		Format:   workspace.Format,
		Target:   workspace.Target,
		Username: workspace.Username,
		Password: workspace.Password,
		Token:    workspace.Token,
	}
}

func DriverSpecToProto(driver *compose.NormalizedDriverSpec) *agentcomposev2.DriverSpec {
	if driver == nil {
		return nil
	}
	result := &agentcomposev2.DriverSpec{Name: driver.Name}
	switch driver.Name {
	case compose.DriverBoxlite:
		config := &agentcomposev2.BoxliteDriverSpec{}
		if driver.Boxlite != nil {
			config.Kernel = driver.Boxlite.Kernel
			config.Rootfs = driver.Boxlite.Rootfs
		}
		result.Config = &agentcomposev2.DriverSpec_Boxlite{Boxlite: config}
	case compose.DriverDocker:
		config := &agentcomposev2.DockerDriverSpec{}
		if driver.Docker != nil {
			config.Host = driver.Docker.Host
		}
		result.Config = &agentcomposev2.DriverSpec_Docker{Docker: config}
	case compose.DriverMicrosandbox:
		config := &agentcomposev2.MicrosandboxDriverSpec{}
		if driver.Microsandbox != nil {
			config.Profile = driver.Microsandbox.Profile
		}
		result.Config = &agentcomposev2.DriverSpec_Microsandbox{Microsandbox: config}
	}
	return result
}

func SchedulerSpecToProto(scheduler *compose.NormalizedSchedulerSpec) *agentcomposev2.SchedulerSpec {
	if scheduler == nil {
		return nil
	}
	if scheduler.HasScript() && strings.TrimSpace(scheduler.Script) == "" {
		return nil
	}
	triggers := make([]*agentcomposev2.TriggerSpec, 0, len(scheduler.Triggers))
	for _, trigger := range scheduler.Triggers {
		triggers = append(triggers, TriggerSpecToProto(trigger))
	}
	return &agentcomposev2.SchedulerSpec{
		Enabled:           scheduler.Enabled,
		Triggers:          triggers,
		Script:            scheduler.Script,
		SandboxPolicy:     schedulerSandboxPolicyToProto(scheduler.SandboxPolicy),
		DisplayName:       scheduler.DisplayName,
		Description:       scheduler.Description,
		Model:             scheduler.Model,
		ConcurrencyPolicy: schedulerConcurrencyPolicyToProto(scheduler.ConcurrencyPolicy),
	}
}

func TriggerSpecToProto(trigger compose.NormalizedTriggerSpec) *agentcomposev2.TriggerSpec {
	result := &agentcomposev2.TriggerSpec{
		Name:          trigger.Name,
		Kind:          triggerKindToProto(trigger.Kind),
		Prompt:        trigger.Prompt,
		SandboxPolicy: schedulerSandboxPolicyToProto(trigger.SandboxPolicy),
		Timezone:      trigger.Timezone,
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
