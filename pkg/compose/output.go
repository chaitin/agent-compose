package compose

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"

	"gopkg.in/yaml.v3"
)

const redactedEnvValue = "********"

type orderedEnvVarSpec struct {
	Name   string `yaml:"name" json:"name"`
	Value  string `yaml:"value" json:"value"`
	Secret bool   `yaml:"secret,omitempty" json:"secret,omitempty"`
}

type orderedProjectSpec struct {
	Name       string                  `yaml:"name" json:"name"`
	Variables  []orderedEnvVarSpec     `yaml:"variables,omitempty" json:"variables,omitempty"`
	Workspaces []orderedNamedWorkspace `yaml:"workspaces,omitempty" json:"workspaces,omitempty"`
	Volumes    []orderedVolumeSpec     `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Agents     []orderedAgentSpec      `yaml:"agents,omitempty" json:"agents,omitempty"`
	Network    *NetworkSpec            `yaml:"network,omitempty" json:"network,omitempty"`
}

type orderedNamedWorkspace struct {
	Key      string `yaml:"key" json:"key"`
	Name     string `yaml:"name,omitempty" json:"name,omitempty"`
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	URL      string `yaml:"url,omitempty" json:"url,omitempty"`
	Branch   string `yaml:"branch,omitempty" json:"branch,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
}

type orderedAgentSpec struct {
	Name         string                      `yaml:"name" json:"name"`
	Provider     string                      `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                      `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                      `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                      `yaml:"image,omitempty" json:"image,omitempty"`
	Build        *NormalizedBuildSpec        `yaml:"build,omitempty" json:"build,omitempty"`
	Driver       *NormalizedDriverSpec       `yaml:"driver" json:"driver"`
	Env          []orderedEnvVarSpec         `yaml:"env,omitempty" json:"env,omitempty"`
	CapsetIDs    []string                    `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Volumes      []NormalizedVolumeMountSpec `yaml:"volumes,omitempty" json:"volumes,omitempty"`
	Workspace    *WorkspaceSpec              `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *NormalizedSchedulerSpec    `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
	Jupyter      *JupyterSpec                `yaml:"jupyter,omitempty" json:"jupyter,omitempty"`
}

type orderedVolumeSpec struct {
	Key      string            `yaml:"key" json:"key"`
	Name     string            `yaml:"name,omitempty" json:"name,omitempty"`
	Driver   string            `yaml:"driver,omitempty" json:"driver,omitempty"`
	External bool              `yaml:"external,omitempty" json:"external,omitempty"`
	Labels   map[string]string `yaml:"labels,omitempty" json:"labels,omitempty"`
	Options  map[string]string `yaml:"options,omitempty" json:"options,omitempty"`
}

func (s *NormalizedProjectSpec) Redacted() *NormalizedProjectSpec {
	if s == nil {
		return nil
	}
	return s.clone(true)
}

func (s *NormalizedProjectSpec) Hash() (string, error) {
	data, err := s.MarshalCanonicalJSON(false)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (s *NormalizedProjectSpec) MarshalCanonicalJSON(redactSecrets bool) ([]byte, error) {
	return json.Marshal(s.ordered(redactSecrets))
}

func (s *NormalizedProjectSpec) MarshalCanonicalYAML(redactSecrets bool) ([]byte, error) {
	return yaml.Marshal(s.ordered(redactSecrets))
}

func (s *NormalizedProjectSpec) ordered(redactSecrets bool) orderedProjectSpec {
	if s == nil {
		return orderedProjectSpec{}
	}
	agents := make([]orderedAgentSpec, 0, len(s.Agents))
	for _, agent := range s.Agents {
		agents = append(agents, orderedAgentSpec{
			Name:         agent.Name,
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Build:        cloneNormalizedBuildSpec(agent.Build),
			Driver:       cloneNormalizedDriverSpec(agent.Driver),
			Env:          orderedEnvVars(agent.Env, redactSecrets),
			CapsetIDs:    slices.Clone(agent.CapsetIDs),
			Volumes:      cloneNormalizedVolumeMountSpecs(agent.Volumes),
			Workspace:    cloneWorkspaceSpec(agent.Workspace),
			Scheduler:    cloneNormalizedSchedulerSpec(agent.Scheduler),
			Jupyter:      cloneJupyterSpec(agent.Jupyter),
		})
	}
	slices.SortFunc(agents, func(a, b orderedAgentSpec) int {
		return compareString(a.Name, b.Name)
	})
	return orderedProjectSpec{
		Name:       s.Name,
		Variables:  orderedEnvVars(s.Variables, redactSecrets),
		Workspaces: orderedWorkspaces(s.Workspaces),
		Volumes:    orderedVolumes(s.Volumes),
		Agents:     agents,
		Network:    cloneNetworkSpecForOutput(s.Network),
	}
}

func (s *NormalizedProjectSpec) clone(redactSecrets bool) *NormalizedProjectSpec {
	ordered := s.ordered(redactSecrets)
	cloned := &NormalizedProjectSpec{
		Name:       ordered.Name,
		Variables:  envVarMapFromOrdered(ordered.Variables),
		Workspaces: workspaceMapFromOrdered(ordered.Workspaces),
		Volumes:    volumeMapFromOrdered(ordered.Volumes),
		Network:    ordered.Network,
	}
	for _, agent := range ordered.Agents {
		cloned.Agents = append(cloned.Agents, NormalizedAgentSpec{
			Name:         agent.Name,
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Build:        agent.Build,
			Driver:       agent.Driver,
			Env:          envVarMapFromOrdered(agent.Env),
			CapsetIDs:    slices.Clone(agent.CapsetIDs),
			Volumes:      cloneNormalizedVolumeMountSpecs(agent.Volumes),
			Workspace:    agent.Workspace,
			Scheduler:    agent.Scheduler,
			Jupyter:      agent.Jupyter,
		})
	}
	return cloned
}

func orderedWorkspaces(values map[string]WorkspaceSpec) []orderedNamedWorkspace {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]orderedNamedWorkspace, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		out = append(out, orderedNamedWorkspace{
			Key:      key,
			Name:     value.Name,
			Provider: value.Provider,
			URL:      value.URL,
			Branch:   value.Branch,
			Path:     value.Path,
		})
	}
	return out
}

