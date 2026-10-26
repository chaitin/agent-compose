package compose

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/pkg/egress"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ParseCanonicalJSON decodes the immutable representation produced by
// NormalizedProjectSpec.MarshalCanonicalJSON.
func ParseCanonicalJSON(data []byte) (*NormalizedProjectSpec, error) {
	trimmed := bytes.TrimSpace(data)
	if err := rejectRemovedNetworkFields(trimmed); err != nil {
		return nil, err
	}
	var ordered orderedProjectSpec
	if err := json.Unmarshal(trimmed, &ordered); err != nil {
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
			Policy:       cloneNormalizedSandboxSpec(agent.Policy),
			Sandbox:      cloneNormalizedSandboxSpec(agent.Sandbox),
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

// rejectRemovedNetworkFields turns a legacy canonical spec's removed protocol
// field into an actionable migration error. The field is no longer represented
// in the ordered types, so decoding would silently drop it; a spec that named a
// protocol must be rejected rather than accepted with an unstated semantic
// change. The field's own default, "any", is the exception: normalization always
// wrote it, every spec stored by an older daemon therefore carries it, and
// dropping it leaves the meaning unchanged, so requiring a re-apply for that
// value alone would make existing projects unloadable for no behavior change.
func rejectRemovedNetworkFields(data []byte) error {
	var shape struct {
		Agents []struct {
			Name    string          `json:"name"`
			Policy  json.RawMessage `json:"policy"`
			Sandbox json.RawMessage `json:"sandbox"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		// Let the main decode report malformed JSON with its existing error.
		return nil
	}
	for _, agent := range shape.Agents {
		for _, candidate := range []struct {
			name string
			raw  json.RawMessage
		}{
			{name: "policy", raw: agent.Policy},
			{name: "sandbox", raw: agent.Sandbox},
		} {
			if len(candidate.raw) == 0 {
				continue
			}
			var spec struct {
				Network *struct {
					Allow []map[string]json.RawMessage `json:"allow"`
					Deny  []map[string]json.RawMessage `json:"deny"`
				} `json:"network"`
			}
			if err := json.Unmarshal(candidate.raw, &spec); err != nil || spec.Network == nil {
				continue
			}
			for index, entry := range spec.Network.Allow {
				if protocol, ok := entry["protocol"]; ok && !removedProtocolIsAny(protocol) {
					return fmt.Errorf("canonical project spec agent %q %s.network.allow[%d] still carries the removed protocol field; remove it and re-apply the project", agent.Name, candidate.name, index)
				}
			}
			for index, entry := range spec.Network.Deny {
				if protocol, ok := entry["protocol"]; ok && !removedProtocolIsAny(protocol) {
					return fmt.Errorf("canonical project spec agent %q %s.network.deny[%d] still carries the removed protocol field; remove it and re-apply the project", agent.Name, candidate.name, index)
				}
			}
		}
	}
	return nil
}

// removedProtocolIsAny reports whether a legacy entry's protocol value is the
// field's default. The comparison is case-insensitive and tolerant of padding
// because the value is data written by older releases, not input this decode
// validates; a value it cannot read as a string is not the default.
func removedProtocolIsAny(raw json.RawMessage) bool {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(value), string(egress.ProtocolAny))
}
