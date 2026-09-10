package driver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
)

func TestDockerWorkspaceContainerSourceMapping(t *testing.T) {
	self := containerapi.InspectResponse{Mounts: []containerapi.MountPoint{
		{Type: mountapi.TypeBind, Source: "/engine/data", Destination: "/data"},
		{Type: mountapi.TypeBind, Source: "/engine/project", Destination: "/data/project"},
		{Type: mountapi.TypeVolume, Source: "/engine/volumes/source", Destination: "/volume"},
		{Type: mountapi.TypeTmpfs, Destination: "/private-tmp"},
		{Type: mountapi.TypeBind, Source: `C:\sources`, Destination: "/windows"},
	}}
	for _, test := range []struct{ source, want string }{
		{"/data/project", "/engine/project"},
		{"/data/project/src", "/engine/project/src"},
		{"/data/project-other", "/engine/data/project-other"},
		{"/volume/nested", "/engine/volumes/source/nested"},
		{"/windows/project", `C:\sources\project`},
		{"/private-tmp/source", ""},
		{"/data-other/source", ""},
		{"/writable-layer/source", ""},
	} {
		t.Run(test.source, func(t *testing.T) {
			got, found, err := dockerContainerBindSource(self, test.source)
			if test.want == "" {
				if err == nil && found {
					t.Fatalf("unmapped container source accepted as %q", got)
				}
				return
			}
			if err != nil || !found || got != test.want {
				t.Fatalf("source mapping = %q, %v; want %q", got, err, test.want)
			}
		})
	}
}

func TestDockerWorkspaceLocalTopologyContract(t *testing.T) {
	for _, test := range []struct {
		name, platform, endpoint, engineName, operatingSystem string
		containerized, want                                   bool
	}{
		{name: "native Linux", platform: "linux", endpoint: "unix:///var/run/docker.sock", engineName: "local-host", want: true},
		{name: "other Linux hostname", platform: "linux", endpoint: "unix:///var/run/docker.sock", engineName: "other-host"},
		{name: "explicit remote", platform: "linux", endpoint: "tcp://other-host:2375", engineName: "local-host"},
		{name: "localhost TCP", platform: "linux", endpoint: "tcp://localhost:2375", engineName: "local-host"},
		{name: "uninspectable container", platform: "linux", endpoint: "unix:///var/run/docker.sock", engineName: "local-host", containerized: true},
		{name: "Docker Desktop", platform: "darwin", endpoint: "unix:///local/docker.sock", operatingSystem: "Docker Desktop", want: true},
		{name: "OrbStack", platform: "darwin", endpoint: "unix:///local/docker.sock", operatingSystem: "OrbStack", want: true},
		{name: "unknown macOS integration", platform: "darwin", endpoint: "unix:///local/docker.sock", operatingSystem: "Ubuntu"},
		{name: "remote Desktop", platform: "darwin", endpoint: "tcp://other-host:2375", operatingSystem: "Docker Desktop"},
		{name: "unknown platform", platform: "freebsd", endpoint: "unix:///local/docker.sock", engineName: "local-host"},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := dockerWorkspaceProcess{platform: test.platform, hostname: "local-host", containerized: test.containerized}
			info := system.Info{OSType: "linux", Name: test.engineName, OperatingSystem: test.operatingSystem}
			if got := dockerWorkspaceLocalEngine(process, test.endpoint, info); got != test.want {
				t.Fatalf("local topology = %v, want %v", got, test.want)
			}
			info.OSType = "windows"
			if dockerWorkspaceLocalEngine(process, test.endpoint, info) {
				t.Fatal("non-Linux guest engine was accepted")
			}
		})
	}
}

type workspaceDockerTransport struct {
	respond func(*http.Request) (*http.Response, error)
}

func (transport workspaceDockerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if err := request.Context().Err(); err != nil {
		return nil, err
	}
	return transport.respond(request)
}

