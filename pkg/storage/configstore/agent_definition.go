package configstore

import (
	"encoding/json"
	"fmt"
	"strings"

	"agent-compose/pkg/capabilities"
	domain "agent-compose/pkg/model"
)

func EncodeAgentEnvJSON(items []domain.SessionEnvVar) (string, error) {
	normalized := domain.NormalizeEnvItems(items)
	if normalized == nil {
		normalized = []domain.SessionEnvVar{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode agent env items: %w", err)
	}
	return string(data), nil
}

func DecodeAgentEnvJSON(raw string) ([]domain.SessionEnvVar, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []domain.SessionEnvVar
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("decode agent env items: %w", err)
	}
	return domain.NormalizeEnvItems(items), nil
}

func ScanAgentDefinition(scan func(dest ...any) error) (domain.AgentDefinition, error) {
	var item domain.AgentDefinition
	var enabled int
	var deletedAtRaw any
	var envJSON string
	var capsetIDsRaw string
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.Description, &enabled, &deletedAtRaw, &item.Provider, &item.Model, &item.SystemPrompt,
		&item.Driver, &item.GuestImage, &item.WorkspaceID, &envJSON, &item.ConfigJSON, &capsetIDsRaw,
		&item.ManagedProjectID, &item.ManagedProjectRevision, &item.ManagedAgentName, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.AgentDefinition{}, fmt.Errorf("scan agent definition: %w", err)
	}
	envItems, err := DecodeAgentEnvJSON(envJSON)
	if err != nil {
		return domain.AgentDefinition{}, err
	}
	item.Enabled = enabled != 0
	item.DeletedAt = ParseStoredTime(deletedAtRaw)
	item.EnvItems = envItems
	item.CapsetIDs = capabilities.DecodeCapsetIDs(capsetIDsRaw)
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
