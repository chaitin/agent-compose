package networks

import (
	"context"
	"errors"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestManagerPrepareSandboxCompilesRuntimeNeutralPlan(t *testing.T) {
	infra := &infrastructureStub{
		deployment: DeploymentContainerBridge,
		access: map[string]NetworkAccess{
			"frontend": {RuntimeNetworkName: "project_frontend", HostGateway: "10.254.1.1", DaemonAddress: "10.254.1.2"},
		},
	}
	allocator := &portAllocatorStub{next: 32000}
	manager := &Manager{Infrastructure: infra, Ports: allocator}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverMicrosandbox)

	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	if sandbox.NetworkState.Deployment != DeploymentContainerBridge {
		t.Fatalf("deployment = %q", sandbox.NetworkState.Deployment)
	}
	if len(sandbox.NetworkState.Bindings) != 1 {
		t.Fatalf("bindings = %#v", sandbox.NetworkState.Bindings)
	}
	binding := sandbox.NetworkState.Bindings[0]
	if binding.HostIP != "10.254.1.2" || binding.HostPort != 32000 || binding.GuestPort != 8080 || binding.Publisher != PublisherDirect {
		t.Fatalf("binding = %#v", binding)
	}
	if got := sandbox.NetworkState.AllowedAddresses; len(got) != 2 || got[0] != "10.254.1.1" || got[1] != "10.254.1.2" {
		t.Fatalf("allowed addresses = %#v", got)
	}
}

func TestManagerPrepareSandboxUsesGatewayForDocker(t *testing.T) {
	manager := &Manager{
		Infrastructure: &infrastructureStub{
			deployment: DeploymentContainerBridge,
			access: map[string]NetworkAccess{
				"frontend": {RuntimeNetworkName: "project_frontend", HostGateway: "10.254.1.1", DaemonAddress: "10.254.1.2"},
			},
		},
		Ports: &portAllocatorStub{next: 32000},
	}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverDocker)
	sandbox.NetworkIntent.Ports = []domain.SandboxPublishedPort{{HostIP: "127.0.0.1", Published: 19000, Target: 9000, Protocol: "tcp"}}

	if err := manager.PrepareSandbox(context.Background(), sandbox); err != nil {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
	internal := bindingWithVisibility(t, sandbox.NetworkState.Bindings, VisibilityInternal)
	if got := internal.HostIP; got != "10.254.1.1" {
		t.Fatalf("internal host IP = %q", got)
	}
	external := bindingWithVisibility(t, sandbox.NetworkState.Bindings, VisibilityExternal)
	if got := external; got.HostIP != "127.0.0.1" || got.HostPort != 19000 || got.Publisher != PublisherDocker {
		t.Fatalf("external binding = %#v", got)
	}
}

func TestManagerRejectsBridgeVMExternalPorts(t *testing.T) {
	manager := &Manager{
		Infrastructure: &infrastructureStub{deployment: DeploymentContainerBridge},
		Ports:          &portAllocatorStub{next: 32000},
	}
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverBoxlite)
	sandbox.NetworkIntent.Attachments = nil
	sandbox.NetworkIntent.Expose = nil
	sandbox.NetworkIntent.Ports = []domain.SandboxPublishedPort{{HostIP: "127.0.0.1", Published: 19000, Target: 9000, Protocol: "tcp"}}

	err := manager.PrepareSandbox(context.Background(), sandbox)
	if !errors.Is(err, ErrUnsupported) {
		t.Fatalf("PrepareSandbox() error = %v, want unsupported", err)
	}
}

func TestManagerAllowsFixedExternalPortWithoutAllocator(t *testing.T) {
	manager := &Manager{Infrastructure: &infrastructureStub{deployment: DeploymentNative}}
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

func TestManagerRequiresInfrastructureForNetworkIntent(t *testing.T) {
	sandbox := networkTestSandbox(driverpkg.RuntimeDriverDocker)
	err := (&Manager{}).PrepareSandbox(context.Background(), sandbox)
	if err == nil || err.Error() != "network infrastructure is required" {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
}

func TestManagerRejectsUnknownDeployment(t *testing.T) {
	manager := &Manager{
		Infrastructure: &infrastructureStub{deployment: "unknown"},
		Ports:          &portAllocatorStub{next: 32000},
	}
	err := manager.PrepareSandbox(context.Background(), networkTestSandbox(driverpkg.RuntimeDriverDocker))
	if err == nil || err.Error() != `unknown daemon network deployment "unknown"` {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
}

func TestManagerReturnsPortAllocationError(t *testing.T) {
	manager := &Manager{
		Infrastructure: &infrastructureStub{
			deployment: DeploymentNative,
			access: map[string]NetworkAccess{
				"frontend": {RuntimeNetworkName: "project_frontend", HostGateway: "10.254.1.1"},
			},
		},
		Ports: &portAllocatorStub{err: errors.New("no ports")},
	}
	err := manager.PrepareSandbox(context.Background(), networkTestSandbox(driverpkg.RuntimeDriverDocker))
	if err == nil || err.Error() != "allocate internal host port: no ports" {
		t.Fatalf("PrepareSandbox() error = %v", err)
	}
}

func TestManagerPreservesAllocatedPortsAcrossResume(t *testing.T) {
	manager := &Manager{
		Infrastructure: &infrastructureStub{
			deployment: DeploymentNative,
			access: map[string]NetworkAccess{
				"frontend": {RuntimeNetworkName: "project_frontend", HostGateway: "10.254.1.1"},
			},
		},
		Ports: &portAllocatorStub{next: 32000},
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

func networkTestSandbox(driver string) *domain.Sandbox {
	return &domain.Sandbox{
		Summary: domain.SandboxSummary{ID: "sandbox-1", Driver: driver},
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
	deployment string
	access     map[string]NetworkAccess
}

func (s *infrastructureStub) Deployment(context.Context) (string, error) {
	return s.deployment, nil
}

func (s *infrastructureStub) EnsureNetwork(_ context.Context, request NetworkRequest) (NetworkAccess, error) {
	access, ok := s.access[request.NetworkName]
	if !ok {
		return NetworkAccess{}, errors.New("network not found")
	}
	return access, nil
}

type portAllocatorStub struct {
	next int
	err  error
}

func (s *portAllocatorStub) AllocateHostPort(context.Context) (int, error) {
	if s.err != nil {
		return 0, s.err
	}
	port := s.next
	s.next++
	return port, nil
}
