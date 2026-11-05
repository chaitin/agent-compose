package driver

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"sort"
	"strings"

	appconfig "github.com/chaitin/agent-compose/pkg/config"
	"github.com/chaitin/agent-compose/pkg/sandboxinit"
	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

// dockerDaemonTopology is the L2 placement decision for Docker sandboxes: which
// network a sandbox joins and whether the daemon itself runs containerized.
type dockerDaemonTopology struct {
	networkMode   containerapi.NetworkMode
	containerized bool
	// strict reports that the strict internal-network profile was selected: the
	// sandbox joins only the configured internal network, so every process in
	// its network namespace is default-denied off that network.
	strict bool
}

// dockerSandboxNetworkInfo is the subset of a Docker network's inspect result
// the placement resolver needs. Gateways are the internal network's host
// gateway addresses; a sandbox on that network reaches the host through one of
// them.
type dockerSandboxNetworkInfo struct {
	Internal bool
	Gateways []string
}

// dockerDaemonEndpoint is the daemon container's identity on one Docker
// network: the names Docker's embedded DNS resolves for it there, and its
// addresses on that network.
type dockerDaemonEndpoint struct {
	aliases   []string
	addresses []string
}

// DockerSandboxNetworkStrictRequested reports whether the deployment selected
// the strict Docker sandbox network profile. The switch is only read in
// SANDBOX_INIT_EGRESS=enforce mode, because that is the mode whose outer
// default-deny it completes; audit and off keep the existing placement so an
// operator can set the switch before changing modes. It is exported because
// project admission uses the same predicate the creation path uses, so the
// report cannot claim a network layer a sandbox would not get.
func DockerSandboxNetworkStrictRequested(config *appconfig.Config) bool {
	if config == nil || !config.DockerSandboxNetworkStrict {
		return false
	}
	return strings.TrimSpace(config.SandboxInitEgress) == string(sandboxinit.EgressModeEnforce)
}

func (r *dockerRuntime) dockerDaemonTopology(ctx context.Context, dockerClient *client.Client) (dockerDaemonTopology, error) {
	networkName, containerized, ambiguous, daemonEndpoints, err := r.dockerNetworkNameFromSelfContainer(ctx, dockerClient)
	if err != nil {
		slog.Warn("failed to inspect current docker container network; falling back to default docker network", "error", err)
		networkName, containerized, ambiguous, daemonEndpoints = "", false, false, nil
	}

	topology, err := selectDockerDaemonTopology(dockerDaemonTopologyInput{
		explicitNetwork: r.config.DockerSandboxNetwork,
		detectedNetwork: networkName,
		daemonEndpoints: daemonEndpoints,
		containerized:   containerized,
		ambiguous:       ambiguous,
		strict:          DockerSandboxNetworkStrictRequested(r.config),
		runtimeBaseURL:  r.config.RuntimeBaseURL,
		networkInspect: func(name string) (dockerSandboxNetworkInfo, error) {
			inspected, inspectErr := dockerClient.NetworkInspect(ctx, name, networkapi.InspectOptions{})
			if inspectErr != nil {
				return dockerSandboxNetworkInfo{}, inspectErr
			}
			return dockerSandboxNetworkInfoFromInspect(inspected), nil
		},
	})
	if err != nil {
		return dockerDaemonTopology{}, err
	}
	if topology.networkMode == containerapi.NetworkMode("default") {
		slog.Warn("current process is not running in an inspectable docker container; falling back to default docker network")
	}
	return topology, nil
}

// dockerSandboxNetworkInfoFromInspect adapts Docker's network inspect result to
// the resolver's pure input.
func dockerSandboxNetworkInfoFromInspect(inspected networkapi.Inspect) dockerSandboxNetworkInfo {
	info := dockerSandboxNetworkInfo{Internal: inspected.Internal}
	for _, config := range inspected.IPAM.Config {
		if gateway := strings.TrimSpace(config.Gateway); gateway != "" {
			info.Gateways = append(info.Gateways, gateway)
		}
	}
	sort.Strings(info.Gateways)
	return info
}

