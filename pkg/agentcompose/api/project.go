package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/projects"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func ProjectToProto(project domain.ProjectRecord, spec *agentcomposev2.ProjectSpec, agents []domain.ProjectAgentRecord, schedulers []domain.ProjectSchedulerRecord) *agentcomposev2.Project {
	agents = currentProjectAgents(project, agents)
	schedulers = currentProjectSchedulers(project, schedulers)
	return &agentcomposev2.Project{
		Summary:    ProjectSummaryToProto(project, agents, schedulers),
		Spec:       spec,
		Agents:     ProjectAgentsToProto(agents),
		Schedulers: ProjectSchedulersToProto(schedulers),
	}
}

func ProjectSummaryToProto(project domain.ProjectRecord, agents []domain.ProjectAgentRecord, schedulers []domain.ProjectSchedulerRecord) *agentcomposev2.ProjectSummary {
	agents = currentProjectAgents(project, agents)
	schedulers = currentProjectSchedulers(project, schedulers)
	return &agentcomposev2.ProjectSummary{
		ProjectId:       project.ID,
		Name:            project.Name,
		SourcePath:      project.SourcePath,
		CurrentRevision: uint64(project.CurrentRevision),
		SpecHash:        project.SpecHash,
		AgentCount:      uint32(len(agents)),
		SchedulerCount:  uint32(len(schedulers)),
		CreatedAt:       FormatProjectTime(project.CreatedAt),
		UpdatedAt:       FormatProjectTime(project.UpdatedAt),
		RemovedAt:       FormatProjectTime(project.RemovedAt),
	}
}

func currentProjectAgents(project domain.ProjectRecord, agents []domain.ProjectAgentRecord) []domain.ProjectAgentRecord {
	if project.CurrentRevision <= 0 {
		return agents
	}
	current := make([]domain.ProjectAgentRecord, 0, len(agents))
	for _, agent := range agents {
		if agent.Revision == project.CurrentRevision {
			current = append(current, agent)
		}
	}
	return current
}

func currentProjectSchedulers(project domain.ProjectRecord, schedulers []domain.ProjectSchedulerRecord) []domain.ProjectSchedulerRecord {
	if project.CurrentRevision <= 0 {
		return schedulers
	}
	current := make([]domain.ProjectSchedulerRecord, 0, len(schedulers))
	for _, scheduler := range schedulers {
		if scheduler.Revision == project.CurrentRevision {
			current = append(current, scheduler)
		}
	}
	return current
}

func ProjectRevisionToProto(revision domain.ProjectRevisionRecord, spec *agentcomposev2.ProjectSpec) *agentcomposev2.ProjectRevision {
	return &agentcomposev2.ProjectRevision{
		ProjectId: revision.ProjectID,
		Revision:  uint64(revision.Revision),
		SpecHash:  revision.SpecHash,
		Spec:      spec,
		CreatedAt: FormatProjectTime(revision.CreatedAt),
	}
}

func ProjectAgentsToProto(agents []domain.ProjectAgentRecord) []*agentcomposev2.ProjectAgent {
	items := make([]*agentcomposev2.ProjectAgent, 0, len(agents))
	for _, agent := range agents {
		status, specErr := projectAgentSpecStatus(agent.SpecJSON)
		enabled := status != agentcomposev2.AgentStatus_AGENT_STATUS_DISABLED
		availability := agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_AVAILABLE
		health := agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_HEALTHY
		if !enabled {
			availability = agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_UNAVAILABLE
		} else if specErr != nil || strings.TrimSpace(agent.ManagedAgentID) == "" || strings.TrimSpace(agent.AgentName) == "" || strings.TrimSpace(agent.Provider) == "" {
			availability = agentcomposev2.ProjectAgentAvailability_PROJECT_AGENT_AVAILABILITY_VALIDATION_FAILED
			health = agentcomposev2.ProjectAgentHealth_PROJECT_AGENT_HEALTH_AT_RISK
		}
		items = append(items, &agentcomposev2.ProjectAgent{
			ProjectId:        agent.ProjectID,
			AgentName:        agent.AgentName,
			ManagedAgentId:   agent.ManagedAgentID,
			Provider:         agent.Provider,
			Model:            agent.Model,
			Image:            agent.Image,
			Driver:           agent.Driver,
			SchedulerEnabled: agent.SchedulerEnabled,
			Enabled:          enabled, Availability: availability, Health: health,
		})
	}
	return items
}

