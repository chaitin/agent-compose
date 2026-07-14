package sessionstore

import (
	"context"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestCreateSandboxPersistsIndependentNetworkIntent(t *testing.T) {
	store := newCoverageStore(t)
	intent := &domain.SandboxNetworkIntent{
		ProjectID:   "project-1",
		ProjectName: "demo",
		AgentName:   "api",
		Attachments: []domain.SandboxNetworkAttachment{{Name: "frontend", Driver: "port_mapping"}},
		Expose:      []domain.SandboxNetworkPort{{Target: 8080, Protocol: "tcp"}},
	}
	sandbox, err := store.CreateSandboxWithOptions(context.Background(), "network", "", driverpkg.RuntimeDriverDocker, "", "", "", nil, nil, nil, CreateSandboxOptions{NetworkIntent: intent})
	if err != nil {
		t.Fatalf("CreateSandboxWithOptions() error = %v", err)
	}
	intent.Attachments[0].Name = "mutated"
	intent.Expose[0].Target = 9999
	if sandbox.NetworkIntent.Attachments[0].Name != "frontend" || sandbox.NetworkIntent.Expose[0].Target != 8080 {
		t.Fatalf("created network intent was aliased: %#v", sandbox.NetworkIntent)
	}
	loaded, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox() error = %v", err)
	}
	if loaded.NetworkIntent == nil || loaded.NetworkIntent.ProjectID != "project-1" || loaded.NetworkIntent.Attachments[0].Name != "frontend" {
		t.Fatalf("persisted network intent = %#v", loaded.NetworkIntent)
	}
}

func TestAllocateHostPortDoesNotReuseStoppedSandboxAllocationAfterRestart(t *testing.T) {
	store := newCoverageStore(t)
	ctx := context.Background()
	sandbox, err := store.CreateSandbox(ctx, "network", "", driverpkg.RuntimeDriverDocker, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	allocated, err := store.AllocateHostPortForSandbox(sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	sandbox.NetworkState = &domain.SandboxNetworkState{Bindings: []domain.SandboxPortBinding{{HostIP: "127.0.0.1", HostPort: allocated, GuestPort: 8080, Protocol: "tcp"}}}
	if err := store.UpdateSandbox(ctx, sandbox); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewWithConfig(store.config)
	if err != nil {
		t.Fatal(err)
	}
	used, err := restarted.persistedHostPorts()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := used[allocated]; !ok {
		t.Fatalf("persisted host ports do not include %d: %#v", allocated, used)
	}
	next, err := restarted.AllocateHostPort()
	if err != nil {
		t.Fatal(err)
	}
	if next == allocated {
		t.Fatalf("reused persisted host port %d", allocated)
	}
}

func TestRemoveSandboxReleasesOwnedHostPortReservations(t *testing.T) {
	store := newCoverageStore(t)
	ctx := context.Background()
	sandbox, err := store.CreateSandbox(ctx, "network", "", driverpkg.RuntimeDriverDocker, "", "", "", nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	port, err := store.AllocateHostPortForSandbox(sandbox.Summary.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner := store.reservedHostPorts[port]; owner != sandbox.Summary.ID {
		t.Fatalf("reservation owner = %q", owner)
	}
	if err := store.RemoveSandbox(ctx, sandbox.Summary.ID); err != nil {
		t.Fatal(err)
	}
	if _, exists := store.reservedHostPorts[port]; exists {
		t.Fatalf("host port %d reservation was not released", port)
	}
}
