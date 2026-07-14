package compose

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/robfig/cron/v3"
)

const (
	DriverBoxlite      = "boxlite"
	DriverDocker       = "docker"
	DriverMicrosandbox = "microsandbox"
	DriverFirecracker  = "firecracker"
)

var stableIdentifierPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
var volumeSourceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
var envReferencePattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)
var exactEnvReferencePattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)
var composeCronParser = cron.NewParser(cron.SecondOptional | cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)

type NormalizeOptions struct {
	ProjectDir           string
	ComposePath          string
	Env                  map[string]string
	ResolveScriptURLs    bool
	ScriptSourceResolver ScriptSourceResolver
	Context              context.Context
}

type NormalizedProjectSpec struct {
	Name       string                             `yaml:"name" json:"name"`
	Variables  map[string]EnvVarSpec              `yaml:"variables,omitempty" json:"variables,omitempty"`
	Workspaces map[string]WorkspaceSpec           `yaml:"workspaces,omitempty" json:"workspaces,omitempty"`
	MCPs       map[string]NormalizedMCPServerSpec `yaml:"mcps,omitempty" json:"mcps,omitempty"`
	Volumes    map[string]NormalizedVolumeSpec    `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Agents     []NormalizedAgentSpec              `yaml:"agents,omitempty" json:"agents,omitempty"`
	Network    *NetworkSpec                       `yaml:"network,omitempty" json:"network,omitempty"`
}

type NormalizedAgentSpec struct {
	Name         string                             `yaml:"name" json:"name"`
	Status       string                             `yaml:"status,omitempty" json:"status,omitempty"`
	Provider     string                             `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                             `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                             `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                             `yaml:"image,omitempty" json:"image,omitempty"`
	Build        *NormalizedBuildSpec               `yaml:"build,omitempty" json:"build,omitempty"`
	Driver       *NormalizedDriverSpec              `yaml:"driver" json:"driver"`
	Env          map[string]EnvVarSpec              `yaml:"env,omitempty" json:"env,omitempty"`
	MCPs         map[string]NormalizedMCPServerSpec `yaml:"mcps,omitempty" json:"mcps,omitempty"`
	CapsetIDs    []string                           `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Skills       []NormalizedSkillSpec              `yaml:"skills,omitempty" json:"skills,omitempty"`
	Volumes      []NormalizedVolumeMountSpec        `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Workspace    *WorkspaceSpec                     `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *NormalizedSchedulerSpec           `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
	Jupyter      *JupyterSpec                       `yaml:"jupyter,omitempty" json:"jupyter,omitempty"`
}

type NormalizedMCPServerSpec struct {
	Type      string                `yaml:"type" json:"type"`
	Transport string                `yaml:"transport,omitempty" json:"transport,omitempty"`
	Command   string                `yaml:"command,omitempty" json:"command,omitempty"`
	Args      []string              `yaml:"args,omitempty" json:"args,omitempty"`
	Env       map[string]EnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	URL       string                `yaml:"url,omitempty" json:"url,omitempty"`
	Headers   map[string]EnvVarSpec `yaml:"headers,omitempty" json:"headers,omitempty"`
}

type NormalizedSkillSpec struct {
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`
	Source   string `yaml:"source,omitempty" json:"source,omitempty"`
	URL      string `yaml:"url,omitempty" json:"url,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Ref      string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	Token    string `yaml:"token,omitempty" json:"token,omitempty"`
}