func projectAgentSpecStatus(specJSON string) (agentcomposev2.AgentStatus, error) {
	var raw struct {
		Status json.RawMessage `json:"status"`
	}
	if err := json.Unmarshal([]byte(specJSON), &raw); err != nil {
		return agentcomposev2.AgentStatus_AGENT_STATUS_UNSPECIFIED, err
	}
	if len(raw.Status) == 0 || string(raw.Status) == "null" {
		return agentcomposev2.AgentStatus_AGENT_STATUS_UNSPECIFIED, nil
	}
	var status string
	if err := json.Unmarshal(raw.Status, &status); err == nil {
		switch strings.ToLower(strings.TrimSpace(status)) {
		case "", "enabled":
			return agentcomposev2.AgentStatus_AGENT_STATUS_ENABLED, nil
		case "disabled":
			return agentcomposev2.AgentStatus_AGENT_STATUS_DISABLED, nil
		default:
			return agentcomposev2.AgentStatus_AGENT_STATUS_UNSPECIFIED, fmt.Errorf("decode project agent spec: unknown agent status %q", status)
		}
	}
	var spec agentcomposev2.AgentSpec
	if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		return agentcomposev2.AgentStatus_AGENT_STATUS_UNSPECIFIED, err
	}
	return spec.GetStatus(), nil
}

func ProjectSchedulersToProto(schedulers []domain.ProjectSchedulerRecord) []*agentcomposev2.ProjectScheduler {
	items := make([]*agentcomposev2.ProjectScheduler, 0, len(schedulers))
	for _, scheduler := range schedulers {
		items = append(items, &agentcomposev2.ProjectScheduler{
			ProjectId:    scheduler.ProjectID,
			AgentName:    scheduler.AgentName,
			SchedulerId:  scheduler.SchedulerID,
			Enabled:      scheduler.Enabled,
			TriggerCount: uint32(scheduler.TriggerCount),
		})
	}
	return items
}

func ProjectApplyChanges(project domain.ProjectRecord, existing domain.ProjectRecord, found bool, revision domain.ProjectRevisionRecord, revisionCreated bool) []*agentcomposev2.ProjectChange {
	projectAction := agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED
	if found {
		projectAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UNCHANGED
		if !projects.ProjectRecordUnchanged(existing, project) {
			projectAction = agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_UPDATED
		}
	}
	return []*agentcomposev2.ProjectChange{
		{
			Action:       projectAction,
			ResourceType: "project",
			ResourceId:   project.ID,
			Name:         project.Name,
		},
	}
}

func DryRunProjectChanges(project domain.ProjectRecord, agents []domain.ProjectAgentRecord, agentDefinitions []domain.AgentDefinition, schedulers []domain.ProjectSchedulerRecord, loaders []domain.Loader) []*agentcomposev2.ProjectChange {
	changes := []*agentcomposev2.ProjectChange{{
		Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
		ResourceType: "project",
		ResourceId:   project.ID,
		Name:         project.Name,
	}}
	for _, agent := range agents {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "project_agent",
			ResourceId:   agent.ManagedAgentID,
			Name:         agent.AgentName,
		})
	}
	for _, agent := range agentDefinitions {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "agent_definition",
			ResourceId:   agent.ID,
			Name:         agent.Name,
		})
	}
	for _, scheduler := range schedulers {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "project_scheduler",
			ResourceId:   scheduler.SchedulerID,
			Name:         scheduler.AgentName,
		})
	}
	for _, loader := range loaders {
		changes = append(changes, &agentcomposev2.ProjectChange{
			Action:       agentcomposev2.ProjectChangeAction_PROJECT_CHANGE_ACTION_CREATED,
			ResourceType: "loader",
			ResourceId:   loader.Summary.ID,
			Name:         loader.Summary.Name,
		})
	}
	return changes
}

func ProjectSpecToProto(spec *compose.NormalizedProjectSpec) *agentcomposev2.ProjectSpec {
	result, err := ProjectSpecToProtoChecked(spec)
	if err != nil {
		return nil
	}
	return result
}

