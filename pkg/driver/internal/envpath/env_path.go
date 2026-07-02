//go:build cgo

package envpath

import (
	"os"
	"strings"
)

func Prepend(key, value string) {
	if strings.TrimSpace(value) == "" {
		return
	}
	current := os.Getenv(key)
	parts := []string{value}
	for _, item := range strings.Split(current, ":") {
		if strings.TrimSpace(item) == "" || item == value {
			continue
		}
		parts = append(parts, item)
	}
	_ = os.Setenv(key, strings.Join(parts, ":"))
}
