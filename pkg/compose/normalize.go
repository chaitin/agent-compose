package compose

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

const (
	DriverBoxlite      = "boxlite"
	DriverDocker       = "docker"
	DriverMicrosandbox = "microsandbox"
	DriverFirecracker  = "firecracker"
)

var stableIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var envReferencePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var composeCronParser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

type NormalizeOptions struct {
	ProjectDir  string
	ComposePath string
	Env         map[string]string
}

type NormalizedProjectSpec struct {
	APIVersion  string                         `yaml:"apiVersion,omitempty" json:"apiVersion,omitempty"`
	Kind        string                         `yaml:"kind,omitempty" json:"kind,omitempty"`
	Name        string                         `yaml:"name" json:"name"`
	Metadata    *ProjectMetadataSpec           `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Variables   map[string]EnvVarSpec          `yaml:"variables,omitempty" json:"variables,omitempty"`
	Runtime     *NormalizedProjectRuntimeSpec  `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Workspace   *WorkspaceSpec                 `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Agents      []NormalizedAgentSpec          `yaml:"agents,omitempty" json:"agents,omitempty"`
	Services    []NormalizedServiceSpec        `yaml:"services,omitempty" json:"services,omitempty"`
	Triggers    []NormalizedProjectTriggerSpec `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	Permissions *PermissionSpec                `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Artifacts   *ArtifactPolicySpec            `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	Network     *NetworkSpec                   `yaml:"network,omitempty" json:"network,omitempty"`
}

type NormalizedProjectRuntimeSpec struct {
	Driver        string                `yaml:"driver,omitempty" json:"driver,omitempty"`
	Image         string                `yaml:"image,omitempty" json:"image,omitempty"`
	Env           map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	Resources     map[string]string     `yaml:"resources,omitempty" json:"resources,omitempty"`
	Network       *NetworkSpec          `yaml:"network,omitempty" json:"network,omitempty"`
	CleanupPolicy string                `yaml:"cleanup_policy,omitempty" json:"cleanup_policy,omitempty"`
}

type NormalizedAgentSpec struct {
	Name         string                   `yaml:"name" json:"name"`
	Provider     string                   `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                   `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                   `yaml:"image,omitempty" json:"image,omitempty"`
	Driver       *NormalizedDriverSpec    `yaml:"driver" json:"driver"`
	Env          map[string]EnvVarSpec    `yaml:"env,omitempty" json:"env,omitempty"`
	CapsetIDs    []string                 `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Metadata     map[string]string        `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Workspace    *WorkspaceSpec           `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *NormalizedSchedulerSpec `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
}

