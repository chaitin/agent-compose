package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	// DockerSandboxNetworkEnvName names the Docker network a newly-created
	// sandbox container joins.
	DockerSandboxNetworkEnvName = "DOCKER_SANDBOX_NETWORK"
	// DockerSandboxNetworkStrictEnvName selects the strict Docker sandbox
	// network profile: the configured network must be an existing
	// user-defined internal network, and every Docker sandbox start fails if it
	// is not. The switch is only read in SANDBOX_INIT_EGRESS=enforce mode,
	// because that is the only mode whose outer default-deny it completes.
	DockerSandboxNetworkStrictEnvName = "DOCKER_SANDBOX_NETWORK_STRICT"
)

// loadDockerSandboxNetworkStrict reads DOCKER_SANDBOX_NETWORK_STRICT. An unset
// value keeps the profile off. An unparseable value fails startup rather than
// falling back to off: this switch is a security posture, and a typo must not
// silently drop the network-namespace layer it asked for.
func loadDockerSandboxNetworkStrict() (bool, error) {
	raw := strings.TrimSpace(os.Getenv(DockerSandboxNetworkStrictEnvName))
	if raw == "" {
		return false, nil
	}
	strict, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false: %w", DockerSandboxNetworkStrictEnvName, err)
	}
	return strict, nil
}
