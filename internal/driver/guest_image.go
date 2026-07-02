package driver

import "strings"

func resolveSessionGuestImage(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func ResolveSessionGuestImage(values ...string) string {
	return resolveSessionGuestImage(values...)
}
