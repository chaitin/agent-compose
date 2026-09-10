package api

import (
	"strings"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func TestSandboxWorkspaceDeliverySafeSnapshot(t *testing.T) {
	const mount = `{"mode":"mount","source_path":"/missing/project/source","project_root":"/missing/project","target":"reference","read_only":true}`
	cases := []struct {
		name      string
		workspace *domain.SandboxWorkspace
		want      *agentcomposev2.SandboxWorkspaceDelivery
	}{
		{"absent", nil, nil},
		{"list index omits config", &domain.SandboxWorkspace{Type: "file"}, nil},
		{"file copy", &domain.SandboxWorkspace{Type: "file", ConfigJSON: `{"root":"/private/managed-secret"}`}, &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY}},
		{"git copy credentials", &domain.SandboxWorkspace{Type: "git", ConfigJSON: `{"url":"https://token-secret@github.com/org/repo","token":"token-secret","password":"password-secret"}`}, &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY}},
		{"mount source unavailable", &domain.SandboxWorkspace{Type: "file", ConfigJSON: mount}, &agentcomposev2.SandboxWorkspaceDelivery{Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT, SourcePath: "/missing/project/source", Target: "reference", ReadOnly: true}},
		{"mount wrong provider", &domain.SandboxWorkspace{Type: "git", ConfigJSON: mount}, &agentcomposev2.SandboxWorkspaceDelivery{}},
		{"unknown mode", &domain.SandboxWorkspace{Type: "file", ConfigJSON: `{"mode":"future","root":"/private/managed-secret"}`}, &agentcomposev2.SandboxWorkspaceDelivery{}},
		{"malformed snapshot", &domain.SandboxWorkspace{Type: "file", ConfigJSON: `{"token":"token-secret"`}, &agentcomposev2.SandboxWorkspaceDelivery{}},
		{"incomplete mount", &domain.SandboxWorkspace{Type: "file", ConfigJSON: `{"mode":"mount","source_path":"/private/managed-secret"}`}, &agentcomposev2.SandboxWorkspaceDelivery{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sandboxToV2(&domain.Sandbox{Summary: domain.SandboxSummary{ID: "sandbox", WorkspacePath: "/sandbox-owned/workspace"}, Workspace: tc.workspace})
			if !proto.Equal(got.GetWorkspaceDelivery(), tc.want) {
				t.Fatalf("workspace delivery = %v, want %v", got.GetWorkspaceDelivery(), tc.want)
			}
			if got.GetWorkspacePath() != "/sandbox-owned/workspace" {
				t.Fatalf("owned workspace_path changed: %q", got.GetWorkspacePath())
			}
			encoded, err := protojson.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{"managed-secret", "token-secret", "password-secret", "config_json", "configJson", "project_root", "projectRoot"} {
				if strings.Contains(string(encoded), forbidden) {
					t.Fatalf("sandbox response exposes %q: %s", forbidden, encoded)
				}
			}
		})
	}
}
