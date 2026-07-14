package adapters

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strings"
	"testing"
	"time"

	containerapi "github.com/docker/docker/api/types/container"
	networkapi "github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"

	"agent-compose/pkg/networks"
)

func TestDockerNetworkInfrastructureDefaultPublishAddress(t *testing.T) {
	fake := &fakeDockerNetworkAPI{network: networkapi.Inspect{
		Name: "bridge",
		IPAM: networkapi.IPAM{Config: []networkapi.IPAMConfig{
			{Subnet: "172.17.0.0/16", Gateway: "172.17.0.1"},
			{Subnet: "fd00::/64", Gateway: "fd00::1"},
		}},
	}}
	infrastructure := &DockerNetworkInfrastructure{client: func() (dockerNetworkAPI, error) { return fake, nil }}
	got, err := infrastructure.DefaultPublishAddress(context.Background())
	if err != nil || got != "172.17.0.1" {
		t.Fatalf("DefaultPublishAddress() = %q, %v", got, err)
	}
}

func TestDockerNetworkInfrastructureRejectsMissingIPv4Gateway(t *testing.T) {
	fake := &fakeDockerNetworkAPI{network: networkapi.Inspect{
		Name: "bridge",
		IPAM: networkapi.IPAM{Config: []networkapi.IPAMConfig{{Subnet: "fd00::/64", Gateway: "fd00::1"}}},
	}}
	infrastructure := &DockerNetworkInfrastructure{client: func() (dockerNetworkAPI, error) { return fake, nil }}
	_, err := infrastructure.DefaultPublishAddress(context.Background())
	if err == nil || !strings.Contains(err.Error(), "has no IPv4 gateway") {
		t.Fatalf("DefaultPublishAddress() error = %v", err)
	}
}

func TestDockerNetworkInfrastructureEnsureNetworkReturnsRuntimeName(t *testing.T) {
	fake := &fakeDockerNetworkAPI{
		network: networkapi.Inspect{
			ID: "network-id", Name: "agent-compose-demo-frontend", Driver: "bridge",
			IPAM: networkapi.IPAM{Config: []networkapi.IPAMConfig{{Subnet: "10.254.1.0/24", Gateway: "10.254.1.1"}}},
		},
	}
	infrastructure := &DockerNetworkInfrastructure{client: func() (dockerNetworkAPI, error) { return fake, nil }}
	runtimeName, err := infrastructure.EnsureNetwork(context.Background(), networks.NetworkRequest{
		ProjectID: "project-1", NetworkName: "frontend", ServiceCIDR: "10.254.0.0/16",
	})
	if err != nil {
		t.Fatalf("EnsureNetwork() error = %v", err)
	}
	if runtimeName != fake.network.Name {
		t.Fatalf("runtime network name = %q", runtimeName)
	}
}

func TestServiceNetworkCandidatesAreDeterministicAndCoverPool(t *testing.T) {
	prefix, err := parseServicePrefix("10.254.0.0/16")
	if err != nil {
		t.Fatal(err)
	}
	first := serviceNetworkCandidates(prefix, "project-1", "frontend")
	second := serviceNetworkCandidates(prefix, "project-1", "frontend")
	if len(first) != 256 || len(second) != len(first) || first[0] != second[0] {
		t.Fatalf("candidates = %d/%d first=%v second=%v", len(first), len(second), first[0], second[0])
	}
	seen := make(map[netip.Prefix]struct{}, len(first))
	for _, candidate := range first {
		if candidate.Bits() != 24 || !prefix.Contains(candidate.Addr()) {
			t.Fatalf("candidate %s is outside %s", candidate, prefix)
		}
		seen[candidate] = struct{}{}
	}
	if len(seen) != len(first) {
		t.Fatalf("candidate set contains duplicates: %d unique", len(seen))
	}
}

