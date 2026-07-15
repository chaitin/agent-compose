package sessionstore

import (
	"context"
	"reflect"
	"testing"

	driverpkg "agent-compose/pkg/driver"
	domain "agent-compose/pkg/model"
)

func TestStorePersistsSandboxNetworkIntent(t *testing.T) {
	store := newCoverageStore(t)
	intent := &domain.SandboxNetworkIntent{
		Version:         1,
		ProjectID:       "project-1",
		ProjectRevision: 7,
		AgentName:       "api",
		Definitions:     []domain.SandboxNetworkDefinition{{Name: "frontend", Driver: "bridge"}},
		Attachments:     []string{"frontend"},
		Expose:          []string{"8080/tcp"},
		Ports:           []string{"127.0.0.1:18080:8080/tcp"},
	}
	sandbox, err := store.CreateSandboxWithOptions(context.Background(), "networked", "", driverpkg.RuntimeDriverDocker, "guest:latest", "", "test", nil, nil, nil, CreateSandboxOptions{NetworkIntent: intent})
	if err != nil {
		t.Fatalf("CreateSandboxWithOptions returned error: %v", err)
	}
	intent.ProjectID = "mutated"
	loaded, err := store.GetSandbox(context.Background(), sandbox.Summary.ID)
	if err != nil {
		t.Fatalf("GetSandbox returned error: %v", err)
	}
	want := "project-1"
	if loaded.NetworkIntent == nil || loaded.NetworkIntent.ProjectID != want || loaded.NetworkIntent.ProjectRevision != 7 || !reflect.DeepEqual(loaded.NetworkIntent.Attachments, []string{"frontend"}) {
		t.Fatalf("loaded network intent = %#v", loaded.NetworkIntent)
	}
}

func TestIntegrationStorePersistsSandboxNetworkIntent(t *testing.T) {
	TestStorePersistsSandboxNetworkIntent(t)
}

func TestE2EStorePersistsSandboxNetworkIntent(t *testing.T) {
	TestStorePersistsSandboxNetworkIntent(t)
}
