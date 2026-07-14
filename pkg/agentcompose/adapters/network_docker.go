package adapters

import (
	"context"
	"fmt"
	"net/netip"
	"strings"

	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

type dockerNetworkAPI interface {
	NetworkInspect(context.Context, string, networkapi.InspectOptions) (networkapi.Inspect, error)
	Close() error
}

type dockerNetworkClientFactory func() (dockerNetworkAPI, error)

type DockerPublishAddressProvider struct {
	client dockerNetworkClientFactory
}

func NewDockerPublishAddressProvider() *DockerPublishAddressProvider {
	return &DockerPublishAddressProvider{client: newDockerNetworkAPI}
}

func newDockerNetworkAPI() (dockerNetworkAPI, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("connect docker daemon: %w", err)
	}
	return dockerClient, nil
}

func (p *DockerPublishAddressProvider) DefaultPublishAddress(ctx context.Context) (string, error) {
	dockerClient, err := p.openClient()
	if err != nil {
		return "", err
	}
	defer func() { _ = dockerClient.Close() }()
	network, err := dockerClient.NetworkInspect(ctx, "bridge", networkapi.InspectOptions{})
	if err != nil {
		return "", fmt.Errorf("inspect Docker default bridge network: %w", err)
	}
	return ipv4Gateway(network, "Docker default bridge")
}

func (p *DockerPublishAddressProvider) openClient() (dockerNetworkAPI, error) {
	if p == nil || p.client == nil {
		return nil, fmt.Errorf("docker network client is required")
	}
	return p.client()
}

func ipv4Gateway(network networkapi.Inspect, description string) (string, error) {
	var gateway string
	for _, config := range network.IPAM.Config {
		candidate := strings.TrimSpace(config.Gateway)
		address, err := netip.ParseAddr(candidate)
		if err != nil || !address.Is4() {
			continue
		}
		if gateway != "" {
			return "", fmt.Errorf("%s network has multiple IPv4 gateways", description)
		}
		gateway = address.String()
	}
	if gateway == "" {
		return "", fmt.Errorf("%s network has no IPv4 gateway", description)
	}
	return gateway, nil
}
