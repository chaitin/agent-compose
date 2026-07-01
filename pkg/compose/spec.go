package compose

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type ProjectSpec struct {
	APIVersion  string                        `yaml:"apiVersion,omitempty" json:"apiVersion,omitempty"`
	Kind        string                        `yaml:"kind,omitempty" json:"kind,omitempty"`
	Name        string                        `yaml:"name,omitempty" json:"name,omitempty"`
	Metadata    *ProjectMetadataSpec          `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Variables   map[string]EnvVarSpec         `yaml:"variables,omitempty" json:"variables,omitempty"`
	Runtime     *ProjectRuntimeSpec           `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Workspace   *WorkspaceSpec                `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Agents      map[string]AgentSpec          `yaml:"agents,omitempty" json:"agents,omitempty"`
	Services    map[string]ServiceSpec        `yaml:"services,omitempty" json:"services,omitempty"`
	Triggers    map[string]ProjectTriggerSpec `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	Permissions *PermissionSpec               `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Artifacts   *ArtifactPolicySpec           `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	Network     *NetworkSpec                  `yaml:"network,omitempty" json:"network,omitempty"`
}

type ProjectMetadataSpec struct {
	Name        string            `yaml:"name,omitempty" json:"name,omitempty"`
	Labels      map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Annotations map[string]string `yaml:"annotations,omitempty" json:"annotations,omitempty"`
}

type ProjectRuntimeSpec struct {
	Driver        string                `yaml:"driver,omitempty" json:"driver,omitempty"`
	Image         string                `yaml:"image,omitempty" json:"image,omitempty"`
	Env           map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	Resources     map[string]string     `yaml:"resources,omitempty" json:"resources,omitempty"`
	Network       *NetworkSpec          `yaml:"network,omitempty" json:"network,omitempty"`
	CleanupPolicy string                `yaml:"cleanup_policy,omitempty" json:"cleanup_policy,omitempty"`
}

type AgentSpec struct {
	Provider     string                `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                `yaml:"image,omitempty" json:"image,omitempty"`
	Driver       *DriverSpec           `yaml:"driver,omitempty" json:"driver,omitempty"`
	Env          map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	CapsetIDs    []string              `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Metadata     map[string]string     `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Workspace    *WorkspaceSpec        `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *SchedulerSpec        `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
}

type ServiceSpec struct {
	Description  string                `yaml:"description,omitempty" json:"description,omitempty"`
	Runtime      string                `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Entry        string                `yaml:"entry,omitempty" json:"entry,omitempty"`
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

type RetryPolicySpec struct {
	MaxAttempts int    `yaml:"max_attempts,omitempty" json:"max_attempts,omitempty"`
	Backoff     string `yaml:"backoff,omitempty" json:"backoff,omitempty"`
}

type ServiceExampleSpec struct {
	Name   string `yaml:"name,omitempty" json:"name,omitempty"`
	Input  string `yaml:"input,omitempty" json:"input,omitempty"`
	Output string `yaml:"output,omitempty" json:"output,omitempty"`
}

type ProjectTriggerSpec struct {
	Type     string              `yaml:"type,omitempty" json:"type,omitempty"`
	Cron     string              `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval string              `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout  string              `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Event    *EventTriggerSpec   `yaml:"event,omitempty" json:"event,omitempty"`
	Webhook  *WebhookTriggerSpec `yaml:"webhook,omitempty" json:"webhook,omitempty"`
	Target   *TriggerTargetSpec  `yaml:"target,omitempty" json:"target,omitempty"`
	Input    string              `yaml:"input,omitempty" json:"input,omitempty"`
}

type WebhookTriggerSpec struct {
	Topic  string `yaml:"topic,omitempty" json:"topic,omitempty"`
	Method string `yaml:"method,omitempty" json:"method,omitempty"`
}

type TriggerTargetSpec struct {
	Service string `yaml:"service,omitempty" json:"service,omitempty"`
	Agent   string `yaml:"agent,omitempty" json:"agent,omitempty"`
}

type PermissionSpec struct {
	Agents       []string `yaml:"agents,omitempty" json:"agents,omitempty"`
	Capabilities []string `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
	Resources    []string `yaml:"resources,omitempty" json:"resources,omitempty"`
}

type ArtifactPolicySpec struct {
	Retention string `yaml:"retention,omitempty" json:"retention,omitempty"`
	Storage   string `yaml:"storage,omitempty" json:"storage,omitempty"`
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
		"apiVersion":  validateScalar,
		"kind":        validateScalar,
		"name":        validateScalar,
		"metadata":    validateProjectMetadata,
		"variables":   validateEnvVarMap,
		"runtime":     validateProjectRuntime,
		"workspace":   validateWorkspace,
		"agents":      validateAgentMap,
		"services":    validateServiceMap,
		"triggers":    validateProjectTriggerMap,
		"permissions": validatePermissions,
		"artifacts":   validateArtifactPolicy,
		"network":     validateNetwork,
	})
}

func validateProjectMetadata(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"name":        validateScalar,
		"labels":      validateStringMap,
		"annotations": validateStringMap,
	})
}

func validateProjectRuntime(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"driver":         validateScalar,
		"image":          validateScalar,
		"env":            validateEnvVarMap,
		"resources":      validateStringMap,
		"network":        validateNetwork,
		"cleanup_policy": validateScalar,
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
		"driver":        validateDriver,
		"env":           validateEnvVarMap,
		"capset_ids":    validateStringList,
		"metadata":      validateStringMap,
		"workspace":     validateWorkspace,
		"scheduler":     validateScheduler,
	})
}

func validateServiceMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateService)
}

func validateService(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"description":   validateScalar,
		"runtime":       validateScalar,
		"entry":         validateScalar,
		"input_schema":  validateScalar,
		"output_schema": validateScalar,
		"error_schema":  validateScalar,
		"timeout":       validateScalar,
		"retry":         validateRetryPolicy,
		"permissions":   validatePermissions,
		"env":           validateEnvVarMap,
		"agents":        validateStringList,
		"examples":      validateServiceExampleList,
	})
}

func validateRetryPolicy(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"max_attempts": validateInt,
		"backoff":      validateScalar,
	})
}

func validateServiceExampleList(node *yaml.Node, path string) error {
	if err := requireKind(node, path, yaml.SequenceNode, "sequence"); err != nil {
		return err
	}
	for i, item := range node.Content {
		if err := validateMapping(item, fmt.Sprintf("%s[%d]", path, i), map[string]nodeValidator{
			"name":   validateScalar,
			"input":  validateScalar,
			"output": validateScalar,
		}); err != nil {
			return err
		}
	}
	return nil
}

func validateProjectTriggerMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateProjectTrigger)
}

func validateProjectTrigger(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"type":     validateScalar,
		"cron":     validateScalar,
		"interval": validateScalar,
		"timeout":  validateScalar,
		"event":    validateEventTrigger,
		"webhook":  validateWebhookTrigger,
		"target":   validateTriggerTarget,
		"input":    validateScalar,
	})
}

func validateWebhookTrigger(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"topic":  validateScalar,
		"method": validateScalar,
	})
}

func validateTriggerTarget(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"service": validateScalar,
		"agent":   validateScalar,
	})
}

func validatePermissions(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"agents":       validateStringList,
		"capabilities": validateStringList,
		"resources":    validateStringList,
	})
}

func validateArtifactPolicy(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"retention": validateScalar,
		"storage":   validateScalar,
	})
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

func validateStringMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateScalar)
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
