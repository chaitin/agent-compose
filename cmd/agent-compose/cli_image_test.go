package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLIImagesAliasesAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			listImages: func(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
				calls++
				if calls == 1 && (req.Msg.GetQuery() != "agent" || !req.Msg.GetAll()) {
					t.Fatalf("ListImages request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ListImagesResponse{
					Images:     []*agentcomposev2.Image{testCLIImage("sha256:abc1234567890", "agent:latest")},
					TotalCount: 1,
					StoreStatus: &agentcomposev2.ImageStoreStatus{
						Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
						Available: true,
						Endpoint:  "unix:///var/run/docker.sock",
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("images", "--host", server.URL, "--json", "--query", "agent", "--all")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("images --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageListOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("images JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.TotalCount != 1 || decoded.Images[0].ImageRef != "agent:latest" || decoded.StoreStatus.Store != "docker" {
		t.Fatalf("images JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("image", "ls", "--host", server.URL)
	if textCode != 0 || textErr != "" {
		t.Fatalf("image ls code/stderr = %d / %q", textCode, textErr)
	}
	for _, want := range []string{"IMAGE ID", "REF", "DISK USAGE", "abc123456789", "agent:latest", "1.0KB"} {
		if !strings.Contains(textOut, want) {
			t.Fatalf("image ls output %q does not contain %q", textOut, want)
		}
	}
	for _, notWant := range []string{"STORE", "STATUS", "CONTENT SIZE", "docker", "available"} {
		if strings.Contains(textOut, notWant) {
			t.Fatalf("image ls default output %q contains %q", textOut, notWant)
		}
	}

	verboseOut, verboseErr, _, verboseCode := executeCLICommand("images", "--host", server.URL, "--verbose")
	if verboseCode != 0 || verboseErr != "" {
		t.Fatalf("images --verbose code/stderr = %d / %q", verboseCode, verboseErr)
	}
	for _, want := range []string{"REF", "IMAGE ID", "STORE", "STATUS", "PLATFORM", "DISK USAGE", "CONTENT SIZE", "CREATED", "docker", "available", "linux/amd64"} {
		if !strings.Contains(verboseOut, want) {
			t.Fatalf("images --verbose output %q does not contain %q", verboseOut, want)
		}
	}
	if calls != 3 {
		t.Fatalf("ListImages calls = %d, want 3", calls)
	}
}

func TestIntegrationCLIImagePullAliasesAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				calls++
				if req.Msg.GetImageRef() != "agent:latest" {
					t.Fatalf("PullImage image_ref = %q", req.Msg.GetImageRef())
				}
				if calls == 1 && (req.Msg.GetPlatform().GetOs() != "linux" || req.Msg.GetPlatform().GetArchitecture() != "amd64") {
					t.Fatalf("PullImage platform = %#v", req.Msg.GetPlatform())
				}
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:pull123456789", "agent:latest"),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: "agent@sha256:def",
					Progress: []*agentcomposev2.ImagePullProgress{{
						Id:           "layer1",
						Status:       "Downloaded",
						CurrentBytes: 3,
						TotalBytes:   3,
					}},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "--json", "--platform", "linux/amd64", "agent:latest")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("pull --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImagePullOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("pull JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.ImageRef != "agent:latest" || decoded.ResolvedRef != "agent@sha256:def" || decoded.Status != "succeeded" || len(decoded.Progress) != 1 {
		t.Fatalf("pull JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("image", "pull", "--host", server.URL, "agent:latest")
	if textCode != 0 || textErr != "" {
		t.Fatalf("image pull code/stderr = %d / %q", textCode, textErr)
	}
	if !strings.Contains(textOut, "Pulled agent:latest") || !strings.Contains(textOut, "agent@sha256:def") {
		t.Fatalf("image pull output = %q", textOut)
	}
	if calls != 2 {
		t.Fatalf("PullImage calls = %d, want 2", calls)
	}
}

func TestIntegrationCLIImagePullSkippedWarnings(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:local123456789", req.Msg.GetImageRef()),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: "agent@sha256:local",
					Warnings:    []string{"skipped pull: image agent:latest already exists locally"},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "agent:latest")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("pull skipped code/stderr = %d / %q", exitCode, stderr)
	}
	if !strings.Contains(stdout, "Skipped agent:latest") || !strings.Contains(stdout, "already exists locally") || strings.Contains(stdout, "Pulled agent:latest") {
		t.Fatalf("pull skipped stdout = %q", stdout)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("pull", "--host", server.URL, "--json", "agent:latest")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("pull skipped --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeImagePullOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("pull skipped JSON decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Warnings) != 1 || !strings.Contains(decoded.Warnings[0], "already exists locally") {
		t.Fatalf("pull skipped JSON = %#v", decoded)
	}
}

func TestIntegrationCLIPullProjectImages(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-pull-project
agents:
  reviewer:
    provider: codex
    image: agent:v1
  tester:
    provider: codex
    image: agent:v1
  builder:
    provider: codex
    image: agent:v2
`)
	var pulled []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				if req.Msg.GetPlatform().GetOs() != "linux" || req.Msg.GetPlatform().GetArchitecture() != "amd64" {
					t.Fatalf("PullImage platform = %#v", req.Msg.GetPlatform())
				}
				imageRef := req.Msg.GetImageRef()
				pulled = append(pulled, imageRef)
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:"+strings.TrimPrefix(imageRef, "agent:"), imageRef),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: imageRef + "@sha256:def",
				}), nil
			},
		},
	})
	defer server.Close()

	commands := []struct {
		name string
		args []string
	}{
		{name: "top-level alias", args: []string{"pull"}},
		{name: "image command", args: []string{"image", "pull"}},
	}
	for _, command := range commands {
		t.Run(command.name, func(t *testing.T) {
			args := append([]string{}, command.args...)
			args = append(args, "--host", server.URL, "--file", composePath, "--platform", "linux/amd64")
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || stderr != "" {
				t.Fatalf("pull project code/stderr = %d / %q", exitCode, stderr)
			}
			for _, want := range []string{"Pulled agent:v2", "agent:v2@sha256:def", "Pulled agent:v1", "agent:v1@sha256:def"} {
				if !strings.Contains(stdout, want) {
					t.Fatalf("pull project stdout %q does not contain %q", stdout, want)
				}
			}
		})
	}
	if len(pulled) != 4 || pulled[0] != "agent:v2" || pulled[1] != "agent:v1" || pulled[2] != "agent:v2" || pulled[3] != "agent:v1" {
		t.Fatalf("pulled images = %#v", pulled)
	}
}

func TestIntegrationCLIPullProjectImagesJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-pull-project-json
agents:
  reviewer:
    provider: codex
    image: agent:v1
  builder:
    provider: codex
    image: agent:v2
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				imageRef := req.Msg.GetImageRef()
				return connect.NewResponse(&agentcomposev2.PullImageResponse{
					Image:       testCLIImage("sha256:"+strings.TrimPrefix(imageRef, "agent:"), imageRef),
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					ResolvedRef: imageRef + "@sha256:def",
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "--file", composePath, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("pull project --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeProjectImagePullOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("pull project JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Images) != 2 || decoded.Images[0].ImageRef != "agent:v2" || decoded.Images[1].ImageRef != "agent:v1" {
		t.Fatalf("pull project JSON = %#v", decoded)
	}
}

func TestIntegrationCLIImageBuildLegacyProject(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "agent")
	if err := os.Mkdir(contextDir, 0o700); err != nil {
		t.Fatalf("create context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile.agent"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	composePath := writeComposeFile(t, dir, `
name: cli-legacy-build-project
agents:
  reviewer:
    provider: codex
    image: reviewer:dev
    build:
      context: agent
      dockerfile: Dockerfile.agent
      target: runtime
      args:
        NODE_ENV: production
      platforms:
        - linux/amd64
      tags:
        - reviewer:latest
      no_cache: true
      pull: true
`)
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			buildImage: func(ctx context.Context, req *connect.Request[agentcomposev2.BuildImageRequest], stream *connect.ServerStream[agentcomposev2.BuildImageEvent]) error {
				calls++
				if req.Msg.GetContextDir() != contextDir {
					t.Fatalf("BuildImage context_dir = %q, want %q", req.Msg.GetContextDir(), contextDir)
				}
				if req.Msg.GetDockerfile() != "Dockerfile.agent" {
					t.Fatalf("BuildImage dockerfile = %q", req.Msg.GetDockerfile())
				}
				if got := req.Msg.GetTags(); len(got) != 3 || got[0] != "reviewer:dev" || got[1] != "reviewer:latest" || got[2] != "reviewer:ci" {
					t.Fatalf("BuildImage tags = %#v", got)
				}
				if req.Msg.GetBuildArgs()["NODE_ENV"] != "development" {
					t.Fatalf("BuildImage build_args = %#v", req.Msg.GetBuildArgs())
				}
				if req.Msg.GetTarget() != "runtime" || !req.Msg.GetNoCache() || !req.Msg.GetPull() {
					t.Fatalf("BuildImage flags target=%q no_cache=%v pull=%v", req.Msg.GetTarget(), req.Msg.GetNoCache(), req.Msg.GetPull())
				}
				if req.Msg.GetPlatform().GetOs() != "linux" || req.Msg.GetPlatform().GetArchitecture() != "amd64" {
					t.Fatalf("BuildImage platform = %#v", req.Msg.GetPlatform())
				}
				if err := stream.Send(&agentcomposev2.BuildImageEvent{
					Status:   agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_RUNNING,
					Message:  "build step",
					ImageRef: "reviewer:dev",
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.BuildImageEvent{
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					Message:     "Built reviewer:dev",
					ImageRef:    "reviewer:dev",
					ResolvedRef: "reviewer:dev@sha256:built",
					Image:       testCLIImage("sha256:built", "reviewer:dev"),
				})
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("image", "build", "--host", server.URL, "--file", composePath, "-t", "reviewer:ci", "--dockerfile", "Dockerfile.agent", "--target", "runtime", "--build-arg", "NODE_ENV=development", "--platform", "linux/amd64", "--no-cache", "--pull", "reviewer")
	if textCode != 0 || textErr != "" {
		t.Fatalf("image build code/stderr = %d / %q", textCode, textErr)
	}
	if !strings.Contains(textOut, "build step") || !strings.Contains(textOut, "Built reviewer:dev") {
		t.Fatalf("image build output = %q", textOut)
	}
	if calls != 1 {
		t.Fatalf("BuildImage calls = %d, want 1", calls)
	}
}

func TestIntegrationCLIProjectBuildImages(t *testing.T) {
	dir := t.TempDir()
	contextDir := filepath.Join(dir, "agent")
	if err := os.Mkdir(contextDir, 0o700); err != nil {
		t.Fatalf("create context: %v", err)
	}
	if err := os.WriteFile(filepath.Join(contextDir, "Dockerfile.agent"), []byte("FROM scratch\n"), 0o600); err != nil {
		t.Fatalf("write Dockerfile: %v", err)
	}
	composePath := writeComposeFile(t, dir, `
name: cli-build-project
agents:
  reviewer:
    provider: codex
    image: reviewer:dev
    build:
      context: agent
      dockerfile: Dockerfile.agent
      target: runtime
      args:
        NODE_ENV: production
      platforms:
        - linux/amd64
      tags:
        - reviewer:latest
      no_cache: true
      pull: true
  tester:
    provider: codex
    image: tester:dev
`)
	var built []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			buildImage: func(ctx context.Context, req *connect.Request[agentcomposev2.BuildImageRequest], stream *connect.ServerStream[agentcomposev2.BuildImageEvent]) error {
				built = append(built, firstNonEmptyString(req.Msg.GetTags()...))
				if req.Msg.GetContextDir() != contextDir {
					t.Fatalf("BuildImage context_dir = %q, want %q", req.Msg.GetContextDir(), contextDir)
				}
				if req.Msg.GetDockerfile() != "Dockerfile.agent" || req.Msg.GetTarget() != "runtime" {
					t.Fatalf("BuildImage dockerfile/target = %q/%q", req.Msg.GetDockerfile(), req.Msg.GetTarget())
				}
				if req.Msg.GetBuildArgs()["NODE_ENV"] != "development" {
					t.Fatalf("CLI build arg did not override compose args: %#v", req.Msg.GetBuildArgs())
				}
				if got := req.Msg.GetTags(); len(got) != 3 || got[0] != "reviewer:dev" || got[1] != "reviewer:latest" || got[2] != "reviewer:ci" {
					t.Fatalf("BuildImage tags = %#v", got)
				}
				if !req.Msg.GetNoCache() || !req.Msg.GetPull() {
					t.Fatalf("BuildImage no_cache/pull = %v/%v", req.Msg.GetNoCache(), req.Msg.GetPull())
				}
				return stream.Send(&agentcomposev2.BuildImageEvent{
					Status:      agentcomposev2.ImageOperationStatus_IMAGE_OPERATION_STATUS_SUCCEEDED,
					Message:     "Built reviewer:dev",
					ImageRef:    "reviewer:dev",
					ResolvedRef: "reviewer:dev@sha256:built",
					Image:       testCLIImage("sha256:built", "reviewer:dev"),
				})
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("build", "--host", server.URL, "--file", composePath, "--json", "--build-arg", "NODE_ENV=development", "-t", "reviewer:ci")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("project build --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeProjectImageBuildOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("project build JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Images) != 1 || decoded.Images[0].ImageRef != "reviewer:dev" {
		t.Fatalf("project build JSON = %#v", decoded)
	}
	if len(built) != 1 || built[0] != "reviewer:dev" {
		t.Fatalf("built images = %#v", built)
	}
}

func TestIntegrationCLIImageRemoveAliasesAndJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			removeImage: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
				calls++
				if req.Msg.GetImageRef() != "agent:old" {
					t.Fatalf("RemoveImage image_ref = %q", req.Msg.GetImageRef())
				}
				if calls == 1 && !req.Msg.GetForce() {
					t.Fatalf("RemoveImage force = false for rmi")
				}
				if calls == 2 && !req.Msg.GetPruneChildren() {
					t.Fatalf("RemoveImage prune_children = false for image rm")
				}
				return connect.NewResponse(&agentcomposev2.RemoveImageResponse{
					ImageRef:     req.Msg.GetImageRef(),
					UntaggedRefs: []string{"agent:old"},
					DeletedIds:   []string{"sha256:old"},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rmi", "--host", server.URL, "--json", "--force", "agent:old")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("rmi --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageRemoveOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("rmi JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.ImageRef != "agent:old" || decoded.UntaggedRefs[0] != "agent:old" || decoded.DeletedIDs[0] != "old" {
		t.Fatalf("rmi JSON = %#v", decoded)
	}

	textOut, textErr, _, textCode := executeCLICommand("image", "rm", "--host", server.URL, "--prune-children", "agent:old")
	if textCode != 0 || textErr != "" {
		t.Fatalf("image rm code/stderr = %d / %q", textCode, textErr)
	}
	if !strings.Contains(textOut, "Untagged: agent:old") || !strings.Contains(textOut, "Deleted: old") {
		t.Fatalf("image rm output = %q", textOut)
	}
	if calls != 2 {
		t.Fatalf("RemoveImage calls = %d, want 2", calls)
	}
}

func TestIntegrationCLIImageRemoveMissingImageMessage(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			removeImage: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
				if req.Msg.GetImageRef() != "missing:latest" {
					t.Fatalf("RemoveImage image_ref = %q", req.Msg.GetImageRef())
				}
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("remove image: image %s: endpoint unix:///var/run/docker.sock: Error response from daemon: No such image: %s", req.Msg.GetImageRef(), req.Msg.GetImageRef()))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rmi", "--host", server.URL, "missing:latest")
	if exitCode != exitCodeUsage {
		t.Fatalf("rmi missing image exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" {
		t.Fatalf("rmi missing image stdout = %q", stdout)
	}
	if want := "image missing:latest does not exist\n"; stderr != want {
		t.Fatalf("rmi missing image stderr = %q, want %q", stderr, want)
	}
}

func TestIntegrationCLIImageInspectJSON(t *testing.T) {
	calls := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			inspectImage: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
				calls++
				if req.Msg.GetImageRef() != "agent:latest" {
					t.Fatalf("InspectImage image_ref = %q", req.Msg.GetImageRef())
				}
				return connect.NewResponse(&agentcomposev2.InspectImageResponse{
					Image: testCLIImage("sha256:inspect123456789", "agent:latest"),
					StoreStatus: &agentcomposev2.ImageStoreStatus{
						Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
						Available: true,
						Endpoint:  "unix:///var/run/docker.sock",
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "image", "agent:latest")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("inspect image code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageInspectOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("inspect image JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.Image.ImageRef != "agent:latest" || decoded.Image.Platform != "linux/amd64" || decoded.StoreStatus.Endpoint == "" {
		t.Fatalf("inspect image JSON = %#v", decoded)
	}

	imageOut, imageErr, _, imageCode := executeCLICommand("image", "inspect", "--host", server.URL, "agent:latest")
	if imageCode != 0 || imageErr != "" {
		t.Fatalf("image inspect code/stderr = %d / %q", imageCode, imageErr)
	}
	var imageDecoded composeImageInspectOutput
	if err := json.Unmarshal([]byte(imageOut), &imageDecoded); err != nil {
		t.Fatalf("image inspect JSON decode failed: %v\n%s", err, imageOut)
	}
	if imageDecoded.Image.ImageRef != "agent:latest" || imageDecoded.StoreStatus.Endpoint == "" {
		t.Fatalf("image inspect JSON = %#v", imageDecoded)
	}
	if calls != 2 {
		t.Fatalf("InspectImage calls = %d, want 2", calls)
	}
}

func TestIntegrationCLIImageInspectMissingImageMessage(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			inspectImage: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
				if req.Msg.GetImageRef() != "missing:latest" {
					t.Fatalf("InspectImage image_ref = %q", req.Msg.GetImageRef())
				}
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("inspect image: image %s: endpoint unix:///var/run/docker.sock: Error response from daemon: No such image: %s", req.Msg.GetImageRef(), req.Msg.GetImageRef()))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "image", "missing:latest")
	if exitCode != exitCodeUsage {
		t.Fatalf("inspect image missing exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" {
		t.Fatalf("inspect image missing stdout = %q", stdout)
	}
	if want := "image missing:latest does not exist\n"; stderr != want {
		t.Fatalf("inspect image missing stderr = %q, want %q", stderr, want)
	}
}

func TestComposeImageOutputFromProtoAcceptsOCIStatus(t *testing.T) {
	image := testCLIImage("sha256:oci123456789", "agent:latest")
	image.Store = agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE
	image.Docker = nil
	image.Oci = &agentcomposev2.OCIImageStatus{
		LayoutCached:   true,
		RootfsCached:   true,
		CacheKey:       "sha256:oci123456789",
		ManifestDigest: "sha256:manifest",
		ConfigDigest:   "sha256:oci123456789",
		MediaType:      "application/vnd.oci.image.manifest.v1+json",
	}

	output := composeImageOutputFromProto(image)
	if output.Store != "oci-cache" || output.ImageID != "oci123456789" || output.ImageRef != "agent:latest" || output.Platform != "linux/amd64" {
		t.Fatalf("OCI image output = %#v", output)
	}
}

func TestIntegrationCLIImagesJSONAcceptsOCIStoreStatus(t *testing.T) {
	image := testCLIImage("sha256:oci123456789", "agent:latest")
	image.Store = agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE
	image.Docker = nil
	image.Oci = &agentcomposev2.OCIImageStatus{
		LayoutCached:   true,
		CacheKey:       "sha256:oci123456789",
		ManifestDigest: "sha256:manifest",
		ConfigDigest:   "sha256:oci123456789",
		MediaType:      "application/vnd.oci.image.manifest.v1+json",
	}
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			listImages: func(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListImagesResponse{
					Images:     []*agentcomposev2.Image{image},
					TotalCount: 1,
					StoreStatus: &agentcomposev2.ImageStoreStatus{
						Store:     agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_OCI_CACHE,
						Available: true,
						Endpoint:  "/tmp/images/oci",
					},
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("images", "--host", server.URL, "--json")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("images --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeImageListOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("images JSON decode failed: %v\n%s", err, stdout)
	}
	if decoded.TotalCount != 1 || decoded.Images[0].Store != "oci-cache" || decoded.StoreStatus.Store != "oci-cache" || decoded.StoreStatus.Endpoint != "/tmp/images/oci" {
		t.Fatalf("images JSON = %#v", decoded)
	}
}

func TestIntegrationCLIImageDockerErrorIsClear(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		image: imageServiceStub{
			pullImage: func(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
				return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("pull image image agent:missing: endpoint tcp://docker.example:2375: docker daemon unavailable"))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("pull", "--host", server.URL, "agent:missing")
	if exitCode != exitCodeUnavailable {
		t.Fatalf("pull Docker error exit code = %d, want %d; stderr=%q", exitCode, exitCodeUnavailable, stderr)
	}
	if stdout != "" {
		t.Fatalf("pull Docker error stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"agent:missing", "tcp://docker.example:2375", "docker daemon unavailable"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("pull Docker error stderr %q does not contain %q", stderr, want)
		}
	}
}

func TestCLIImageRootCommandShowsHelp(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("image")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("image root code/stderr = %d / %q", exitCode, stderr)
	}
	for _, want := range []string{"Manage daemon images", "build", "inspect", "pull"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("image root help output %q does not contain %q", stdout, want)
		}
	}
	if strings.Contains(strings.ToLower(stdout), "deprecated") {
		t.Fatalf("image root help output = %q", stdout)
	}
}

func useTestDockerImage(t *testing.T, imageRef string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch {
		case strings.HasSuffix(req.URL.Path, "/_ping"):
			w.Header().Set("API-Version", "1.48")
			_, _ = w.Write([]byte("OK"))
		case strings.HasSuffix(req.URL.Path, "/images/"+imageRef+"/json"):
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":       "sha256:test-image",
				"RepoTags": []string{imageRef},
				"Os":       "linux",
			})
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(server.URL, "http://"))
}

type imageServiceStub struct {
	listImages   func(context.Context, *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error)
	pullImage    func(context.Context, *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error)
	inspectImage func(context.Context, *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error)
	removeImage  func(context.Context, *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error)
	buildImage   func(context.Context, *connect.Request[agentcomposev2.BuildImageRequest], *connect.ServerStream[agentcomposev2.BuildImageEvent]) error

	agentcomposev2connect.UnimplementedImageServiceHandler
}

func (s imageServiceStub) ListImages(ctx context.Context, req *connect.Request[agentcomposev2.ListImagesRequest]) (*connect.Response[agentcomposev2.ListImagesResponse], error) {
	if s.listImages == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListImages stub is not configured"))
	}
	return s.listImages(ctx, req)
}

func (s imageServiceStub) PullImage(ctx context.Context, req *connect.Request[agentcomposev2.PullImageRequest]) (*connect.Response[agentcomposev2.PullImageResponse], error) {
	if s.pullImage == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("PullImage stub is not configured"))
	}
	return s.pullImage(ctx, req)
}

func (s imageServiceStub) InspectImage(ctx context.Context, req *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
	if s.inspectImage == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("InspectImage stub is not configured"))
	}
	return s.inspectImage(ctx, req)
}

func (s imageServiceStub) RemoveImage(ctx context.Context, req *connect.Request[agentcomposev2.RemoveImageRequest]) (*connect.Response[agentcomposev2.RemoveImageResponse], error) {
	if s.removeImage == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveImage stub is not configured"))
	}
	return s.removeImage(ctx, req)
}

func (s imageServiceStub) BuildImage(ctx context.Context, req *connect.Request[agentcomposev2.BuildImageRequest], stream *connect.ServerStream[agentcomposev2.BuildImageEvent]) error {
	if s.buildImage == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("BuildImage stub is not configured"))
	}
	return s.buildImage(ctx, req, stream)
}

func testCLIImage(imageID, imageRef string) *agentcomposev2.Image {
	return &agentcomposev2.Image{
		ImageId:            imageID,
		ImageRef:           imageRef,
		ResolvedRef:        "agent@sha256:def",
		RepoTags:           []string{imageRef},
		RepoDigests:        []string{"agent@sha256:def"},
		Store:              agentcomposev2.ImageStoreKind_IMAGE_STORE_KIND_DOCKER_DAEMON,
		AvailabilityStatus: agentcomposev2.ImageAvailabilityStatus_IMAGE_AVAILABILITY_STATUS_AVAILABLE,
		Platform: &agentcomposev2.ImagePlatform{
			Os:           "linux",
			Architecture: "amd64",
		},
		SizeBytes:        1024,
		VirtualSizeBytes: 1024,
		CreatedAt:        "2026-06-11T00:00:00Z",
		InspectedAt:      "2026-06-11T01:00:00Z",
		Docker:           &agentcomposev2.DockerImageStatus{Local: true},
		Labels:           map[string]string{"role": "test"},
	}
}
