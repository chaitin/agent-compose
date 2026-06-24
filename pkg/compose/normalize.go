package compose

import (
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
var envReferencePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var composeCronParser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

type NormalizeOptions struct {
	ProjectDir  string
	ComposePath string
	Env         map[string]string
}

type NormalizedProjectSpec struct {
	Name      string                `yaml:"name" json:"name"`
	Variables map[string]EnvVarSpec `yaml:"variables,omitempty" json:"variables,omitempty"`
	Workspace *WorkspaceSpec        `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Agents    []NormalizedAgentSpec `yaml:"agents,omitempty" json:"agents,omitempty"`
	Network   *NetworkSpec          `yaml:"network,omitempty" json:"network,omitempty"`
}

type NormalizedAgentSpec struct {
	Name         string                   `yaml:"name" json:"name"`
	Provider     string                   `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                   `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                   `yaml:"image,omitempty" json:"image,omitempty"`
	Driver       *NormalizedDriverSpec    `yaml:"driver" json:"driver"`
	Env          map[string]EnvVarSpec    `yaml:"env,omitempty" json:"env,omitempty"`
	Workspace    *WorkspaceSpec           `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *NormalizedSchedulerSpec `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
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
		Name:      name,
		Variables: nil,
		Workspace: cloneWorkspaceSpec(spec.Workspace),
		Network:   normalizeNetworkDefault(spec.Network),
	}
	variables, err := normalizeEnvVarMap("variables", spec.Variables, options)
	if err != nil {
		return nil, err
	}
	normalized.Variables = variables
	if err := validateNetworkSpec(normalized.Network); err != nil {
		return nil, err
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

	return normalized, nil
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
		Workspace:    cloneWorkspaceSpec(agent.Workspace),
		Scheduler:    scheduler,
	}, nil
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
		interpolated, err := interpolateEnvValue(joinPath(path, key)+".value", value.Value, options)
		if err != nil {
			return nil, err
		}
		value.Value = interpolated
		normalized[key] = value
	}
	return normalized, nil
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