// ProjectSpecToProtoChecked prevents an unresolved CLI-only script URL from
// being mistaken for inline scheduler source on the wire.
func ProjectSpecToProtoChecked(spec *compose.NormalizedProjectSpec) (*agentcomposev2.ProjectSpec, error) {
	if spec == nil {
		return nil, nil
	}
	if err := spec.ValidateResolvedScriptURLs(); err != nil {
		return nil, err
	}
	return &agentcomposev2.ProjectSpec{
		Name:       spec.Name,
		Variables:  EnvVarSpecsToProto(spec.Variables),
		Workspaces: NamedWorkspaceSpecsToProto(spec.Workspaces),
		Agents:     AgentSpecsToProto(spec.Agents),
		Network:    NetworkSpecToProto(spec.Network),
		Volumes:    ProjectVolumeSpecsToProto(spec.Volumes),
		Mcps:       MCPServerSpecsToProto(spec.MCPs),
		Networks:   ProjectNetworkSpecsToProto(spec.Networks),
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
			Name:         agent.Name,
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Build:        BuildSpecToProto(agent.Build),
			Driver:       DriverSpecToProto(agent.Driver),
			Env:          EnvVarSpecsToProto(agent.Env),
			CapsetIds:    capabilities.NormalizeCapsetIDs(agent.CapsetIDs),
			Skills:       SkillSpecsToProto(agent.Skills),
			Workspace:    WorkspaceSpecToProto(agent.Workspace),
			Scheduler:    SchedulerSpecToProto(agent.Scheduler),
			Jupyter:      JupyterSpecToProto(agent.Jupyter),
			Volumes:      VolumeMountSpecsToProto(agent.Volumes),
			Mcps:         MCPServerSpecsToProto(agent.MCPs),
			Status:       agentStatusToProto(agent.Status),
			Networks:     append([]string(nil), agent.Networks...),
			Expose:       ExposedPortSpecsToProto(agent.Expose),
			Ports:        PublishedPortSpecsToProto(agent.Ports),
		})
	}
	return items
}

func ProjectNetworkSpecsToProto(networks map[string]compose.ProjectNetworkSpec) []*agentcomposev2.NamedNetworkSpec {
	keys := make([]string, 0, len(networks))
	for key := range networks {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	items := make([]*agentcomposev2.NamedNetworkSpec, 0, len(keys))
	for _, key := range keys {
		items = append(items, &agentcomposev2.NamedNetworkSpec{Name: key, Driver: networks[key].Driver})
	}
	return items
}

func ExposedPortSpecsToProto(ports []compose.ExposedPortSpec) []*agentcomposev2.ExposedPortSpec {
	items := make([]*agentcomposev2.ExposedPortSpec, 0, len(ports))
	for _, port := range ports {
		items = append(items, &agentcomposev2.ExposedPortSpec{Target: uint32(port.Target), Protocol: port.Protocol})
	}
	return items
}

func PublishedPortSpecsToProto(ports []compose.PublishedPortSpec) []*agentcomposev2.PublishedPortSpec {
	items := make([]*agentcomposev2.PublishedPortSpec, 0, len(ports))
	for _, port := range ports {
		items = append(items, &agentcomposev2.PublishedPortSpec{
			HostIp:    port.HostIP,
			Published: uint32(port.Published),
			Target:    uint32(port.Target),
			Protocol:  port.Protocol,
		})
	}
	return items
}

func agentStatusToProto(status string) agentcomposev2.AgentStatus {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "enabled":
		return agentcomposev2.AgentStatus_AGENT_STATUS_ENABLED
	case "disabled":
		return agentcomposev2.AgentStatus_AGENT_STATUS_DISABLED
	default:
		return agentcomposev2.AgentStatus_AGENT_STATUS_UNSPECIFIED
	}
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
			Source:   skill.Source,
			Url:      skill.URL,
			Path:     skill.Path,
			Ref:      skill.Ref,
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
			Type:     volume.Type,
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
		Branch:   workspace.Branch,
		Path:     workspace.Path,
	}
}

func NetworkSpecToProto(network *compose.NetworkSpec) *agentcomposev2.NetworkSpec {
	if network == nil {
		return nil
	}
	return &agentcomposev2.NetworkSpec{Mode: network.Mode}
}

func DriverSpecToProto(driver *compose.NormalizedDriverSpec) *agentcomposev2.DriverSpec {
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
		Enabled:       scheduler.Enabled,
		Triggers:      triggers,
		Script:        scheduler.Script,
		SandboxPolicy: scheduler.SandboxPolicy,
	}
}

