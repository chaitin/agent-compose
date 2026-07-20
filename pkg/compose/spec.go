package compose

import (
	"fmt"
	"os"
	"strings"

	"agent-compose/pkg/sources"

	"gopkg.in/yaml.v3"
)

type ProjectSpec struct {
	Name       string                   `yaml:"name,omitempty" json:"name,omitempty"`
	EnvFiles   EnvFileSpec              `yaml:"env_file,omitempty" json:"env_file,omitempty"`
	Variables  map[string]EnvVarSpec    `yaml:"variables,omitempty" json:"variables,omitempty"`
	Workspaces map[string]WorkspaceSpec `yaml:"workspaces,omitempty" json:"workspaces,omitempty"`
	MCPServers map[string]MCPServerSpec `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`
	Volumes    map[string]VolumeSpec    `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Agents     map[string]AgentSpec     `yaml:"agents,omitempty" json:"agents,omitempty"`
}

// EnvFileSpec lists dotenv files used while loading a project configuration.
// The scalar form is shorthand for a single-item list.
type EnvFileSpec []string

func (s *EnvFileSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		var path string
		if err := value.Decode(&path); err != nil {
			return err
		}
		*s = EnvFileSpec{path}
		return nil
	}
	if value.Kind != yaml.SequenceNode {
		return fmt.Errorf("env_file must be a string or list of strings")
	}
	var paths []string
	if err := value.Decode(&paths); err != nil {
		return err
	}
	*s = paths
	return nil
}

type AgentSpec struct {
	Enabled      *bool                 `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	DisplayName  string                `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Description  string                `yaml:"description,omitempty" json:"description,omitempty"`
	Provider     string                `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                `yaml:"image,omitempty" json:"image,omitempty"`
	Build        *BuildSpec            `yaml:"build,omitempty" json:"build,omitempty"`
	Driver       *DriverSpec           `yaml:"driver,omitempty" json:"driver,omitempty"`
	Env          map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	MCPServers   AgentMCPEntriesSpec   `yaml:"mcp_servers,omitempty" json:"mcp_servers,omitempty"`
	CapsetIDs    []string              `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Skills       []SkillSpec           `yaml:"skills,omitempty" json:"skills,omitempty"`
	Volumes      []VolumeMountSpec     `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Workspace    *WorkspaceSpec        `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *SchedulerSpec        `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
	Jupyter      *JupyterSpec          `yaml:"jupyter,omitempty" json:"jupyter,omitempty"`
}

type AgentMCPEntriesSpec []AgentMCPEntrySpec

type AgentMCPEntrySpec struct {
	Ref       string                `yaml:"-" json:"-"`
	Name      string                `yaml:"name,omitempty" json:"name,omitempty"`
	Type      string                `yaml:"type,omitempty" json:"type,omitempty"`
	Transport string                `yaml:"transport,omitempty" json:"transport,omitempty"`
	Command   string                `yaml:"command,omitempty" json:"command,omitempty"`
	Args      []string              `yaml:"args,omitempty" json:"args,omitempty"`
	Env       map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	URL       string                `yaml:"url,omitempty" json:"url,omitempty"`
	Headers   map[string]EnvVarSpec `yaml:"headers,omitempty" json:"headers,omitempty"`
}

