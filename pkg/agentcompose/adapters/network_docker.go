package adapters

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net/netip"
	"os"
	"slices"
	"strings"
	"sync"

	containerapi "github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"

	"agent-compose/pkg/networks"
)

const (
	agentComposeNetworkLabel       = "agent-compose.network"
	agentComposeNetworkProjectID   = agentComposeNetworkLabel + ".project_id"
	agentComposeNetworkLogicalName = agentComposeNetworkLabel + ".name"
)

type dockerNetworkAPI interface {
	ContainerInspect(context.Context, string) (containerapi.InspectResponse, error)
	NetworkList(context.Context, networkapi.ListOptions) ([]networkapi.Summary, error)
	NetworkInspect(context.Context, string, networkapi.InspectOptions) (networkapi.Inspect, error)
	NetworkCreate(context.Context, string, networkapi.CreateOptions) (networkapi.CreateResponse, error)
	NetworkConnect(context.Context, string, string, *networkapi.EndpointSettings) error
	Close() error
}

type dockerNetworkClientFactory func() (dockerNetworkAPI, error)

type DockerNetworkInfrastructure struct {
	client dockerNetworkClientFactory
	mu     sync.Mutex
}

func NewDockerNetworkInfrastructure() *DockerNetworkInfrastructure {
	return &DockerNetworkInfrastructure{client: newDockerNetworkAPI}
}

func newDockerNetworkAPI() (dockerNetworkAPI, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect docker daemon: %w", err)
	}
	return dockerClient, nil
}

func (i *DockerNetworkInfrastructure) Deployment(ctx context.Context) (string, error) {
	dockerClient, err := i.openClient()
	if err != nil {
		return "", err
	}
	defer func() { _ = dockerClient.Close() }()
	self, ok, err := inspectDaemonContainer(ctx, dockerClient)
	if err != nil {
		return "", err
	}
	if !ok {
		return networks.DeploymentNative, nil
	}
	if self.HostConfig != nil && self.HostConfig.NetworkMode.IsHost() {
		return networks.DeploymentContainerHost, nil
	}
	return networks.DeploymentContainerBridge, nil
}