type NormalizedServiceSpec struct {
	Name         string                `yaml:"name" json:"name"`
	Description  string                `yaml:"description,omitempty" json:"description,omitempty"`
	Runtime      string                `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Entry        string                `yaml:"entry" json:"entry"`
	InputSchema  string                `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`
	OutputSchema string                `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`
	ErrorSchema  string                `yaml:"error_schema,omitempty" json:"error_schema,omitempty"`
	Timeout      string                `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retry        *RetryPolicySpec      `yaml:"retry,omitempty" json:"retry,omitempty"`
	Permissions  *PermissionSpec       `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Env          map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	Agents       []string              `yaml:"agents,omitempty" json:"agents,omitempty"`
	Examples     []ServiceExampleSpec  `yaml:"examples,omitempty" json:"examples,omitempty"`
}

type NormalizedProjectTriggerSpec struct {
	Name     string              `yaml:"name" json:"name"`
	Kind     string              `yaml:"kind" json:"kind"`
	Cron     string              `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval string              `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string              `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Event    *EventTriggerSpec   `yaml:"event,omitempty" json:"event,omitempty"`
	Webhook  *WebhookTriggerSpec `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	Target   TriggerTargetSpec   `yaml:"target" json:"target"`
	Input    string              `yaml:"input,omitempty" json:"input,omitempty"`
}

type NormalizedDriverSpec struct {
	Name         string                  `yaml:"name" json:"name"`
	Boxlite      *BoxliteDriverSpec      `yaml:"boxlite,omitempty" json:"boxlite,omitempty"`
	Docker       *DockerDriverSpec       `yaml:"docker,omitempty" json:"docker,omitempty"`
	Microsandbox *MicrosandboxDriverSpec `yaml:"microsandbox,omitempty" json:"microsandbox,omitempty"`
}

type NormalizedSchedulerSpec struct {
	Enabled  bool                    `yaml:"enabled" json:"enabled"`
	Script   string                  `yaml:"script,omitempty" json:"script,omitempty"`
	Triggers []NormalizedTriggerSpec `yaml:"triggers,omitempty" json:"triggers,omitempty"`
}

type NormalizedTriggerSpec struct {
	Name     string            `yaml:"name,omitempty" json:"name,omitempty"`
	Kind     string            `yaml:"kind" json:"kind"`
	Cron     string            `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval string            `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Event    *EventTriggerSpec `yaml:"event,omitempty" json:"event,omitempty"`
	Prompt   string            `yaml:"prompt,omitempty" json:"prompt,omitempty"`
}

type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Path == "" {
		return "validate compose: " + e.Message
	}
	return fmt.Sprintf("validate compose field %s: %s", e.Path, e.Message)
}

func Normalize(spec *ProjectSpec, options NormalizeOptions) (*NormalizedProjectSpec, error) {
	if spec == nil {
		return nil, &ValidationError{Message: "spec is required"}
	}

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = defaultProjectName(options)
	}
	if err := validateStableIdentifier("name", name, "project name"); err != nil {
		return nil, err
	}

	normalized := &NormalizedProjectSpec{
		APIVersion: strings.TrimSpace(spec.APIVersion),
		Kind:       strings.TrimSpace(spec.Kind),
		Name:       name,
		Metadata:   cloneProjectMetadataSpec(spec.Metadata),
		Workspace:  cloneWorkspaceSpec(spec.Workspace),
		Network:    normalizeNetworkDefault(spec.Network),
	}
	variables, err := normalizeEnvVarMap("variables", spec.Variables, options)
	if err != nil {
		return nil, err
	}
	normalized.Variables = variables
	runtime, err := normalizeProjectRuntimeSpec("runtime", spec.Runtime, options)
	if err != nil {
		return nil, err
	}
	normalized.Runtime = runtime
	if err := validateNetworkSpec(normalized.Network); err != nil {
		return nil, err
	}
	if normalized.Runtime != nil && normalized.Runtime.Network != nil {
		if err := validateNetworkSpec(normalized.Runtime.Network); err != nil {
			err.(*ValidationError).Path = "runtime." + err.(*ValidationError).Path
			return nil, err
		}
	}

	agentNames := make([]string, 0, len(spec.Agents))
	for name := range spec.Agents {
		agentNames = append(agentNames, name)
	}
	slices.Sort(agentNames)

	for _, agentName := range agentNames {
		if err := validateStableIdentifier(joinPath("agents", agentName), agentName, "agent name"); err != nil {
			return nil, err
		}
		agent := spec.Agents[agentName]
		normalizedAgent, err := normalizeAgent(agentName, agent, options)
		if err != nil {
			return nil, err
		}
		normalized.Agents = append(normalized.Agents, normalizedAgent)
	}

	serviceNames := make([]string, 0, len(spec.Services))
	for name := range spec.Services {
		serviceNames = append(serviceNames, name)
	}
	slices.Sort(serviceNames)
	for _, serviceName := range serviceNames {
		if err := validateStableIdentifier(joinPath("services", serviceName), serviceName, "service name"); err != nil {
			return nil, err
		}
		service, err := normalizeServiceSpec(serviceName, spec.Services[serviceName], options)
		if err != nil {
			return nil, err
		}
		normalized.Services = append(normalized.Services, service)
	}

	triggerNames := make([]string, 0, len(spec.Triggers))
	for name := range spec.Triggers {
		triggerNames = append(triggerNames, name)
	}
	slices.Sort(triggerNames)
	for _, triggerName := range triggerNames {
		if err := validateStableIdentifier(joinPath("triggers", triggerName), triggerName, "trigger name"); err != nil {
			return nil, err
		}
		trigger, err := normalizeProjectTriggerSpec(triggerName, spec.Triggers[triggerName])
		if err != nil {
			return nil, err
		}
		normalized.Triggers = append(normalized.Triggers, trigger)
	}
	normalized.Permissions = normalizePermissionSpec(spec.Permissions)
	normalized.Artifacts = cloneArtifactPolicySpec(spec.Artifacts)

	return normalized, nil
}

func normalizeProjectRuntimeSpec(path string, runtime *ProjectRuntimeSpec, options NormalizeOptions) (*NormalizedProjectRuntimeSpec, error) {
	if runtime == nil {
		return nil, nil
	}
	env, err := normalizeEnvVarMap(path+".env", runtime.Env, options)
	if err != nil {
		return nil, err
	}
	return &NormalizedProjectRuntimeSpec{
		Driver:        strings.TrimSpace(runtime.Driver),
		Image:         strings.TrimSpace(runtime.Image),
		Env:           env,
		Resources:     normalizeStringMap(runtime.Resources),
		Network:       normalizeNetworkDefault(runtime.Network),
		CleanupPolicy: strings.TrimSpace(runtime.CleanupPolicy),
	}, nil
}

func NormalizeFile(path string) (*NormalizedProjectSpec, error) {
	spec, err := ParseFile(path)
	if err != nil {
		return nil, err
	}
	normalized, err := Normalize(spec, NormalizeOptions{ComposePath: path})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return normalized, nil
}

func normalizeAgent(name string, agent AgentSpec, options NormalizeOptions) (NormalizedAgentSpec, error) {
	driver, err := normalizeDriverSpec(joinPath("agents", name)+".driver", agent.Driver)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	scheduler, err := normalizeSchedulerSpec(joinPath("agents", name)+".scheduler", agent.Scheduler)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	env, err := normalizeEnvVarMap(joinPath("agents", name)+".env", agent.Env, options)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	model, err := interpolateEnvValue(joinPath("agents", name)+".model", strings.TrimSpace(agent.Model), options)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	return NormalizedAgentSpec{
		Name:         name,
		Provider:     strings.TrimSpace(agent.Provider),
		Model:        model,
		SystemPrompt: agent.SystemPrompt,
		Image:        strings.TrimSpace(agent.Image),
		Driver:       driver,
		Env:          env,
		CapsetIDs:    normalizeStringList(agent.CapsetIDs),
		Metadata:     normalizeStringMap(agent.Metadata),
		Workspace:    cloneWorkspaceSpec(agent.Workspace),
		Scheduler:    scheduler,
	}, nil
}

func normalizeServiceSpec(name string, service ServiceSpec, options NormalizeOptions) (NormalizedServiceSpec, error) {
	path := joinPath("services", name)
	entry := strings.TrimSpace(service.Entry)
	if entry == "" {
		return NormalizedServiceSpec{}, &ValidationError{Path: path + ".entry", Message: "service entry is required"}
	}
	entry, err := normalizeRelativeFileReference(path+".entry", entry, "service entry")
	if err != nil {
		return NormalizedServiceSpec{}, err
	}
	if service.Timeout != "" {
		if err := validatePositiveDuration(path+".timeout", strings.TrimSpace(service.Timeout)); err != nil {
			return NormalizedServiceSpec{}, err
		}
	}
	if service.Retry != nil {
		if service.Retry.MaxAttempts < 0 {
			return NormalizedServiceSpec{}, &ValidationError{Path: path + ".retry.max_attempts", Message: "max_attempts must be non-negative"}
		}
		if strings.TrimSpace(service.Retry.Backoff) != "" {
			if err := validatePositiveDuration(path+".retry.backoff", strings.TrimSpace(service.Retry.Backoff)); err != nil {
				return NormalizedServiceSpec{}, err
			}
		}
	}
	env, err := normalizeEnvVarMap(path+".env", service.Env, options)
	if err != nil {
		return NormalizedServiceSpec{}, err
	}
	inputSchema, err := normalizeSchemaValue(path+".input_schema", service.InputSchema)
	if err != nil {
		return NormalizedServiceSpec{}, err
	}
	outputSchema, err := normalizeSchemaValue(path+".output_schema", service.OutputSchema)
	if err != nil {
		return NormalizedServiceSpec{}, err
	}
	errorSchema, err := normalizeSchemaValue(path+".error_schema", service.ErrorSchema)
	if err != nil {
		return NormalizedServiceSpec{}, err
	}
	return NormalizedServiceSpec{
		Name:         name,
		Description:  strings.TrimSpace(service.Description),
		Runtime:      strings.TrimSpace(service.Runtime),
		Entry:        entry,
		InputSchema:  inputSchema,
		OutputSchema: outputSchema,
		ErrorSchema:  errorSchema,
		Timeout:      strings.TrimSpace(service.Timeout),
		Retry:        cloneRetryPolicySpec(service.Retry),
		Permissions:  normalizePermissionSpec(service.Permissions),
		Env:          env,
		Agents:       normalizeStringList(service.Agents),
		Examples:     cloneServiceExamples(service.Examples),
	}, nil
}

func normalizeProjectTriggerSpec(name string, trigger ProjectTriggerSpec) (NormalizedProjectTriggerSpec, error) {
	path := joinPath("triggers", name)
	kind := strings.TrimSpace(trigger.Type)
	if kind == "" {
		kind = inferProjectTriggerKind(trigger)
	}
	if kind == "" {
		return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path, Message: "trigger type is required"}
	}
	target := normalizeTriggerTarget(trigger.Target)
	if target.Service == "" && target.Agent == "" {
		return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path + ".target", Message: "trigger target requires service or agent"}
	}
	if target.Service != "" && target.Agent != "" {
		return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path + ".target", Message: "trigger target requires exactly one of service or agent"}
	}
	normalized := NormalizedProjectTriggerSpec{Name: name, Kind: kind, Target: target, Input: strings.TrimSpace(trigger.Input)}
	switch kind {
	case "cron":
		normalized.Cron = strings.TrimSpace(trigger.Cron)
		if normalized.Cron == "" {
			return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path + ".cron", Message: "cron expression is required"}
		}
		if _, err := composeCronParser.Parse(normalized.Cron); err != nil {
			return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path + ".cron", Message: fmt.Sprintf("invalid cron expression: %v", err)}
		}
	case "interval":
		normalized.Interval = strings.TrimSpace(trigger.Interval)
		if err := validatePositiveDuration(path+".interval", normalized.Interval); err != nil {
			return NormalizedProjectTriggerSpec{}, err
		}
	case "timeout":
		normalized.Timeout = strings.TrimSpace(trigger.Timeout)
		if err := validatePositiveDuration(path+".timeout", normalized.Timeout); err != nil {
			return NormalizedProjectTriggerSpec{}, err
		}
	case "event":
		if trigger.Event == nil || strings.TrimSpace(trigger.Event.Topic) == "" {
			return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path + ".event.topic", Message: "event trigger topic is required"}
		}
		normalized.Event = &EventTriggerSpec{Topic: strings.TrimSpace(trigger.Event.Topic)}
	case "webhook":
		if trigger.Webhook == nil || strings.TrimSpace(trigger.Webhook.Topic) == "" {
			return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path + ".webhook.topic", Message: "webhook trigger topic is required"}
		}
		normalized.Webhook = &WebhookTriggerSpec{Topic: strings.TrimSpace(trigger.Webhook.Topic), Method: strings.TrimSpace(trigger.Webhook.Method)}
	default:
		return NormalizedProjectTriggerSpec{}, &ValidationError{Path: path + ".type", Message: fmt.Sprintf("unsupported trigger type %q", kind)}
	}
	return normalized, nil
}

func inferProjectTriggerKind(trigger ProjectTriggerSpec) string {
	set := make([]string, 0, 5)
	if strings.TrimSpace(trigger.Cron) != "" {
		set = append(set, "cron")
	}
	if strings.TrimSpace(trigger.Interval) != "" {
		set = append(set, "interval")
	}
	if strings.TrimSpace(trigger.Timeout) != "" {
		set = append(set, "timeout")
	}
	if trigger.Event != nil {
		set = append(set, "event")
	}
	if trigger.Webhook != nil {
		set = append(set, "webhook")
	}
	if len(set) == 1 {
		return set[0]
	}
	return ""
}

func normalizeTriggerTarget(target *TriggerTargetSpec) TriggerTargetSpec {
	if target == nil {
		return TriggerTargetSpec{}
	}
	return TriggerTargetSpec{Service: strings.TrimSpace(target.Service), Agent: strings.TrimSpace(target.Agent)}
}

func normalizeStringList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	normalized := make(map[string]string, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" {
			continue
		}
		normalized[key] = value
	}
	if len(normalized) == 0 {
		return nil
	}
	return normalized
}

func normalizeDriverSpec(path string, driver *DriverSpec) (*NormalizedDriverSpec, error) {
	if driver == nil {
		return &NormalizedDriverSpec{Name: DriverDocker, Docker: &DockerDriverSpec{}}, nil
	}

	enabled := make([]string, 0, 4)
	if driver.Boxlite != nil {
		enabled = append(enabled, DriverBoxlite)
	}
	if driver.Docker != nil {
		enabled = append(enabled, DriverDocker)
	}
	if driver.Microsandbox != nil {
		enabled = append(enabled, DriverMicrosandbox)
	}
	if driver.Firecracker != nil {
		enabled = append(enabled, DriverFirecracker)
	}
	if len(enabled) == 0 {
		return nil, &ValidationError{Path: path, Message: "driver requires exactly one runtime"}
	}
	if len(enabled) > 1 {
		return nil, &ValidationError{Path: path, Message: fmt.Sprintf("driver requires exactly one runtime, got %s", strings.Join(enabled, ", "))}
	}
	if enabled[0] == DriverFirecracker {
		return nil, &ValidationError{Path: path + ".firecracker", Message: "unsupported runtime driver firecracker"}
	}

	normalized := &NormalizedDriverSpec{Name: enabled[0]}
	switch enabled[0] {
	case DriverBoxlite:
		normalized.Boxlite = cloneBoxliteDriverSpec(driver.Boxlite)
	case DriverDocker:
		normalized.Docker = cloneDockerDriverSpec(driver.Docker)
	case DriverMicrosandbox:
		normalized.Microsandbox = cloneMicrosandboxDriverSpec(driver.Microsandbox)
	}
	return normalized, nil
}

func normalizeSchedulerSpec(path string, scheduler *SchedulerSpec) (*NormalizedSchedulerSpec, error) {
	if scheduler == nil {
		return nil, nil
	}

	enabled := true
	if scheduler.Enabled != nil {
		enabled = *scheduler.Enabled
	}
	script := strings.TrimSpace(scheduler.Script)
	if script != "" && len(scheduler.Triggers) > 0 {
		return nil, &ValidationError{Path: path, Message: "scheduler script and triggers are mutually exclusive"}
	}
	normalized := &NormalizedSchedulerSpec{Enabled: enabled, Script: script}
	for i, trigger := range scheduler.Triggers {
		normalizedTrigger, err := normalizeTriggerSpec(fmt.Sprintf("%s.triggers[%d]", path, i), trigger)
		if err != nil {
			return nil, err
		}
		normalized.Triggers = append(normalized.Triggers, normalizedTrigger)
	}
	return normalized, nil
}

func normalizeTriggerSpec(path string, trigger TriggerSpec) (NormalizedTriggerSpec, error) {
	kinds := make([]string, 0, 4)
	if trigger.cronSet {
		kinds = append(kinds, "cron")
	}
	if trigger.intervalSet {
		kinds = append(kinds, "interval")
	}
	if trigger.timeoutSet {
		kinds = append(kinds, "timeout")
	}
	if trigger.eventSet {
		kinds = append(kinds, "event")
	}
	if len(kinds) == 0 {
		return NormalizedTriggerSpec{}, &ValidationError{Path: path, Message: "trigger requires exactly one kind"}
	}
	if len(kinds) > 1 {
		return NormalizedTriggerSpec{}, &ValidationError{Path: path, Message: fmt.Sprintf("trigger requires exactly one kind, got %s", strings.Join(kinds, ", "))}
	}

	normalized := NormalizedTriggerSpec{
		Name:   strings.TrimSpace(trigger.Name),
		Kind:   kinds[0],
		Prompt: trigger.Prompt,
	}
	switch kinds[0] {
	case "cron":
		normalized.Cron = strings.TrimSpace(trigger.Cron)
		if normalized.Cron == "" {
			return NormalizedTriggerSpec{}, &ValidationError{Path: path + ".cron", Message: "cron expression is required"}
		}
		if _, err := composeCronParser.Parse(normalized.Cron); err != nil {
			return NormalizedTriggerSpec{}, &ValidationError{Path: path + ".cron", Message: fmt.Sprintf("invalid cron expression: %v", err)}
		}
	case "interval":
		normalized.Interval = strings.TrimSpace(trigger.Interval)
		if err := validatePositiveDuration(path+".interval", normalized.Interval); err != nil {
			return NormalizedTriggerSpec{}, err
		}
	case "timeout":
		normalized.Timeout = strings.TrimSpace(trigger.Timeout)
		if err := validatePositiveDuration(path+".timeout", normalized.Timeout); err != nil {
			return NormalizedTriggerSpec{}, err
		}
	case "event":
		if trigger.Event == nil {
			return NormalizedTriggerSpec{}, &ValidationError{Path: path + ".event.topic", Message: "event trigger topic is required"}
		}
		topic := strings.TrimSpace(trigger.Event.Topic)
		if topic == "" {
			return NormalizedTriggerSpec{}, &ValidationError{Path: path + ".event.topic", Message: "event trigger topic is required"}
		}
		normalized.Event = &EventTriggerSpec{Topic: topic}
	}

	return normalized, nil
}

func validatePositiveDuration(path string, value string) error {
	duration, err := time.ParseDuration(value)
	if err != nil {
		return &ValidationError{Path: path, Message: fmt.Sprintf("invalid duration: %v", err)}
	}
	if duration <= 0 {
		return &ValidationError{Path: path, Message: "duration must be positive"}
	}
	return nil
}

func validateNetworkSpec(network *NetworkSpec) error {
	if network == nil {
		return nil
	}
	mode := strings.TrimSpace(network.Mode)
	network.Mode = mode
	if mode == "" || mode == "default" {
		network.Mode = "default"
		return nil
	}
	return &ValidationError{Path: "network.mode", Message: fmt.Sprintf("unsupported network mode %q; only default is supported", mode)}
}

func validateStableIdentifier(path string, value string, label string) error {
	if value == "" {
		return &ValidationError{Path: path, Message: label + " is required"}
	}
	if !stableIdentifierPattern.MatchString(value) {
		return &ValidationError{Path: path, Message: label + " must match " + stableIdentifierPattern.String()}
	}
	return nil
}

func defaultProjectName(options NormalizeOptions) string {
	dir := strings.TrimSpace(options.ProjectDir)
	if dir == "" && strings.TrimSpace(options.ComposePath) != "" {
		composePath := strings.TrimSpace(options.ComposePath)
		if abs, err := filepath.Abs(composePath); err == nil {
			composePath = abs
		}
		dir = filepath.Dir(composePath)
	}
	if dir == "" {
		return ""
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	return filepath.Base(filepath.Clean(dir))
}

func normalizeNetworkDefault(value *NetworkSpec) *NetworkSpec {
	if value == nil {
		return &NetworkSpec{Mode: "default"}
	}
	cloned := *value
	return &cloned
}

func normalizeEnvVarMap(path string, values map[string]EnvVarSpec, options NormalizeOptions) (map[string]EnvVarSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make(map[string]EnvVarSpec, len(values))
	for key, value := range values {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, &ValidationError{Path: path, Message: "environment variable name is required"}
		}
		if !envNamePattern.MatchString(key) {
			return nil, &ValidationError{Path: joinPath(path, key), Message: "environment variable name must match " + envNamePattern.String()}
		}
		interpolated, err := interpolateEnvValue(joinPath(path, key)+".value", value.Value, options)
		if err != nil {
			return nil, err
		}
		value.Value = interpolated
		normalized[key] = value
	}
	return normalized, nil
}

func normalizeSchemaValue(path string, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[") {
		if !json.Valid([]byte(value)) {
			return "", &ValidationError{Path: path, Message: "schema must be valid JSON when inline"}
		}
		return value, nil
	}
	return normalizeRelativeFileReference(path, value, "schema reference")
}

func normalizeRelativeFileReference(path string, value string, label string) (string, error) {
	if filepath.IsAbs(value) {
		return "", &ValidationError{Path: path, Message: label + " must be a relative path"}
	}
	cleaned := filepath.Clean(value)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", &ValidationError{Path: path, Message: label + " must stay within the project directory"}
	}
	return filepath.ToSlash(cleaned), nil
}

func interpolateEnvValue(path string, value string, options NormalizeOptions) (string, error) {
	matches := envReferencePattern.FindAllStringSubmatchIndex(value, -1)
	if len(matches) == 0 {
		return value, nil
	}
	var b strings.Builder
	b.Grow(len(value))
	last := 0
	for _, match := range matches {
		b.WriteString(value[last:match[0]])
		name := value[match[2]:match[3]]
		envValue, ok := lookupInterpolationEnv(name, options)
		if !ok {
			return "", &ValidationError{Path: path, Message: fmt.Sprintf("environment variable %s is required", name)}
		}
		b.WriteString(envValue)
		last = match[1]
	}
	b.WriteString(value[last:])
	return b.String(), nil
}

func lookupInterpolationEnv(name string, options NormalizeOptions) (string, bool) {
	if options.Env != nil {
		value, ok := options.Env[name]
		return value, ok
	}
	return os.LookupEnv(name)
}

func cloneWorkspaceSpec(value *WorkspaceSpec) *WorkspaceSpec {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Provider = strings.TrimSpace(cloned.Provider)
	cloned.URL = strings.TrimSpace(cloned.URL)
	cloned.Branch = strings.TrimSpace(cloned.Branch)
	cloned.Path = strings.TrimSpace(cloned.Path)
	return &cloned
}

func cloneProjectMetadataSpec(value *ProjectMetadataSpec) *ProjectMetadataSpec {
	if value == nil {
		return nil
	}
	return &ProjectMetadataSpec{
		Name:        strings.TrimSpace(value.Name),
		Labels:      normalizeStringMap(value.Labels),
		Annotations: normalizeStringMap(value.Annotations),
	}
}

func normalizePermissionSpec(value *PermissionSpec) *PermissionSpec {
	if value == nil {
		return nil
	}
	normalized := &PermissionSpec{
		Agents:       normalizeStringList(value.Agents),
		Capabilities: normalizeStringList(value.Capabilities),
		Resources:    normalizeStringList(value.Resources),
	}
	if len(normalized.Agents) == 0 && len(normalized.Capabilities) == 0 && len(normalized.Resources) == 0 {
		return nil
	}
	return normalized
}

func cloneArtifactPolicySpec(value *ArtifactPolicySpec) *ArtifactPolicySpec {
	if value == nil {
		return nil
	}
	return &ArtifactPolicySpec{Retention: strings.TrimSpace(value.Retention), Storage: strings.TrimSpace(value.Storage)}
}

func cloneRetryPolicySpec(value *RetryPolicySpec) *RetryPolicySpec {
	if value == nil {
		return nil
	}
	return &RetryPolicySpec{MaxAttempts: value.MaxAttempts, Backoff: strings.TrimSpace(value.Backoff)}
}

func cloneServiceExamples(values []ServiceExampleSpec) []ServiceExampleSpec {
	if len(values) == 0 {
		return nil
	}
	out := make([]ServiceExampleSpec, 0, len(values))
	for _, value := range values {
		item := ServiceExampleSpec{
			Name:   strings.TrimSpace(value.Name),
			Input:  strings.TrimSpace(value.Input),
			Output: strings.TrimSpace(value.Output),
		}
		if item.Name == "" && item.Input == "" && item.Output == "" {
			continue
		}
		out = append(out, item)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func cloneBoxliteDriverSpec(value *BoxliteDriverSpec) *BoxliteDriverSpec {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Kernel = strings.TrimSpace(cloned.Kernel)
	cloned.Rootfs = strings.TrimSpace(cloned.Rootfs)
	return &cloned
}

func cloneDockerDriverSpec(value *DockerDriverSpec) *DockerDriverSpec {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Host = strings.TrimSpace(cloned.Host)
	return &cloned
}

func cloneMicrosandboxDriverSpec(value *MicrosandboxDriverSpec) *MicrosandboxDriverSpec {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Profile = strings.TrimSpace(cloned.Profile)
	return &cloned
}
