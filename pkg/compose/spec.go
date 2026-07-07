package compose

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type ProjectSpec struct {
	Name      string                `yaml:"name,omitempty" json:"name,omitempty"`
	Variables map[string]EnvVarSpec `yaml:"variables,omitempty" json:"variables,omitempty"`
	Workspace *WorkspaceSpec        `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Volumes   map[string]VolumeSpec `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Agents    map[string]AgentSpec  `yaml:"agents,omitempty" json:"agents,omitempty"`
	Network   *NetworkSpec          `yaml:"network,omitempty" json:"network,omitempty"`
}

type AgentSpec struct {
	Provider     string                `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                `yaml:"image,omitempty" json:"image,omitempty"`
	Build        *BuildSpec            `yaml:"build,omitempty" json:"build,omitempty"`
	Driver       *DriverSpec           `yaml:"driver,omitempty" json:"driver,omitempty"`
	Env          map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	CapsetIDs    []string              `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Volumes      []VolumeMountSpec     `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Workspace    *WorkspaceSpec        `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *SchedulerSpec        `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
	Jupyter      *JupyterSpec          `yaml:"jupyter,omitempty" json:"jupyter,omitempty"`
}

type BuildSpec struct {
	Context    string            `yaml:"context,omitempty" json:"context,omitempty"`
	Dockerfile string            `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	Target     string            `yaml:"target,omitempty" json:"target,omitempty"`
	Args       map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
	Platforms  []string          `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Tags       []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	NoCache    bool              `yaml:"no_cache,omitempty" json:"no_cache,omitempty"`
	Pull       bool              `yaml:"pull,omitempty" json:"pull,omitempty"`
}

type JupyterSpec struct {
	Enabled   bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	GuestPort int  `yaml:"guest_port,omitempty" json:"guest_port,omitempty"`
}

type VolumeSpec struct {
	Name     string            `yaml:"name,omitempty" json:"name,omitempty"`
	Driver   string            `yaml:"driver,omitempty" json:"driver,omitempty"`
	External bool              `yaml:"external,omitempty" json:"external,omitempty"`
	Labels   map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Options  map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

type VolumeMountSpec struct {
	Type     string `yaml:"type,omitempty" json:"type,omitempty"`
	Source   string `yaml:"source,omitempty" json:"source,omitempty"`
	Target   string `yaml:"target,omitempty" json:"target,omitempty"`
	ReadOnly bool   `yaml:"read_only,omitempty" json:"read_only,omitempty"`
}

type SchedulerSpec struct {
	Enabled  *bool         `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Triggers []TriggerSpec `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	Script   string        `yaml:"script,omitempty" json:"script,omitempty"`
}

type TriggerSpec struct {
	Name     string            `yaml:"name,omitempty" json:"name,omitempty"`
	Cron     string            `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval string            `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Event    *EventTriggerSpec `yaml:"event,omitempty" json:"event,omitempty"`
	Prompt   string            `yaml:"prompt,omitempty" json:"prompt,omitempty"`

	cronSet     bool
	intervalSet bool
	timeoutSet  bool
	eventSet    bool
}

type EventTriggerSpec struct {
	Topic string `yaml:"topic,omitempty" json:"topic,omitempty"`
}

type WorkspaceSpec struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	URL      string `yaml:"url,omitempty" json:"url,omitempty"`
	Branch   string `yaml:"branch,omitempty" json:"branch,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
}

type NetworkSpec struct {
	Mode string `yaml:"mode,omitempty" json:"mode,omitempty"`
}

type DriverSpec struct {
	Boxlite      *BoxliteDriverSpec      `yaml:"boxlite,omitempty" json:"boxlite,omitempty"`
	Docker       *DockerDriverSpec       `yaml:"docker,omitempty" json:"docker,omitempty"`
	Microsandbox *MicrosandboxDriverSpec `yaml:"microsandbox,omitempty" json:"microsandbox,omitempty"`
	Firecracker  *FirecrackerDriverSpec  `yaml:"firecracker,omitempty" json:"firecracker,omitempty"`
}

