package model

import "strings"

func MergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	if len(globalItems) == 0 && len(sessionItems) == 0 {
		return nil
	}
	merged := make([]SessionEnvVar, 0, len(globalItems)+len(sessionItems))
	seen := map[string]int{}
	for _, item := range globalItems {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		item.Name = name
		seen[strings.ToUpper(name)] = len(merged)
		merged = append(merged, item)
	}
	for _, item := range sessionItems {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		item.Name = name
		key := strings.ToUpper(name)
		if index, ok := seen[key]; ok {
			merged[index] = item
			continue
		}
		seen[key] = len(merged)
		merged = append(merged, item)
	}
	if len(merged) == 0 {
		return nil
	}
	return merged
}

func mergeEnvItems(globalItems, sessionItems []SessionEnvVar) []SessionEnvVar {
	return MergeEnvItems(globalItems, sessionItems)
}

func FirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