func workspaceMapFromOrdered(values []orderedNamedWorkspace) map[string]WorkspaceSpec {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]WorkspaceSpec, len(values))
	for _, value := range values {
		out[value.Key] = WorkspaceSpec{
			Name:     value.Name,
			Provider: value.Provider,
			URL:      value.URL,
			Branch:   value.Branch,
			Path:     value.Path,
		}
	}
	return out
}

func orderedVolumes(values map[string]NormalizedVolumeSpec) []orderedVolumeSpec {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	out := make([]orderedVolumeSpec, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		out = append(out, orderedVolumeSpec{
			Key:      key,
			Name:     value.Name,
			Driver:   value.Driver,
			External: value.External,
			Labels:   cloneStringMap(value.Labels),
			Options:  cloneStringMap(value.Options),
		})
	}
	return out
}

func volumeMapFromOrdered(values []orderedVolumeSpec) map[string]NormalizedVolumeSpec {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]NormalizedVolumeSpec, len(values))
	for _, value := range values {
		out[value.Key] = NormalizedVolumeSpec{
			Name:     value.Name,
			Driver:   value.Driver,
			External: value.External,
			Labels:   cloneStringMap(value.Labels),
			Options:  cloneStringMap(value.Options),
		}
	}
	return out
}

func cloneNormalizedVolumeMountSpecs(values []NormalizedVolumeMountSpec) []NormalizedVolumeMountSpec {
	if len(values) == 0 {
		return nil
	}
	return slices.Clone(values)
}

func cloneNormalizedBuildSpec(value *NormalizedBuildSpec) *NormalizedBuildSpec {
	if value == nil {
		return nil
	}
	cloned := &NormalizedBuildSpec{
		Context:    value.Context,
		Dockerfile: value.Dockerfile,
		Target:     value.Target,
		Args:       cloneStringMap(value.Args),
		Platforms:  slices.Clone(value.Platforms),
		Tags:       slices.Clone(value.Tags),
		NoCache:    value.NoCache,
		Pull:       value.Pull,
	}
	return cloned
}

func cloneStringMap(value map[string]string) map[string]string {
	if len(value) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(value))
	for key, item := range value {
		cloned[key] = item
	}
	return cloned
}

func orderedEnvVars(values map[string]EnvVarSpec, redactSecrets bool) []orderedEnvVarSpec {
	if len(values) == 0 {
		return nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	ordered := make([]orderedEnvVarSpec, 0, len(names))
	for _, name := range names {
		value := values[name]
		displayValue := value.Value
		if redactSecrets && value.Secret {
			displayValue = redactedEnvValue
		}
		ordered = append(ordered, orderedEnvVarSpec{
			Name:   name,
			Value:  displayValue,
			Secret: value.Secret,
		})
	}
	return ordered
}

func envVarMapFromOrdered(values []orderedEnvVarSpec) map[string]EnvVarSpec {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]EnvVarSpec, len(values))
	for _, value := range values {
		result[value.Name] = EnvVarSpec{Value: value.Value, Secret: value.Secret}
	}
	return result
}

func cloneNormalizedDriverSpec(value *NormalizedDriverSpec) *NormalizedDriverSpec {
	if value == nil {
		return nil
	}
	cloned := *value
	cloned.Boxlite = cloneBoxliteDriverSpec(value.Boxlite)
	cloned.Docker = cloneDockerDriverSpec(value.Docker)
	cloned.Microsandbox = cloneMicrosandboxDriverSpec(value.Microsandbox)
	return &cloned
}

func cloneNormalizedSchedulerSpec(value *NormalizedSchedulerSpec) *NormalizedSchedulerSpec {
	if value == nil {
		return nil
	}
	cloned := &NormalizedSchedulerSpec{Enabled: value.Enabled, Script: value.Script}
	for _, trigger := range value.Triggers {
		cloned.Triggers = append(cloned.Triggers, cloneNormalizedTriggerSpec(trigger))
	}
	return cloned
}

func cloneNormalizedTriggerSpec(value NormalizedTriggerSpec) NormalizedTriggerSpec {
	cloned := value
	if value.Event != nil {
		event := *value.Event
		cloned.Event = &event
	}
	return cloned
}

func cloneNetworkSpecForOutput(value *NetworkSpec) *NetworkSpec {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneJupyterSpec(value *JupyterSpec) *JupyterSpec {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func compareString(a string, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}