type BoxliteDriverSpec struct {
	Kernel string `yaml:"kernel,omitempty" json:"kernel,omitempty"`
	Rootfs string `yaml:"rootfs,omitempty" json:"rootfs,omitempty"`
}

type DockerDriverSpec struct {
	Host string `yaml:"host,omitempty" json:"host,omitempty"`
}

type MicrosandboxDriverSpec struct {
	Profile string `yaml:"profile,omitempty" json:"profile,omitempty"`
}

type FirecrackerDriverSpec struct {
	Kernel string `yaml:"kernel,omitempty" json:"kernel,omitempty"`
	Rootfs string `yaml:"rootfs,omitempty" json:"rootfs,omitempty"`
}

type EnvVarSpec struct {
	Value  string `yaml:"value,omitempty" json:"value,omitempty"`
	Secret bool   `yaml:"secret,omitempty" json:"secret,omitempty"`
}

func (s *EnvVarSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var raw string
		if err := value.Decode(&raw); err != nil {
			return err
		}
		s.Value = raw
		s.Secret = false
		return nil
	case yaml.MappingNode:
		type envVarSpec EnvVarSpec
		var decoded envVarSpec
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		*s = EnvVarSpec(decoded)
		return nil
	default:
		return fmt.Errorf("expected scalar or mapping, got %s", nodeKindName(value.Kind))
	}
}

func (s *BuildSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var contextDir string
		if err := value.Decode(&contextDir); err != nil {
			return err
		}
		s.Context = contextDir
		return nil
	case yaml.MappingNode:
		type buildSpec BuildSpec
		var decoded buildSpec
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		*s = BuildSpec(decoded)
		return nil
	default:
		return fmt.Errorf("expected scalar or mapping, got %s", nodeKindName(value.Kind))
	}
}

func (s *VolumeMountSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var raw string
		if err := value.Decode(&raw); err != nil {
			return err
		}
		parsed, err := parseVolumeMountShortSyntax(raw)
		if err != nil {
			return err
		}
		*s = parsed
		return nil
	case yaml.MappingNode:
		type volumeMountSpec VolumeMountSpec
		var decoded volumeMountSpec
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		*s = VolumeMountSpec(decoded)
		return nil
	default:
		return fmt.Errorf("expected scalar or mapping, got %s", nodeKindName(value.Kind))
	}
}

func (s *TriggerSpec) UnmarshalYAML(value *yaml.Node) error {
	type triggerSpec TriggerSpec
	var decoded triggerSpec
	if err := value.Decode(&decoded); err != nil {
		return err
	}
	for i := 0; i < len(value.Content); i += 2 {
		switch value.Content[i].Value {
		case "cron":
			decoded.cronSet = true
		case "interval":
			decoded.intervalSet = true
		case "timeout":
			decoded.timeoutSet = true
		case "event":
			decoded.eventSet = true
		}
	}
	*s = TriggerSpec(decoded)
	return nil
}

type ParseError struct {
	Path    string
	Line    int
	Column  int
	Message string
}

func (e *ParseError) Error() string {
	var b strings.Builder
	b.WriteString("parse compose")
	if e.Path != "" {
		b.WriteString(" field ")
		b.WriteString(e.Path)
	}
	if e.Line > 0 {
		fmt.Fprintf(&b, " at line %d", e.Line)
		if e.Column > 0 {
			fmt.Fprintf(&b, ", column %d", e.Column)
		}
	}
	if e.Message != "" {
		b.WriteString(": ")
		b.WriteString(e.Message)
	}
	return b.String()
}

func Parse(data []byte) (*ProjectSpec, error) {
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return nil, &ParseError{Message: err.Error()}
	}
	if len(document.Content) == 0 {
		return nil, &ParseError{Message: "empty document"}
	}

	root := document.Content[0]
	if err := validateProjectNode(root); err != nil {
		return nil, err
	}

	var spec ProjectSpec
	if err := root.Decode(&spec); err != nil {
		return nil, &ParseError{Message: err.Error()}
	}
	return &spec, nil
}

