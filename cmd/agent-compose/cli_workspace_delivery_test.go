package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"connectrpc.com/connect"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestCLIWorkspaceDeliveryJSONContract(t *testing.T) {
	cases := []struct {
		name     string
		delivery *agentcomposev2.SandboxWorkspaceDelivery
		want     map[string]any
	}{
		{"absent", nil, nil},
		{"copy", &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY}, map[string]any{"mode": "copy", "read_only": false}},
		{"mount rw", &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT, SourcePath: "/project/source", Target: "."}, map[string]any{"mode": "mount", "source_path": "/project/source", "target": ".", "read_only": false}},
		{"mount ro", &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT, SourcePath: "/project/source", Target: "reference", ReadOnly: true}, map[string]any{"mode": "mount", "source_path": "/project/source", "target": "reference", "read_only": true}},
		{"unspecified", &agentcomposev2.SandboxWorkspaceDelivery{}, map[string]any{"mode": "unknown", "read_only": false}},
		{"unknown enum", &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode(999)}, map[string]any{"mode": "unknown", "read_only": false}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			output := composeSandboxOutputFromSummary(&agentcomposev2.Sandbox{WorkspacePath: "/sandbox-owned/workspace", WorkspaceDelivery: tc.delivery})
			encoded, err := json.Marshal(output)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded["workspace_path"] != "/sandbox-owned/workspace" {
				t.Fatalf("owned workspace path = %v", decoded["workspace_path"])
			}
			delivery, present := decoded["workspace_delivery"]
			if tc.want == nil {
				if present {
					t.Fatalf("absent metadata must omit workspace_delivery: %s", encoded)
				}
				return
			}
			if !reflect.DeepEqual(delivery, tc.want) {
				t.Fatalf("delivery JSON = %#v, want full field contract %#v", delivery, tc.want)
			}
		})
	}
}

func TestIntegrationCLIInspectSandboxWorkspaceDelivery(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), "name: workspace-inspect\nagents:\n  worker:\n    provider: codex\n")
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{getProject: func(context.Context, *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: testCLIProject("project-inspect", "workspace-inspect", composePath)}), nil
		}},
		session: sessionServiceStub{getSession: func(_ context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: &agentcomposev2.Sandbox{
				SandboxId: req.Msg.GetSandboxId(), WorkspacePath: "/sandbox-owned/workspace",
				WorkspaceDelivery: &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT, SourcePath: "/project/source", Target: "reference", ReadOnly: true},
			}}), nil
		}},
	})
	defer server.Close()
	stdout, stderr, _, code := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "sandbox", "sandbox-inspect")
	if code != 0 || stderr != "" {
		t.Fatalf("inspect exit=%d stderr=%q", code, stderr)
	}
	var output composeSandboxOutput
	if err := json.Unmarshal([]byte(stdout), &output); err != nil {
		t.Fatal(err)
	}
	want := &composeWorkspaceDeliveryOutput{Mode: "mount", SourcePath: "/project/source", Target: "reference", ReadOnly: true}
	if !reflect.DeepEqual(output.WorkspaceDelivery, want) || output.WorkspacePath != "/sandbox-owned/workspace" {
		t.Fatalf("inspect sandbox = %#v, delivery = %#v", output, output.WorkspaceDelivery)
	}
}