func workspaceDockerClient(t *testing.T, apiVersion string, self containerapi.InspectResponse) *client.Client {
	t.Helper()
	payload, err := json.Marshal(self)
	if err != nil {
		t.Fatal(err)
	}
	transport := workspaceDockerTransport{respond: func(request *http.Request) (*http.Response, error) {
		if !strings.Contains(request.URL.Path, "/containers/") || !strings.HasSuffix(request.URL.Path, "/json") {
			return nil, fmt.Errorf("unexpected Docker request %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
	}}
	dockerClient, err := client.NewClientWithOpts(client.WithHost("unix:///test/docker.sock"), client.WithVersion(apiVersion), client.WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := dockerClient.Close(); err != nil {
			t.Error(err)
		}
	})
	return dockerClient
}

func TestDockerWorkspaceMountUsesExternalMappingAndRecursiveReadOnly(t *testing.T) {
	for _, target := range []string{".", "reference"} {
		for _, readOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/readonly=%v", target, readOnly), func(t *testing.T) {
				sandbox, source := testWorkspaceMountSandbox(t, target, readOnly)
				config := testRuntimeMountConfig()
				config.SandboxRoot = filepath.Dir(hostSandboxDir(sandbox))
				config.DockerHostSandboxRoot = "/engine/sandboxes"
				volumeSource := filepath.Join(hostSandboxDir(sandbox), "existing-volume")
				sandbox.VolumeMounts = []SandboxVolumeMount{{Type: "bind", HostPath: volumeSource, Target: "/cache", ReadOnly: true}}
				if _, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker); err != nil {
					t.Fatal(err)
				}
				self := containerapi.InspectResponse{Mounts: []containerapi.MountPoint{{Type: mountapi.TypeBind, Source: "/engine/project", Destination: filepath.Dir(source)}}}
				dockerClient := workspaceDockerClient(t, "1.44", self)
				runtime := &dockerRuntime{config: config, workspaceProcess: testContainerizedDockerWorkspaceProcess}
				mounts, err := runtime.dockerRuntimeMounts(context.Background(), dockerClient, sandbox)
				if err != nil {
					t.Fatal(err)
				}
				guestTarget := filepath.Join(config.GuestWorkspacePath, target)
				seen := make(map[string]bool)
				for _, mount := range mounts {
					if seen[mount.Target] {
						t.Fatalf("duplicate Docker mount %q", mount.Target)
					}
					seen[mount.Target] = true
					if mount.Target == guestTarget {
						if mount.Source != "/engine/project/source" || mount.ReadOnly != readOnly {
							t.Fatalf("workspace mount = %+v", mount)
						}
						if readOnly && (mount.BindOptions == nil || !mount.BindOptions.ReadOnlyForceRecursive) {
							t.Fatalf("readonly workspace is not forced recursive: %+v", mount)
						}
						if !readOnly && mount.BindOptions != nil {
							t.Fatalf("writable workspace changed bind options: %+v", mount)
						}
					} else if !strings.HasPrefix(mount.Source, "/engine/sandboxes/") || mount.BindOptions != nil {
						t.Fatalf("owned/existing volume mapping changed: %+v", mount)
					}
				}
				if !seen[guestTarget] || !seen["/cache"] {
					t.Fatalf("expected workspace and volume missing: %+v", mounts)
				}
			})
		}
	}
}

func TestDockerWorkspaceReadOnlyRejectsOlderAPI(t *testing.T) {
	sandbox, source := testWorkspaceMountSandbox(t, ".", true)
	config := testRuntimeMountConfig()
	if _, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker); err != nil {
		t.Fatal(err)
	}
	self := containerapi.InspectResponse{Mounts: []containerapi.MountPoint{{Type: mountapi.TypeBind, Source: "/engine/source", Destination: source}}}
	dockerClient := workspaceDockerClient(t, "1.43", self)
	runtime := &dockerRuntime{config: config, workspaceProcess: testContainerizedDockerWorkspaceProcess}
	if _, err := runtime.dockerRuntimeMounts(context.Background(), dockerClient, sandbox); err == nil || !strings.Contains(err.Error(), "1.44") {
		t.Fatalf("older API readonly error = %v", err)
	}
}

func TestDockerWorkspaceMountRejectsWritableLayerSource(t *testing.T) {
	sandbox, _ := testWorkspaceMountSandbox(t, ".", false)
	config := testRuntimeMountConfig()
	if _, err := prepareRuntimeMountManifest(config, sandbox, RuntimeDriverDocker); err != nil {
		t.Fatal(err)
	}
	dockerClient := workspaceDockerClient(t, "1.44", containerapi.InspectResponse{})
	runtime := &dockerRuntime{config: config, workspaceProcess: testContainerizedDockerWorkspaceProcess}
	if _, err := runtime.dockerRuntimeMounts(context.Background(), dockerClient, sandbox); err == nil || !strings.Contains(err.Error(), "writable-layer") {
		t.Fatalf("writable layer source error = %v", err)
	}
}

func testContainerizedDockerWorkspaceProcess() (dockerWorkspaceProcess, error) {
	return dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container", containerized: true}, nil
}
