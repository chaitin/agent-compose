package llms

import (
	"sort"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

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

func EnvItemsFromMap(values map[string]string, secret bool) []domain.SandboxEnvVar {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]domain.SandboxEnvVar, 0, len(keys))
	for _, key := range keys {
		items = append(items, domain.SandboxEnvVar{Name: key, Value: values[key], Secret: secret})
	}
	return items
}