func ParseFile(path string) (*ProjectSpec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	spec, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return spec, nil
}

type nodeValidator func(node *yaml.Node, path string) error

func validateProjectNode(node *yaml.Node) error {
	return validateMapping(node, "", map[string]nodeValidator{
		"name":      validateScalar,
		"variables": validateEnvVarMap,
		"workspace": validateWorkspace,
		"volumes":   validateVolumeMap,
		"agents":    validateAgentMap,
		"network":   validateNetwork,
	})
}

func validateAgentMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateAgent)
}

func validateAgent(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"provider":      validateScalar,
		"model":         validateScalar,
		"system_prompt": validateScalar,
		"image":         validateScalar,
		"build":         validateBuild,
		"driver":        validateDriver,
		"env":           validateEnvVarMap,
		"capset_ids":    validateStringList,
		"volumes":       validateVolumeMountList,
		"workspace":     validateWorkspace,
		"scheduler":     validateScheduler,
		"jupyter":       validateJupyter,
	})
}

func validateVolumeMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateVolume)
}

func validateVolume(node *yaml.Node, path string) error {
	if node.Kind == yaml.ScalarNode && strings.TrimSpace(node.Value) == "" {
		return nil
	}
	return validateMapping(node, path, map[string]nodeValidator{
		"name":     validateScalar,
		"driver":   validateScalar,
		"external": validateBool,
		"labels":   validateStringMap,
		"options":  validateStringMap,
	})
}

func validateVolumeMountList(node *yaml.Node, path string) error {
	if err := requireKind(node, path, yaml.SequenceNode, "sequence"); err != nil {
		return err
	}
	for i, item := range node.Content {
		if err := validateVolumeMount(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateVolumeMount(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return validateScalar(node, path)
	case yaml.MappingNode:
		return validateMapping(node, path, map[string]nodeValidator{
			"type":      validateScalar,
			"source":    validateScalar,
			"target":    validateScalar,
			"read_only": validateBool,
		})
	default:
		return newParseError(node, path, "expected scalar or mapping")
	}
}

func validateBuild(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return validateScalar(node, path)
	case yaml.MappingNode:
		return validateMapping(node, path, map[string]nodeValidator{
			"context":    validateScalar,
			"dockerfile": validateScalar,
			"target":     validateScalar,
			"args":       validateBuildArgMap,
			"platforms":  validateStringList,
			"tags":       validateStringList,
			"no_cache":   validateBool,
			"pull":       validateBool,
		})
	default:
		return newParseError(node, path, "expected scalar or mapping")
	}
}

func validateBuildArgMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateScalar)
}

func validateStringList(node *yaml.Node, path string) error {
	if err := requireKind(node, path, yaml.SequenceNode, "sequence"); err != nil {
		return err
	}
	for index, item := range node.Content {
		if err := validateScalar(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
			return err
		}
	}
	return nil
}

func validateScheduler(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"enabled":  validateBool,
		"triggers": validateTriggerList,
		"script":   validateScalar,
	})
}

func validateJupyter(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"enabled":    validateBool,
		"guest_port": validateInt,
	})
}

func validateTriggerList(node *yaml.Node, path string) error {
	if err := requireKind(node, path, yaml.SequenceNode, "sequence"); err != nil {
		return err
	}
	for i, item := range node.Content {
		if err := validateTrigger(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateTrigger(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"name":     validateScalar,
		"cron":     validateScalar,
		"interval": validateScalar,
		"timeout":  validateScalar,
		"event":    validateEventTrigger,
		"prompt":   validateScalar,
	})
}

func validateEventTrigger(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"topic": validateScalar,
	})
}

func validateWorkspace(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"provider": validateScalar,
		"url":      validateScalar,
		"branch":   validateScalar,
		"path":     validateScalar,
	})
}

