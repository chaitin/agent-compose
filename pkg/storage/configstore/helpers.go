package configstore

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	domain "agent-compose/pkg/model"
	"agent-compose/pkg/storage/storeutil"
)

func NormalizeWorkspaceConfig(item domain.WorkspaceConfig, assignID bool) (domain.WorkspaceConfig, error) {
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.Type = strings.ToLower(strings.TrimSpace(item.Type))
	item.ConfigJSON = strings.TrimSpace(item.ConfigJSON)
	item.Comment = strings.TrimSpace(item.Comment)
	if assignID && item.ID == "" {
		item.ID = uuid.NewString()
	}
	if item.ID == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace config id is required")
	}
	if item.Name == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace config name is required")
	}
	if item.Type == "" {
		return domain.WorkspaceConfig{}, fmt.Errorf("workspace config type is required")
	}
	if item.Type != "git" && item.Type != "file" {
		return domain.WorkspaceConfig{}, fmt.Errorf("unsupported workspace config type %q", item.Type)
	}
	if item.ConfigJSON == "" {
		item.ConfigJSON = "{}"
	}
	return item, nil
}

func ScanWorkspaceConfig(scan func(dest ...any) error) (domain.WorkspaceConfig, error) {
	var item domain.WorkspaceConfig
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.Type, &item.ConfigJSON, &item.Comment, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.WorkspaceConfig{}, fmt.Errorf("scan workspace config: %w", err)
	}
	item.CreatedAt = ParseStoredTime(createdAtRaw)
	item.UpdatedAt = ParseStoredTime(updatedAtRaw)
	return item, nil
}

func ParseStoredLoaderTriggerTime(value any) time.Time {
	switch typed := value.(type) {
	case nil:
		return time.Time{}
	case int64:
		return storeutil.ParseStoredUnixTimeAuto(typed)
	case int:
		return storeutil.ParseStoredUnixTimeAuto(int64(typed))
	case float64:
		return storeutil.ParseStoredUnixTimeAuto(int64(typed))
	case []byte:
		return ParseStoredLoaderTriggerTime(string(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}
		}
		if unixValue, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return storeutil.ParseStoredUnixTimeAuto(unixValue)
		}
		return ParseStoredTime(trimmed)
	default:
		return ParseStoredTime(value)
	}
}

func ParseStoredTime(value any) time.Time {
	switch typed := value.(type) {
	case nil:
		return time.Time{}
	case int64:
		return storeutil.ParseStoredUnixTimeAuto(typed)
	case int:
		return storeutil.ParseStoredUnixTimeAuto(int64(typed))
	case float64:
		return storeutil.ParseStoredUnixTimeAuto(int64(typed))
	case []byte:
		return ParseStoredTime(string(typed))
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return time.Time{}
		}
		if unixValue, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return storeutil.ParseStoredUnixTimeAuto(unixValue)
		}
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05.000Z"} {
			if parsed, err := time.Parse(layout, trimmed); err == nil {
				return parsed.UTC()
			}
		}
	}
	return time.Time{}
}

func BoolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