type NormalizedVolumeSpec struct {
	Name     string            `yaml:"name,omitempty" json:"name,omitempty"`
	Driver   string            `yaml:"driver,omitempty" json:"driver,omitempty"`
	External bool              `yaml:"external,omitempty" json:"external,omitempty"`
	Labels   map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Options  map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

type NormalizedVolumeMountSpec struct {
	Type     string `yaml:"type" json:"type"`
	Source   string `yaml:"source" json:"source"`
	Target   string `yaml:"target" json:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty" json:"read_only,omitempty"`
}

type NormalizedBuildSpec struct {
	Context    string            `yaml:"context,omitempty" json:"context,omitempty"`
	Dockerfile string            `yaml:"dockerfile,omitempty" json:"dockerfile,omitempty"`
	Target     string            `yaml:"target,omitempty" json:"target,omitempty"`
	Args       map[string]string `yaml:"args,omitempty" json:"args,omitempty"`
	Platforms  []string          `yaml:"platforms,omitempty" json:"platforms,omitempty"`
	Tags       []string          `yaml:"tags,omitempty" json:"tags,omitempty"`
	NoCache    bool              `yaml:"no_cache,omitempty" json:"no_cache,omitempty"`
	Pull       bool              `yaml:"pull,omitempty" json:"pull,omitempty"`
}

type NormalizedDriverSpec struct {
	Name         string                  `yaml:"name" json:"name"`
	Boxlite      *BoxliteDriverSpec      `yaml:"boxlite,omitempty" json:"boxlite,omitempty"`
	Docker       *DockerDriverSpec       `yaml:"docker,omitempty" json:"docker,omitempty"`
	Microsandbox *MicrosandboxDriverSpec `yaml:"microsandbox,omitempty" json:"microsandbox,omitempty"`
}

type NormalizedSchedulerSpec struct {
	Enabled       bool                    `yaml:"enabled" json:"enabled"`
	SandboxPolicy string                  `yaml:"sandbox_policy" json:"sandbox_policy"`
	Script        string                  `yaml:"script,omitempty" json:"script,omitempty"`
	Triggers      []NormalizedTriggerSpec `yaml:"triggers,omitempty" json:"triggers,omitempty"`

	scriptURL string
}

// HasScript reports whether a scheduler has either an inline script snapshot
// or an unresolved URL source.
func (s *NormalizedSchedulerSpec) HasScript() bool {
	return s != nil && (strings.TrimSpace(s.Script) != "" || s.scriptURL != "")
}

func (s *NormalizedSchedulerSpec) hasUnresolvedScriptURL() bool {
	return s != nil && s.scriptURL != ""
}

type NormalizedTriggerSpec struct {
	Name          string            `yaml:"name,omitempty" json:"name,omitempty"`
	Kind          string            `yaml:"kind" json:"kind"`
	Cron          string            `yaml:"cron,omitempty" json:"cron,omitempty"`
	Interval      string            `yaml:"interval,omitempty" json:"interval,omitempty"`
	Timeout       string            `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Event         *EventTriggerSpec `yaml:"event,omitempty" json:"event,omitempty"`
	Prompt        string            `yaml:"prompt,omitempty" json:"prompt,omitempty"`
	SandboxPolicy string            `yaml:"sandbox_policy,omitempty" json:"sandbox_policy,omitempty"`
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
		Network:   normalizeNetworkDefault(spec.Network),
	}
	variables, err := normalizeEnvVarMap("variables", spec.Variables, options)
	if err != nil {
		return nil, err
	}
	normalized.Variables = variables
	workspaces, err := normalizeProjectWorkspaces(spec.Workspaces)
	if err != nil {
		return nil, err
	}
	normalized.Workspaces = workspaces
	mcps, err := normalizeMCPMap("mcps", spec.MCPs, options)
	if err != nil {
		return nil, err
	}
	normalized.MCPs = mcps
	if err := validateNetworkSpec(normalized.Network); err != nil {
		return nil, err
	}
	volumes, err := normalizeProjectVolumes(spec.Volumes)
	if err != nil {
		return nil, err
	}
	normalized.Volumes = volumes

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
		normalizedAgent, err := normalizeAgent(agentName, agent, options, normalized.Volumes, normalized.Workspaces, normalized.MCPs)
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

func normalizeAgent(name string, agent AgentSpec, options NormalizeOptions, projectVolumes map[string]NormalizedVolumeSpec, projectWorkspaces map[string]WorkspaceSpec, projectMCPs map[string]NormalizedMCPServerSpec) (NormalizedAgentSpec, error) {
	status := strings.ToLower(strings.TrimSpace(agent.Status))
	if status != "" && status != "enabled" && status != "disabled" {
		return NormalizedAgentSpec{}, fmt.Errorf("%s.status must be enabled or disabled", joinPath("agents", name))
	}
	driver, err := normalizeDriverSpec(joinPath("agents", name)+".driver", agent.Driver)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	scheduler, err := normalizeSchedulerSpec(joinPath("agents", name)+".scheduler", agent.Scheduler, options)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	jupyter, err := normalizeJupyterSpec(joinPath("agents", name)+".jupyter", agent.Jupyter)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	env, err := normalizeEnvVarMap(joinPath("agents", name)+".env", agent.Env, options)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	agentMCPs, err := normalizeAgentMCPEntries(joinPath("agents", name), agent.MCPs, projectMCPs, options)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	build, err := normalizeBuildSpec(joinPath("agents", name)+".build", agent.Build)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	volumes, err := normalizeVolumeMountSpecs(joinPath("agents", name)+".volumes", agent.Volumes, projectVolumes)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	skills, err := normalizeSkillSpecs(joinPath("agents", name)+".skills", agent.Skills, options)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	model, err := interpolateEnvValue(joinPath("agents", name)+".model", strings.TrimSpace(agent.Model), options)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	workspace, err := resolveAgentWorkspace(joinPath("agents", name)+".workspace", agent.Workspace, projectWorkspaces)
	if err != nil {
		return NormalizedAgentSpec{}, err
	}
	return NormalizedAgentSpec{
		Name:         name,
		Status:       status,
		Provider:     strings.TrimSpace(agent.Provider),
		Model:        model,
		SystemPrompt: agent.SystemPrompt,
		Image:        strings.TrimSpace(agent.Image),
		Build:        build,
		Driver:       driver,
		Env:          env,
		MCPs:         agentMCPs,
		CapsetIDs:    normalizeStringList(agent.CapsetIDs),
		Skills:       skills,
		Volumes:      volumes,
		Workspace:    workspace,
		Scheduler:    scheduler,
		Jupyter:      jupyter,
	}, nil
}

func normalizeProjectWorkspaces(values map[string]WorkspaceSpec) (map[string]WorkspaceSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	normalized := make(map[string]WorkspaceSpec, len(values))
	for _, rawKey := range keys {
		key := strings.TrimSpace(rawKey)
		if err := validateStableIdentifier(joinPath("workspaces", rawKey), key, "workspace name"); err != nil {
			return nil, err
		}
		if _, exists := normalized[key]; exists {
			return nil, &ValidationError{Path: joinPath("workspaces", rawKey), Message: fmt.Sprintf("duplicate workspace %q", key)}
		}
		item := values[rawKey]
		workspace, err := normalizeInlineWorkspaceSpec(joinPath("workspaces", key), &item, key)
		if err != nil {
			return nil, err
		}
		normalized[key] = *workspace
	}
	return normalized, nil
}

func normalizeMCPMap(path string, values map[string]MCPServerSpec, options NormalizeOptions) (map[string]NormalizedMCPServerSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	normalized := make(map[string]NormalizedMCPServerSpec, len(values))
	for _, key := range keys {
		if err := validateStableIdentifier(joinPath(path, key), key, "mcp name"); err != nil {
			return nil, err
		}
		server, err := normalizeMCPServer(joinPath(path, key), values[key], options)
		if err != nil {
			return nil, err
		}
		normalized[key] = server
	}
	return normalized, nil
}

func resolveAgentWorkspace(path string, spec *WorkspaceSpec, globals map[string]WorkspaceSpec) (*WorkspaceSpec, error) {
	if spec == nil {
		switch len(globals) {
		case 1:
			for _, workspace := range globals {
				resolved := cloneWorkspaceSpec(&workspace)
				resolved.Name = ""
				return resolved, nil
			}
		case 0:
			return nil, nil
		default:
			return nil, &ValidationError{Path: path, Message: "workspace is required when project workspaces has multiple entries"}
		}
	}
	trimmed := cloneWorkspaceSpec(spec)
	hasName := trimmed.Name != ""
	hasInline := trimmed.Provider != "" || trimmed.URL != "" || trimmed.Branch != "" || trimmed.Path != ""
	switch {
	case hasName && !hasInline:
		workspace, ok := globals[trimmed.Name]
		if !ok {
			return nil, &ValidationError{Path: path + ".name", Message: fmt.Sprintf("workspace %q is not defined", trimmed.Name)}
		}
		resolved := cloneWorkspaceSpec(&workspace)
		resolved.Name = ""
		return resolved, nil
	case hasInline:
		return normalizeInlineWorkspaceSpec(path, trimmed, trimmed.Name)
	default:
		return nil, &ValidationError{Path: path, Message: "workspace is required"}
	}
}

func normalizeInlineWorkspaceSpec(path string, spec *WorkspaceSpec, defaultName string) (*WorkspaceSpec, error) {
	if spec == nil {
		return nil, &ValidationError{Path: path, Message: "workspace is required"}
	}
	workspace := cloneWorkspaceSpec(spec)
	workspace.Name = strings.TrimSpace(workspace.Name)
	provider := strings.ToLower(strings.TrimSpace(workspace.Provider))
	if provider == "" {
		return nil, &ValidationError{Path: path + ".provider", Message: "workspace provider is required"}
	}
	workspace.Provider = provider
	workspace.Name = defaultName
	switch provider {
	case "local":
		if strings.TrimSpace(workspace.URL) != "" {
			return nil, &ValidationError{Path: path + ".url", Message: "local workspace does not support url"}
		}
		if strings.TrimSpace(workspace.Branch) != "" {
			return nil, &ValidationError{Path: path + ".branch", Message: "local workspace does not support branch"}
		}
		if _, err := cleanComposeLocalWorkspacePath(workspace.Path); err != nil {
			return nil, &ValidationError{Path: path + ".path", Message: err.Error()}
		}
	case "git":
		if strings.TrimSpace(workspace.URL) == "" {
			return nil, &ValidationError{Path: path + ".url", Message: "git workspace url is required"}
		}
		if strings.TrimSpace(workspace.Path) != "" {
			cleanPath := filepath.Clean(workspace.Path)
			if filepath.IsAbs(workspace.Path) || cleanPath == ".." || strings.HasPrefix(cleanPath, ".."+string(filepath.Separator)) {
				return nil, &ValidationError{Path: path + ".path", Message: fmt.Sprintf("git workspace path %q must stay within workspace root", workspace.Path)}
			}
			workspace.Path = cleanPath
		} else {
			workspace.Path = "."
		}
		workspace.URL = strings.TrimSpace(workspace.URL)
	default:
		return nil, &ValidationError{Path: path + ".provider", Message: fmt.Sprintf("unsupported workspace provider %q", workspace.Provider)}
	}
	return workspace, nil
}

func cleanComposeLocalWorkspacePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("local workspace path is required")
	}
	if filepath.IsAbs(trimmed) {
		return "", fmt.Errorf("local workspace path %q must be relative", trimmed)
	}
	clean := filepath.Clean(trimmed)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("local workspace path %q escapes project root", trimmed)
	}
	return clean, nil
}

func normalizeMCPServer(path string, server MCPServerSpec, options NormalizeOptions) (NormalizedMCPServerSpec, error) {
	serverType := strings.ToLower(strings.TrimSpace(server.Type))
	transport := strings.ToLower(strings.TrimSpace(server.Transport))
	command := strings.TrimSpace(server.Command)
	args := normalizeStringList(server.Args)
	env, err := normalizeEnvVarMap(path+".env", server.Env, options)
	if err != nil {
		return NormalizedMCPServerSpec{}, err
	}
	url, err := interpolateEnvValue(path+".url", strings.TrimSpace(server.URL), options)
	if err != nil {
		return NormalizedMCPServerSpec{}, err
	}
	headers, err := normalizeEnvVarMap(path+".headers", server.Headers, options)
	if err != nil {
		return NormalizedMCPServerSpec{}, err
	}
	switch serverType {
	case "local":
		if command == "" {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".command", Message: "command is required for local mcp"}
		}
		if transport != "" {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".transport", Message: "transport is not supported for local mcp"}
		}
		if url != "" {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".url", Message: "url is not supported for local mcp"}
		}
		if len(headers) > 0 {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".headers", Message: "headers are not supported for local mcp"}
		}
		return NormalizedMCPServerSpec{Type: serverType, Command: command, Args: args, Env: env}, nil
	case "remote":
		if transport != "sse" && transport != "http" {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".transport", Message: "transport must be sse or http for remote mcp"}
		}
		if url == "" {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".url", Message: "url is required for remote mcp"}
		}
		if command != "" {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".command", Message: "command is not supported for remote mcp"}
		}
		if len(args) > 0 {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".args", Message: "args are not supported for remote mcp"}
		}
		if len(env) > 0 {
			return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".env", Message: "env is not supported for remote mcp"}
		}
		return NormalizedMCPServerSpec{Type: serverType, Transport: transport, URL: url, Headers: headers}, nil
	default:
		return NormalizedMCPServerSpec{}, &ValidationError{Path: path + ".type", Message: "type must be local or remote"}
	}
}

func normalizeAgentMCPEntries(path string, entries AgentMCPEntriesSpec, projectMCPs map[string]NormalizedMCPServerSpec, options NormalizeOptions) (map[string]NormalizedMCPServerSpec, error) {
	if len(entries) == 0 {
		return nil, nil
	}
	result := make(map[string]NormalizedMCPServerSpec, len(entries))
	seenNames := make(map[string]string, len(entries))
	for index, entry := range entries {
		itemPath := fmt.Sprintf("%s.mcps[%d]", path, index)
		if ref := strings.TrimSpace(entry.Ref); ref != "" {
			server, ok := projectMCPs[ref]
			if !ok {
				return nil, &ValidationError{Path: itemPath, Message: fmt.Sprintf("mcp %q is not defined", ref)}
			}
			if _, ok := seenNames[ref]; ok {
				continue
			}
			seenNames[ref] = itemPath
			result[ref] = server
			continue
		}
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			return nil, &ValidationError{Path: itemPath + ".name", Message: "name is required for inline agent mcp"}
		}
		if prev, ok := seenNames[name]; ok {
			return nil, &ValidationError{Path: itemPath + ".name", Message: fmt.Sprintf("mcp %q is already declared at %s", name, prev)}
		}
		server, err := normalizeMCPServer(itemPath, MCPServerSpec{
			Type:      entry.Type,
			Transport: entry.Transport,
			Command:   entry.Command,
			Args:      entry.Args,
			Env:       entry.Env,
			URL:       entry.URL,
			Headers:   entry.Headers,
		}, options)
		if err != nil {
			return nil, err
		}
		seenNames[name] = itemPath
		result[name] = server
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func normalizeSkillSpecs(path string, values []SkillSpec, options NormalizeOptions) ([]NormalizedSkillSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]NormalizedSkillSpec, 0, len(values))
	seenNames := make(map[string]struct{}, len(values))
	for index, value := range values {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		current, err := normalizeSkillSpec(itemPath, value, options)
		if err != nil {
			return nil, err
		}
		if _, ok := seenNames[current.Name]; ok {
			return nil, &ValidationError{Path: itemPath + ".name", Message: fmt.Sprintf("duplicate skill name %q", current.Name)}
		}
		seenNames[current.Name] = struct{}{}
		normalized = append(normalized, current)
	}
	return normalized, nil
}

func normalizeSkillSpec(path string, value SkillSpec, options NormalizeOptions) (NormalizedSkillSpec, error) {
	name, err := interpolateEnvValue(path+".name", strings.TrimSpace(value.Name), options)
	if err != nil {
		return NormalizedSkillSpec{}, err
	}
	source, err := interpolateEnvValue(path+".source", strings.TrimSpace(value.Source), options)
	if err != nil {
		return NormalizedSkillSpec{}, err
	}
	source = strings.ToLower(strings.TrimSpace(source))
	urlValue, err := interpolateEnvValue(path+".url", strings.TrimSpace(value.URL), options)
	if err != nil {
		return NormalizedSkillSpec{}, err
	}
	pathValue, err := interpolateEnvValue(path+".path", strings.TrimSpace(value.Path), options)
	if err != nil {
		return NormalizedSkillSpec{}, err
	}
	ref, err := interpolateEnvValue(path+".ref", strings.TrimSpace(value.Ref), options)
	if err != nil {
		return NormalizedSkillSpec{}, err
	}
	username := strings.TrimSpace(value.Username)
	if username != "" {
		username, err = interpolateEnvValue(path+".username", username, options)
		if err != nil {
			return NormalizedSkillSpec{}, err
		}
	}
	password := strings.TrimSpace(value.Password)
	token := strings.TrimSpace(value.Token)
	if err := validateSecretReference(path+".password", password); err != nil {
		return NormalizedSkillSpec{}, err
	}
	if err := validateSecretReference(path+".token", token); err != nil {
		return NormalizedSkillSpec{}, err
	}
	if source == "" {
		source = inferSkillSource(pathValue, urlValue)
	}
	if source == "" {
		return NormalizedSkillSpec{}, &ValidationError{Path: path + ".source", Message: "skill source is required"}
	}
	if source == "zip" && urlValue != "" {
		urlValue = normalizeLocalSkillArchivePath(urlValue, options)
	}
	if pathValue != "" {
		pathValue, err = normalizeSkillPath(path+".path", source, urlValue, pathValue, options)
		if err != nil {
			return NormalizedSkillSpec{}, err
		}
	}
	switch source {
	case "git":
		if parsedURL, parsedPath, parsedRef, ok := parseGitHubSkillShorthand(urlValue, pathValue, ref); ok {
			urlValue, pathValue, ref = parsedURL, parsedPath, parsedRef
		} else if parsedURL, parsedPath, parsedRef, ok := parseGitHubSkillShorthand(pathValue, "", ref); ok {
			urlValue, pathValue, ref = parsedURL, parsedPath, parsedRef
		}
		if urlValue == "" {
			urlValue = pathValue
			pathValue = ""
		}
		if strings.TrimSpace(urlValue) == "" {
			return NormalizedSkillSpec{}, &ValidationError{Path: path + ".url", Message: "git skill url is required"}
		}
	case "file":
		if strings.TrimSpace(pathValue) == "" {
			return NormalizedSkillSpec{}, &ValidationError{Path: path + ".path", Message: "file skill path is required"}
		}
	case "zip":
		if strings.TrimSpace(urlValue) == "" && strings.TrimSpace(pathValue) == "" {
			return NormalizedSkillSpec{}, &ValidationError{Path: path + ".url", Message: "zip skill url or path is required"}
		}
	default:
		return NormalizedSkillSpec{}, &ValidationError{Path: path + ".source", Message: fmt.Sprintf("skill source %q is not supported", source)}
	}
	if name == "" {
		name = inferSkillName(source, urlValue, pathValue)
	}
	if err := validateStableIdentifier(path+".name", name, "skill name"); err != nil {
		return NormalizedSkillSpec{}, err
	}
	return NormalizedSkillSpec{
		Name:     name,
		Source:   source,
		URL:      urlValue,
		Path:     pathValue,
		Ref:      ref,
		Username: username,
		Password: password,
		Token:    token,
	}, nil
}

func inferSkillSource(pathValue, urlValue string) string {
	candidate := strings.ToLower(strings.TrimSpace(urlValue))
	if candidate == "" {
		candidate = strings.ToLower(strings.TrimSpace(pathValue))
	}
	switch {
	case strings.HasPrefix(candidate, "github:"):
		return "git"
	case strings.HasSuffix(candidate, ".git"):
		return "git"
	case strings.HasSuffix(candidate, ".zip"):
		return "zip"
	case strings.HasPrefix(candidate, "http://"), strings.HasPrefix(candidate, "https://"):
		return ""
	default:
		return "file"
	}
}

func validateSecretReference(path, value string) error {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	if !exactEnvReferencePattern.MatchString(strings.TrimSpace(value)) {
		return &ValidationError{Path: path, Message: "secret value must be an environment reference like ${NAME}"}
	}
	return nil
}

func normalizeLocalSkillArchivePath(value string, options NormalizeOptions) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		return value
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	base := composeBaseDir(options)
	if base == "" {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}

func normalizeSkillPath(path, source, urlValue, value string, options NormalizeOptions) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	if source != "file" && source != "zip" {
		return value, nil
	}
	if source == "zip" && strings.TrimSpace(urlValue) != "" {
		return value, nil
	}
	if strings.HasPrefix(strings.ToLower(value), "http://") || strings.HasPrefix(strings.ToLower(value), "https://") {
		return value, nil
	}
	if filepath.IsAbs(value) {
		return filepath.Clean(value), nil
	}
	base := composeBaseDir(options)
	if base == "" {
		return filepath.Clean(value), nil
	}
	return filepath.Clean(filepath.Join(base, value)), nil
}

func composeBaseDir(options NormalizeOptions) string {
	if strings.TrimSpace(options.ComposePath) != "" {
		if abs, err := filepath.Abs(strings.TrimSpace(options.ComposePath)); err == nil {
			return filepath.Dir(abs)
		}
		return filepath.Dir(strings.TrimSpace(options.ComposePath))
	}
	if strings.TrimSpace(options.ProjectDir) != "" {
		if abs, err := filepath.Abs(strings.TrimSpace(options.ProjectDir)); err == nil {
			return abs
		}
		return strings.TrimSpace(options.ProjectDir)
	}
	return ""
}

func inferSkillName(source, urlValue, pathValue string) string {
	candidate := strings.TrimSpace(pathValue)
	if candidate == "" {
		candidate = strings.TrimSpace(urlValue)
	}
	if source == "git" {
		if _, parsedPath, _, ok := parseGitHubSkillShorthand(candidate, "", ""); ok && parsedPath != "" {
			candidate = parsedPath
		}
	}
	candidate = strings.TrimSuffix(candidate, "/")
	candidate = strings.TrimSuffix(candidate, ".zip")
	candidate = strings.TrimSuffix(candidate, ".git")
	name := filepath.Base(candidate)
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-_")
}

func parseGitHubSkillShorthand(value, fallbackPath, fallbackRef string) (string, string, string, bool) {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "github:") {
		return "", "", "", false
	}
	raw := strings.TrimPrefix(value, "github:")
	ref := strings.TrimSpace(fallbackRef)
	if before, after, ok := strings.Cut(raw, "@"); ok {
		raw = before
		ref = strings.TrimSpace(after)
	}
	repo := raw
	pathValue := strings.TrimSpace(fallbackPath)
	if before, after, ok := strings.Cut(raw, "//"); ok {
		repo = before
		pathValue = strings.TrimSpace(after)
	}
	repo = strings.Trim(repo, "/")
	if strings.Count(repo, "/") != 1 {
		return "", "", "", false
	}
	return "https://github.com/" + repo + ".git", pathValue, ref, true
}

func normalizeProjectVolumes(values map[string]VolumeSpec) (map[string]NormalizedVolumeSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	normalized := make(map[string]NormalizedVolumeSpec, len(values))
	for _, key := range keys {
		if err := validateStableIdentifier(joinPath("volumes", key), key, "volume key"); err != nil {
			return nil, err
		}
		value := values[key]
		driver := strings.ToLower(strings.TrimSpace(value.Driver))
		if driver == "" {
			driver = "local"
		}
		if driver != "local" {
			return nil, &ValidationError{Path: joinPath(joinPath("volumes", key), "driver"), Message: "only local volume driver is supported"}
		}
		normalized[key] = NormalizedVolumeSpec{
			Name:     strings.TrimSpace(value.Name),
			Driver:   driver,
			External: value.External,
			Labels:   normalizeStringMap(value.Labels),
			Options:  normalizeStringMap(value.Options),
		}
	}
	return normalized, nil
}

func normalizeStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make(map[string]string, len(values))
	for _, key := range keys {
		name := strings.TrimSpace(key)
		if name == "" {
			continue
		}
		out[name] = strings.TrimSpace(values[key])
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeVolumeMountSpecs(path string, values []VolumeMountSpec, projectVolumes map[string]NormalizedVolumeSpec) ([]NormalizedVolumeMountSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make([]NormalizedVolumeMountSpec, 0, len(values))
	seenTargets := make(map[string]struct{}, len(values))
	for index, value := range values {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		current, err := normalizeVolumeMountSpec(itemPath, value, projectVolumes)
		if err != nil {
			return nil, err
		}
		if _, ok := seenTargets[current.Target]; ok {
			return nil, &ValidationError{Path: itemPath + ".target", Message: fmt.Sprintf("duplicate volume target %q", current.Target)}
		}
		seenTargets[current.Target] = struct{}{}
		normalized = append(normalized, current)
	}
	return normalized, nil
}

func normalizeVolumeMountSpec(path string, value VolumeMountSpec, projectVolumes map[string]NormalizedVolumeSpec) (NormalizedVolumeMountSpec, error) {
	source := strings.TrimSpace(value.Source)
	target := strings.TrimSpace(value.Target)
	if source == "" {
		return NormalizedVolumeMountSpec{}, &ValidationError{Path: path + ".source", Message: "volume source is required"}
	}
	if target == "" {
		return NormalizedVolumeMountSpec{}, &ValidationError{Path: path + ".target", Message: "volume target is required"}
	}
	if !filepath.IsAbs(target) {
		return NormalizedVolumeMountSpec{}, &ValidationError{Path: path + ".target", Message: "volume target must be absolute"}
	}
	mountType := strings.ToLower(strings.TrimSpace(value.Type))
	if mountType == "" {
		mountType = inferVolumeMountType(source, projectVolumes)
	}
	switch mountType {
	case "volume":
		if !volumeSourceNamePattern.MatchString(source) {
			return NormalizedVolumeMountSpec{}, &ValidationError{Path: path + ".source", Message: "volume source name is invalid"}
		}
	case "bind":
		if source == "" {
			return NormalizedVolumeMountSpec{}, &ValidationError{Path: path + ".source", Message: "bind source is required"}
		}
	default:
		return NormalizedVolumeMountSpec{}, &ValidationError{Path: path + ".type", Message: fmt.Sprintf("volume mount type %q is not supported", mountType)}
	}
	return NormalizedVolumeMountSpec{
		Type:     mountType,
		Source:   source,
		Target:   filepath.Clean(target),
		ReadOnly: value.ReadOnly,
	}, nil
}

func inferVolumeMountType(source string, projectVolumes map[string]NormalizedVolumeSpec) string {
	if _, ok := projectVolumes[source]; ok {
		return "volume"
	}
	if filepath.IsAbs(source) || strings.HasPrefix(source, ".") {
		return "bind"
	}
	return "volume"
}

func parseVolumeMountShortSyntax(raw string) (VolumeMountSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return VolumeMountSpec{}, fmt.Errorf("volume short syntax is required")
	}
	parts := strings.Split(raw, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return VolumeMountSpec{}, fmt.Errorf("volume short syntax must be source:target[:ro]")
	}
	source := strings.TrimSpace(parts[0])
	target := strings.TrimSpace(parts[1])
	if source == "" || target == "" {
		return VolumeMountSpec{}, fmt.Errorf("volume short syntax requires source and target")
	}
	readOnly := false
	if len(parts) == 3 {
		mode := strings.ToLower(strings.TrimSpace(parts[2]))
		switch mode {
		case "ro", "readonly":
			readOnly = true
		case "rw", "":
		default:
			return VolumeMountSpec{}, fmt.Errorf("unsupported volume short syntax mode %q", parts[2])
		}
	}
	return VolumeMountSpec{Source: source, Target: target, ReadOnly: readOnly}, nil
}

func normalizeBuildSpec(path string, build *BuildSpec) (*NormalizedBuildSpec, error) {
	if build == nil {
		return nil, nil
	}
	contextDir := strings.TrimSpace(build.Context)
	if contextDir == "" {
		contextDir = "."
	}
	dockerfile := strings.TrimSpace(build.Dockerfile)
	if dockerfile == "" {
		dockerfile = "Dockerfile"
	}
	platforms := normalizeStringList(build.Platforms)
	if len(platforms) > 1 {
		return nil, &ValidationError{Path: path + ".platforms", Message: "multiple build platforms are not supported yet"}
	}
	args := make(map[string]string, len(build.Args))
	for key, value := range build.Args {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, &ValidationError{Path: path + ".args", Message: "build arg name is required"}
		}
		args[key] = value
	}
	if len(args) == 0 {
		args = nil
	}
	return &NormalizedBuildSpec{
		Context:    contextDir,
		Dockerfile: dockerfile,
		Target:     strings.TrimSpace(build.Target),
		Args:       args,
		Platforms:  platforms,
		Tags:       normalizeStringList(build.Tags),
		NoCache:    build.NoCache,
		Pull:       build.Pull,
	}, nil
}

func normalizeJupyterSpec(path string, jupyter *JupyterSpec) (*JupyterSpec, error) {
	if jupyter == nil {
		return nil, nil
	}
	normalized := *jupyter
	if normalized.GuestPort < 0 || normalized.GuestPort > 65535 {
		return nil, &ValidationError{Path: path + ".guest_port", Message: "guest_port must be 0 or a valid TCP port between 1 and 65535"}
	}
	if !normalized.Enabled && normalized.GuestPort == 0 {
		return nil, nil
	}
	return &normalized, nil
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

func normalizeSchedulerSpec(path string, scheduler *SchedulerSpec, options NormalizeOptions) (*NormalizedSchedulerSpec, error) {
	if scheduler == nil {
		return nil, nil
	}

	enabled := true
	if scheduler.Enabled != nil {
		enabled = *scheduler.Enabled
	}
	script := strings.TrimSpace(scheduler.Script.Inline)
	scriptURL := strings.TrimSpace(scheduler.Script.URL)
	if scheduler.Script.Inline != "" && scriptURL != "" {
		return nil, &ValidationError{Path: path + ".script", Message: "script must use exactly one of inline content or url"}
	}
	if scheduler.Script.URL != "" && scriptURL == "" {
		return nil, &ValidationError{Path: path + ".script.url", Message: "script URL is required"}
	}
	if scriptURL != "" {
		var err error
		scriptURL, err = normalizeScriptSourceURL(scriptURL, options)
		if err != nil {
			return nil, &ValidationError{Path: path + ".script.url", Message: err.Error()}
		}
	}
	if (script != "" || scriptURL != "") && len(scheduler.Triggers) > 0 {
		return nil, &ValidationError{Path: path, Message: "scheduler script and triggers are mutually exclusive"}
	}
	sandboxPolicy, err := normalizeSandboxPolicy(path+".sandbox_policy", scheduler.SandboxPolicy, "new")
	if err != nil {
		return nil, err
	}
	normalized := &NormalizedSchedulerSpec{Enabled: enabled, SandboxPolicy: sandboxPolicy, Script: script}
	if scriptURL != "" {
		if !options.ResolveScriptURLs {
			normalized.scriptURL = scriptURL
		} else {
			resolver := options.ScriptSourceResolver
			if resolver == nil {
				resolver = NewDefaultScriptSourceResolver()
			}
			ctx := options.Context
			if ctx == nil {
				ctx = context.Background()
			}
			content, err := resolver.Resolve(ctx, scriptURL)
			if err != nil {
				return nil, &ValidationError{Path: path + ".script.url", Message: err.Error()}
			}
			if !utf8.Valid(content) {
				return nil, &ValidationError{Path: path + ".script.url", Message: "script content must be valid UTF-8"}
			}
			text := strings.TrimPrefix(string(content), "\ufeff")
			normalized.Script = strings.TrimSpace(text)
			if normalized.Script == "" {
				return nil, &ValidationError{Path: path + ".script.url", Message: "script content is empty"}
			}
		}
	}
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
	var err error
	normalized.SandboxPolicy, err = normalizeSandboxPolicy(path+".sandbox_policy", trigger.SandboxPolicy, "")
	if err != nil {
		return NormalizedTriggerSpec{}, err
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

func normalizeSandboxPolicy(path string, value *string, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	policy := strings.ToLower(strings.TrimSpace(*value))
	switch policy {
	case "sticky", "new":
		return policy, nil
	default:
		return "", &ValidationError{Path: path, Message: fmt.Sprintf("sandbox_policy must be sticky or new, got %q", policy)}
	}
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
	cloned.Name = strings.TrimSpace(cloned.Name)
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
