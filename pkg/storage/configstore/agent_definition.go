package configstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-compose/pkg/capabilities"
	domain "agent-compose/pkg/model"
)

func EncodeAgentEnvJSON(items []domain.SandboxEnvVar) (string, error) {
	normalized := domain.NormalizeEnvItems(items)
	if normalized == nil {
		normalized = []domain.SandboxEnvVar{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode agent env items: %w", err)
	}
	return string(data), nil
}

func DecodeAgentEnvJSON(raw string) ([]domain.SandboxEnvVar, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []domain.SandboxEnvVar
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("decode agent env items: %w", err)
	}
	return domain.NormalizeEnvItems(items), nil
}

func EncodeAgentVolumesJSON(items []domain.VolumeMountSpec) (string, error) {
	normalized, err := domain.NormalizeVolumeMountSpecs(items)
	if err != nil {
		return "", err
	}
	if normalized == nil {
		normalized = []domain.VolumeMountSpec{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode agent volumes: %w", err)
	}
	return string(data), nil
}

func DecodeAgentVolumesJSON(raw string) ([]domain.VolumeMountSpec, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []domain.VolumeMountSpec
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("decode agent volumes: %w", err)
	}
	return domain.NormalizeVolumeMountSpecs(items)
}

func EncodeAgentSkillsJSON(items []domain.AgentSkill) (string, error) {
	normalized := domain.NormalizeAgentSkills(items)
	if normalized == nil {
		normalized = []domain.AgentSkill{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode agent skills: %w", err)
	}
	return string(data), nil
}

func DecodeAgentSkillsJSON(raw string) ([]domain.AgentSkill, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []domain.AgentSkill
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("decode agent skills: %w", err)
	}
	return domain.NormalizeAgentSkills(items), nil
}

func ScanAgentDefinition(scan func(dest ...any) error) (domain.AgentDefinition, error) {
	var item domain.AgentDefinition
	var enabled int
	var deletedAtRaw any
	var envJSON string
	var volumesJSON string
	var capsetIDsRaw string
	var skillsJSON string
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.Description, &enabled, &deletedAtRaw, &item.Provider, &item.Model, &item.SystemPrompt,
		&item.Driver, &item.GuestImage, &item.WorkspaceID, &envJSON, &volumesJSON, &item.ConfigJSON, &capsetIDsRaw, &skillsJSON,
		&item.ManagedProjectID, &item.ManagedProjectRevision, &item.ManagedAgentName, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("scan agent definition: %w", err)
	}
	envItems, err := DecodeAgentEnvJSON(envJSON)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	volumes, err := DecodeAgentVolumesJSON(volumesJSON)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	skills, err := DecodeAgentSkillsJSON(skillsJSON)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	item.Enabled = enabled != 0
	item.DeletedAt = ParseStoredTime(deletedAtRaw)
	item.EnvItems = envItems
	item.Volumes = volumes
	item.CapsetIDs = capabilities.DecodeCapsetIDs(capsetIDsRaw)
	item.Skills = skills
	item.ManagedProjectID = strings.TrimSpace(item.ManagedProjectID)
	item.ManagedAgentName = strings.TrimSpace(item.ManagedAgentName)
	item.CreatedAt = ParseStoredTime(createdAtRaw)
	item.UpdatedAt = ParseStoredTime(updatedAtRaw)
	return item, nil
}

func AgentMatchesQuery(item domain.AgentDefinition, query string) bool {
	if query == "" {
		return true
	}
	fields := []string{item.Name, item.Description, item.Provider, item.ManagedProjectID, item.ManagedAgentName}
	for _, field := range fields {
		if strings.Contains(strings.ToLower(field), query) {
			return true
		}
	}
	return false
}
