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
	Name        string                         `yaml:"name" json:"name"`
	Metadata    *ProjectMetadataSpec           `yaml:"metadata,omitempty" json:"metadata,omitempty"`
	Variables   []orderedEnvVarSpec            `yaml:"variables,omitempty" json:"variables,omitempty"`
	Runtime     *orderedProjectRuntimeSpec     `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Workspace   *WorkspaceSpec                 `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Agents      []orderedAgentSpec             `yaml:"agents,omitempty" json:"agents,omitempty"`
	Services    []orderedServiceSpec           `yaml:"services,omitempty" json:"services,omitempty"`
	Triggers    []NormalizedProjectTriggerSpec `yaml:"triggers,omitempty" json:"triggers,omitempty"`
	Permissions *PermissionSpec                `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Artifacts   *ArtifactPolicySpec            `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	Network     *NetworkSpec                   `yaml:"network,omitempty" json:"network,omitempty"`
}

type orderedProjectRuntimeSpec struct {
	Driver        string              `yaml:"driver,omitempty" json:"driver,omitempty"`
	Image         string              `yaml:"image,omitempty" json:"image,omitempty"`
	Env           []orderedEnvVarSpec `yaml:"env,omitempty" json:"env,omitempty"`
	Resources     map[string]string   `yaml:"resources,omitempty" json:"resources,omitempty"`
	Network       *NetworkSpec        `yaml:"network,omitempty" json:"network,omitempty"`
	CleanupPolicy string              `yaml:"cleanup_policy,omitempty" json:"cleanup_policy,omitempty"`
}

type orderedAgentSpec struct {
	Name         string                   `yaml:"name" json:"name"`
	Provider     string                   `yaml:"provider,omitempty" json:"provider,omitempty"`
	Model        string                   `yaml:"model,omitempty" json:"model,omitempty"`
	SystemPrompt string                   `yaml:"system_prompt,omitempty" json:"system_prompt,omitempty"`
	Image        string                   `yaml:"image,omitempty" json:"image,omitempty"`
	Driver       *NormalizedDriverSpec    `yaml:"driver" json:"driver"`
	Env          []orderedEnvVarSpec      `yaml:"env,omitempty" json:"env,omitempty"`
	CapsetIDs    []string                 `yaml:"capset_ids,omitempty" json:"capset_ids,omitempty"`
	Workspace    *WorkspaceSpec           `yaml:"workspace,omitempty" json:"workspace,omitempty"`
	Scheduler    *NormalizedSchedulerSpec `yaml:"scheduler,omitempty" json:"scheduler,omitempty"`
}

type orderedServiceSpec struct {
	Name         string               `yaml:"name" json:"name"`
	Description  string               `yaml:"description,omitempty" json:"description,omitempty"`
	Runtime      string               `yaml:"runtime,omitempty" json:"runtime,omitempty"`
	Entry        string               `yaml:"entry" json:"entry"`
	InputSchema  string               `yaml:"input_schema,omitempty" json:"input_schema,omitempty"`
	OutputSchema string               `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`
	ErrorSchema  string               `yaml:"error_schema,omitempty" json:"error_schema,omitempty"`
	Timeout      string               `yaml:"timeout,omitempty" json:"timeout,omitempty"`
	Retry        *RetryPolicySpec     `yaml:"retry,omitempty" json:"retry,omitempty"`
	Permissions  *PermissionSpec      `yaml:"permissions,omitempty" json:"permissions,omitempty"`
	Env          []orderedEnvVarSpec  `yaml:"env,omitempty" json:"env,omitempty"`
	Agents       []string             `yaml:"agents,omitempty" json:"agents,omitempty"`
	Examples     []ServiceExampleSpec `yaml:"examples,omitempty" json:"examples,omitempty"`
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
	runtime := orderedProjectRuntime(s.Runtime, redactSecrets)
	agents := make([]orderedAgentSpec, 0, len(s.Agents))
	for _, agent := range s.Agents {
		agents = append(agents, orderedAgentSpec{
			Name:         agent.Name,
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Driver:       cloneNormalizedDriverSpec(agent.Driver),
			Env:          orderedEnvVars(agent.Env, redactSecrets),
			CapsetIDs:    slices.Clone(agent.CapsetIDs),
			Workspace:    cloneWorkspaceSpec(agent.Workspace),
			Scheduler:    cloneNormalizedSchedulerSpec(agent.Scheduler),
		})
	}
	slices.SortFunc(agents, func(a, b orderedAgentSpec) int {
		return compareString(a.Name, b.Name)
	})
	services := make([]orderedServiceSpec, 0, len(s.Services))
	for _, service := range s.Services {
		services = append(services, orderedServiceSpec{
			Name:         service.Name,
			Description:  service.Description,
			Runtime:      service.Runtime,
			Entry:        service.Entry,
			InputSchema:  service.InputSchema,
			OutputSchema: service.OutputSchema,
			ErrorSchema:  service.ErrorSchema,
			Timeout:      service.Timeout,
			Retry:        cloneRetryPolicySpec(service.Retry),
			Permissions:  clonePermissionSpecForOutput(service.Permissions),
			Env:          orderedEnvVars(service.Env, redactSecrets),
			Agents:       slices.Clone(service.Agents),
			Examples:     slices.Clone(service.Examples),
		})
	}
	slices.SortFunc(services, func(a, b orderedServiceSpec) int {
		return compareString(a.Name, b.Name)
	})
	triggers := slices.Clone(s.Triggers)
	slices.SortFunc(triggers, func(a, b NormalizedProjectTriggerSpec) int {
		return compareString(a.Name, b.Name)
	})
	return orderedProjectSpec{
		Name:        s.Name,
		Metadata:    cloneProjectMetadataSpec(s.Metadata),
		Variables:   orderedEnvVars(s.Variables, redactSecrets),
		Runtime:     runtime,
		Workspace:   cloneWorkspaceSpec(s.Workspace),
		Agents:      agents,
		Services:    services,
		Triggers:    triggers,
		Permissions: clonePermissionSpecForOutput(s.Permissions),
		Artifacts:   cloneArtifactPolicySpec(s.Artifacts),
		Network:     cloneNetworkSpecForOutput(s.Network),
	}
}