// dockerDaemonTopologyInput bundles the resolver's inputs so it stays a
// single-argument pure function under the repository argument-count limit.
type dockerDaemonTopologyInput struct {
	explicitNetwork string
	detectedNetwork string
	// daemonEndpoints is keyed by network name and describes the daemon
	// container's aliases and addresses on each attached network.
	daemonEndpoints map[string]dockerDaemonEndpoint
	containerized   bool
	ambiguous       bool
	strict          bool
	// runtimeBaseURL is AGENT_COMPOSE_RUNTIME_BASE_URL: the address a sandbox
	// uses to reach the daemon. Strict mode requires it to be explicit and
	// reachable from the internal network.
	runtimeBaseURL string
	// networkInspect reports the network's inspect facts, or the inspect error
	// that makes it unavailable.
	networkInspect func(string) (dockerSandboxNetworkInfo, error)
}

func (input dockerDaemonTopologyInput) hasDaemonNetwork(name string) bool {
	_, ok := input.daemonEndpoints[name]
	return ok
}

func (input dockerDaemonTopologyInput) daemonEndpoint(name string) dockerDaemonEndpoint {
	return input.daemonEndpoints[name]
}

// selectDockerDaemonTopology resolves the network a Docker sandbox joins from
// the explicit configuration and the daemon's self-inspected network. It is pure
// so the precedence and fail-closed behavior stay testable without a Docker
// engine. An explicit network wins, must exist, must not be a Docker built-in
// (host/none/bridge/default), and — when the daemon is containerized — must be
// one of the networks the daemon is attached to (networkInspect reports the
// network or the inspect error, daemonEndpoints reports the attached networks).
// Without an explicit network, the daemon's own network is used, falling back to
// Docker's default network when the daemon is not running in an inspectable
// container; a containerized daemon attached to more than one non-default
// network is ambiguous and fails closed instead of guessing, since the overlay
// injects DOCKER_SANDBOX_NETWORK whenever it dual-homes the daemon.
//
// When strict is true the resolver instead requires an explicit user-defined
// internal network and a daemon-reachable runtime base URL, and never falls
// back to the daemon or default network.
func selectDockerDaemonTopology(input dockerDaemonTopologyInput) (dockerDaemonTopology, error) {
	name := strings.TrimSpace(input.explicitNetwork)
	if input.strict {
		return selectDockerStrictDaemonTopology(input, name)
	}
	if name != "" {
		if isDockerBuiltinNetworkName(name) {
			return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q is not an isolated user-defined network", name)
		}
		if err := input.inspectNetwork(name); err != nil {
			return dockerDaemonTopology{}, err
		}
		if input.containerized && !input.hasDaemonNetwork(name) {
			return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q is not attached to the daemon; attach the daemon to it or enable the docker-compose.sandbox-network.yml overlay", name)
		}
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode(name), containerized: input.containerized}, nil
	}
	if input.ambiguous {
		return dockerDaemonTopology{}, fmt.Errorf("docker daemon is attached to multiple non-default networks; set DOCKER_SANDBOX_NETWORK or enable the docker-compose.sandbox-network.yml overlay")
	}
	if input.detectedNetwork == "" {
		return dockerDaemonTopology{networkMode: containerapi.NetworkMode("default")}, nil
	}
	return dockerDaemonTopology{networkMode: containerapi.NetworkMode(input.detectedNetwork), containerized: input.containerized}, nil
}

// inspectNetwork reports the network's facts through the injected inspector,
// normalizing the unavailable-network error so every caller states the same
// thing.
func (input dockerDaemonTopologyInput) inspectNetwork(name string) error {
	if input.networkInspect == nil {
		return fmt.Errorf("docker sandbox network %q unavailable: no Docker network inspector is configured", name)
	}
	if _, err := input.networkInspect(name); err != nil {
		return fmt.Errorf("docker sandbox network %q unavailable: %w", name, err)
	}
	return nil
}

