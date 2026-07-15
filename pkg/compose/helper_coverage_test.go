package compose

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestComposePrivateHelperCoverage(t *testing.T) {
	if cloneStringMap(nil) != nil || cloneStringMap(map[string]string{}) != nil {
		t.Fatalf("empty cloneStringMap returned non-nil")
	}
	values := map[string]string{"A": "1"}
	cloned := cloneStringMap(values)
	cloned["A"] = "2"
	if values["A"] != "1" {
		t.Fatalf("cloneStringMap aliased input: %#v", values)
	}

	workspace := cloneWorkspaceSpec(&WorkspaceSpec{Provider: " git ", URL: " https://example.test/repo.git ", Branch: " main ", Path: " app "})
	if workspace.Provider != "git" || workspace.URL != "https://example.test/repo.git" || workspace.Branch != "main" || workspace.Path != "app" {
		t.Fatalf("cloneWorkspaceSpec = %#v", workspace)
	}
	if cloneWorkspaceSpec(nil) != nil {
		t.Fatalf("cloneWorkspaceSpec nil returned non-nil")
	}

	build := cloneNormalizedBuildSpec(&NormalizedBuildSpec{
		Context: "agent",
		Args:    map[string]string{"A": "1"},
		Tags:    []string{"app:latest"},
	})
	build.Args["A"] = "2"
	build.Tags[0] = "changed"
	if build.Context != "agent" {
		t.Fatalf("cloneNormalizedBuildSpec changed context: %#v", build)
	}
	original := &NormalizedBuildSpec{Args: map[string]string{"A": "1"}, Tags: []string{"app:latest"}}
	clonedBuild := cloneNormalizedBuildSpec(original)
	clonedBuild.Args["A"] = "2"
	clonedBuild.Tags[0] = "changed"
	if original.Args["A"] != "1" || original.Tags[0] != "app:latest" {
		t.Fatalf("cloneNormalizedBuildSpec aliased original: %#v", original)
	}
	if cloneNormalizedBuildSpec(nil) != nil {
		t.Fatalf("cloneNormalizedBuildSpec nil returned non-nil")
	}

	if compareString("a", "b") >= 0 || compareString("b", "a") <= 0 || compareString("a", "a") != 0 {
		t.Fatalf("compareString returned unexpected ordering")
	}
	for _, tc := range []struct {
		kind yaml.Kind
		want string
	}{
		{yaml.DocumentNode, "document"},
		{yaml.SequenceNode, "sequence"},
		{yaml.MappingNode, "mapping"},
		{yaml.ScalarNode, "scalar"},
		{yaml.AliasNode, "alias"},
		{yaml.Kind(99), "kind(99)"},
	} {
		if got := nodeKindName(tc.kind); got != tc.want {
			t.Fatalf("nodeKindName(%d) = %q, want %q", tc.kind, got, tc.want)
		}
	}
}