func (s *NormalizedProjectSpec) clone(redactSecrets bool) *NormalizedProjectSpec {
	ordered := s.ordered(redactSecrets)
	cloned := &NormalizedProjectSpec{
		Name:        ordered.Name,
		Metadata:    ordered.Metadata,
		Variables:   envVarMapFromOrdered(ordered.Variables),
		Runtime:     normalizedProjectRuntimeFromOrdered(ordered.Runtime),
		Workspace:   ordered.Workspace,
		Permissions: ordered.Permissions,
		Artifacts:   ordered.Artifacts,
		Network:     ordered.Network,
	}
	for _, agent := range ordered.Agents {
		cloned.Agents = append(cloned.Agents, NormalizedAgentSpec{
			Name:         agent.Name,
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Driver:       agent.Driver,
			Env:          envVarMapFromOrdered(agent.Env),
			CapsetIDs:    slices.Clone(agent.CapsetIDs),
			Workspace:    agent.Workspace,
			Scheduler:    agent.Scheduler,
		})
	}
	for _, service := range ordered.Services {
		cloned.Services = append(cloned.Services, NormalizedServiceSpec{
			Name:         service.Name,
			Description:  service.Description,
			Runtime:      service.Runtime,
			Entry:        service.Entry,
			InputSchema:  service.InputSchema,
			OutputSchema: service.OutputSchema,
			ErrorSchema:  service.ErrorSchema,
			Timeout:      service.Timeout,
			Retry:        service.Retry,
			Permissions:  service.Permissions,
			Env:          envVarMapFromOrdered(service.Env),
			Agents:       slices.Clone(service.Agents),
			Examples:     slices.Clone(service.Examples),
		})
	}
	cloned.Triggers = slices.Clone(ordered.Triggers)
	return cloned
}

func orderedProjectRuntime(value *NormalizedProjectRuntimeSpec, redactSecrets bool) *orderedProjectRuntimeSpec {
	if value == nil {
		return nil
	}
	return &orderedProjectRuntimeSpec{
		Driver:        value.Driver,
		Image:         value.Image,
		Env:           orderedEnvVars(value.Env, redactSecrets),
		Resources:     cloneStringMapForOutput(value.Resources),
		Network:       cloneNetworkSpecForOutput(value.Network),
		CleanupPolicy: value.CleanupPolicy,
	}
}

func normalizedProjectRuntimeFromOrdered(value *orderedProjectRuntimeSpec) *NormalizedProjectRuntimeSpec {
	if value == nil {
		return nil
	}
	return &NormalizedProjectRuntimeSpec{
		Driver:        value.Driver,
		Image:         value.Image,
		Env:           envVarMapFromOrdered(value.Env),
		Resources:     cloneStringMapForOutput(value.Resources),
		Network:       cloneNetworkSpecForOutput(value.Network),
		CleanupPolicy: value.CleanupPolicy,
	}
}

func clonePermissionSpecForOutput(value *PermissionSpec) *PermissionSpec {
	if value == nil {
		return nil
	}
	return &PermissionSpec{
		Agents:       slices.Clone(value.Agents),
		Capabilities: slices.Clone(value.Capabilities),
		Resources:    slices.Clone(value.Resources),
	}
}

func cloneStringMapForOutput(value map[string]string) map[string]string {
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