func TestIntegrationDockerNetworkInfrastructurePublishesReachableGatewayPort(t *testing.T) {
	if os.Getenv("AGENT_COMPOSE_DOCKER_NETWORK_INTEGRATION") != "1" {
		t.Skip("set AGENT_COMPOSE_DOCKER_NETWORK_INTEGRATION=1 to run the Docker network data-plane test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	infrastructure := NewDockerNetworkInfrastructure()
	request := networks.NetworkRequest{
		ProjectID: "integration-" + fmt.Sprint(time.Now().UnixNano()), NetworkName: "frontend", ServiceCIDR: "10.254.0.0/16",
	}
	runtimeNetworkName, err := infrastructure.EnsureNetwork(ctx, request)
	if err != nil {
		t.Fatalf("EnsureNetwork() error = %v", err)
	}
	publishAddress, err := infrastructure.DefaultPublishAddress(ctx)
	if err != nil {
		t.Fatalf("DefaultPublishAddress() error = %v", err)
	}
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dockerClient.Close() }()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = dockerClient.NetworkRemove(cleanupCtx, runtimeNetworkName)
	})

	guestPort := nat.Port("80/tcp")
	target, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{Image: "nginx:alpine", ExposedPorts: nat.PortSet{guestPort: struct{}{}}},
		&containerapi.HostConfig{
			NetworkMode:  containerapi.NetworkMode(runtimeNetworkName),
			PortBindings: nat.PortMap{guestPort: []nat.PortBinding{{HostIP: publishAddress, HostPort: ""}}},
		},
		&networkapi.NetworkingConfig{EndpointsConfig: map[string]*networkapi.EndpointSettings{runtimeNetworkName: {}}}, nil, "",
	)
	if err != nil {
		t.Fatalf("create target container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), target.ID, containerapi.RemoveOptions{Force: true})
	})
	if err := dockerClient.ContainerStart(ctx, target.ID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("start target container: %v", err)
	}
	inspected, err := dockerClient.ContainerInspect(ctx, target.ID)
	if err != nil {
		t.Fatal(err)
	}
	published := inspected.NetworkSettings.Ports[guestPort]
	if len(published) == 0 || strings.TrimSpace(published[0].HostPort) == "" {
		t.Fatalf("target published ports = %#v", published)
	}
	url := fmt.Sprintf("http://%s:%s", publishAddress, published[0].HostPort)
	source, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{
			Image: "curlimages/curl:latest", Tty: true,
			Cmd: []string{"-fsS", "--retry", "10", "--retry-connrefused", "--retry-delay", "1", url},
		},
		&containerapi.HostConfig{NetworkMode: containerapi.NetworkMode(runtimeNetworkName)},
		&networkapi.NetworkingConfig{EndpointsConfig: map[string]*networkapi.EndpointSettings{runtimeNetworkName: {}}}, nil, "",
	)
	if err != nil {
		t.Fatalf("create source container: %v", err)
	}
	t.Cleanup(func() {
		_ = dockerClient.ContainerRemove(context.Background(), source.ID, containerapi.RemoveOptions{Force: true})
	})
	if err := dockerClient.ContainerStart(ctx, source.ID, containerapi.StartOptions{}); err != nil {
		t.Fatalf("start source container: %v", err)
	}
	wait, waitErr := dockerClient.ContainerWait(ctx, source.ID, containerapi.WaitConditionNotRunning)
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatal(err)
		}
	case result := <-wait:
		if result.StatusCode != 0 {
			t.Fatalf("source exit code = %d", result.StatusCode)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	logs, err := dockerClient.ContainerLogs(ctx, source.ID, containerapi.LogsOptions{ShowStdout: true, ShowStderr: true})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logs.Close() }()
	output, err := io.ReadAll(logs)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(output), "Welcome to nginx") {
		t.Fatalf("source output does not contain nginx response: %q", output)
	}
}

type fakeDockerNetworkAPI struct {
	network networkapi.Inspect
}

func (f *fakeDockerNetworkAPI) NetworkList(context.Context, networkapi.ListOptions) ([]networkapi.Summary, error) {
	return []networkapi.Summary{f.network}, nil
}

func (f *fakeDockerNetworkAPI) NetworkInspect(context.Context, string, networkapi.InspectOptions) (networkapi.Inspect, error) {
	return f.network, nil
}

func (f *fakeDockerNetworkAPI) NetworkCreate(context.Context, string, networkapi.CreateOptions) (networkapi.CreateResponse, error) {
	return networkapi.CreateResponse{}, errors.New("unexpected network create")
}

func (f *fakeDockerNetworkAPI) Close() error { return nil }
