package compose

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

const defaultNamedNetwork = "default"

func normalizeNamedNetworkSpecs(values map[string]NamedNetworkSpec) (map[string]NamedNetworkSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	normalized := make(map[string]NamedNetworkSpec, len(values))
	for rawName, value := range values {
		name := strings.TrimSpace(rawName)
		if err := validateStableIdentifier(joinPath("networks", rawName), name, "network name"); err != nil {
			return nil, err
		}
		if _, exists := normalized[name]; exists {
			return nil, &ValidationError{Path: joinPath("networks", rawName), Message: fmt.Sprintf("duplicate network %q", name)}
		}
		driver := strings.ToLower(strings.TrimSpace(value.Driver))
		if driver == "" {
			driver = "bridge"
		}
		if driver != "bridge" {
			return nil, &ValidationError{Path: joinPath("networks", name) + ".driver", Message: fmt.Sprintf("unsupported network driver %q; only bridge is supported", driver)}
		}
		normalized[name] = NamedNetworkSpec{Driver: driver}
	}
	return normalized, nil
}

func normalizeNetworkAttachments(path string, values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	normalized := make([]string, 0, len(values))
	for index, raw := range values {
		name := strings.TrimSpace(raw)
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		if err := validateStableIdentifier(itemPath, name, "network name"); err != nil {
			return nil, err
		}
		if _, exists := seen[name]; exists {
			return nil, &ValidationError{Path: itemPath, Message: fmt.Sprintf("duplicate network %q", name)}
		}
		seen[name] = struct{}{}
		normalized = append(normalized, name)
	}
	return normalized, nil
}

func normalizeExposedPorts(path string, values []string) ([]string, error) {
	return normalizePortList(path, values, normalizeExposedPort)
}

func normalizePublishedPorts(path string, values []string) ([]string, error) {
	return normalizePortList(path, values, normalizePublishedPort)
}

func normalizePortList(path string, values []string, normalize func(string) (string, error)) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(values))
	items := make([]string, 0, len(values))
	for index, raw := range values {
		itemPath := fmt.Sprintf("%s[%d]", path, index)
		item, err := normalize(strings.TrimSpace(raw))
		if err != nil {
			return nil, &ValidationError{Path: itemPath, Message: err.Error()}
		}
		if _, exists := seen[item]; exists {
			return nil, &ValidationError{Path: itemPath, Message: fmt.Sprintf("duplicate port %q", item)}
		}
		seen[item] = struct{}{}
		items = append(items, item)
	}
	return items, nil
}

func normalizeExposedPort(raw string) (string, error) {
	port, protocol, err := parseContainerPort(raw)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(port) + "/" + protocol, nil
}

func normalizePublishedPort(raw string) (string, error) {
	parts := strings.Split(raw, ":")
	var hostIP, hostPortRaw, containerPortRaw string
	switch len(parts) {
	case 1:
		hostIP = "127.0.0.1"
		containerPortRaw = parts[0]
	case 2:
		hostIP = "127.0.0.1"
		hostPortRaw = parts[0]
		containerPortRaw = parts[1]
	case 3:
		hostIP = strings.TrimSpace(parts[0])
		hostPortRaw = strings.TrimSpace(parts[1])
		containerPortRaw = parts[2]
	default:
		return "", fmt.Errorf("published port must be container, host:container, or host_ip:host:container")
	}
	if net.ParseIP(hostIP) == nil {
		return "", fmt.Errorf("host IP %q is invalid", hostIP)
	}
	hostPort := 0
	if hostPortRaw != "" && hostPortRaw != "0" {
		var err error
		hostPort, err = parseTCPPort(hostPortRaw)
		if err != nil {
			return "", fmt.Errorf("invalid host port: %w", err)
		}
	}
	containerPort, protocol, err := parseContainerPort(containerPortRaw)
	if err != nil {
		return "", fmt.Errorf("invalid container port: %w", err)
	}
	return fmt.Sprintf("%s:%d:%d/%s", hostIP, hostPort, containerPort, protocol), nil
}

func parseContainerPort(raw string) (int, string, error) {
	portRaw, protocol, found := strings.Cut(strings.TrimSpace(raw), "/")
	if !found {
		protocol = "tcp"
	}
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol != "tcp" {
		return 0, "", fmt.Errorf("unsupported port protocol %q; only tcp is supported", protocol)
	}
	port, err := parseTCPPort(portRaw)
	if err != nil {
		return 0, "", err
	}
	return port, protocol, nil
}

func parseTCPPort(raw string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("port must be between 1 and 65535")
	}
	return port, nil
}

func validateProjectNetworkUsage(project *NormalizedProjectSpec) error {
	if project == nil {
		return nil
	}
	hasNamedNetworks := len(project.Networks) > 0
	needsDefault := false
	if hasNamedNetworks {
		for _, agent := range project.Agents {
			if len(agent.Networks) == 0 {
				needsDefault = true
				break
			}
		}
		if needsDefault {
			if _, exists := project.Networks[defaultNamedNetwork]; !exists {
				project.Networks[defaultNamedNetwork] = NamedNetworkSpec{Driver: "bridge"}
			}
		}
	}
	for index := range project.Agents {
		agent := &project.Agents[index]
		path := joinPath("agents", agent.Name)
		if len(agent.Expose) > 0 || len(agent.Ports) > 0 {
			if agent.Driver == nil || agent.Driver.Name != DriverDocker {
				return &ValidationError{Path: path + ".driver", Message: "expose and ports require the docker driver"}
			}
		}
		if !hasNamedNetworks {
			if len(agent.Networks) > 0 {
				return &ValidationError{Path: path + ".networks", Message: "agent networks require top-level networks"}
			}
			continue
		}
		if agent.Driver == nil || agent.Driver.Name != DriverDocker {
			return &ValidationError{Path: path + ".driver", Message: "project named networks require every agent to use the docker driver"}
		}
		if len(agent.Networks) == 0 {
			agent.Networks = []string{defaultNamedNetwork}
		}
		for attachmentIndex, name := range agent.Networks {
			if _, exists := project.Networks[name]; !exists {
				return &ValidationError{
					Path:    fmt.Sprintf("%s.networks[%d]", path, attachmentIndex),
					Message: fmt.Sprintf("network %q is not defined", name),
				}
			}
		}
	}
	return nil
}
