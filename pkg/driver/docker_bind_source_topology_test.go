package driver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	containerapi "github.com/docker/docker/api/types/container"
	mountapi "github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/system"
	"github.com/docker/docker/client"
)

func TestDockerWorkspaceSourceResolvesProcessTopologyBeforeContainerName(t *testing.T) {
	probeFailure := errors.New("process topology unavailable")
	for _, test := range []struct {
		name                  string
		process               dockerWorkspaceProcess
		probeErr              error
		endpoint              string
		engine                system.Info
		inspectStatus         int
		unshared              bool
		override              bool
		source                string
		want                  string
		wantInspect, wantInfo int
	}{
		{name: "native Linux hostname collision", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", Name: "daemon-container"}, want: "/project/src", wantInfo: 1},
		{name: "native Linux missing same-name container", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", Name: "daemon-container"}, inspectStatus: 404, want: "/project/src", wantInfo: 1},
		{name: "native Linux remote socket hostname", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", Name: "remote-host"}, wantInfo: 1},
		{name: "native Linux remote TCP collision", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", Name: "daemon-container"}, endpoint: "tcp://remote:2375", wantInfo: 1},
		{name: "native Docker Desktop collision", process: dockerWorkspaceProcess{platform: "darwin", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", OperatingSystem: "Docker Desktop"}, want: "/project/src", wantInfo: 1},
		{name: "native OrbStack collision", process: dockerWorkspaceProcess{platform: "darwin", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", OperatingSystem: "OrbStack"}, want: "/project/src", wantInfo: 1},
		{name: "native unknown macOS engine", process: dockerWorkspaceProcess{platform: "darwin", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", OperatingSystem: "unknown"}, wantInfo: 1},
		{name: "unknown process platform", process: dockerWorkspaceProcess{platform: "unknown", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", Name: "daemon-container"}, wantInfo: 1},
		{name: "container shared source", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container", containerized: true}, want: "/engine/project/src", wantInspect: 1},
		{name: "container writable layer", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container", containerized: true}, unshared: true, wantInspect: 1},
		{name: "container absent from engine", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container", containerized: true}, inspectStatus: 404, wantInspect: 1},
		{name: "container inspect denied", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container", containerized: true}, inspectStatus: 403, wantInspect: 1},
		{name: "container hostname unknown", process: dockerWorkspaceProcess{platform: "linux", containerized: true}},
		{name: "process probe failed", probeErr: probeFailure},
		{name: "explicit override bypasses unknown process", probeErr: probeFailure, override: true, source: "/sandboxes/project", want: "/override/project"},
		{name: "external source with override still verifies native", process: dockerWorkspaceProcess{platform: "linux", hostname: "daemon-container"}, engine: system.Info{OSType: "linux", Name: "daemon-container"}, override: true, want: "/project/src", wantInfo: 1},
	} {
		for _, policy := range []dockerBindSourcePolicy{dockerBindSourceConfigured, dockerBindSourceVerified} {
			t.Run(fmt.Sprintf("%s/policy=%d", test.name, policy), func(t *testing.T) {
				endpoint := test.endpoint
				if endpoint == "" {
					endpoint = "unix:///test/docker.sock"
				}
				inspectCalls, infoCalls, probeCalls := 0, 0, 0
				transport := workspaceDockerTransport{respond: func(request *http.Request) (*http.Response, error) {
					status := http.StatusOK
					var response any
					switch {
					case strings.Contains(request.URL.Path, "/containers/"):
						inspectCalls++
						if test.inspectStatus != 0 {
							status = test.inspectStatus
						}
						if status != http.StatusOK {
							response = map[string]string{"message": "container inspect unavailable"}
						} else {
							self := containerapi.InspectResponse{}
							if !test.unshared {
								self.Mounts = []containerapi.MountPoint{{Type: mountapi.TypeBind, Source: "/engine/project", Destination: "/project"}}
							}
							response = self
						}
					case strings.HasSuffix(request.URL.Path, "/info"):
						infoCalls++
						response = test.engine
					default:
						return nil, fmt.Errorf("unexpected request %s", request.URL.Path)
					}
					payload, err := json.Marshal(response)
					if err != nil {
						return nil, err
					}
					return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(payload)))}, nil
				}}
				dockerClient, err := client.NewClientWithOpts(client.WithHost(endpoint), client.WithVersion("1.44"), client.WithHTTPClient(&http.Client{Transport: transport}))
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = dockerClient.Close() })
				config := testRuntimeMountConfig()
				config.SandboxRoot = "/sandboxes"
				if test.override {
					config.DockerHostSandboxRoot = "/override"
				}
				runtime := &dockerRuntime{config: config, workspaceProcess: func() (dockerWorkspaceProcess, error) { probeCalls++; return test.process, test.probeErr }}
				source := test.source
				if source == "" {
					source = "/project/src"
				}
				want, wantInspect, wantInfo := test.want, test.wantInspect, test.wantInfo
				wantProbe := 1
				if test.override && source == "/sandboxes/project" {
					wantProbe = 0
				}
				if policy == dockerBindSourceConfigured {
					// Compatibility: configured binds keep their historical inspect/fallback
					// contract regardless of the workspace-only topology probe.
					wantProbe, wantInfo = 0, 0
					wantInspect = 1
					want = "/engine/project/src"
					if test.inspectStatus == 404 || test.unshared {
						want = source
					}
					if test.inspectStatus != 0 && test.inspectStatus != 404 {
						want = ""
					}
					if test.override {
						wantInspect = 0
						want = ""
						if source == "/sandboxes/project" {
							want = "/override/project"
						}
					}
				}
				got, err := runtime.resolveDockerBindSource(context.Background(), dockerClient, source, policy)
				if want == "" {
					if err == nil {
						t.Fatalf("unknown topology accepted as %q", got)
					}
				} else if err != nil || got != want {
					t.Fatalf("source = %q, %v; want %q", got, err, want)
				}
				if inspectCalls != wantInspect || infoCalls != wantInfo || probeCalls != wantProbe {
					t.Fatalf("inspect/info/probe calls = %d/%d/%d, want %d/%d/%d", inspectCalls, infoCalls, probeCalls, wantInspect, wantInfo, wantProbe)
				}
				if test.probeErr != nil && policy == dockerBindSourceVerified && !test.override && !errors.Is(err, test.probeErr) {
					t.Fatalf("process probe error lost: %v", err)
				}
			})
		}
	}
}

func TestDockerNativeWorkspaceSourcePreservesInfoErrorsAndCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			requests := 0
			transport := workspaceDockerTransport{respond: func(request *http.Request) (*http.Response, error) {
				requests++
				if !strings.HasSuffix(request.URL.Path, "/info") {
					t.Fatalf("native daemon queried a container: %s", request.URL.Path)
				}
				return nil, fmt.Errorf("engine unavailable")
			}}
			dockerClient, err := client.NewClientWithOpts(client.WithHost("unix:///test/docker.sock"), client.WithVersion("1.44"), client.WithHTTPClient(&http.Client{Transport: transport}))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dockerClient.Close() })
			runtime := &dockerRuntime{config: testRuntimeMountConfig(), workspaceProcess: func() (dockerWorkspaceProcess, error) {
				return dockerWorkspaceProcess{platform: "linux", hostname: "local-host"}, nil
			}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			if _, err := runtime.bindWorkspaceMountSource(ctx, dockerClient, "/project/src"); err == nil {
				t.Fatal("native source mapping hid Engine failure")
			}
			if canceled && requests != 0 {
				t.Fatalf("canceled mapping reached Engine: %d requests", requests)
			}
			if !canceled && requests == 0 {
				t.Fatal("native source was accepted without inspecting Engine")
			}
		})
	}
}
