package llms

import (
	"fmt"
	"sort"
	"strings"

	domain "agent-compose/pkg/model"
)

func SplitOpenCodeModel(model string) (string, string, error) {
	model = strings.TrimSpace(model)
	providerID, modelName, ok := strings.Cut(model, "/")
	providerID = strings.TrimSpace(providerID)
	modelName = strings.TrimSpace(modelName)
	if !ok || providerID == "" || modelName == "" {
		return "", "", fmt.Errorf("opencode model must be in provider/model format")
	}
	return providerID, modelName, nil
}

func MergeManagedExecEnv(base map[string]string, managed map[string]string) map[string]string {
	if len(base) == 0 && len(managed) == 0 {
		return nil
	}
	result := make(map[string]string, len(base)+len(managed))
	for key, value := range base {
		if ProviderKeyName(key) {
			continue
		}
		result[key] = value
	}
	for key, value := range managed {
		result[key] = value
	}
	return result
}

func EnvItemsFromMap(values map[string]string, secret bool) []domain.SessionEnvVar {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]domain.SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		items = append(items, domain.SessionEnvVar{Name: key, Value: values[key], Secret: secret})
	}
	return items
}
