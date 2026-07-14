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