// selectDockerStrictDaemonTopology is the fail-closed strict profile. It never
// falls back: the configured network must exist as a user-defined internal
// network, the daemon must be attached to it when containerized, and the
// configured runtime base URL must name an address a sandbox can reach on that
// network.
func selectDockerStrictDaemonTopology(input dockerDaemonTopologyInput, name string) (dockerDaemonTopology, error) {
	if name == "" {
		return dockerDaemonTopology{}, fmt.Errorf("%s=true requires %s to name an existing user-defined Docker network created with --internal; refusing to fall back to the daemon or default network", appconfig.DockerSandboxNetworkStrictEnvName, appconfig.DockerSandboxNetworkEnvName)
	}
	if isDockerBuiltinNetworkName(name) {
		return dockerDaemonTopology{}, fmt.Errorf("%s=true requires a user-defined internal network; %q is a Docker built-in network", appconfig.DockerSandboxNetworkStrictEnvName, name)
	}
	if input.networkInspect == nil {
		return dockerDaemonTopology{}, fmt.Errorf("cannot validate strict docker sandbox network %q: no Docker network inspector is configured", name)
	}
	info, err := input.networkInspect(name)
	if err != nil {
		return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q unavailable: %w", name, err)
	}
	if !info.Internal {
		return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q is not internal; %s=true requires a user-defined network created with docker network create --internal", name, appconfig.DockerSandboxNetworkStrictEnvName)
	}
	if input.containerized && !input.hasDaemonNetwork(name) {
		return dockerDaemonTopology{}, fmt.Errorf("docker sandbox network %q is not attached to the daemon; attach the daemon to that internal network or enable the docker-compose.sandbox-network.yml overlay with its strict network", name)
	}
	if err := validateDockerStrictRuntimeBaseURL(input, name, info); err != nil {
		return dockerDaemonTopology{}, err
	}
	return dockerDaemonTopology{networkMode: containerapi.NetworkMode(name), containerized: input.containerized, strict: true}, nil
}

// validateDockerStrictRuntimeBaseURL checks the deployment requirement that
// makes the internal network usable: the daemon has to be reachable from the
// sandbox at AGENT_COMPOSE_RUNTIME_BASE_URL through the network gateway or a
// daemon alias on that network. It validates form and, when the daemon is
// containerized (so its endpoints are inspectable), that an address literal or
// name matches one of them. Whether a name resolves inside the sandbox cannot
// be proven from the host, so that part stays a documented deployment
// requirement rather than an invented per-sandbox gateway rewrite.
func validateDockerStrictRuntimeBaseURL(input dockerDaemonTopologyInput, networkName string, info dockerSandboxNetworkInfo) error {
	raw := strings.TrimSpace(input.runtimeBaseURL)
	if raw == "" {
		return fmt.Errorf("%s=true requires AGENT_COMPOSE_RUNTIME_BASE_URL to name the daemon on the internal sandbox network %q (its alias on that network, or its gateway address); an unset URL leaves the sandbox with no route to the daemon", appconfig.DockerSandboxNetworkStrictEnvName, networkName)
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("AGENT_COMPOSE_RUNTIME_BASE_URL %q must be an absolute http(s) URL: %w", raw, err)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if (scheme != "http" && scheme != "https") || parsed.Hostname() == "" {
		return fmt.Errorf("AGENT_COMPOSE_RUNTIME_BASE_URL %q must be an absolute http(s) URL with a host", raw)
	}
	host := parsed.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		return validateDockerStrictRuntimeBaseIP(input, networkName, info, ip)
	}
	if strings.EqualFold(host, "localhost") {
		return loopbackDockerRuntimeBaseURL(raw)
	}
	if input.containerized {
		endpoint := input.daemonEndpoint(networkName)
		if !containsFold(endpoint.aliases, host) {
			return fmt.Errorf("AGENT_COMPOSE_RUNTIME_BASE_URL host %q is not an alias of the daemon on the internal sandbox network %q; Docker DNS on that network resolves only attached containers (daemon aliases: %v)", host, networkName, endpoint.aliases)
		}
	}
	return nil
}

func validateDockerStrictRuntimeBaseIP(input dockerDaemonTopologyInput, networkName string, info dockerSandboxNetworkInfo, ip net.IP) error {
	if ip.IsLoopback() || ip.IsUnspecified() {
		return loopbackDockerRuntimeBaseURL(ip.String())
	}
	if containsDockerAddress(info.Gateways, ip) {
		return nil
	}
	if input.containerized {
		endpoint := input.daemonEndpoint(networkName)
		if containsDockerAddress(endpoint.addresses, ip) {
			return nil
		}
		return fmt.Errorf("AGENT_COMPOSE_RUNTIME_BASE_URL address %s is neither the internal network gateway nor an address of the daemon on the internal sandbox network %q; the sandbox has no route there (daemon addresses: %v, gateways: %v)", ip, networkName, endpoint.addresses, info.Gateways)
	}
	return fmt.Errorf("AGENT_COMPOSE_RUNTIME_BASE_URL address %s is not the gateway of the internal sandbox network %q (gateways: %v); a native daemon is reachable from an internal network only through that gateway", ip, networkName, info.Gateways)
}