type MCPServerSpec struct {
	Type      string                `yaml:"type,omitempty" json:"type,omitempty"`
	Transport string                `yaml:"transport,omitempty" json:"transport,omitempty"`
	Command   string                `yaml:"command,omitempty" json:"command,omitempty"`
	Args      []string              `yaml:"args,omitempty" json:"args,omitempty"`
	Env       map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	URL       string                `yaml:"url,omitempty" json:"url,omitempty"`
	Headers   map[string]EnvVarSpec `yaml:"headers,omitempty" json:"headers,omitempty"`
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

type SkillSpec struct {
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	URL      string `yaml:"url,omitempty" json:"url,omitempty"`
	Ref      string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Format   string `yaml:"format,omitempty" json:"format,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	Token    string `yaml:"token,omitempty" json:"token,omitempty"`
}

type SchedulerSpec struct {
	Enabled       *bool         `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	SandboxPolicy *string       `yaml:"sandbox_policy,omitempty" json:"sandbox_policy,omitempty"`
	DisplayName   string        `yaml:"display_name,omitempty" json:"display_name,omitempty"`
	Description   string        `yaml:"description,omitempty" json:"description,omitempty"`
	Triggers      []TriggerSpec `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	Script        ScriptSource  `yaml:"script,omitempty" json:"script,omitempty"`
}

// ScriptSource is the authoring shape accepted by scheduler.script. Inline is
// populated for the scalar form and Source for a flat provider mapping. The
// two forms are mutually exclusive.
type ScriptSource struct {
	Inline string
	Source sources.Source
}

// IsZero reports whether neither authoring form is set.
func (s ScriptSource) IsZero() bool {
	return s.Inline == "" && !s.Source.HasContent()
}

func (s *ScriptSource) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var inline string
		if err := value.Decode(&inline); err != nil {
			return err
		}
		*s = ScriptSource{Inline: inline}
		return nil
	case yaml.MappingNode:
		var source sources.Source
		if err := value.Decode(&source); err != nil {
			return err
		}
		*s = ScriptSource{Source: source}
		return nil
	default:
		return fmt.Errorf("expected scalar or mapping, got %s", nodeKindName(value.Kind))
	}
}

func (s ScriptSource) MarshalYAML() (any, error) {
	if s.Source.HasContent() {
		return s.Source, nil
	}
	return s.Inline, nil
}

type TriggerSpec struct {
	Name          string            `yaml:"name,omitempty" json:"name,omitempty"`
	Cron          string            `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval      string            `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout       string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Event         *EventTriggerSpec `yaml:"event,omitempty" json:"event,omitempty"`
	Prompt        string            `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	SandboxPolicy *string           `yaml:"sandbox_policy,omitempty" json:"sandbox_policy,omitempty"`

	cronSet     bool
	intervalSet bool
	timeoutSet  bool
	eventSet    bool
}

type EventTriggerSpec struct {
	Topic string `yaml:"topic,omitempty" json:"topic,omitempty"`
}

type WorkspaceSpec struct {
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	URL      string `yaml:"url,omitempty" json:"url,omitempty"`
	Ref      string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Format   string `yaml:"format,omitempty" json:"format,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	Token    string `yaml:"token,omitempty" json:"token,omitempty"`
	Target   string `yaml:"target,omitempty" json:"target,omitempty"`
}

func (s WorkspaceSpec) ContentSource() sources.Source {
	return sources.Source{
		Provider: s.Provider,
		URL:      s.URL,
		Ref:      s.Ref,
		Path:     s.Path,
		Format:   s.Format,
		Username: s.Username,
		Password: s.Password,
		Token:    s.Token,
	}.Normalized()
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

func (s *AgentMCPEntrySpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		var raw string
		if err := value.Decode(&raw); err != nil {
			return err
		}
		*s = AgentMCPEntrySpec{Ref: raw}
		return nil
	case yaml.MappingNode:
		type alias AgentMCPEntrySpec
		var decoded alias
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		decoded.Ref = ""
		*s = AgentMCPEntrySpec(decoded)
		return nil
	default:
		return fmt.Errorf("expected scalar or mapping, got %s", nodeKindName(value.Kind))
	}
}

func (s *AgentMCPEntriesSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode, yaml.MappingNode:
		var entry AgentMCPEntrySpec
		if err := value.Decode(&entry); err != nil {
			return err
		}
		*s = AgentMCPEntriesSpec{entry}
		return nil
	case yaml.SequenceNode:
		entries := make([]AgentMCPEntrySpec, 0, len(value.Content))
		for _, item := range value.Content {
			var entry AgentMCPEntrySpec
			if err := item.Decode(&entry); err != nil {
				return err
			}
			entries = append(entries, entry)
		}
		*s = AgentMCPEntriesSpec(entries)
		return nil
	default:
		return fmt.Errorf("expected scalar, mapping, or sequence, got %s", nodeKindName(value.Kind))
	}
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
		"name":        validateScalar,
		"env_file":    validateScalarOrStringList,
		"variables":   validateEnvVarMap,
		"workspaces":  validateWorkspaceMap,
		"mcp_servers": validateMCPMap,
		"volumes":     validateVolumeMap,
		"agents":      validateAgentMap,
	})
}

func validateScalarOrStringList(node *yaml.Node, path string) error {
	if node.Kind == yaml.ScalarNode {
		return validateScalar(node, path)
	}
	return validateStringList(node, path)
}

func validateWorkspaceMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateWorkspace)
}

func validateAgentMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateAgent)
}

func validateAgent(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"enabled":       validateBool,
		"display_name":  validateScalar,
		"description":   validateScalar,
		"provider":      validateScalar,
		"model":         validateScalar,
		"system_prompt": validateScalar,
		"image":         validateScalar,
		"build":         validateBuild,
		"driver":        validateDriver,
		"env":           validateEnvVarMap,
		"mcp_servers":   validateAgentMCPEntries,
		"capset_ids":    validateStringList,
		"skills":        validateSkillList,
		"volumes":       validateVolumeMountList,
		"workspace":     validateWorkspace,
		"scheduler":     validateScheduler,
		"jupyter":       validateJupyter,
	})
}

func validateMCPMap(node *yaml.Node, path string) error {
	return validateNamedMap(node, path, validateMCPServer)
}

func validateMCPServer(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"type":      validateScalar,
		"transport": validateScalar,
		"command":   validateScalar,
		"args":      validateStringList,
		"env":       validateEnvVarMap,
		"url":       validateScalar,
		"headers":   validateEnvVarMap,
	})
}

func validateAgentMCPEntries(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return validateScalar(node, path)
	case yaml.MappingNode:
		return validateAgentMCPEntry(node, path)
	case yaml.SequenceNode:
		for i, item := range node.Content {
			itemPath := fmt.Sprintf("%s[%d]", path, i)
			switch item.Kind {
			case yaml.ScalarNode:
				if err := validateScalar(item, itemPath); err != nil {
					return err
				}
			case yaml.MappingNode:
				if err := validateAgentMCPEntry(item, itemPath); err != nil {
					return err
				}
			default:
				return newParseError(item, itemPath, "expected scalar or mapping")
			}
		}
		return nil
	default:
		return newParseError(node, path, "expected scalar, mapping, or sequence")
	}
}

func validateAgentMCPEntry(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"name":      validateScalar,
		"type":      validateScalar,
		"transport": validateScalar,
		"command":   validateScalar,
		"args":      validateStringList,
		"env":       validateEnvVarMap,
		"url":       validateScalar,
		"headers":   validateEnvVarMap,
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

func validateSkillList(node *yaml.Node, path string) error {
	if err := requireKind(node, path, yaml.SequenceNode, "sequence"); err != nil {
		return err
	}
	for i, item := range node.Content {
		if err := validateSkill(item, fmt.Sprintf("%s[%d]", path, i)); err != nil {
			return err
		}
	}
	return nil
}

func validateSkill(node *yaml.Node, path string) error {
	return validateMapping(node, path, sourceFieldValidators(map[string]nodeValidator{
		"name": validateScalar,
	}))
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
		"enabled":        validateBool,
		"sandbox_policy": validateScalar,
		"display_name":   validateScalar,
		"description":    validateScalar,
		"triggers":       validateTriggerList,
		"script":         validateScriptSource,
	})
}

func validateScriptSource(node *yaml.Node, path string) error {
	switch node.Kind {
	case yaml.ScalarNode:
		return validateScalar(node, path)
	case yaml.MappingNode:
		return validateMapping(node, path, sourceFieldValidators(nil))
	default:
		return newParseError(node, path, "expected scalar or mapping")
	}
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
		"name":           validateScalar,
		"cron":           validateScalar,
		"interval":       validateScalar,
		"timeout":        validateScalar,
		"event":          validateEventTrigger,
		"prompt":         validateScalar,
		"sandbox_policy": validateScalar,
	})
}

func validateEventTrigger(node *yaml.Node, path string) error {
	return validateMapping(node, path, map[string]nodeValidator{
		"topic": validateScalar,
	})
}

func validateWorkspace(node *yaml.Node, path string) error {
	return validateMapping(node, path, sourceFieldValidators(map[string]nodeValidator{
		"name":   validateScalar,
		"target": validateScalar,
	}))
}

func sourceFieldValidators(extra map[string]nodeValidator) map[string]nodeValidator {
	fields := map[string]nodeValidator{
		"provider": validateScalar,
		"url":      validateScalar,
		"ref":      validateScalar,
		"path":     validateScalar,
		"format":   validateScalar,
		"username": validateScalar,
		"password": validateScalar,
		"token":    validateScalar,
	}
	for name, validator := range extra {
		fields[name] = validator
	}
	return fields
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