func (i *DockerNetworkInfrastructure) EnsureNetwork(ctx context.Context, request networks.NetworkRequest) (networks.NetworkAccess, error) {
	if strings.TrimSpace(request.ProjectID) == "" {
		return networks.NetworkAccess{}, fmt.Errorf("project ID is required")
	}
	if strings.TrimSpace(request.NetworkName) == "" {
		return networks.NetworkAccess{}, fmt.Errorf("network name is required")
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	dockerClient, err := i.openClient()
	if err != nil {
		return networks.NetworkAccess{}, err
	}
	defer func() { _ = dockerClient.Close() }()
	network, found, err := findAgentComposeNetwork(ctx, dockerClient, request)
	if err != nil {
		return networks.NetworkAccess{}, err
	}
	if !found {
		network, err = createAgentComposeNetwork(ctx, dockerClient, request)
		if err != nil {
			return networks.NetworkAccess{}, err
		}
	}
	access, err := networkAccess(network)
	if err != nil {
		return networks.NetworkAccess{}, err
	}
	self, containerized, err := inspectDaemonContainer(ctx, dockerClient)
	if err != nil {
		return networks.NetworkAccess{}, err
	}
	if !containerized || (self.HostConfig != nil && self.HostConfig.NetworkMode.IsHost()) {
		return access, nil
	}
	if _, connected := network.Containers[self.ID]; !connected {
		if err := dockerClient.NetworkConnect(ctx, network.ID, self.ID, nil); err != nil {
			return networks.NetworkAccess{}, fmt.Errorf("connect daemon container %s to network %s: %w", self.ID, network.Name, err)
		}
		network, err = dockerClient.NetworkInspect(ctx, network.ID, networkapi.InspectOptions{})
		if err != nil {
			return networks.NetworkAccess{}, fmt.Errorf("inspect network %s after connecting daemon: %w", network.Name, err)
		}
	}
	endpoint, ok := network.Containers[self.ID]
	if !ok {
		return networks.NetworkAccess{}, fmt.Errorf("network %s has no daemon container endpoint", network.Name)
	}
	daemonAddress, err := addressWithoutPrefix(endpoint.IPv4Address)
	if err != nil {
		return networks.NetworkAccess{}, fmt.Errorf("network %s daemon endpoint: %w", network.Name, err)
	}
	access.DaemonAddress = daemonAddress
	return access, nil
}

func (i *DockerNetworkInfrastructure) openClient() (dockerNetworkAPI, error) {
	if i == nil || i.client == nil {
		return nil, fmt.Errorf("docker network client is required")
	}
	return i.client()
}

func inspectDaemonContainer(ctx context.Context, dockerClient dockerNetworkAPI) (containerapi.InspectResponse, bool, error) {
	hostname, err := os.Hostname()
	if err != nil || strings.TrimSpace(hostname) == "" {
		return containerapi.InspectResponse{}, false, nil
	}
	info, err := dockerClient.ContainerInspect(ctx, hostname)
	if client.IsErrNotFound(err) {
		return containerapi.InspectResponse{}, false, nil
	}
	if err != nil {
		return containerapi.InspectResponse{}, false, fmt.Errorf("inspect daemon container %q: %w", hostname, err)
	}
	if !strings.HasPrefix(info.ID, hostname) {
		return containerapi.InspectResponse{}, false, nil
	}
	return info, true, nil
}

func findAgentComposeNetwork(ctx context.Context, dockerClient dockerNetworkAPI, request networks.NetworkRequest) (networkapi.Inspect, bool, error) {
	listed, err := dockerClient.NetworkList(ctx, networkapi.ListOptions{Filters: filters.NewArgs(
		filters.Arg("label", agentComposeNetworkLabel+"=true"),
		filters.Arg("label", agentComposeNetworkProjectID+"="+request.ProjectID),
		filters.Arg("label", agentComposeNetworkLogicalName+"="+request.NetworkName),
	)})
	if err != nil {
		return networkapi.Inspect{}, false, fmt.Errorf("list project networks: %w", err)
	}
	if len(listed) == 0 {
		return networkapi.Inspect{}, false, nil
	}
	if len(listed) > 1 {
		return networkapi.Inspect{}, false, fmt.Errorf("multiple runtime networks match project %s network %s", request.ProjectID, request.NetworkName)
	}
	inspected, err := dockerClient.NetworkInspect(ctx, listed[0].ID, networkapi.InspectOptions{})
	if err != nil {
		return networkapi.Inspect{}, false, fmt.Errorf("inspect project network %s: %w", listed[0].Name, err)
	}
	if inspected.Driver != "bridge" {
		return networkapi.Inspect{}, false, fmt.Errorf("project network %s uses unexpected Docker driver %q", inspected.Name, inspected.Driver)
	}
	return inspected, true, nil
}

func createAgentComposeNetwork(ctx context.Context, dockerClient dockerNetworkAPI, request networks.NetworkRequest) (networkapi.Inspect, error) {
	servicePrefix, err := parseServicePrefix(request.ServiceCIDR)
	if err != nil {
		return networkapi.Inspect{}, err
	}
	listed, err := dockerClient.NetworkList(ctx, networkapi.ListOptions{})
	if err != nil {
		return networkapi.Inspect{}, fmt.Errorf("list Docker address pools: %w", err)
	}
	used := dockerNetworkPrefixes(listed)
	candidates := serviceNetworkCandidates(servicePrefix, request.ProjectID, request.NetworkName)
	for _, subnet := range candidates {
		if prefixOverlapsAny(subnet, used) {
			continue
		}
		gateway := subnet.Addr().Next()
		created, err := dockerClient.NetworkCreate(ctx, runtimeNetworkName(request), networkapi.CreateOptions{
			Driver: "bridge",
			IPAM: &networkapi.IPAM{Config: []networkapi.IPAMConfig{{
				Subnet:  subnet.String(),
				Gateway: gateway.String(),
			}}},
			Labels: map[string]string{
				agentComposeNetworkLabel:       "true",
				agentComposeNetworkProjectID:   request.ProjectID,
				agentComposeNetworkLogicalName: request.NetworkName,
			},
		})
		if err != nil {
			return networkapi.Inspect{}, fmt.Errorf("create project network %s with subnet %s: %w", request.NetworkName, subnet, err)
		}
		inspected, err := dockerClient.NetworkInspect(ctx, created.ID, networkapi.InspectOptions{})
		if err != nil {
			return networkapi.Inspect{}, fmt.Errorf("inspect created project network %s: %w", request.NetworkName, err)
		}
		return inspected, nil
	}
	return networkapi.Inspect{}, fmt.Errorf("service address pool %s has no available /24 subnet", servicePrefix)
}

func networkAccess(network networkapi.Inspect) (networks.NetworkAccess, error) {
	if len(network.IPAM.Config) != 1 {
		return networks.NetworkAccess{}, fmt.Errorf("project network %s must have exactly one IPv4 subnet", network.Name)
	}
	gateway := strings.TrimSpace(network.IPAM.Config[0].Gateway)
	if addr, err := netip.ParseAddr(gateway); err != nil || !addr.Is4() {
		return networks.NetworkAccess{}, fmt.Errorf("project network %s has invalid IPv4 gateway %q", network.Name, gateway)
	}
	return networks.NetworkAccess{RuntimeNetworkName: network.Name, HostGateway: gateway}, nil
}

func parseServicePrefix(value string) (netip.Prefix, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() > 24 {
		return netip.Prefix{}, fmt.Errorf("service CIDR %q must be an IPv4 prefix no smaller than /24", value)
	}
	return prefix.Masked(), nil
}

func serviceNetworkCandidates(servicePrefix netip.Prefix, projectID, networkName string) []netip.Prefix {
	count := 1 << (24 - servicePrefix.Bits())
	seed := sha256.Sum256([]byte(projectID + "\x00" + networkName))
	start := int(binary.BigEndian.Uint32(seed[:4]) % uint32(count))
	base := binary.BigEndian.Uint32(servicePrefix.Addr().AsSlice())
	result := make([]netip.Prefix, 0, count)
	for offset := 0; offset < count; offset++ {
		index := (start + offset) % count
		var raw [4]byte
		binary.BigEndian.PutUint32(raw[:], base+uint32(index<<8))
		result = append(result, netip.PrefixFrom(netip.AddrFrom4(raw), 24))
	}
	return result
}

func dockerNetworkPrefixes(networks []networkapi.Summary) []netip.Prefix {
	var result []netip.Prefix
	for _, network := range networks {
		for _, config := range network.IPAM.Config {
			prefix, err := netip.ParsePrefix(strings.TrimSpace(config.Subnet))
			if err == nil && prefix.Addr().Is4() {
				result = append(result, prefix.Masked())
			}
		}
	}
	return result
}

func prefixOverlapsAny(candidate netip.Prefix, values []netip.Prefix) bool {
	return slices.ContainsFunc(values, func(value netip.Prefix) bool {
		return candidate.Contains(value.Addr()) || value.Contains(candidate.Addr())
	})
}

func runtimeNetworkName(request networks.NetworkRequest) string {
	hash := sha256.Sum256([]byte(request.ProjectID))
	logical := sanitizeNetworkName(request.NetworkName)
	return fmt.Sprintf("agent-compose-%x-%s", hash[:5], logical)
}

func sanitizeNetworkName(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var result strings.Builder
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' || char == '_' {
			result.WriteRune(char)
		} else {
			result.WriteByte('-')
		}
		if result.Len() >= 40 {
			break
		}
	}
	if result.Len() == 0 {
		return "default"
	}
	return result.String()
}

func addressWithoutPrefix(value string) (string, error) {
	prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
	if err != nil || !prefix.Addr().Is4() {
		return "", fmt.Errorf("invalid IPv4 endpoint address %q", value)
	}
	return prefix.Addr().String(), nil
}