func loopbackDockerRuntimeBaseURL(raw string) error {
	return fmt.Errorf("AGENT_COMPOSE_RUNTIME_BASE_URL %q is loopback; a sandbox on the internal network cannot reach the daemon through it, so set the daemon's alias on that network or its gateway address", raw)
}

// isDockerBuiltinNetworkName reports whether name is one of Docker's built-in
// networks. Both strict and non-strict explicit placement reject them, since a
// built-in network is not an isolated user-defined network.
func isDockerBuiltinNetworkName(name string) bool {
	switch name {
	case "host", "none", "bridge", "default":
		return true
	default:
		return false
	}
}

// containsFold reports whether any candidate equals name case-insensitively.
func containsFold(candidates []string, name string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(strings.TrimSpace(candidate), name) {
			return true
		}
	}
	return false
}

// containsDockerAddress reports whether any raw address parses to want.
func containsDockerAddress(candidates []string, want net.IP) bool {
	for _, candidate := range candidates {
		if ip := net.ParseIP(strings.TrimSpace(candidate)); ip != nil && ip.Equal(want) {
			return true
		}
	}
	return false
}

func (r *dockerRuntime) dockerNetworkNameFromSelfContainer(ctx context.Context, dockerClient *client.Client) (string, bool, bool, map[string]dockerDaemonEndpoint, error) {
	hostname, err := os.Hostname()
	if err != nil {
		return "", false, false, nil, nil
	}
	hostname = strings.TrimSpace(hostname)
	if hostname == "" {
		return "", false, false, nil, nil
	}

	containerInfo, err := dockerClient.ContainerInspect(ctx, hostname)
	if err != nil {
		if isDockerNotFound(err) {
			return "", false, false, nil, nil
		}
		return "", false, false, nil, fmt.Errorf("inspect current docker container %s: %w", hostname, err)
	}
	networkName, ok, ambiguous := selectDockerNetworkName(containerInfo)
	return networkName, ok, ambiguous, dockerDaemonEndpoints(containerInfo), nil
}

// dockerDaemonEndpoints returns the daemon container's aliases and addresses on
// every attached network. The container's own name is a Docker-provided DNS
// alias on each network, so it is included even when an older engine omits it
// from EndpointSettings.Aliases.
func dockerDaemonEndpoints(containerInfo containerapi.InspectResponse) map[string]dockerDaemonEndpoint {
	if containerInfo.NetworkSettings == nil || len(containerInfo.NetworkSettings.Networks) == 0 {
		return nil
	}
	containerName := ""
	if containerInfo.ContainerJSONBase != nil {
		containerName = strings.TrimPrefix(strings.TrimSpace(containerInfo.Name), "/")
	}
	endpoints := make(map[string]dockerDaemonEndpoint, len(containerInfo.NetworkSettings.Networks))
	for rawName, settings := range containerInfo.NetworkSettings.Networks {
		name := strings.TrimSpace(rawName)
		if name == "" {
			continue
		}
		endpoint := dockerDaemonEndpoint{}
		if settings != nil {
			endpoint.aliases = append(endpoint.aliases, settings.Aliases...)
			// DNSNames is what the engine registers for Docker's embedded
			// resolver; older engines populate only Aliases, so both are read.
			endpoint.aliases = append(endpoint.aliases, settings.DNSNames...)
			endpoint.addresses = append(endpoint.addresses, settings.IPAddress, settings.GlobalIPv6Address)
		}
		if containerName != "" {
			endpoint.aliases = append(endpoint.aliases, containerName)
		}
		endpoint.aliases = sortedUniqueStrings(endpoint.aliases)
		endpoint.addresses = sortedUniqueStrings(endpoint.addresses)
		endpoints[name] = endpoint
	}
	return endpoints
}

// sortedUniqueStrings trims, drops empty values, deduplicates, and sorts names
// or addresses so validation errors and matching stay deterministic.
func sortedUniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	unique := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		unique = append(unique, value)
	}
	sort.Strings(unique)
	if len(unique) == 0 {
		return nil
	}
	return unique
}

