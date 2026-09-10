package driver

import (
	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	"testing"
)

func TestDockerMountIdentityRejectsStaleExtraAndDuplicateBinds(t *testing.T) {
	expected := []mountapi.Mount{{Type: mountapi.TypeBind, Source: "/source", Target: "/workspace"}}
	actual := containerapi.InspectResponse{Mounts: []containerapi.MountPoint{{Type: mountapi.TypeBind, Source: "/source", Destination: "/workspace", RW: true}}}
	if !dockerContainerMountsMatch(actual, expected) {
		t.Fatal("matching mount rejected")
	}
	for _, extra := range []containerapi.MountPoint{
		{Type: mountapi.TypeBind, Source: "/stale", Destination: "/workspace/old", RW: true},
		{Type: mountapi.TypeBind, Source: "/source", Destination: "/workspace", RW: true},
	} {
		changed := actual
		changed.Mounts = append(append([]containerapi.MountPoint(nil), actual.Mounts...), extra)
		if dockerContainerMountsMatch(changed, expected) {
			t.Fatalf("extra bind was accepted: %+v", extra)
		}
	}
	if dockerContainerMountsMatch(actual, append(expected, expected[0])) {
		t.Fatal("duplicate expected bind was accepted")
	}
	actual.Mounts = append(actual.Mounts, containerapi.MountPoint{Type: mountapi.TypeVolume, Name: "image-volume", Destination: "/image-data"})
	if !dockerContainerMountsMatch(actual, expected) {
		t.Fatal("unrelated image-declared volume changed the bind contract")
	}
}

func TestDockerMountIdentityRequiresRecursiveReadOnlyContract(t *testing.T) {
	expected := mountapi.Mount{Type: mountapi.TypeBind, Source: "/source", Target: "/workspace", ReadOnly: true, BindOptions: &mountapi.BindOptions{ReadOnlyForceRecursive: true}}
	for _, test := range []struct {
		name    string
		options *mountapi.BindOptions
		want    bool
	}{
		{name: "missing options"},
		{name: "best effort", options: &mountapi.BindOptions{}},
		{name: "forced recursive", options: &mountapi.BindOptions{ReadOnlyForceRecursive: true}, want: true},
		{name: "nonrecursive", options: &mountapi.BindOptions{ReadOnlyForceRecursive: true, NonRecursive: true}},
		{name: "nonrecursive readonly", options: &mountapi.BindOptions{ReadOnlyForceRecursive: true, ReadOnlyNonRecursive: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			configured := expected
			configured.BindOptions = test.options
			actual := containerapi.InspectResponse{
				ContainerJSONBase: &containerapi.ContainerJSONBase{HostConfig: &containerapi.HostConfig{Mounts: []mountapi.Mount{configured}}},
				Mounts:            []containerapi.MountPoint{{Type: mountapi.TypeBind, Source: "/source", Destination: "/workspace", RW: false}},
			}
			if got := dockerContainerMountsMatch(actual, []mountapi.Mount{expected}); got != test.want {
				t.Fatalf("recursive readonly identity = %v, want %v", got, test.want)
			}
		})
	}
}
