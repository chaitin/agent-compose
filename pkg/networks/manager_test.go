package networks

import (
	"context"
	"errors"
	"slices"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestManagerUsesConfiguredRuntimePublishAddress(t *testing.T) {
	infra := &infrastructureStub{networks: map[string]string{"frontend": "project_frontend"}}
	manager := &Manager{
		Infrastructure:        infra,
		Ports:                 &portAllocatorStub{next: 32000},
		DockerPublishAddress:  "172.23.0.1",
		RuntimePublishAddress: "172.23.0.2",
	}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverMicrosandbox)

	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if infra.defaultCalls != 0 {
		t.Fatalf("DefaultPublishAddress() calls = %d", infra.defaultCalls)
	}
	binding := sandbox.NetworkState.Bindings[0]
	if binding.HostIP != "172.23.0.2" || binding.HostPort != 32000 || binding.GuestPort != 8080 || binding.Publisher != PublisherDirect {
		t.Fatalf("binding = %#v", binding)
	}
	if !slices.Equal(binding.Networks, []string{"frontend"}) {
		t.Fatalf("binding networks = %#v", binding.Networks)
	}
}

func TestManagerUsesConfiguredDockerPublishAddress(t *testing.T) {
	manager := &Manager{
		Infrastructure:        &infrastructureStub{networks: map[string]string{"frontend": "project_frontend"}},
		Ports:                 &portAllocatorStub{next: 32000},
		DockerPublishAddress:  "172.23.0.1",
		RuntimePublishAddress: "172.23.0.2",
	}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverDocker)
	sandbox.NetworkIntent.Ports = []domain.SandboxPublishedPort{{HostIP: "192.0.2.10", Published: 19000, Target: 9000, Protocol: "tcp"}}

	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	internal := bindingWithVisibility(t, sandbox.NetworkState.Bindings, VisibilityInternal)
	if internal.HostIP != "172.23.0.1" || internal.Publisher != PublisherDocker {
		t.Fatalf("internal binding = %#v", internal)
	}
	external := bindingWithVisibility(t, sandbox.NetworkState.Bindings, VisibilityExternal)
	if external.HostIP != "192.0.2.10" || external.HostPort != 19000 || external.Publisher != PublisherDocker {
		t.Fatalf("external binding = %#v", external)
	}
}

func TestManagerFallsBackToDefaultBridgeGateway(t *testing.T) {
	infra := &infrastructureStub{
		defaultAddress: "172.17.0.1",
		networks:       map[string]string{"frontend": "project_frontend"},
	}
	manager := &Manager{Infrastructure: infra, Ports: &portAllocatorStub{next: 32000}}

	if err := manager.PrepareSandbox(context.Background(), networkTestSandbox(driverpkg.RuntimeDriverBoxlite)); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if infra.defaultCalls != 1 {
		t.Fatalf("DefaultPublishAddress() calls = %d", infra.defaultCalls)
	}
}

func TestManagerCreatesOneListenerForMultipleNetworks(t *testing.T) {
	infra := &infrastructureStub{networks: map[string]string{
		"frontend": "project_frontend",
		"backend":  "project_backend",
	}}
	manager := &Manager{Infrastructure: infra, Ports: &portAllocatorStub{next: 32000}, DockerPublishAddress: "172.17.0.1"}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverDocker)
	sandbox.NetworkIntent.Attachments = append(sandbox.NetworkIntent.Attachments,
		domain.SandboxNetworkAttachment{Name: "backend", Driver: "port_mapping"})

	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if len(sandbox.NetworkState.Attachments) != 2 || len(sandbox.NetworkState.Bindings) != 1 {
		t.Fatalf("network state = %#v", sandbox.NetworkState)
	}
	if !slices.Equal(sandbox.NetworkState.Bindings[0].Networks, []string{"frontend", "backend"}) {
		t.Fatalf("binding networks = %#v", sandbox.NetworkState.Bindings[0].Networks)
	}
}

