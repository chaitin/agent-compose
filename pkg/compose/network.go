package compose

import (
	"fmt"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

const NetworkDriverPortMapping = "port_mapping"

type ExposedPortSpec struct {
	Target   int    `yaml:"target" json:"target"`
	HostPort int    `yaml:"host_port,omitempty" json:"host_port,omitempty"`
	Protocol string `yaml:"protocol" json:"protocol"`
}

func (s *ExposedPortSpec) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		parsed, err := parseExposedPort(value.Value)
		if err != nil {
			return err
		}
		*s = parsed
		return nil
	case yaml.MappingNode:
		type exposedPortSpec ExposedPortSpec
		var decoded exposedPortSpec
		if err := value.Decode(&decoded); err != nil {
			return err
		}
		*s = ExposedPortSpec(decoded)
		return nil
	default:
		return fmt.Errorf("expose entry must use short or long syntax")
	}
}

func (s ExposedPortSpec) MarshalYAML() (any, error) {
	if s.HostPort == 0 && (s.Protocol == "" || s.Protocol == "tcp") {
		return strconv.Itoa(s.Target), nil
	}
	if s.HostPort != 0 {
		type exposedPortSpec ExposedPortSpec
		return exposedPortSpec(s), nil
	}
	return fmt.Sprintf("%d/%s", s.Target, s.Protocol), nil
}

type PublishedPortSpec struct {
	HostIP    string `yaml:"host_ip" json:"host_ip"`
	Published int    `yaml:"published" json:"published"`
	Target    int    `yaml:"target" json:"target"`
	Protocol  string `yaml:"protocol" json:"protocol"`
}

func (s *PublishedPortSpec) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind != yaml.ScalarNode {
		return fmt.Errorf("ports entry must use short syntax")
	}
	parsed, err := parsePublishedPort(value.Value)
	if err != nil {
		return err
	}
	*s = parsed
	return nil
}

func (s PublishedPortSpec) MarshalYAML() (any, error) {
	protocol := ""
	if s.Protocol != "" && s.Protocol != "tcp" {
		protocol = "/" + s.Protocol
	}
	if s.Published == 0 {
		return strconv.Itoa(s.Target) + protocol, nil
	}
	if s.HostIP == "" || s.HostIP == "127.0.0.1" {
		return fmt.Sprintf("%d:%d%s", s.Published, s.Target, protocol), nil
	}
	return fmt.Sprintf("%s:%d:%d%s", s.HostIP, s.Published, s.Target, protocol), nil
}

func normalizeProjectNetworks(values map[string]ProjectNetworkSpec) (map[string]ProjectNetworkSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	result := make(map[string]ProjectNetworkSpec, len(values))
	for _, rawName := range keys {
		name := strings.TrimSpace(rawName)
		if err := validateStableIdentifier(joinPath("networks", rawName), name, "network name"); err != nil {
			return nil, err
		}
		if _, exists := result[name]; exists {
			return nil, &ValidationError{Path: joinPath("networks", rawName), Message: fmt.Sprintf("duplicate network %q", name)}
		}
		driver := strings.ToLower(strings.TrimSpace(values[rawName].Driver))
		if driver == "" {
			driver = NetworkDriverPortMapping
		}
		if driver != NetworkDriverPortMapping {
			return nil, &ValidationError{Path: joinPath("networks", name) + ".driver", Message: fmt.Sprintf("unsupported network driver %q", driver)}
		}
		result[name] = ProjectNetworkSpec{Driver: driver}
	}
	return result, nil
}

func normalizeAgentNetworkConfig(path string, agent AgentSpec, available map[string]ProjectNetworkSpec, enabled bool) ([]string, []ExposedPortSpec, []PublishedPortSpec, error) {
	expose, err := normalizeExposedPorts(path+".expose", agent.Expose)
	if err != nil {
		return nil, nil, nil, err
	}
	ports, err := normalizePublishedPorts(path+".ports", agent.Ports)
	if err != nil {
		return nil, nil, nil, err
	}
	if !enabled {
		return nil, expose, ports, nil
	}
	networks := normalizeStringList(agent.Networks)
	if len(networks) == 0 {
		networks = []string{"default"}
	}
	for _, name := range networks {
		if _, ok := available[name]; !ok {
			return nil, nil, nil, &ValidationError{Path: path + ".networks", Message: fmt.Sprintf("unknown network %q", name)}
		}
	}
	return networks, expose, ports, nil
}