func TriggerSpecToProto(trigger compose.NormalizedTriggerSpec) *agentcomposev2.TriggerSpec {
	result := &agentcomposev2.TriggerSpec{
		Name:          trigger.Name,
		Kind:          trigger.Kind,
		Prompt:        trigger.Prompt,
		SandboxPolicy: trigger.SandboxPolicy,
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

func ProjectSpecYAMLShape(spec *agentcomposev2.ProjectSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	root := map[string]any{}
	if strings.TrimSpace(spec.GetName()) != "" {
		root["name"] = spec.GetName()
	}
	if variables, issues := EnvVarYAMLMap("variables", spec.GetVariables()); len(issues) > 0 {
		return nil, issues
	} else if len(variables) > 0 {
		root["variables"] = variables
	}
	if workspaces, issues := NamedWorkspaceYAMLMap(spec.GetWorkspaces()); len(issues) > 0 {
		return nil, issues
	} else if len(workspaces) > 0 {
		root["workspaces"] = workspaces
	}
	if agents, issues := AgentYAMLMap(spec.GetAgents()); len(issues) > 0 {
		return nil, issues
	} else if len(agents) > 0 {
		root["agents"] = agents
	}
	if volumes, issues := ProjectVolumeYAMLMap(spec.GetVolumes()); len(issues) > 0 {
		return nil, issues
	} else if len(volumes) > 0 {
		root["volumes"] = volumes
	}
	if mcps, issues := MCPServerYAMLMap("mcps", spec.GetMcps()); len(issues) > 0 {
		return nil, issues
	} else if len(mcps) > 0 {
		root["mcps"] = mcps
	}
	if network := NetworkYAMLShape(spec.GetNetwork()); len(network) > 0 {
		root["network"] = network
	}
	if networks, issues := ProjectNetworkYAMLMap(spec.GetNetworks()); len(issues) > 0 {
		return nil, issues
	} else if len(networks) > 0 {
		root["networks"] = networks
	}
	return root, nil
}

func ProjectNetworkYAMLMap(networks []*agentcomposev2.NamedNetworkSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(networks))
	for index, network := range networks {
		name := strings.TrimSpace(network.GetName())
		if name == "" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("networks[%d].name", index), "network name is required")}
		}
		if _, exists := values[name]; exists {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("networks[%d].name", index), fmt.Sprintf("duplicate network %q", name))}
		}
		raw := map[string]any{}
		if driver := strings.TrimSpace(network.GetDriver()); driver != "" {
			raw["driver"] = driver
		}
		values[name] = raw
	}
	return values, nil
}

func ProjectVolumeYAMLMap(volumes []*agentcomposev2.ProjectVolumeSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(volumes))
	for i, volume := range volumes {
		key := strings.TrimSpace(volume.GetKey())
		if key == "" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("volumes[%d].key", i), "volume key is required")}
		}
		if _, ok := values[key]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("volumes[%d].key", i), fmt.Sprintf("duplicate volume %q", key))}
		}
		raw := map[string]any{}
		if strings.TrimSpace(volume.GetName()) != "" {
			raw["name"] = volume.GetName()
		}
		if strings.TrimSpace(volume.GetDriver()) != "" {
			raw["driver"] = volume.GetDriver()
		}
		if volume.GetExternal() {
			raw["external"] = true
		}
		if len(volume.GetLabels()) > 0 {
			raw["labels"] = cloneProjectStringMap(volume.GetLabels())
		}
		if len(volume.GetOptions()) > 0 {
			raw["options"] = cloneProjectStringMap(volume.GetOptions())
		}
		values[key] = raw
	}
	return values, nil
}

func EnvVarYAMLMap(path string, vars []*agentcomposev2.EnvVarSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(vars))
	for i, env := range vars {
		name := strings.TrimSpace(env.GetName())
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].name", path, i), fmt.Sprintf("duplicate environment variable %q", name))}
		}
		if env.GetSecret() {
			values[name] = map[string]any{
				"value":  env.GetValue(),
				"secret": true,
			}
		} else {
			values[name] = env.GetValue()
		}
	}
	return values, nil
}

