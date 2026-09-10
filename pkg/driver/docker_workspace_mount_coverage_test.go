package driver

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
)

func TestDockerWorkspaceMountRejectsOverlappingImageMounts(t *testing.T) {
	for _, target := range []string{".", "reference"} {
		for _, readOnly := range []bool{false, true} {
			sandbox, _ := testWorkspaceMountSandbox(t, target, readOnly)
			runtime := &dockerRuntime{config: testRuntimeMountConfig()}
			guestTarget := filepath.Join("/workspace", target)
			expected := []mountapi.Mount{{Type: mountapi.TypeBind, Source: "/source", Target: guestTarget, ReadOnly: readOnly}}
			if readOnly {
				expected[0].BindOptions = &mountapi.BindOptions{ReadOnlyForceRecursive: true}
			}
			for _, extra := range []struct {
				target string
				valid  bool
			}{
				{guestTarget, false}, {guestTarget + "/child", false}, {"/", false},
				{"/workspace", false}, {"/image-data", true}, {guestTarget + "-other", true},
			} {
				for _, kind := range []mountapi.Type{mountapi.TypeVolume, mountapi.TypeTmpfs} {
					t.Run(fmt.Sprintf("%s/ro=%v/%s/%s", target, readOnly, kind, extra.target), func(t *testing.T) {
						info := containerapi.InspectResponse{
							ContainerJSONBase: &containerapi.ContainerJSONBase{ID: "created-container", HostConfig: &containerapi.HostConfig{Mounts: expected}},
							Mounts: []containerapi.MountPoint{
								{Type: mountapi.TypeBind, Source: "/source", Destination: guestTarget, RW: !readOnly},
								{Type: kind, Destination: extra.target, RW: true},
							},
						}
						if got := runtime.dockerSandboxMountsMatch(info, expected, sandbox); got != extra.valid {
							t.Fatalf("mount identity = %v, want %v", got, extra.valid)
						}
						remover := &recordingDockerContainerRemover{}
						err := runtime.validateCreatedDockerWorkspaceMounts(context.Background(), remover, sandbox, info, expected)
						if (err == nil) != extra.valid {
							t.Fatalf("created mount validation = %v, want accepted=%v", err, extra.valid)
						}
						if extra.valid {
							if remover.containerID != "" {
								t.Fatal("valid container removed")
							}
						} else if remover.containerID != info.ID || !remover.options.Force || !remover.options.RemoveVolumes {
							t.Fatalf("rejected container or its anonymous volumes not cleaned: %#v", remover)
						}
						// Copy retains its existing image-volume behavior.
						copySandbox := *sandbox
						copySandbox.Workspace = nil
						if !runtime.dockerSandboxMountsMatch(info, expected, &copySandbox) {
							t.Fatal("copy image-volume behavior changed")
						}
						copyRemover := &recordingDockerContainerRemover{}
						if err := runtime.validateCreatedDockerWorkspaceMounts(context.Background(), copyRemover, &copySandbox, info, expected); err != nil || copyRemover.containerID != "" {
							t.Fatalf("copy container creation changed: error=%v remover=%#v", err, copyRemover)
						}
					})
				}
			}
		}
	}
}

func TestDockerWorkspaceMountRejectionPreservesCleanupError(t *testing.T) {
	sandbox, _ := testWorkspaceMountSandbox(t, ".", true)
	runtime := &dockerRuntime{config: testRuntimeMountConfig()}
	info := containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{ID: "only-created-container"}}
	expected := []mountapi.Mount{{Type: mountapi.TypeBind, Source: "/source", Target: "/workspace"}}
	cause := errors.New("cannot remove container")
	remover := &recordingDockerContainerRemover{err: cause}
	err := runtime.validateCreatedDockerWorkspaceMounts(context.Background(), remover, sandbox, info, expected)
	if !errors.Is(err, cause) || remover.containerID != info.ID {
		t.Fatalf("cleanup failure was lost or another container removed: error=%v remover=%#v", err, remover)
	}
}

func TestDockerWorkspaceMountRejectionCleansAfterCancellation(t *testing.T) {
	sandbox, _ := testWorkspaceMountSandbox(t, ".", true)
	runtime := &dockerRuntime{config: testRuntimeMountConfig()}
	info := containerapi.InspectResponse{ContainerJSONBase: &containerapi.ContainerJSONBase{ID: "only-created-container"}}
	expected := []mountapi.Mount{{Type: mountapi.TypeBind, Source: "/source", Target: "/workspace"}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	remover := &recordingDockerContainerCleaner{}
	if err := runtime.validateCreatedDockerWorkspaceMounts(ctx, remover, sandbox, info, expected); err == nil {
		t.Fatal("invalid created mount accepted")
	}
	if remover.removedID != info.ID || remover.removeCtxErr != nil || !remover.removeCtxHasDeadline || !remover.removeOptions.RemoveVolumes {
		t.Fatalf("rejected container cleanup did not survive cancellation with a deadline: %#v", remover)
	}
}
