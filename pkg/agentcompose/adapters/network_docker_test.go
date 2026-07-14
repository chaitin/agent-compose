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

func TestDockerNetworkInfrastructureDeployment(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		mode containerapi.NetworkMode
		want string
	}{
		{name: "bridge container", mode: "bridge", want: networks.DeploymentContainerBridge},
		{name: "host container", mode: "host", want: networks.DeploymentContainerHost},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeDockerNetworkAPI{container: containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{
				ID: hostname + "-full-id", HostConfig: &containerapi.HostConfig{NetworkMode: tt.mode},
			}}}
			infrastructure := &DockerNetworkInfrastructure{client: func() (dockerNetworkAPI, error) { return fake, nil }}
			got, err := infrastructure.Deployment(context.Background())
			if err != nil || got != tt.want {
				t.Fatalf("Deployment() = %q, %v, want %q", got, err, tt.want)
			}
		})
	}
}

func TestDockerNetworkInfrastructureEnsureNetworkConnectsBridgeDaemon(t *testing.T) {
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeDockerNetworkAPI{
		container: containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{
			ID: hostname + "-full-id", HostConfig: &containerapi.HostConfig{NetworkMode: "bridge"},
		}},
		network: networkapi.Inspect{
			ID: "network-id", Name: "agent-compose-demo-frontend", Driver: "bridge",
			IPAM:       networkapi.IPAM{Config: []networkapi.IPAMConfig{{Subnet: "10.254.1.0/24", Gateway: "10.254.1.1"}}},
			Containers: map[string]networkapi.EndpointResource{},
		},
	}
	infrastructure := &DockerNetworkInfrastructure{client: func() (dockerNetworkAPI, error) { return fake, nil }}
	access, err := infrastructure.EnsureNetwork(context.Background(), networks.NetworkRequest{
		ProjectID: "project-1", NetworkName: "frontend", ServiceCIDR: "10.254.0.0/16",
	})
	if err != nil {
		t.Fatalf("EnsureNetwork() error = %v", err)
	}
	if fake.connects != 1 || access.RuntimeNetworkName != fake.network.Name || access.HostGateway != "10.254.1.1" || access.DaemonAddress != "10.254.1.2" {
		t.Fatalf("access = %#v, connects = %d", access, fake.connects)
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
	access, err := infrastructure.EnsureNetwork(ctx, request)
	if err != nil {
		t.Fatalf("EnsureNetwork() error = %v", err)
	}
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dockerClient.Close() }()
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		_ = dockerClient.NetworkRemove(cleanupCtx, access.RuntimeNetworkName)
	})

	guestPort := nat.Port("80/tcp")
	target, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{Image: "nginx:alpine", ExposedPorts: nat.PortSet{guestPort: struct{}{}}},
		&containerapi.HostConfig{
			NetworkMode:  containerapi.NetworkMode(access.RuntimeNetworkName),
			PortBindings: nat.PortMap{guestPort: []nat.PortBinding{{HostIP: access.HostGateway, HostPort: ""}}},
		},
		&networkapi.NetworkingConfig{EndpointsConfig: map[string]*networkapi.EndpointSettings{access.RuntimeNetworkName: {}}}, nil, "",
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
	url := fmt.Sprintf("http://%s:%s", access.HostGateway, published[0].HostPort)
	source, err := dockerClient.ContainerCreate(ctx,
		&containerapi.Config{
			Image: "curlimages/curl:latest", Tty: true,
			Cmd: []string{"-fsS", "--retry", "10", "--retry-connrefused", "--retry-delay", "1", url},
		},
		&containerapi.HostConfig{NetworkMode: containerapi.NetworkMode(access.RuntimeNetworkName)},
		&networkapi.NetworkingConfig{EndpointsConfig: map[string]*networkapi.EndpointSettings{access.RuntimeNetworkName: {}}}, nil, "",
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
	container containerapi.InspectResponse
	network   networkapi.Inspect
	connects  int
}

func (f *fakeDockerNetworkAPI) ContainerInspect(context.Context, string) (containerapi.InspectResponse, error) {
	if f.container.ContainerJSONBase == nil {
		return containerapi.InspectResponse{}, errors.New("container unavailable")
	}
	return f.container, nil
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

func (f *fakeDockerNetworkAPI) NetworkConnect(_ context.Context, _, containerID string, _ *networkapi.EndpointSettings) error {
	f.connects++
	if f.network.Containers == nil {
		f.network.Containers = map[string]networkapi.EndpointResource{}
	}
	f.network.Containers[containerID] = networkapi.EndpointResource{IPv4Address: "10.254.1.2/24"}
	return nil
}

func (f *fakeDockerNetworkAPI) Close() error { return nil }
