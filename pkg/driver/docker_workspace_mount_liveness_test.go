package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
)

func TestDockerWorkspaceMountLivenessSurvivesUnavailableSource(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "unavailable.sock"))
	for _, target := range []string{".", "reference"} {
		for _, readOnly := range []bool{false, true} {
			for _, replacement := range []string{"missing", "file", "symlink"} {
				t.Run(fmt.Sprintf("%s/ro=%v/%s", target, readOnly, replacement), func(t *testing.T) {
					sandbox, source := testWorkspaceMountSandbox(t, target, readOnly)
					config := testRuntimeMountConfig()
					config.SandboxRoot = filepath.Dir(hostSandboxDir(sandbox))
					config.DockerHostSandboxRoot = "/engine/sandboxes"
					if _, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker); err != nil {
						t.Fatal(err)
					}
					self := containerapi.InspectResponse{Mounts: []containerapi.MountPoint{{Type: mountapi.TypeBind, Source: "/engine/source", Destination: source}}}
					dockerClient := workspaceDockerClient(t, "1.44", self)
					runtime := &dockerRuntime{config: config, workspaceProcess: testContainerizedDockerWorkspaceProcess}
					before, err := runtime.dockerRuntimeMounts(context.Background(), dockerClient, sandbox)
					if err != nil {
						t.Fatal(err)
					}
					moved := source + "-moved"
					if err := os.Rename(source, moved); err != nil {
						t.Fatal(err)
					}
					switch replacement {
					case "file":
						if err := os.WriteFile(source, []byte("replacement"), 0o600); err != nil {
							t.Fatal(err)
						}
					case "symlink":
						if err := os.Symlink(moved, source); err != nil {
							t.Fatal(err)
						}
					}
					actual, err := runtime.dockerRuntimeMountsForLiveSandbox(context.Background(), dockerClient, sandbox)
					if err != nil || !reflect.DeepEqual(actual, before) {
						t.Fatalf("live bind identity changed: got=%+v want=%+v error=%v", actual, before, err)
					}
					if _, err := runtime.dockerRuntimeMounts(context.Background(), dockerClient, sandbox); err == nil {
						t.Fatal("new container accepted unavailable source")
					}
					if _, err := runtime.EnsureSandbox(context.Background(), sandbox, VMState{}, ProxyState{}); err == nil || !strings.Contains(err.Error(), "validate sandbox workspace mount") {
						t.Fatalf("Ensure must reject the source before connecting to Docker: %v", err)
					}
					if _, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker); err == nil {
						t.Fatal("prepare accepted unavailable source")
					}
					if replacement == "missing" {
						if _, err := os.Lstat(source); !os.IsNotExist(err) {
							t.Fatalf("missing source was recreated: %v", err)
						}
					}
				})
			}
		}
	}
}

func TestDockerWorkspaceMountLivenessStillRejectsStaleManifest(t *testing.T) {
	sandbox, source := testWorkspaceMountSandbox(t, ".", true)
	config := testRuntimeMountConfig()
	manifest, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(source); err != nil {
		t.Fatal(err)
	}
	runtime := &dockerRuntime{config: config, workspaceProcess: testContainerizedDockerWorkspaceProcess}
	for _, field := range []string{"source", "readonly", "extra mount"} {
		t.Run(field, func(t *testing.T) {
			changed := manifest
			changed.Mounts = append([]RuntimeMount(nil), manifest.Mounts...)
			switch field {
			case "source":
				changed.Mounts[0].HostPath += "-stale"
			case "readonly":
				changed.Mounts[0].ReadOnly = false
			case "extra mount":
				changed.Mounts = append(changed.Mounts, RuntimeMount{Type: "bind", HostPath: "/stale", GuestPath: "/workspace/extra"})
			}
			payload, err := json.Marshal(changed)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(runtimeMountManifestPath(sandbox), payload, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.dockerRuntimeMountsForLiveSandbox(context.Background(), nil, sandbox); err == nil || !strings.Contains(err.Error(), "does not match") {
				t.Fatalf("stale manifest must be rejected before Docker source mapping: %v", err)
			}
		})
	}
}