func AgentYAMLMap(agents []*agentcomposev2.AgentSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(agents))
	for i, agent := range agents {
		name := strings.TrimSpace(agent.GetName())
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("agents[%d].name", i), fmt.Sprintf("duplicate agent %q", name))}
		}
		raw := map[string]any{}
		switch agent.GetStatus() {
		case agentcomposev2.AgentStatus_AGENT_STATUS_ENABLED:
			raw["status"] = "enabled"
		case agentcomposev2.AgentStatus_AGENT_STATUS_DISABLED:
			raw["status"] = "disabled"
		case agentcomposev2.AgentStatus_AGENT_STATUS_UNSPECIFIED:
		default:
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("agents[%d].status", i), "unknown agent status")}
		}
		if strings.TrimSpace(agent.GetProvider()) != "" {
			raw["provider"] = agent.GetProvider()
		}
		if strings.TrimSpace(agent.GetModel()) != "" {
			raw["model"] = agent.GetModel()
		}
		if agent.GetSystemPrompt() != "" {
			raw["system_prompt"] = agent.GetSystemPrompt()
		}
		if strings.TrimSpace(agent.GetImage()) != "" {
			raw["image"] = agent.GetImage()
		}
		if build := BuildYAMLShape(agent.GetBuild()); len(build) > 0 {
			raw["build"] = build
		}
		if driver, issues := DriverYAMLShape(fmt.Sprintf("agents[%d].driver", i), agent.GetDriver()); len(issues) > 0 {
			return nil, issues
		} else if len(driver) > 0 {
			raw["driver"] = driver
		}
		if env, issues := EnvVarYAMLMap(fmt.Sprintf("agents[%d].env", i), agent.GetEnv()); len(issues) > 0 {
			return nil, issues
		} else if len(env) > 0 {
			raw["env"] = env
		}
		if capsetIDs := capabilities.NormalizeCapsetIDs(agent.GetCapsetIds()); len(capsetIDs) > 0 {
			raw["capset_ids"] = capsetIDs
		}
		if skills, issues := SkillYAMLList(fmt.Sprintf("agents[%d].skills", i), agent.GetSkills()); len(issues) > 0 {
			return nil, issues
		} else if len(skills) > 0 {
			raw["skills"] = skills
		}
		if workspace := WorkspaceYAMLShape(agent.GetWorkspace()); len(workspace) > 0 {
			raw["workspace"] = workspace
		}
		if scheduler := SchedulerYAMLShape(agent.GetScheduler()); len(scheduler) > 0 {
			raw["scheduler"] = scheduler
		}
		if jupyter := JupyterYAMLShape(agent.GetJupyter()); len(jupyter) > 0 {
			raw["jupyter"] = jupyter
		}
		if len(agent.GetNetworks()) > 0 {
			raw["networks"] = append([]string(nil), agent.GetNetworks()...)
		}
		if expose, issues := ExposedPortYAMLList(fmt.Sprintf("agents[%d].expose", i), agent.GetExpose()); len(issues) > 0 {
			return nil, issues
		} else if len(expose) > 0 {
			raw["expose"] = expose
		}
		if ports, issues := PublishedPortYAMLList(fmt.Sprintf("agents[%d].ports", i), agent.GetPorts()); len(issues) > 0 {
			return nil, issues
		} else if len(ports) > 0 {
			raw["ports"] = ports
		}
		if volumes := VolumeMountYAMLList(agent.GetVolumes()); len(volumes) > 0 {
			raw["volumes"] = volumes
		}
		if mcps, issues := AgentMCPYAMLList(fmt.Sprintf("agents[%d].mcps", i), agent.GetMcps()); len(issues) > 0 {
			return nil, issues
		} else if len(mcps) > 0 {
			raw["mcps"] = mcps
		}
		values[name] = raw
	}
	return values, nil
}

func ExposedPortYAMLList(path string, ports []*agentcomposev2.ExposedPortSpec) ([]string, []*agentcomposev2.ProjectValidationIssue) {
	result := make([]string, 0, len(ports))
	for index, port := range ports {
		if port.GetTarget() == 0 || port.GetTarget() > 65535 {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].target", path, index), "target must be a valid TCP port")}
		}
		protocol := strings.ToLower(strings.TrimSpace(port.GetProtocol()))
		if protocol != "" && protocol != "tcp" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].protocol", path, index), "only TCP ports are supported")}
		}
		result = append(result, strconv.FormatUint(uint64(port.GetTarget()), 10))
	}
	return result, nil
}

