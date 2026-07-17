package main

import (
	"fmt"
	"path/filepath"
	"strings"
)

func parseBuildArgs(values []string) (map[string]string, error) {
	return parseCLIStringMap(values, "--build-arg")
}

func parseCLIStringMap(values []string, flagName string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, argValue, ok := strings.Cut(value, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid %s %q: expected KEY=VALUE", flagName, value)
		}
		result[key] = argValue
	}
	return result, nil
}

func resolveComposeBuildPath(composeDir string, value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		value = "."
	}
	if filepath.IsAbs(value) {
		return value
	}
	return filepath.Join(composeDir, value)
}

func normalizeCLIStringList(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