func normalizeExposedPorts(path string, values []ExposedPortSpec) ([]ExposedPortSpec, error) {
	result := make([]ExposedPortSpec, 0, len(values))
	seenTargets := make(map[int]struct{}, len(values))
	seenHostPorts := make(map[int]struct{}, len(values))
	for index, value := range values {
		protocol, err := normalizeTCPProtocol(value.Protocol)
		if err != nil {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d]", path, index), Message: err.Error()}
		}
		if err := validatePort(value.Target); err != nil {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d].target", path, index), Message: err.Error()}
		}
		if value.HostPort < 0 || value.HostPort > 65535 {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d].host_port", path, index), Message: "port must be between 1 and 65535 or omitted"}
		}
		if _, ok := seenTargets[value.Target]; ok {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d]", path, index), Message: fmt.Sprintf("duplicate exposed TCP port %d", value.Target)}
		}
		seenTargets[value.Target] = struct{}{}
		if value.HostPort != 0 {
			if _, ok := seenHostPorts[value.HostPort]; ok {
				return nil, &ValidationError{Path: fmt.Sprintf("%s[%d].host_port", path, index), Message: fmt.Sprintf("duplicate listener TCP port %d", value.HostPort)}
			}
			seenHostPorts[value.HostPort] = struct{}{}
		}
		result = append(result, ExposedPortSpec{Target: value.Target, HostPort: value.HostPort, Protocol: protocol})
	}
	slices.SortFunc(result, func(a, b ExposedPortSpec) int { return a.Target - b.Target })
	return result, nil
}

func normalizePublishedPorts(path string, values []PublishedPortSpec) ([]PublishedPortSpec, error) {
	result := make([]PublishedPortSpec, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		protocol, err := normalizeTCPProtocol(value.Protocol)
		if err != nil {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d]", path, index), Message: err.Error()}
		}
		if err := validatePort(value.Target); err != nil {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d].target", path, index), Message: err.Error()}
		}
		if value.Published < 0 || value.Published > 65535 {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d].published", path, index), Message: "port must be between 1 and 65535 or omitted"}
		}
		hostIP := strings.TrimSpace(value.HostIP)
		if hostIP == "" {
			hostIP = "127.0.0.1"
		}
		addr, err := netip.ParseAddr(hostIP)
		if err != nil || !addr.Is4() {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d].host_ip", path, index), Message: "host IP must be a valid IPv4 address"}
		}
		key := fmt.Sprintf("%s/%d/%d", hostIP, value.Published, value.Target)
		if _, ok := seen[key]; ok {
			return nil, &ValidationError{Path: fmt.Sprintf("%s[%d]", path, index), Message: "duplicate published TCP port"}
		}
		seen[key] = struct{}{}
		result = append(result, PublishedPortSpec{HostIP: hostIP, Published: value.Published, Target: value.Target, Protocol: protocol})
	}
	slices.SortFunc(result, func(a, b PublishedPortSpec) int {
		if compare := strings.Compare(a.HostIP, b.HostIP); compare != 0 {
			return compare
		}
		if a.Published != b.Published {
			return a.Published - b.Published
		}
		return a.Target - b.Target
	})
	return result, nil
}

func parseExposedPort(raw string) (ExposedPortSpec, error) {
	port, protocol, err := splitPortProtocol(raw)
	if err != nil {
		return ExposedPortSpec{}, err
	}
	target, err := parsePort(port)
	if err != nil {
		return ExposedPortSpec{}, err
	}
	return ExposedPortSpec{Target: target, Protocol: protocol}, nil
}

func parsePublishedPort(raw string) (PublishedPortSpec, error) {
	value, protocol, err := splitPortProtocol(raw)
	if err != nil {
		return PublishedPortSpec{}, err
	}
	parts := strings.Split(value, ":")
	result := PublishedPortSpec{HostIP: "127.0.0.1", Protocol: protocol}
	switch len(parts) {
	case 1:
		result.Target, err = parsePort(parts[0])
	case 2:
		result.Published, err = parsePort(parts[0])
		if err == nil {
			result.Target, err = parsePort(parts[1])
		}
	case 3:
		result.HostIP = strings.TrimSpace(parts[0])
		result.Published, err = parsePort(parts[1])
		if err == nil {
			result.Target, err = parsePort(parts[2])
		}
	default:
		return PublishedPortSpec{}, fmt.Errorf("ports entry %q must use TARGET, PUBLISHED:TARGET, or HOST_IP:PUBLISHED:TARGET", raw)
	}
	if err != nil {
		return PublishedPortSpec{}, err
	}
	return result, nil
}

func splitPortProtocol(raw string) (string, string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", "", fmt.Errorf("port entry is required")
	}
	protocol := "tcp"
	if base, suffix, ok := strings.Cut(value, "/"); ok {
		value = strings.TrimSpace(base)
		protocol = strings.ToLower(strings.TrimSpace(suffix))
	}
	protocol, err := normalizeTCPProtocol(protocol)
	if err != nil {
		return "", "", err
	}
	return value, protocol, nil
}

func normalizeTCPProtocol(protocol string) (string, error) {
	protocol = strings.ToLower(strings.TrimSpace(protocol))
	if protocol == "" {
		protocol = "tcp"
	}
	if protocol != "tcp" {
		return "", fmt.Errorf("only TCP ports are supported")
	}
	return protocol, nil
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0, fmt.Errorf("invalid port %q", value)
	}
	if err := validatePort(port); err != nil {
		return 0, err
	}
	return port, nil
}

func validatePort(port int) error {
	if port < 1 || port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	return nil
}
