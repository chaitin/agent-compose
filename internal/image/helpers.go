package image

import (
	"fmt"
	"strings"
)

type imageBackendOpError struct {
	Op       string
	Endpoint string
	ImageRef string
	Err      error
}

func (e imageBackendOpError) Error() string {
	parts := []string{strings.TrimSpace(e.Op)}
	if e.ImageRef != "" {
		parts = append(parts, fmt.Sprintf("image %s", e.ImageRef))
	}
	if e.Endpoint != "" {
		parts = append(parts, fmt.Sprintf("endpoint %s", e.Endpoint))
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e imageBackendOpError) Unwrap() error {
	return e.Err
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func nonNegativeUint64(value int64) uint64 {
	if value <= 0 {
		return 0
	}
	return uint64(value)
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}
