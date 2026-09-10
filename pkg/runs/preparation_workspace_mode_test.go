package runs

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/chaitin/agent-compose/pkg/compose"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

func TestRevisionWorkspaceCompleteFieldRoundTrip(t *testing.T) {
	workspace := compose.WorkspaceSpec{Name: "label", Provider: "file", URL: "url", Ref: "ref", Path: "src", Format: "format", Username: "username", Password: "password", Token: "token", Target: "target", Mode: "mount", ReadOnly: true}
	spec := &compose.NormalizedProjectSpec{Name: "demo", Workspaces: map[string]compose.WorkspaceSpec{"repo": workspace}, Agents: []compose.NormalizedAgentSpec{{Name: "worker", Enabled: true, Workspace: &workspace}}}
	data, err := spec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeRevisionSpec(string(data))
	if err != nil {
		t.Fatal(err)
	}
	inline, err := ComposeWorkspaceSpecFromV2(decoded.GetAgents()[0].GetWorkspace())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*inline, workspace) {
		t.Fatalf("inline revision lost field: %#v, want %#v", inline, workspace)
	}
	named, err := ComposeWorkspaceSpecFromV2(decoded.GetWorkspaces()[0].GetWorkspace())
	if err != nil {
		t.Fatal(err)
	}
	workspace.Name = ""
	if !reflect.DeepEqual(*named, workspace) {
		t.Fatalf("flat named revision lost field: %#v, want %#v", named, workspace)
	}
}

func TestRevisionWorkspaceModeJSONRepresentations(t *testing.T) {
	for _, shape := range []string{"flat", "nested", "inline"} {
		for _, mode := range []string{`"mount"`, `"WORKSPACE_MODE_MOUNT"`, `2`} {
			for _, readonlyField := range []string{"read_only", "readOnly"} {
				t.Run(shape+"/"+mode+"/"+readonlyField, func(t *testing.T) {
					workspace := fmt.Sprintf(`{"provider":"file","path":"src","target":"docs","mode":%s,%q:true}`, mode, readonlyField)
					var raw string
					switch shape {
					case "flat":
						raw = `{"workspaces":[{"key":"repo",` + workspace[1:] + `]}`
					case "nested":
						raw = `{"workspaces":[{"name":"repo","workspace":` + workspace + `}]}`
					case "inline":
						raw = `{"agents":[{"name":"worker","workspace":` + workspace + `}]}`
					}
					decoded, err := DecodeRevisionSpec(raw)
					if err != nil {
						t.Fatalf("DecodeRevisionSpec(%s): %v", raw, err)
					}
					var got *agentcomposev2.WorkspaceSpec
					if shape == "inline" {
						got = decoded.GetAgents()[0].GetWorkspace()
					} else {
						got = decoded.GetWorkspaces()[0].GetWorkspace()
					}
					if got.GetMode() != agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT || !got.GetReadOnly() || got.GetPath() != "src" || got.GetTarget() != "docs" {
						t.Fatalf("restoration lost fields: %#v", got)
					}
				})
			}
		}
	}
}

func TestRevisionWorkspaceRejectsUnknownModesEveryShape(t *testing.T) {
	for _, shape := range []string{"flat", "nested", "inline"} {
		for _, mode := range []string{`"map"`, `"WORKSPACE_MODE_UNKNOWN"`, `99`, `-1`, `1.5`, `true`, `{}`, `[]`} {
			workspace := fmt.Sprintf(`{"provider":"file","path":"src","mode":%s}`, mode)
			var raw string
			switch shape {
			case "flat":
				raw = `{"workspaces":[{"key":"repo",` + workspace[1:] + `]}`
			case "nested":
				raw = `{"workspaces":[{"name":"repo","workspace":` + workspace + `}]}`
			case "inline":
				raw = `{"agents":[{"name":"worker","workspace":` + workspace + `}]}`
			}
			if _, err := DecodeRevisionSpec(raw); err == nil {
				t.Fatalf("accepted %s", raw)
			}
		}
	}
	if _, err := ComposeWorkspaceSpecFromV2(&agentcomposev2.WorkspaceSpec{Mode: agentcomposev2.WorkspaceMode(99)}); err == nil {
		t.Fatal("unknown direct mode accepted")
	}
}

func TestRunWorkspaceReferenceOverridesAndDefaults(t *testing.T) {
	globals := []*agentcomposev2.NamedWorkspaceSpec{{Name: "repo", Workspace: &agentcomposev2.WorkspaceSpec{Provider: "file", Path: ".", Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT, ReadOnly: true}}}
	for _, ref := range []*agentcomposev2.WorkspaceSpec{
		{Name: "repo"}, {Name: "repo", Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_COPY},
		{Name: "repo", Mode: agentcomposev2.WorkspaceMode_WORKSPACE_MODE_MOUNT}, {Name: "repo", ReadOnly: true},
	} {
		_, got, err := ProjectRunWorkspaceSpecsFromV2(globals, ref)
		valid := ref.GetMode() == agentcomposev2.WorkspaceMode_WORKSPACE_MODE_UNSPECIFIED && !ref.GetReadOnly()
		if (err == nil) != valid {
			t.Fatalf("reference %#v: error=%v, valid=%t", ref, err, valid)
		}
		if valid && (got.Mode != "mount" || !got.ReadOnly) {
			t.Fatalf("reference lost fields: %#v", got)
		}
	}
	legacy, err := DecodeRevisionSpec(`{"agents":[{"name":"worker","workspace":{"provider":"file","path":"."}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	workspace, err := ComposeWorkspaceSpecFromV2(legacy.GetAgents()[0].GetWorkspace())
	if err != nil || workspace.Mode != "" || workspace.ReadOnly {
		t.Fatalf("legacy defaults = %#v, %v", workspace, err)
	}
}