func PublishedPortYAMLList(path string, ports []*agentcomposev2.PublishedPortSpec) ([]string, []*agentcomposev2.ProjectValidationIssue) {
	result := make([]string, 0, len(ports))
	for index, port := range ports {
		if port.GetTarget() == 0 || port.GetTarget() > 65535 || port.GetPublished() > 65535 {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d]", path, index), "published and target must be valid TCP ports")}
		}
		protocol := strings.ToLower(strings.TrimSpace(port.GetProtocol()))
		if protocol != "" && protocol != "tcp" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].protocol", path, index), "only TCP ports are supported")}
		}
		hostIP := strings.TrimSpace(port.GetHostIp())
		target := strconv.FormatUint(uint64(port.GetTarget()), 10)
		if port.GetPublished() == 0 {
			result = append(result, target)
			continue
		}
		published := strconv.FormatUint(uint64(port.GetPublished()), 10)
		if hostIP == "" || hostIP == "127.0.0.1" {
			result = append(result, published+":"+target)
			continue
		}
		result = append(result, hostIP+":"+published+":"+target)
	}
	return result, nil
}

func MCPServerYAMLMap(path string, mcps []*agentcomposev2.MCPServerSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(mcps))
	for i, mcp := range mcps {
		name := strings.TrimSpace(mcp.GetName())
		if name == "" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].name", path, i), "mcp name is required")}
		}
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].name", path, i), fmt.Sprintf("duplicate mcp %q", name))}
		}
		values[name] = mcpServerYAMLShape(mcp)
	}
	return values, nil
}

func AgentMCPYAMLList(path string, mcps []*agentcomposev2.MCPServerSpec) ([]map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make([]map[string]any, 0, len(mcps))
	seen := make(map[string]struct{}, len(mcps))
	for i, mcp := range mcps {
		name := strings.TrimSpace(mcp.GetName())
		if name == "" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].name", path, i), "mcp name is required")}
		}
		if _, ok := seen[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].name", path, i), fmt.Sprintf("duplicate mcp %q", name))}
		}
		seen[name] = struct{}{}
		shape := mcpServerYAMLShape(mcp)
		shape["name"] = name
		values = append(values, shape)
	}
	return values, nil
}

func mcpServerYAMLShape(mcp *agentcomposev2.MCPServerSpec) map[string]any {
	raw := map[string]any{}
	if mcp == nil {
		return raw
	}
	if strings.TrimSpace(mcp.GetType()) != "" {
		raw["type"] = mcp.GetType()
	}
	if strings.TrimSpace(mcp.GetTransport()) != "" {
		raw["transport"] = mcp.GetTransport()
	}
	if strings.TrimSpace(mcp.GetCommand()) != "" {
		raw["command"] = mcp.GetCommand()
	}
	if len(mcp.GetArgs()) > 0 {
		raw["args"] = append([]string(nil), mcp.GetArgs()...)
	}
	if env, issues := EnvVarYAMLMap("env", mcp.GetEnv()); len(issues) == 0 && len(env) > 0 {
		raw["env"] = env
	}
	if strings.TrimSpace(mcp.GetUrl()) != "" {
		raw["url"] = mcp.GetUrl()
	}
	if headers, issues := EnvVarYAMLMap("headers", mcp.GetHeaders()); len(issues) == 0 && len(headers) > 0 {
		raw["headers"] = headers
	}
	return raw
}

func SkillYAMLList(path string, skills []*agentcomposev2.SkillSpec) ([]map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	items := make([]map[string]any, 0, len(skills))
	seen := make(map[string]struct{}, len(skills))
	for i, skill := range skills {
		name := strings.TrimSpace(skill.GetName())
		if name != "" {
			if _, ok := seen[name]; ok {
				return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("%s[%d].name", path, i), fmt.Sprintf("duplicate skill %q", name))}
			}
			seen[name] = struct{}{}
		}
		raw := map[string]any{}
		if name != "" {
			raw["name"] = name
		}
		if strings.TrimSpace(skill.GetSource()) != "" {
			raw["source"] = skill.GetSource()
		}
		if strings.TrimSpace(skill.GetUrl()) != "" {
			raw["url"] = skill.GetUrl()
		}
		if strings.TrimSpace(skill.GetPath()) != "" {
			raw["path"] = skill.GetPath()
		}
		if strings.TrimSpace(skill.GetRef()) != "" {
			raw["ref"] = skill.GetRef()
		}
		if strings.TrimSpace(skill.GetUsername()) != "" {
			raw["username"] = skill.GetUsername()
		}
		if strings.TrimSpace(skill.GetPassword()) != "" {
			raw["password"] = skill.GetPassword()
		}
		if strings.TrimSpace(skill.GetToken()) != "" {
			raw["token"] = skill.GetToken()
		}
		if len(raw) > 0 {
			items = append(items, raw)
		}
	}
	return items, nil
}

