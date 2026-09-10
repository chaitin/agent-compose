package compose

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestWorkspaceDeliveryAuthoringMatrix(t *testing.T) {
	drivers := map[string]*DriverSpec{
		"default":      nil,
		"docker":       {Docker: &DockerDriverSpec{}},
		"boxlite":      {Boxlite: &BoxliteDriverSpec{}},
		"microsandbox": {Microsandbox: &MicrosandboxDriverSpec{}},
		"k8s":          {K8s: &K8sDriverSpec{}},
	}
	for _, provider := range []string{"file", "git"} {
		for _, mode := range []string{"", "copy", "mount", " MOUNT ", "map"} {
			for _, readonly := range []bool{false, true} {
				for driverName, driver := range drivers {
					for _, named := range []bool{false, true} {
						t.Run(fmt.Sprintf("%s/%s/ro=%t/%s/named=%t", provider, mode, readonly, driverName, named), func(t *testing.T) {
							workspace := WorkspaceSpec{Provider: provider, Mode: mode, ReadOnly: readonly, Target: "docs"}
							if provider == "file" {
								workspace.Path = "."
							} else {
								workspace.URL = "https://example.test/repo.git"
							}
							spec := &ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {Provider: "codex", Workspace: &workspace, Driver: driver}}}
							if named {
								spec.Workspaces = map[string]WorkspaceSpec{"repo": workspace}
								agent := spec.Agents["worker"]
								agent.Workspace = &WorkspaceSpec{Name: "repo"}
								spec.Agents["worker"] = agent
							}
							normalized, err := Normalize(spec, NormalizeOptions{})
							mount := strings.EqualFold(strings.TrimSpace(mode), "mount")
							valid := mode != "map" && (!readonly || mount) && (!mount || provider == "file" && (driverName == "default" || driverName == "docker"))
							if (err == nil) != valid {
								t.Fatalf("Normalize error = %v, valid = %t", err, valid)
							}
							if !valid {
								return
							}
							got := normalized.Agents[0].Workspace
							wantMode := ""
							if mount {
								wantMode = "mount"
							}
							if got.Mode != wantMode || got.ReadOnly != readonly || got.Target != "docs" {
								t.Fatalf("workspace = %#v", got)
							}
						})
					}
				}
			}
		}
	}
}

func TestWorkspaceReferenceDeliveryOverrides(t *testing.T) {
	for _, workspace := range []WorkspaceSpec{
		{Name: "repo"}, {Name: "repo", Mode: "copy"}, {Name: "repo", Mode: "mount"},
		{Name: "repo", ReadOnly: true}, {Name: "repo", ReadOnly: false},
		{Name: "label", Provider: "file", Path: ".", Mode: "mount", ReadOnly: true},
	} {
		spec := &ProjectSpec{Name: "demo", Workspaces: map[string]WorkspaceSpec{"repo": {Provider: "file", Path: ".", Mode: "mount"}}, Agents: map[string]AgentSpec{"worker": {Workspace: &workspace}}}
		normalized, err := Normalize(spec, NormalizeOptions{})
		valid := workspace.Provider != "" || workspace.Mode == "" && !workspace.ReadOnly
		if (err == nil) != valid {
			t.Fatalf("workspace %#v: err=%v, valid=%t", workspace, err, valid)
		}
		if valid && normalized.Agents[0].Workspace.Mode != "mount" {
			t.Fatalf("reference/inline lost mode: %#v", normalized)
		}
	}
}

func TestWorkspaceDeliveryStrictYAMLTypes(t *testing.T) {
	for _, declaration := range []string{"mode: [mount]", "mode: {mount: true}", "read_only: mount", "read_only: 1", "read_only: [true]", "mount: true"} {
		_, err := Parse([]byte("workspaces:\n  repo:\n    provider: file\n    path: .\n    " + declaration + "\n"))
		if err == nil {
			t.Fatalf("accepted invalid workspace declaration %s", declaration)
		}
	}
}