func TestManagerAllowsFixedExternalPortWithoutInfrastructureOrAllocator(t *testing.T) {
	manager := &Manager{}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverBoxlite)
	sandbox.NetworkIntent.Attachments = nil
	sandbox.NetworkIntent.Expose = nil
	sandbox.NetworkIntent.Ports = []domain.SandboxPublishedPort{{HostIP: "127.0.0.1", Published: 19000, Target: 9000, Protocol: "tcp"}}

	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	binding := bindingWithVisibility(t, sandbox.NetworkState.Bindings, VisibilityExternal)
	if binding.HostPort != 19000 || binding.GuestPort != 9000 {
		t.Fatalf("binding = %#v", binding)
	}
}

func TestManagerReturnsDefaultPublishAddressError(t *testing.T) {
	manager := &Manager{
		Infrastructure: &infrastructureStub{
			defaultErr: errors.New("Docker unavailable"),
			networks:   map[string]string{"frontend": "project_frontend"},
		},
		Ports: &portAllocatorStub{next: 32000},
	}
	err := manager.PrepareSandbox(context.Background(), networkTestSandbox(driverpkg.RuntimeDriverDocker))
	if err == nil || err.Error() != "resolve default network publish address: Docker unavailable" {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
}

func TestManagerReturnsPortAllocationError(t *testing.T) {
	manager := &Manager{
		Infrastructure:       &infrastructureStub{networks: map[string]string{"frontend": "project_frontend"}},
		Ports:                &portAllocatorStub{err: errors.New("no ports")},
		DockerPublishAddress: "172.17.0.1",
	}
	err := manager.PrepareSandbox(context.Background(), networkTestSandbox(driverpkg.RuntimeDriverDocker))
	if err == nil || err.Error() != "allocate internal host port: no ports" {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
}

func TestManagerPreservesAllocatedPortsAcrossResume(t *testing.T) {
	manager := &Manager{
		Infrastructure:       &infrastructureStub{networks: map[string]string{"frontend": "project_frontend"}},
		Ports:                &portAllocatorStub{next: 32000},
		DockerPublishAddress: "172.17.0.1",
	}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverDocker)
	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("first PrepareSandbox() error = %v", err)
	}
	first := sandbox.NetworkState.Bindings[0].HostPort
	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("second PrepareSandbox() error = %v", err)
	}
	if got := sandbox.NetworkState.Bindings[0].HostPort; got != first {
		t.Fatalf("resumed host port = %d, want %d", got, first)
	}
}

func networkTestSandbox(runtimeDriver string) *domain.Sandbox {
	return &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-1", Driver: runtimeDriver},
		NetworkIntent: &domain.SandboxNetworkIntent{
			ProjectID:   "project-1",
			ProjectName: "demo",
			AgentName:   "api",
			Attachments: []domain.SandboxNetworkAttachment{{Name: "frontend", Driver: "port_mapping"}},
			Expose:      []domain.SandboxNetworkPort{{Target: 8080, Protocol: "tcp"}},
		},
	}
}

func bindingWithVisibility(t *testing.T, bindings []domain.SandboxPortBinding, visibility string) domain.SandboxPortBinding {
	t.Helper()
	for _, binding := range bindings {
		if binding.Visibility == visibility {
			return binding
		}
	}
	t.Fatalf("binding with visibility %q not found in %#v", visibility, bindings)
	return domain.SandboxPortBinding{}
}

type infrastructureStub struct {
	defaultAddress string
	defaultErr     error
	defaultCalls   int
	networks       map[string]string
}

func (s *infrastructureStub) DefaultPublishAddress(context.Context) (string, error) {
	s.defaultCalls++
	return s.defaultAddress, s.defaultErr
}

func (s *infrastructureStub) EnsureNetwork(_ context.Context, request NetworkRequest) (string, error) {
	name, ok := s.networks[request.NetworkName]
	if !ok {
		return "", errors.New("network not found")
	}
	return name, nil
}

type portAllocatorStub struct {
	next  int
	err   error
	calls int
}

func (s *portAllocatorStub) AllocateHostPort(context.Context, string) (int, error) {
	s.calls++
	if s.err != nil {
		return 0, s.err
	}
	port := s.next
	s.next++
	return port, nil
}
