package api

import (
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"gopkg.in/yaml.v3"
)

func TestWorkspaceAPICompleteFieldRoundTrip(t *testing.T) {
	// Populate every workspace field so any manual boundary omission is visible.
	workspace := compose.WorkspaceSpec{Name: "label", Provider: "file", URL: "url", Ref: "ref", Path: "src", Format: "format", Username: "username", Password: "password", Token: "token", Target: "target", Mode: "mount", ReadOnly: true}
	spec := &compose.NormalizedProjectSpec{Name: "demo", Workspaces: map[string]compose.WorkspaceSpec{"repo": workspace}, Agents: []compose.NormalizedAgentSpec{{Name: "worker", Enabled: true, Workspace: &workspace}}}
	wire, err := ProjectSpecToProtoChecked(spec)
	if err != nil {
		t.Fatal(err)
	}
	shape, issues := ProjectSpecYAMLShape(wire)
	if len(issues) != 0 {
		t.Fatalf("YAML shape issues = %v", issues)
	}
	data, err := yaml.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*parsed.Agents["worker"].Workspace, workspace) {
		t.Fatalf("inline round-trip = %#v, want %#v", parsed.Agents["worker"].Workspace, workspace)
	}
	workspace.Name = "" // Named workspace labels live in the wrapper map key.
	if !reflect.DeepEqual(parsed.Workspaces["repo"], workspace) {
		t.Fatalf("named round-trip = %#v, want %#v", parsed.Workspaces["repo"], workspace)
	}
	redacted := ProjectSpecToProtoRedacted(spec)
	for _, item := range []*agentcomposev2.WorkspaceSpec{redacted.GetAgents()[0].GetWorkspace(), redacted.GetWorkspaces()[0].GetWorkspace()} {
		if item.GetMode() != agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT || !item.GetReadOnly() {
			t.Fatalf("redaction lost delivery fields: %#v", item)
		}
		if item.GetToken() != "********" || item.GetPassword() != "********" {
			t.Fatalf("redaction leaked credentials: %#v", item)
		}
	}
}

func TestWorkspaceAPIUnspecifiedPreservesNamedReference(t *testing.T) {
	wire := &agentcomposev2.ProjectSpec{Name: "demo", Workspaces: []*agentcomposev2.NamedWorkspaceSpec{{Name: "repo", Workspace: &agentcomposev2.WorkspaceSpec{Provider: "file", Path: ".", Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT}}}, Agents: []*agentcomposev2.AgentSpec{{Name: "worker", Workspace: &agentcomposev2.WorkspaceSpec{Name: "repo"}}}}
	shape, issues := ProjectSpecYAMLShape(wire)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	data, err := yaml.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := compose.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	normalized, err := compose.Normalize(parsed, compose.NormalizeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if normalized.Agents[0].Workspace.Mode != "mount" {
		t.Fatalf("named mode lost: %#v", normalized)
	}
	wire.Agents[0].Workspace.Mode = agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY
	shape, issues = ProjectSpecYAMLShape(wire)
	if len(issues) != 0 {
		t.Fatal(issues)
	}
	data, err = yaml.Marshal(shape)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err = compose.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := compose.Normalize(parsed, compose.NormalizeOptions{}); err == nil {
		t.Fatal("explicit copy on name-only reference was silently ignored")
	}
}

func TestWorkspaceAPIUnknownModeRejectedAtBothBoundaries(t *testing.T) {
	for _, named := range []bool{false, true} {
		t.Run(map[bool]string{false: "inline", true: "named"}[named], func(t *testing.T) {
			workspace := &agentcomposev2.WorkspaceSpec{Provider: "file", Path: ".", Mode: agentcomposev2.WorkspaceMode(99)}
			wire := &agentcomposev2.ProjectSpec{Name: "demo"}
			normalized := &compose.NormalizedProjectSpec{Name: "demo"}
			if named {
				wire.Workspaces = []*agentcomposev2.NamedWorkspaceSpec{{Name: "repo", Workspace: workspace}}
				normalized.Workspaces = map[string]compose.WorkspaceSpec{"repo": {Provider: "file", Path: ".", Mode: "bogus"}}
			} else {
				wire.Agents = []*agentcomposev2.AgentSpec{{Name: "worker", Workspace: workspace}}
				normalized.Agents = []compose.NormalizedAgentSpec{{Name: "worker", Workspace: &compose.WorkspaceSpec{Provider: "file", Path: ".", Mode: "bogus"}}}
			}
			if _, issues := ProjectSpecYAMLShape(wire); len(issues) == 0 {
				t.Fatal("unknown wire enum accepted")
			}
			if _, err := ProjectSpecToProtoChecked(normalized); err == nil {
				t.Fatal("unknown normalized mode accepted")
			}
		})
	}
}
