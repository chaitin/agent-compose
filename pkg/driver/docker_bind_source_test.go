package driver

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
)

func TestDockerBindSourceMappingPolicies(t *testing.T) {
	self := containerapi.InspectResponse{Mounts: []containerapi.MountPoint{
		{Type: mountapi.TypeBind, Source: "/engine/data", Destination: "/data"},
		{Type: mountapi.TypeBind, Source: "/engine/project", Destination: "/data/project"},
		{Type: mountapi.TypeVolume, Source: "/engine/volume", Destination: "/volume"},
		{Type: mountapi.TypeBind, Source: `C:\sources`, Destination: "/windows"},
		{Type: mountapi.TypeBind, Source: `\\server\share`, Destination: "/unc"},
		{Type: mountapi.TypeTmpfs, Destination: "/data/project/private"},
		{Type: mountapi.TypeBind, Destination: "/data/incomplete"},
		{Type: mountapi.TypeBind, Source: "/invalid", Destination: ""},
	}}
	dockerClient := workspaceDockerClient(t, "1.44", self)
	runtime := &dockerRuntime{config: testRuntimeMountConfig(), workspaceProcess: testContainerizedDockerWorkspaceProcess}
	for _, test := range []struct {
		source, want string
		unshared     bool
	}{
		{source: "/data", want: "/engine/data"},
		{source: "/data/project", want: "/engine/project"},
		{source: "/data/project/src", want: "/engine/project/src"},
		{source: "/data/project-other", want: "/engine/data/project-other"},
		{source: "/volume/src", want: "/engine/volume/src"},
		{source: "/windows/src", want: `C:\sources\src`},
		{source: "/unc/src", want: `\\server\share\src`},
		{source: "/data/project/private"},
		{source: "/data/project/private/file"},
		{source: "/data/incomplete/file"},
		{source: "/data-other/src", unshared: true},
		{source: "/private/src", unshared: true},
	} {
		for _, policy := range []dockerBindSourcePolicy{dockerBindSourceConfigured, dockerBindSourceVerified} {
			t.Run(fmt.Sprintf("%s/%d", test.source, policy), func(t *testing.T) {
				want := test.want
				if test.unshared && policy == dockerBindSourceConfigured {
					want = test.source
				}
				got, err := runtime.resolveDockerBindSource(context.Background(), dockerClient, test.source, policy)
				if want == "" {
					if err == nil {
						t.Fatalf("unshareable source mapped to %q", got)
					}
					return
				}
				if err != nil || got != want {
					t.Fatalf("mapping = %q, %v; want %q", got, err, want)
				}
			})
		}
	}
}

func TestDockerBindSourceExplicitMappingPrecedenceAndScope(t *testing.T) {
	config := testRuntimeMountConfig()
	config.SandboxRoot = "/data/sandboxes"
	config.DockerHostSandboxRoot = "/configured/sandboxes"
	runtime := &dockerRuntime{config: config, workspaceProcess: testContainerizedDockerWorkspaceProcess}
	self := containerapi.InspectResponse{Mounts: []containerapi.MountPoint{
		{Type: mountapi.TypeBind, Source: "/inspected/sandboxes", Destination: config.SandboxRoot},
		{Type: mountapi.TypeBind, Source: "/inspected/project", Destination: "/project"},
	}}
	dockerClient := workspaceDockerClient(t, "1.44", self)
	for _, policy := range []dockerBindSourcePolicy{dockerBindSourceConfigured, dockerBindSourceVerified} {
		for _, suffix := range []string{"", "sandbox/workspace", "sandbox/home/.agents/skills", "sandbox/volumes/cache"} {
			t.Run(fmt.Sprintf("%d/%s", policy, suffix), func(t *testing.T) {
				// Explicit mappings do not need an inspectable local daemon.
				for _, c := range []*client.Client{nil, dockerClient} {
					got, err := runtime.resolveDockerBindSource(context.Background(), c, filepath.Join(config.SandboxRoot, suffix), policy)
					want := filepath.Join(config.DockerHostSandboxRoot, suffix)
					if err != nil || got != want {
						t.Fatalf("configured mapping = %q, %v; want %q", got, err, want)
					}
				}
			})
		}
	}
	got, err := runtime.bindWorkspaceMountSource(context.Background(), dockerClient, "/project/src")
	if err != nil || got != "/inspected/project/src" {
		t.Fatalf("external workspace used SandboxRoot override: %q, %v", got, err)
	}
	if _, err := runtime.bindRuntimeMountSource(context.Background(), dockerClient, "/project/src"); err == nil {
		t.Fatal("existing configured bind contract no longer rejects a path outside SandboxRoot")
	}
	if _, err := runtime.bindWorkspaceMountSource(context.Background(), nil, "/data/sandboxes-other/src"); err == nil {
		t.Fatal("prefix sibling incorrectly used explicit mapping")
	}
}

func TestDockerBindSourceResolutionFailureContracts(t *testing.T) {
	runtime := &dockerRuntime{config: testRuntimeMountConfig(), workspaceProcess: testContainerizedDockerWorkspaceProcess}
	transport := workspaceDockerTransport{respond: func(request *http.Request) (*http.Response, error) {
		return nil, fmt.Errorf("engine unavailable")
	}}
	dockerClient, err := client.NewClientWithOpts(client.WithHost("unix:///test/docker.sock"), client.WithVersion("1.44"), client.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dockerClient.Close() })
	for _, policy := range []dockerBindSourcePolicy{dockerBindSourceConfigured, dockerBindSourceVerified} {
		if _, err := runtime.resolveDockerBindSource(context.Background(), dockerClient, "/project/src", policy); err == nil {
			t.Fatalf("policy %d hid Docker inspect failure", policy)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := runtime.resolveDockerBindSource(ctx, dockerClient, "/project/src", policy); err == nil {
			t.Fatalf("policy %d hid cancellation", policy)
		}
		if _, err := runtime.resolveDockerBindSource(context.Background(), nil, " ", policy); err == nil || !strings.Contains(err.Error(), "empty") {
			t.Fatalf("policy %d accepted empty source: %v", policy, err)
		}
	}
}
