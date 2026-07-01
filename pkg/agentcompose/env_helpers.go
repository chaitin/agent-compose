package agentcompose

import (
	"sort"
	"strings"
)

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	merged := make(map[string]SessionEnvVar, len(items))
	for _, item := range items {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		merged[name] = SessionEnvVar{Name: name, Value: item.Value, Secret: item.Secret}
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	merged := make(map[string]SessionEnvVar, len(globalItems)+len(sessionItems))
	for _, item := range normalizeEnvItems(globalItems) {
		merged[item.Name] = item
	}
	for _, item := range normalizeEnvItems(sessionItems) {
		merged[item.Name] = item
	}
	if len(merged) == 0 {
		return nil
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]SessionEnvVar, 0, len(keys))
	for _, key := range keys {
		result = append(result, merged[key])
	}
	return result
}
