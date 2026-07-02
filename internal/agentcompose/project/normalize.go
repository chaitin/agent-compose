package project

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

type NormalizedV2Project struct {
	Spec       *compose.NormalizedProjectSpec
	SpecProto  *agentcomposev2.ProjectSpec
	SpecHash   string
	SourcePath string
}

func NormalizeProjectServiceSpec(spec *agentcomposev2.ProjectSpec, source *agentcomposev2.ProjectSource, expectedHash string) (NormalizedV2Project, []*agentcomposev2.ProjectValidationIssue, error) {
	if spec == nil {
		return NormalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue("spec", "project spec is required")}, nil
	}
	raw, issues := ProjectSpecYAMLShape(spec)
	if len(issues) > 0 {
		return NormalizedV2Project{}, issues, nil
	}
	data, err := yaml.Marshal(raw)
	if err != nil {
		return NormalizedV2Project{}, nil, fmt.Errorf("marshal project spec: %w", err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		return NormalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{IssueFromComposeError(err)}, nil
	}
	sourcePath := ProjectServiceSourcePath(source)
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{
		ComposePath: sourcePath,
		ProjectDir:  strings.TrimSpace(source.GetProjectDir()),
	})
	if err != nil {
		return NormalizedV2Project{}, []*agentcomposev2.ProjectValidationIssue{IssueFromComposeError(err)}, nil
	}
	hash, err := normalized.Hash()
	if err != nil {
		return NormalizedV2Project{}, nil, fmt.Errorf("hash project spec: %w", err)
	}
	result := NormalizedV2Project{
		Spec:       normalized,
		SpecProto:  ProjectSpecResponse(normalized),
		SpecHash:   hash,
		SourcePath: sourcePath,
	}
	expectedHash = strings.TrimSpace(expectedHash)
	if expectedHash != "" && expectedHash != hash {
		return result, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue("expected_spec_hash", fmt.Sprintf("expected spec hash %s does not match normalized spec hash %s", expectedHash, hash))}, nil
	}
	return result, nil, nil
}

func ProjectSpecYAMLShape(spec *agentcomposev2.ProjectSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	root := map[string]any{}
	if strings.TrimSpace(spec.GetName()) != "" {
		root["name"] = spec.GetName()
	}
	if variables, issues := envVarYAMLMap("variables", spec.GetVariables()); len(issues) > 0 {
		return nil, issues
	} else if len(variables) > 0 {
		root["variables"] = variables
	}
	if workspace := workspaceYAMLShape(spec.GetWorkspace()); len(workspace) > 0 {
		root["workspace"] = workspace
	}
	if agents, issues := agentYAMLMap(spec.GetAgents()); len(issues) > 0 {
		return nil, issues
	} else if len(agents) > 0 {
		root["agents"] = agents
	}
	if network := networkYAMLShape(spec.GetNetwork()); len(network) > 0 {
		root["network"] = network
	}
	return root, nil
}

func envVarYAMLMap(path string, vars []*agentcomposev2.EnvVarSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
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

func agentYAMLMap(agents []*agentcomposev2.AgentSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(agents))
	for i, agent := range agents {
		name := strings.TrimSpace(agent.GetName())
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("agents[%d].name", i), fmt.Sprintf("duplicate agent %q", name))}
		}
		raw := map[string]any{}
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
		if driver, issues := driverYAMLShape(fmt.Sprintf("agents[%d].driver", i), agent.GetDriver()); len(issues) > 0 {
			return nil, issues
		} else if len(driver) > 0 {
			raw["driver"] = driver
		}
		if env, issues := envVarYAMLMap(fmt.Sprintf("agents[%d].env", i), agent.GetEnv()); len(issues) > 0 {
			return nil, issues
		} else if len(env) > 0 {
			raw["env"] = env
		}
		if capsetIDs := normalizeStringIDs(agent.GetCapsetIds()); len(capsetIDs) > 0 {
			raw["capset_ids"] = capsetIDs
		}
		if workspace := workspaceYAMLShape(agent.GetWorkspace()); len(workspace) > 0 {
			raw["workspace"] = workspace
		}
		if scheduler := schedulerYAMLShape(agent.GetScheduler()); len(scheduler) > 0 {
			raw["scheduler"] = scheduler
		}
		values[name] = raw
	}
	return values, nil
}

func driverYAMLShape(path string, driver *agentcomposev2.DriverSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
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

func schedulerYAMLShape(scheduler *agentcomposev2.SchedulerSpec) map[string]any {
	if scheduler == nil {
		return nil
	}
	raw := map[string]any{"enabled": scheduler.GetEnabled()}
	triggers := make([]map[string]any, 0, len(scheduler.GetTriggers()))
	for _, trigger := range scheduler.GetTriggers() {
		triggers = append(triggers, triggerYAMLShape(trigger))
	}
	if len(triggers) > 0 {
		raw["triggers"] = triggers
	}
	if scheduler.GetScript() != "" {
		raw["script"] = scheduler.GetScript()
	}
	return raw
}

func triggerYAMLShape(trigger *agentcomposev2.TriggerSpec) map[string]any {
	raw := map[string]any{}
	if strings.TrimSpace(trigger.GetName()) != "" {
		raw["name"] = trigger.GetName()
	}
	if trigger.GetPrompt() != "" {
		raw["prompt"] = trigger.GetPrompt()
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

func workspaceYAMLShape(workspace *agentcomposev2.WorkspaceSpec) map[string]any {
	if workspace == nil {
		return nil
	}
	raw := map[string]any{}
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

func networkYAMLShape(network *agentcomposev2.NetworkSpec) map[string]any {
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