func VolumeMountYAMLList(volumes []*agentcomposev2.VolumeMountSpec) []map[string]any {
	items := make([]map[string]any, 0, len(volumes))
	for _, volume := range volumes {
		raw := map[string]any{}
		if strings.TrimSpace(volume.GetType()) != "" {
			raw["type"] = volume.GetType()
		}
		if strings.TrimSpace(volume.GetSource()) != "" {
			raw["source"] = volume.GetSource()
		}
		if strings.TrimSpace(volume.GetTarget()) != "" {
			raw["target"] = volume.GetTarget()
		}
		if volume.GetReadOnly() {
			raw["read_only"] = true
		}
		if len(raw) > 0 {
			items = append(items, raw)
		}
	}
	return items
}

func BuildYAMLShape(build *agentcomposev2.BuildSpec) map[string]any {
	if build == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(build.GetContext()) != "" {
		raw["context"] = build.GetContext()
	}
	if strings.TrimSpace(build.GetDockerfile()) != "" {
		raw["dockerfile"] = build.GetDockerfile()
	}
	if strings.TrimSpace(build.GetTarget()) != "" {
		raw["target"] = build.GetTarget()
	}
	if len(build.GetArgs()) > 0 {
		raw["args"] = cloneProjectStringMap(build.GetArgs())
	}
	if len(build.GetPlatforms()) > 0 {
		raw["platforms"] = append([]string(nil), build.GetPlatforms()...)
	}
	if len(build.GetTags()) > 0 {
		raw["tags"] = append([]string(nil), build.GetTags()...)
	}
	if build.GetNoCache() {
		raw["no_cache"] = true
	}
	if build.GetPull() {
		raw["pull"] = true
	}
	return raw
}

func cloneProjectStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func JupyterYAMLShape(jupyter *agentcomposev2.JupyterSpec) map[string]any {
	if jupyter == nil {
		return nil
	}
	raw := map[string]any{}
	if jupyter.GetEnabled() {
		raw["enabled"] = true
	}
	if jupyter.GetGuestPort() != 0 {
		raw["guest_port"] = jupyter.GetGuestPort()
	}
	return raw
}

func DriverYAMLShape(path string, driver *agentcomposev2.DriverSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	if driver == nil {
		return nil, nil
	}
	byName := strings.ToLower(strings.TrimSpace(driver.GetName()))
	runtimes := make(map[string]any, 3)
	if driver.GetBoxlite() != nil {
		runtimes[compose.DriverBoxlite] = map[string]any{
			"kernel": driver.GetBoxlite().GetKernel(),
			"rootfs": driver.GetBoxlite().GetRootfs(),
		}
	}
	if driver.GetDocker() != nil {
		runtimes[compose.DriverDocker] = map[string]any{"host": driver.GetDocker().GetHost()}
	}
	if driver.GetMicrosandbox() != nil {
		runtimes[compose.DriverMicrosandbox] = map[string]any{"profile": driver.GetMicrosandbox().GetProfile()}
	}
	switch byName {
	case "":
	case compose.DriverBoxlite, compose.DriverDocker, compose.DriverMicrosandbox:
		for runtimeName := range runtimes {
			if runtimeName != byName {
				return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(path, fmt.Sprintf("driver name %q conflicts with %q runtime config", byName, runtimeName))}
			}
		}
		if existing, ok := runtimes[byName]; ok {
			return map[string]any{byName: existing}, nil
		}
		return map[string]any{byName: map[string]any{}}, nil
	default:
		return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(path+".name", fmt.Sprintf("unsupported runtime driver %q", byName))}
	}
	return runtimes, nil
}

func SchedulerYAMLShape(scheduler *agentcomposev2.SchedulerSpec) map[string]any {
	if scheduler == nil {
		return nil
	}
	raw := map[string]any{"enabled": scheduler.GetEnabled()}
	if scheduler.GetSandboxPolicy() != "" {
		raw["sandbox_policy"] = scheduler.GetSandboxPolicy()
	}
	triggers := make([]map[string]any, 0, len(scheduler.GetTriggers()))
	for _, trigger := range scheduler.GetTriggers() {
		triggers = append(triggers, TriggerYAMLShape(trigger))
	}
	if len(triggers) > 0 {
		raw["triggers"] = triggers
	}
	if scheduler.GetScript() != "" {
		raw["script"] = scheduler.GetScript()
	}
	return raw
}

