package loaders

import (
	"fmt"
	"strings"
	"time"
)

func ParseRunTimeout(raw string) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	timeout, err := time.ParseDuration(raw)
	if err != nil {
		return 0, err
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("loader run timeout must be positive")
	}
	return timeout, nil
}
