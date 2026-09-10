package adapters

import (
	"encoding/json"
	"reflect"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/workspaces"
)

func TestSchedulerStickyHashTracksEveryWorkspaceMountField(t *testing.T) {
	base := workspaces.FileWorkspaceMountConfig{Mode: "mount", SourcePath: "/project/source", ProjectRoot: "/project", Target: "inputs"}
	changes := map[string]func(*workspaces.FileWorkspaceMountConfig){
		"Mode":        func(item *workspaces.FileWorkspaceMountConfig) { item.Mode = "copy" },
		"SourcePath":  func(item *workspaces.FileWorkspaceMountConfig) { item.SourcePath = "/project/other" },
		"ProjectRoot": func(item *workspaces.FileWorkspaceMountConfig) { item.ProjectRoot = "/other-project" },
		"Target":      func(item *workspaces.FileWorkspaceMountConfig) { item.Target = "other" },
		"ReadOnly":    func(item *workspaces.FileWorkspaceMountConfig) { item.ReadOnly = true },
	}
	typ := reflect.TypeOf(base)
	if len(changes) != typ.NumField() {
		t.Fatalf("mount snapshot gained fields; update the complete hash contract: %v", typ)
	}
	hash := func(item workspaces.FileWorkspaceMountConfig) string {
		t.Helper()
		payload, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		value, err := schedulerRequestSandboxConfigHash(schedulerRequestSandboxConfigHashRequest{
			BaseHash: "scheduler", Driver: "docker",
			Workspace: &domain.SandboxWorkspace{ID: "workspace", Type: "file", ConfigJSON: string(payload)},
		})
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	original := hash(base)
	for i := range typ.NumField() {
		name := typ.Field(i).Name
		change, ok := changes[name]
		if !ok {
			t.Fatalf("mount field %s is missing from hash contract", name)
		}
		t.Run(name, func(t *testing.T) {
			modified := base
			change(&modified)
			if hash(modified) == original {
				t.Fatalf("changing mount %s reused the sticky sandbox hash", name)
			}
		})
	}
}