func validateDriver(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"boxlite":      validateBoxliteDriver,
		"docker":       validateDockerDriver,
		"microsandbox": validateMicrosandboxDriver,
		"firecracker":  validateFirecrackerDriver,
	})
}

func validateBoxliteDriver(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"kernel": validateScalar,
		"rootfs": validateScalar,
	})
}

func validateDockerDriver(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"host": validateScalar,
	})
}

func validateMicrosandboxDriver(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"profile": validateScalar,
	})
}

func validateFirecrackerDriver(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"kernel": validateScalar,
		"rootfs": validateScalar,
	})
}

func validateEnvVarMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateEnvVar)
}

func validateStringMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, func(value *yaml.Node, valuePath string) error {
		return validateScalar(value, valuePath)
	})
}

func validateEnvVar(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return nil
	case yaml.MappingNode:
		return validateMapping(node, path, map[string]nodeValidator{
			"value":  validateScalar,
			"secret": validateBool,
		})
	default:
		return newParseError(node, path, "expected scalar or mapping")
	}
}

func validateNetwork(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"mode": validateScalar,
	})
}

func validateNamedMap(node *yaml.Node, path string, validateValue nodeValidator) error {
	if err := requireKind(node, path, yaml.MappingNode, "mapping"); err != nil {
		return err
	}
	seen := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if err := validateScalar(key, path); err != nil {
			return err
		}
		if _, ok := seen[key.Value]; ok {
			return newParseError(key, joinPath(path, key.Value), "duplicate field")
		}
		seen[key.Value] = key
		if err := validateValue(value, joinPath(path, key.Value)); err != nil {
			return err
		}
	}
	return nil
}

func validateMapping(node *yaml.Node, path string, fields map[string]nodeValidator) error {
	if err := requireKind(node, path, yaml.MappingNode, "mapping"); err != nil {
		return err
	}
	seen := map[string]*yaml.Node{}
	for i := 0; i < len(node.Content); i += 2 {
		key := node.Content[i]
		value := node.Content[i+1]
		if err := validateScalar(key, path); err != nil {
			return err
		}
		fieldPath := joinPath(path, key.Value)
		if _, ok := seen[key.Value]; ok {
			return newParseError(key, fieldPath, "duplicate field")
		}
		seen[key.Value] = key
		validator, ok := fields[key.Value]
		if !ok {
			return newParseError(key, fieldPath, "unknown field")
		}
		if err := validator(value, fieldPath); err != nil {
			return err
		}
	}
	return nil
}

func validateScalar(node *yaml.Node, path string) error {
	return requireKind(node, path, yaml.ScalarNode, "scalar")
}

func validateBool(node *yaml.Node, path string) error {
	if err := validateScalar(node, path); err != nil {
		return err
	}
	var value bool
	if err := node.Decode(&value); err != nil {
		return newParseError(node, path, "expected bool")
	}
	return nil
}

func validateInt(node *yaml.Node, path string) error {
	if err := validateScalar(node, path); err != nil {
		return err
	}
	var value int
	if err := node.Decode(&value); err != nil {
		return newParseError(node, path, "expected int")
	}
	return nil
}

func requireKind(node *yaml.Node, path string, want yaml.Kind, wantName string) error {
	if node.Kind != want {
		return newParseError(node, path, fmt.Sprintf("expected %s, got %s", wantName, nodeKindName(node.Kind)))
	}
	return nil
}

func newParseError(node *yaml.Node, path string, message string) error {
	return &ParseError{
		Path:    path,
		Line:    node.Line,
		Column:  node.Column,
		Message: message,
	}
}

func joinPath(parent string, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

func nodeKindName(kind yaml.Kind) string {
	switch kind {
	case yaml.DocumentNode:
		return "document"
	case yaml.SequenceNode:
		return "sequence"
	case yaml.MappingNode:
		return "mapping"
	case yaml.ScalarNode:
		return "scalar"
	case yaml.AliasNode:
		return "alias"
	default:
		return fmt.Sprintf("kind(%d)", kind)
	}
}
