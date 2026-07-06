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
	Name      string              `yaml:"name" json:"name"`
	Variables []orderedEnvVarSpec `yaml:"variables,omitempty" json:"variables,omitempty"`
	Workspace *WorkspaceSpec      `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Agents    []orderedAgentSpec  `yaml:"agents,omitempty" json:"agents,omitempty"`
	Network   *NetworkSpec        `yaml:"network,omitempty" json:"network,omitempty"`
}

type orderedAgentSpec struct {
	Name         string                   `yaml:"name" json:"name"`
	Provider     string                   `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                   `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                   `yaml:"image,omitempty" json:"image,omitempty"`
	Build        *NormalizedBuildSpec     `yaml:"build,omitempty" json:"build,omitempty"`
	Driver       *NormalizedDriverSpec    `yaml:"driver" json:"driver"`
	Env          []orderedEnvVarSpec      `yaml:"env,omitempty" json:"env,omitempty"`
	CapsetIDs    []string                 `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Workspace    *WorkspaceSpec           `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *NormalizedSchedulerSpec `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
	Jupyter      *JupyterSpec             `yaml:"jupyter,omitempty" json:"jupyter,omitempty"`
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
			Workspace:    cloneWorkspaceSpec(agent.Workspace),
			Scheduler:    cloneNormalizedSchedulerSpec(agent.Scheduler),
			Jupyter:      cloneJupyterSpec(agent.Jupyter),
		})
	}
	slices.SortFunc(agents, func(a, b orderedAgentSpec) int {
		return compareString(a.Name, b.Name)
	})
	return orderedProjectSpec{
		Name:      s.Name,
		Variables: orderedEnvVars(s.Variables, redactSecrets),
		Workspace: cloneWorkspaceSpec(s.Workspace),
		Agents:    agents,
		Network:   cloneNetworkSpecForOutput(s.Network),
	}
}

func (s *NormalizedProjectSpec) clone(redactSecrets bool) *NormalizedProjectSpec {
	ordered := s.ordered(redactSecrets)
	cloned := &NormalizedProjectSpec{
		Name:      ordered.Name,
		Variables: envVarMapFromOrdered(ordered.Variables),
		Workspace: ordered.Workspace,
		Network:   ordered.Network,
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
			Workspace:    agent.Workspace,
			Scheduler:    agent.Scheduler,
			Jupyter:      agent.Jupyter,
		})
	}
	return cloned
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