func TriggerYAMLShape(trigger *agentcomposev2.TriggerSpec) map[string]any {
	raw := map[string]any{}
	if strings.TrimSpace(trigger.GetName()) != "" {
		raw["name"] = trigger.GetName()
	}
	if trigger.GetPrompt() != "" {
		raw["prompt"] = trigger.GetPrompt()
	}
	if trigger.GetSandboxPolicy() != "" {
		raw["sandbox_policy"] = trigger.GetSandboxPolicy()
	}
	kind := strings.ToLower(strings.TrimSpace(trigger.GetKind()))
	if kind == "" || kind == "cron" {
		if kind == "cron" || strings.TrimSpace(trigger.GetCron()) != "" {
			raw["cron"] = trigger.GetCron()
		}
	}
	if kind == "" || kind == "interval" {
		if kind == "interval" || strings.TrimSpace(trigger.GetInterval()) != "" {
			raw["interval"] = trigger.GetInterval()
		}
	}
	if kind == "" || kind == "timeout" {
		if kind == "timeout" || strings.TrimSpace(trigger.GetTimeout()) != "" {
			raw["timeout"] = trigger.GetTimeout()
		}
	}
	if kind == "" || kind == "event" {
		if kind == "event" || trigger.GetEvent() != nil {
			raw["event"] = map[string]any{"topic": trigger.GetEvent().GetTopic()}
		}
	}
	if kind != "" && kind != "cron" && kind != "interval" && kind != "timeout" && kind != "event" {
		raw[kind] = ""
	}
	return raw
}

func WorkspaceYAMLShape(workspace *agentcomposev2.WorkspaceSpec) map[string]any {
	if workspace == nil {
		return nil
	}
	raw := map[string]any{}
	if strings.TrimSpace(workspace.GetName()) != "" {
		raw["name"] = workspace.GetName()
	}
	if strings.TrimSpace(workspace.GetProvider()) != "" {
		raw["provider"] = workspace.GetProvider()
	}
	if strings.TrimSpace(workspace.GetUrl()) != "" {
		raw["url"] = workspace.GetUrl()
	}
	if strings.TrimSpace(workspace.GetBranch()) != "" {
		raw["branch"] = workspace.GetBranch()
	}
	if strings.TrimSpace(workspace.GetPath()) != "" {
		raw["path"] = workspace.GetPath()
	}
	return raw
}

func NamedWorkspaceYAMLMap(workspaces []*agentcomposev2.NamedWorkspaceSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(workspaces))
	for i, item := range workspaces {
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("workspaces[%d].name", i), "workspace name is required")}
		}
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("workspaces[%d].name", i), fmt.Sprintf("duplicate workspace %q", name))}
		}
		workspace := WorkspaceYAMLShape(item.GetWorkspace())
		delete(workspace, "name")
		values[name] = workspace
	}
	return values, nil
}

func NetworkYAMLShape(network *agentcomposev2.NetworkSpec) map[string]any {
	if network == nil {
		return nil
	}
	return map[string]any{"mode": network.GetMode()}
}

func ProjectServiceSourcePath(source *agentcomposev2.ProjectSource) string {
	if source == nil {
		return ""
	}
	if composePath := strings.TrimSpace(source.GetComposePath()); composePath != "" {
		return composePath
	}
	if projectDir := strings.TrimSpace(source.GetProjectDir()); projectDir != "" {
		return filepath.Join(projectDir, "agent-compose.yml")
	}
	return ""
}

func IssueFromComposeError(err error) *agentcomposev2.ProjectValidationIssue {
	var validationErr *compose.ValidationError
	if errors.As(err, &validationErr) {
		return ProjectValidationIssue(validationErr.Path, validationErr.Message)
	}
	var parseErr *compose.ParseError
	if errors.As(err, &parseErr) {
		return ProjectValidationIssue(parseErr.Path, parseErr.Message)
	}
	return ProjectValidationIssue("spec", err.Error())
}

func ProjectValidationIssue(path, message string) *agentcomposev2.ProjectValidationIssue {
	if strings.TrimSpace(path) == "" {
		path = "spec"
	}
	return &agentcomposev2.ProjectValidationIssue{
		Severity: agentcomposev2.ProjectValidationSeverity_PROJECT_VALIDATION_SEVERITY_ERROR,
		Path:     path,
		Message:  message,
	}
}
