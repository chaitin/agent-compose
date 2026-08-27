package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

// ParseCanonicalJSON decodes the immutable representation produced by
// NormalizedProjectSpec.MarshalCanonicalJSON.
func ParseCanonicalJSON(data []byte) (*NormalizedProjectSpec, error) {
	var ordered orderedProjectSpec
	if err := json.Unmarshal(bytes.TrimSpace(data), &ordered); err != nil {
		return nil, fmt.Errorf("decode canonical project spec: %w", err)
	}
	applyHistoricalAgentEnabledDefaults(data, ordered.Agents)
	applyHistoricalSchedulerDefaults(ordered.Agents)
	return normalizedProjectSpecFromOrdered(ordered), nil
}

func normalizedProjectSpecFromOrdered(ordered orderedProjectSpec) *NormalizedProjectSpec {
	spec := &NormalizedProjectSpec{
		Name:           ordered.Name,
		Variables:      envVarMapFromOrdered(ordered.Variables),
		Workspaces:     workspaceMapFromOrdered(ordered.Workspaces),
		MCPServers:     mcpMapFromOrdered(ordered.MCPServers),
		OctoBusServers: octoBusMapFromOrdered(ordered.OctoBusServers),
		Volumes:        volumeMapFromOrdered(ordered.Volumes),
	}
	for _, agent := range ordered.Agents {
		spec.Agents = append(spec.Agents, NormalizedAgentSpec{
			Name:         agent.Name,
			Enabled:      agent.Enabled,
			DisplayName:  agent.DisplayName,
			Description:  agent.Description,
			InputSchema:  cloneJSONSchema(agent.InputSchema),
			OutputSchema: cloneJSONSchema(agent.OutputSchema),
			Provider:     agent.Provider,
			Model:        agent.Model,
			SystemPrompt: agent.SystemPrompt,
			Image:        agent.Image,
			Build:        agent.Build,
			Driver:       agent.Driver,
			Env:          envVarMapFromOrdered(agent.Env),
			MCPServers:   mcpMapFromOrdered(agent.MCPServers),
			CapsetIDs:    append([]string(nil), agent.CapsetIDs...),
			Skills:       cloneNormalizedSkillSpecs(agent.Skills),
			Volumes:      cloneNormalizedVolumeMountSpecs(agent.Volumes),
			Workspace:    cloneWorkspaceSpec(agent.Workspace),
			Sandbox:      agent.Sandbox,
			Scheduler:    cloneNormalizedSchedulerSpec(agent.Scheduler),
			Jupyter:      cloneJupyterSpec(agent.Jupyter),
		})
	}
	return spec
}

func applyHistoricalAgentEnabledDefaults(data []byte, agents []orderedAgentSpec) {
	var shape struct {
		Agents []struct {
			Enabled *bool `json:"enabled"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return
	}
	for index := range agents {
		if index >= len(shape.Agents) || shape.Agents[index].Enabled == nil {
			agents[index].Enabled = true
		}
	}
}

func applyHistoricalSchedulerDefaults(agents []orderedAgentSpec) {
	for index := range agents {
		scheduler := agents[index].Scheduler
		if scheduler == nil {
			continue
		}
		if strings.TrimSpace(scheduler.SandboxPolicy) == "" {
			scheduler.SandboxPolicy = domain.SchedulerSandboxPolicyNew
		}
		if strings.TrimSpace(scheduler.ConcurrencyPolicy) == "" {
			scheduler.ConcurrencyPolicy = domain.SchedulerConcurrencyPolicySkip
		}
	}
}
