package main

import (
	"agent-compose/pkg/config"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

func normalizeCLIHost(name, value string) (string, error) {
	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("invalid %s %q: %w", name, value, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("invalid %s %q: scheme must be http or https", name, value)
	}
	if parsed.Host == "" {
		return "", fmt.Errorf("invalid %s %q: host is required", name, value)
	}
	return strings.TrimRight(value, "/"), nil
}

func resolveAgentComposeSocketForCLI(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		value = config.DefaultAgentComposeSocket()
	}
	if value == "" {
		return "", fmt.Errorf("AGENT_COMPOSE_SOCKET is empty")
	}
	if strings.IndexByte(value, 0) >= 0 {
		return "", fmt.Errorf("invalid AGENT_COMPOSE_SOCKET %q: path contains NUL byte", value)
	}
	resolved, err := filepath.Abs(value)
	if err != nil {
		return value, nil
	}
	return resolved, nil
}