// dockerNetworkNames returns the non-empty network names a container is attached
// to, sorted for deterministic comparison.
func dockerNetworkNames(containerInfo containerapi.InspectResponse) []string {
	if containerInfo.NetworkSettings == nil || len(containerInfo.NetworkSettings.Networks) == 0 {
		return nil
	}
	networkNames := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for name := range containerInfo.NetworkSettings.Networks {
		name = strings.TrimSpace(name)
		if name != "" {
			networkNames = append(networkNames, name)
		}
	}
	sort.Strings(networkNames)
	return networkNames
}

func selectDockerNetworkName(containerInfo containerapi.InspectResponse) (string, bool, bool) {
	networkNames := dockerNetworkNames(containerInfo)
	if len(networkNames) == 0 {
		return "", false, false
	}
	selected := ""
	userDefined := 0
	for _, name := range networkNames {
		switch name {
		case "bridge", "host", "none":
			continue
		default:
			userDefined++
			if selected == "" {
				selected = name
			}
		}
	}
	if selected == "" {
		// Every network is a Docker built-in (bridge/host/none); keep the
		// historical fallback to the first sorted name so a container on the
		// default bridge still resolves to a usable network.
		return networkNames[0], true, false
	}
	return selected, true, userDefined > 1
}

// dockerSandboxNetworkAttachEnabled reports whether EnsureSandbox should attach
// the container to the resolved network after creating or reusing it. It is
// disabled when an explicit sandbox network is configured: a newly-created
// container is already on that network, and an existing container is reused
// unchanged, so enabling DOCKER_SANDBOX_NETWORK never migrates already-created
// sandboxes. Strict mode always configures an explicit network, so it never
// takes this path.
func dockerSandboxNetworkAttachEnabled(containerized bool, explicitNetwork string) bool {
	return containerized && strings.TrimSpace(explicitNetwork) == ""
}

// validateDockerSandboxStrictNetwork fails closed when a container that will be
// reused is not attached only to the strict internal network. A container
// created in strict mode always joins that network alone, so a mismatch means
// the sandbox predates strict mode and would keep its old, wider egress path.
func validateDockerSandboxStrictNetwork(containerInfo containerapi.InspectResponse, networkName string) error {
	networkName = strings.TrimSpace(networkName)
	containerID := ""
	if containerInfo.ContainerJSONBase != nil {
		containerID = containerInfo.ID
	}
	if networkName == "" {
		return fmt.Errorf("cannot validate the strict docker sandbox network: the resolved network name is empty")
	}
	if containerInfo.NetworkSettings == nil || len(containerInfo.NetworkSettings.Networks) == 0 {
		return fmt.Errorf("cannot verify that docker sandbox container %s is attached only to the strict internal network %q: the container reports no networks; remove the sandbox and create it again", containerID, networkName)
	}
	attached := false
	extra := make([]string, 0, len(containerInfo.NetworkSettings.Networks))
	for rawName := range containerInfo.NetworkSettings.Networks {
		name := strings.TrimSpace(rawName)
		if name == networkName {
			attached = true
			continue
		}
		extra = append(extra, name)
	}
	if attached && len(extra) == 0 {
		return nil
	}
	return fmt.Errorf("docker sandbox container %s is attached to networks %v, but %s=true requires only the internal network %q; remove the sandbox and create it again so it is placed on the strict network", containerID, dockerNetworkNames(containerInfo), appconfig.DockerSandboxNetworkStrictEnvName, networkName)
}

func ensureDockerContainerNetwork(ctx context.Context, dockerClient *client.Client, containerInfo containerapi.InspectResponse, networkName string) error {
	networkName = strings.TrimSpace(networkName)
	if networkName == "" || networkName == "default" {
		return nil
	}
	if containerInfo.NetworkSettings != nil {
		if _, ok := containerInfo.NetworkSettings.Networks[networkName]; ok {
			return nil
		}
	}
	if err := dockerClient.NetworkConnect(ctx, networkName, containerInfo.ID, nil); err != nil {
		return fmt.Errorf("connect docker container %s to daemon network %s: %w", containerInfo.ID, networkName, err)
	}
	return nil
}