func TestWorkspaceDefaultCanonicalHashCompatibility(t *testing.T) {
	var baselineJSON []byte
	var baselineHash string
	for _, mode := range []string{"", "copy", " COPY "} {
		spec := &ProjectSpec{Name: "demo", Workspaces: map[string]WorkspaceSpec{"repo": {Provider: "file", Path: ".", Mode: mode}}, Agents: map[string]AgentSpec{"worker": {Workspace: &WorkspaceSpec{Provider: "file", Path: ".", Mode: mode}}}}
		normalized, err := Normalize(spec, NormalizeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		data, err := normalized.MarshalCanonicalJSON(false)
		if err != nil {
			t.Fatal(err)
		}
		hash, err := normalized.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "mode") || strings.Contains(string(data), "read_only") {
			t.Fatalf("default fields changed historical output: %s", data)
		}
		if mode == "" {
			baselineJSON, baselineHash = data, hash
		} else if string(data) != string(baselineJSON) || hash != baselineHash {
			t.Fatalf("explicit copy changed canonical/hash: %s", data)
		}
	}
}

func TestWorkspaceCanonicalCompleteFieldRoundTrip(t *testing.T) {
	// This fixture exercises serialization independently of provider validation.
	workspace := WorkspaceSpec{Name: "label", Provider: "file", URL: "url", Ref: "ref", Path: "src", Format: "format", Username: "username", Password: "password", Token: "token", Target: "target", Mode: "mount", ReadOnly: true}
	fields := []string{"Mode", "ReadOnly", "Name", "Provider", "URL", "Ref", "Path", "Format", "Username", "Password", "Token", "Target"}
	typ := reflect.TypeOf(workspace)
	var actual []string
	for index := 0; index < typ.NumField(); index++ {
		actual = append(actual, typ.Field(index).Name)
	}
	if !reflect.DeepEqual(actual, fields) {
		t.Fatalf("WorkspaceSpec field contract changed: got %v, want %v; update the full fixture deliberately", actual, fields)
	}
	spec := &NormalizedProjectSpec{Name: "demo", Workspaces: map[string]WorkspaceSpec{"repo": workspace}, Agents: []NormalizedAgentSpec{{Name: "worker", Enabled: true, Workspace: &workspace}}}
	data, err := spec.MarshalCanonicalJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := ParseCanonicalJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded.Workspaces["repo"], workspace) || !reflect.DeepEqual(*decoded.Agents[0].Workspace, workspace) {
		t.Fatalf("canonical lost workspace fields: %s", data)
	}
	encoded, err := json.Marshal(workspace)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	if len(value) != len(fields) {
		t.Fatalf("fixture does not populate every field: %s", encoded)
	}
}

func TestWorkspaceMountConfigurationChangesRevisionHash(t *testing.T) {
	seen := make(map[string]WorkspaceSpec)
	for _, workspace := range []WorkspaceSpec{
		{Provider: "file", Path: "."},
		{Provider: "file", Path: ".", Mode: "mount"},
		{Provider: "file", Path: ".", Mode: "mount", ReadOnly: true},
		{Provider: "file", Path: "other", Mode: "mount", ReadOnly: true},
		{Provider: "file", Path: ".", Target: "docs", Mode: "mount", ReadOnly: true},
	} {
		spec := &ProjectSpec{Name: "demo", Agents: map[string]AgentSpec{"worker": {Workspace: &workspace}}}
		normalized, err := Normalize(spec, NormalizeOptions{})
		if err != nil {
			t.Fatal(err)
		}
		hash, err := normalized.Hash()
		if err != nil {
			t.Fatal(err)
		}
		if previous, exists := seen[hash]; exists {
			t.Fatalf("different workspace configurations share hash %s: %#v and %#v", hash, previous, workspace)
		}
		seen[hash] = workspace
	}
}
