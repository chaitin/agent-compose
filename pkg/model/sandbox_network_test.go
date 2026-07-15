package model

import (
	"reflect"
	"testing"
)

func TestCloneSandboxNetworkIntentDeepCopiesSlices(t *testing.T) {
	if CloneSandboxNetworkIntent(nil) != nil {
		t.Fatal("CloneSandboxNetworkIntent(nil) returned non-nil")
	}
	original := &SandboxNetworkIntent{
		Version:     1,
		ProjectID:   "project-1",
		Definitions: []SandboxNetworkDefinition{{Name: "frontend", Driver: "bridge"}},
		Attachments: []string{"frontend"},
		Expose:      []string{"8080/tcp"},
		Ports:       []string{"127.0.0.1:0:8080/tcp"},
	}
	cloned := CloneSandboxNetworkIntent(original)
	if !reflect.DeepEqual(cloned, original) {
		t.Fatalf("cloned intent = %#v, want %#v", cloned, original)
	}
	cloned.Definitions[0].Name = "backend"
	cloned.Attachments[0] = "backend"
	cloned.Expose[0] = "9090/tcp"
	cloned.Ports[0] = "127.0.0.1:0:9090/tcp"
	if original.Definitions[0].Name != "frontend" || original.Attachments[0] != "frontend" || original.Expose[0] != "8080/tcp" || original.Ports[0] != "127.0.0.1:0:8080/tcp" {
		t.Fatalf("clone mutated original: %#v", original)
	}
}

func TestIntegrationSandboxNetworkIntentCloneWorkflow(t *testing.T) {
	TestCloneSandboxNetworkIntentDeepCopiesSlices(t)
}

func TestE2ESandboxNetworkIntentCloneWorkflow(t *testing.T) {
	TestCloneSandboxNetworkIntentDeepCopiesSlices(t)
}
