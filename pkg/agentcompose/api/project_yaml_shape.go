package api

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-compose/pkg/capabilities"
	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

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
	if mcps, issues := MCPServerYAMLMap("mcp_servers", spec.GetMcpServers()); len(issues) > 0 {
		return nil, issues
	} else if len(mcps) > 0 {
		root["mcp_servers"] = mcps
	}
	if octobusServers, issues := OctoBusServerYAMLMap(spec.GetOctobusServers()); len(issues) > 0 {
		return nil, issues
	} else if len(octobusServers) > 0 {
		root["octobus_servers"] = octobusServers
	}
	return root, nil
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
		if strings.TrimSpace(agent.GetDisplayName()) != "" {
			raw["display_name"] = agent.GetDisplayName()
		}
		if strings.TrimSpace(agent.GetDescription()) != "" {
			raw["description"] = agent.GetDescription()
		}
		if schema, issue := agentJSONSchemaYAMLValue(fmt.Sprintf("agents[%d].input_schema_json", i), agent.GetInputSchemaJson()); issue != nil {
			return nil, []*agentcomposev2.ProjectValidationIssue{issue}
		} else if schema != nil {
			raw["input_schema"] = schema
		}
		if schema, issue := agentJSONSchemaYAMLValue(fmt.Sprintf("agents[%d].output_schema_json", i), agent.GetOutputSchemaJson()); issue != nil {
			return nil, []*agentcomposev2.ProjectValidationIssue{issue}
		} else if schema != nil {
			raw["output_schema"] = schema
		}
		if agent.Enabled != nil {
			raw["enabled"] = agent.GetEnabled()
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
		if sandbox := agent.GetSandbox(); sandbox != nil {
			raw["sandbox"] = map[string]any{"stopped_runtime_policy": sandbox.GetStoppedRuntimePolicy()}
		}
		if scheduler := SchedulerYAMLShape(agent.GetScheduler()); len(scheduler) > 0 {
			raw["scheduler"] = scheduler
		}
		if jupyter := JupyterYAMLShape(agent.GetJupyter()); len(jupyter) > 0 {
			raw["jupyter"] = jupyter
		}
		if volumes := VolumeMountYAMLList(agent.GetVolumes()); len(volumes) > 0 {
			raw["volumes"] = volumes
		}
		if mcps, issues := AgentMCPYAMLList(fmt.Sprintf("agents[%d].mcp_servers", i), agent.GetMcpServers()); len(issues) > 0 {
			return nil, issues
		} else if len(mcps) > 0 {
			raw["mcp_servers"] = mcps
		}
		values[name] = raw
	}
	return values, nil
}

func agentJSONSchemaYAMLValue(path, raw string) (any, *agentcomposev2.ProjectValidationIssue) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var value any
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return nil, ProjectValidationIssue(path, "must contain valid JSON")
	}
	switch value.(type) {
	case map[string]any, bool:
		return value, nil
	default:
		return nil, ProjectValidationIssue(path, "must contain a JSON Schema object or boolean")
	}
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
		if strings.TrimSpace(skill.GetProvider()) != "" {
			raw["provider"] = skill.GetProvider()
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
		if strings.TrimSpace(skill.GetFormat()) != "" {
			raw["format"] = skill.GetFormat()
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
		if mountType := VolumeMountTypeText(volume.GetType()); mountType != "" {
			raw["type"] = mountType
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
	name := strings.ToLower(strings.TrimSpace(driver.GetName()))
	var configName string
	var config map[string]any
	switch driver.GetConfig().(type) {
	case *agentcomposev2.DriverSpec_Boxlite:
		configName = compose.DriverBoxlite
		config = map[string]any{
			"kernel": driver.GetBoxlite().GetKernel(),
			"rootfs": driver.GetBoxlite().GetRootfs(),
		}
	case *agentcomposev2.DriverSpec_Docker:
		configName = compose.DriverDocker
		config = map[string]any{"host": driver.GetDocker().GetHost()}
	case *agentcomposev2.DriverSpec_Microsandbox:
		configName = compose.DriverMicrosandbox
		config = map[string]any{"profile": driver.GetMicrosandbox().GetProfile()}
	default:
		return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(path, "driver requires exactly one runtime config")}
	}
	if name == "" {
		return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(path+".name", "driver name is required")}
	}
	if name != configName {
		return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(path, fmt.Sprintf("driver name %q conflicts with %q runtime config", name, configName))}
	}
	return map[string]any{configName: config}, nil
}

func SchedulerYAMLShape(scheduler *agentcomposev2.SchedulerSpec) map[string]any {
	if scheduler == nil {
		return nil
	}
	raw := map[string]any{"enabled": scheduler.GetEnabled()}
	if strings.TrimSpace(scheduler.GetDisplayName()) != "" {
		raw["display_name"] = scheduler.GetDisplayName()
	}
	if strings.TrimSpace(scheduler.GetDescription()) != "" {
		raw["description"] = scheduler.GetDescription()
	}
	if strings.TrimSpace(scheduler.GetModel()) != "" {
		raw["model"] = scheduler.GetModel()
	}
	if policy := schedulerSandboxPolicyText(scheduler.GetSandboxPolicy()); policy != "" {
		raw["sandbox_policy"] = policy
	}
	if policy := schedulerConcurrencyPolicyText(scheduler.GetConcurrencyPolicy()); policy != "" {
		raw["concurrency_policy"] = policy
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
	if strings.TrimSpace(trigger.GetTimezone()) != "" {
		raw["timezone"] = trigger.GetTimezone()
	}
	if policy := schedulerSandboxPolicyText(trigger.GetSandboxPolicy()); policy != "" {
		raw["sandbox_policy"] = policy
	}
	kind := triggerKindText(trigger.GetKind())
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
	if strings.TrimSpace(workspace.GetRef()) != "" {
		raw["ref"] = workspace.GetRef()
	}
	if strings.TrimSpace(workspace.GetPath()) != "" {
		raw["path"] = workspace.GetPath()
	}
	if strings.TrimSpace(workspace.GetFormat()) != "" {
		raw["format"] = workspace.GetFormat()
	}
	if strings.TrimSpace(workspace.GetTarget()) != "" {
		raw["target"] = workspace.GetTarget()
	}
	if strings.TrimSpace(workspace.GetUsername()) != "" {
		raw["username"] = workspace.GetUsername()
	}
	if strings.TrimSpace(workspace.GetPassword()) != "" {
		raw["password"] = workspace.GetPassword()
	}
	if strings.TrimSpace(workspace.GetToken()) != "" {
		raw["token"] = workspace.GetToken()
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
