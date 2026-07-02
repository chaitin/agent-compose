package image

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	cerrdefs "github.com/containerd/errdefs"

	driverpkg "agent-compose/internal/driver"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestImageEnsureSkipsNonDockerDrivers(t *testing.T) {
	testImageEnsureSkipsNonDockerDrivers(t)
}

func TestIntegrationImageEnsureSkipsNonDockerDrivers(t *testing.T) {
	testImageEnsureSkipsNonDockerDrivers(t)
}

func TestE2EImageEnsureSkipsNonDockerDrivers(t *testing.T) {
	testImageEnsureSkipsNonDockerDrivers(t)
}

func testImageEnsureSkipsNonDockerDrivers(t *testing.T) {
	t.Helper()
	service := newProjectServiceTestService(t, newTestConfigStore(t))
	service.images = &fakeImageBackend{
		inspectImage: func(context.Context, ImageInspectRequest) (ImageInspectResult, error) {
			t.Fatal("non-Docker driver should not inspect Docker images")
			return ImageInspectResult{}, nil
		},
	}
	for _, driver := range []string{driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverMicrosandbox} {
		if err := service.ensureDriverImage(context.Background(), driverImageEnsureRequest{
			Driver:      driver,
			ImageRef:    "guest:v1",
			ProjectName: "skip",
			AgentName:   driver,
		}); err != nil {
			t.Fatalf("ensureDriverImage(%s) returned error: %v", driver, err)
		}
	}
}

func TestApplyProjectDockerImageEnsurePullsMissingImage(t *testing.T) {
	testApplyProjectDockerImageEnsurePullsMissingImage(t)
}

func TestIntegrationApplyProjectDockerImageEnsurePullsMissingImage(t *testing.T) {
	testApplyProjectDockerImageEnsurePullsMissingImage(t)
}

func TestE2EApplyProjectDockerImageEnsurePullsMissingImage(t *testing.T) {
	testApplyProjectDockerImageEnsurePullsMissingImage(t)
}

func testApplyProjectDockerImageEnsurePullsMissingImage(t *testing.T) {
	t.Helper()
	service := newProjectServiceTestService(t, newTestConfigStore(t))
	var inspected []string
	var pulled []string
	service.images = &fakeImageBackend{
		inspectImage: func(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
			inspected = append(inspected, req.ImageRef)
			return ImageInspectResult{}, imageBackendOpError{Op: "inspect image", Endpoint: "unix:///var/run/docker.sock", ImageRef: req.ImageRef, Err: cerrdefs.ErrNotFound}
		},
		pullImage: func(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
			pulled = append(pulled, req.ImageRef)
			return ImagePullResult{ResolvedRef: req.ImageRef}, nil
		},
	}

	resp, err := service.ApplyProject(context.Background(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: dockerEnsureProjectSpec("ensure-apply", "docker", ""),
		Source: &agentcomposev2.ProjectSource{
			ComposePath: "/tmp/ensure-apply/agent-compose.yml",
		},
	}))
	if err != nil {
		t.Fatalf("ApplyProject returned error: %v", err)
	}
	if !resp.Msg.GetApplied() {
		t.Fatalf("ApplyProject response = %#v", resp.Msg)
	}
	if strings.Join(inspected, ",") != "guest:latest" || strings.Join(pulled, ",") != "guest:latest" {
		t.Fatalf("image ensure inspected=%v pulled=%v, want guest:latest once", inspected, pulled)
	}
}

func TestApplyProjectNonDockerDriversDoNotRequireDockerImageBackend(t *testing.T) {
	testApplyProjectNonDockerDriversDoNotRequireDockerImageBackend(t)
}

func TestIntegrationApplyProjectNonDockerDriversDoNotRequireDockerImageBackend(t *testing.T) {
	testApplyProjectNonDockerDriversDoNotRequireDockerImageBackend(t)
}

func TestE2EApplyProjectNonDockerDriversDoNotRequireDockerImageBackend(t *testing.T) {
	testApplyProjectNonDockerDriversDoNotRequireDockerImageBackend(t)
}

func testApplyProjectNonDockerDriversDoNotRequireDockerImageBackend(t *testing.T) {
	t.Helper()
	for _, driver := range []string{driverpkg.RuntimeDriverBoxlite, driverpkg.RuntimeDriverMicrosandbox} {
		t.Run(driver, func(t *testing.T) {
			service := newProjectServiceTestService(t, newTestConfigStore(t))
			service.images = noDockerImageBackend(t, driver+" apply")

			resp, err := service.ApplyProject(context.Background(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
				Spec: dockerEnsureProjectSpec("ensure-"+driver, driver, "guest:v1"),
				Source: &agentcomposev2.ProjectSource{
					ComposePath: "/tmp/ensure-" + driver + "/agent-compose.yml",
				},
			}))
			if err != nil {
				t.Fatalf("ApplyProject returned error: %v", err)
			}
			if !resp.Msg.GetApplied() {
				t.Fatalf("ApplyProject response = %#v", resp.Msg)
			}
		})
	}
}

func TestApplyProjectDockerImageEnsureErrorIncludesDriverImageEndpoint(t *testing.T) {
	testApplyProjectDockerImageEnsureErrorIncludesDriverImageEndpoint(t)
}

func TestIntegrationApplyProjectDockerImageEnsureErrorIncludesDriverImageEndpoint(t *testing.T) {
	testApplyProjectDockerImageEnsureErrorIncludesDriverImageEndpoint(t)
}

func TestE2EApplyProjectDockerImageEnsureErrorIncludesDriverImageEndpoint(t *testing.T) {
	testApplyProjectDockerImageEnsureErrorIncludesDriverImageEndpoint(t)
}

func testApplyProjectDockerImageEnsureErrorIncludesDriverImageEndpoint(t *testing.T) {
	t.Helper()
	service := newProjectServiceTestService(t, newTestConfigStore(t))
	service.images = &fakeImageBackend{
		inspectImage: func(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
			return ImageInspectResult{}, imageBackendOpError{Op: "inspect image", Endpoint: "tcp://docker.example:2375", ImageRef: req.ImageRef, Err: errors.New("docker daemon unavailable")}
		},
	}

	_, err := service.ApplyProject(context.Background(), connect.NewRequest(&agentcomposev2.ApplyProjectRequest{
		Spec: dockerEnsureProjectSpec("ensure-error", "docker", "agent:missing"),
		Source: &agentcomposev2.ProjectSource{
			ComposePath: "/tmp/ensure-error/agent-compose.yml",
		},
	}))
	if connect.CodeOf(err) != connect.CodeUnavailable {
		t.Fatalf("ApplyProject error code = %v, want unavailable; err=%v", connect.CodeOf(err), err)
	}
	for _, want := range []string{"driver docker", "agent:missing", "tcp://docker.example:2375", "docker daemon unavailable"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ApplyProject error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestRunAgentDockerImageEnsurePullsMissingImage(t *testing.T) {
	testRunAgentDockerImageEnsurePullsMissingImage(t)
}

func TestIntegrationRunAgentDockerImageEnsurePullsMissingImage(t *testing.T) {
	testRunAgentDockerImageEnsurePullsMissingImage(t)
}

func TestE2ERunAgentDockerImageEnsurePullsMissingImage(t *testing.T) {
	testRunAgentDockerImageEnsurePullsMissingImage(t)
}

func testRunAgentDockerImageEnsurePullsMissingImage(t *testing.T) {
	t.Helper()
	_, service, projectID := setupRunPreparationProject(t, dockerEnsureProjectSpec("ensure-run", "docker", "run:v1"), t.TempDir())
	var inspected []string
	var pulled []string
	service.images = &fakeImageBackend{
		inspectImage: func(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
			inspected = append(inspected, req.ImageRef)
			return ImageInspectResult{}, imageBackendOpError{Op: "inspect image", Endpoint: "unix:///var/run/docker.sock", ImageRef: req.ImageRef, Err: cerrdefs.ErrNotFound}
		},
		pullImage: func(ctx context.Context, req ImagePullRequest) (ImagePullResult, error) {
			pulled = append(pulled, req.ImageRef)
			return ImagePullResult{ResolvedRef: req.ImageRef}, nil
		},
	}

	resp, err := service.RunAgent(context.Background(), connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "run image ensure",
		ClientRequestId: "docker-image-ensure",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	if resp.Msg.GetRun().GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("RunAgent response = %#v", resp.Msg.GetRun().GetSummary())
	}
	if strings.Join(inspected, ",") != "run:v1" || strings.Join(pulled, ",") != "run:v1" {
		t.Fatalf("run image ensure inspected=%v pulled=%v, want run:v1 once", inspected, pulled)
	}
}

func TestRunAgentBoxliteDoesNotRequireDockerImageBackend(t *testing.T) {
	testRunAgentBoxliteDoesNotRequireDockerImageBackend(t)
}

func TestIntegrationRunAgentBoxliteDoesNotRequireDockerImageBackend(t *testing.T) {
	testRunAgentBoxliteDoesNotRequireDockerImageBackend(t)
}

func TestE2ERunAgentBoxliteDoesNotRequireDockerImageBackend(t *testing.T) {
	testRunAgentBoxliteDoesNotRequireDockerImageBackend(t)
}

func testRunAgentBoxliteDoesNotRequireDockerImageBackend(t *testing.T) {
	t.Helper()
	_, service, projectID := setupRunPreparationProject(t, dockerEnsureProjectSpec("ensure-boxlite", "boxlite", "box:v1"), t.TempDir())
	service.images = noDockerImageBackend(t, "boxlite run")

	resp, err := service.RunAgent(context.Background(), connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "run boxlite image skip",
		ClientRequestId: "boxlite-image-skip",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	if resp.Msg.GetRun().GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("RunAgent response = %#v", resp.Msg.GetRun().GetSummary())
	}
}

func TestRunAgentMicrosandboxDoesNotRequireDockerImageBackend(t *testing.T) {
	testRunAgentMicrosandboxDoesNotRequireDockerImageBackend(t)
}

func TestIntegrationRunAgentMicrosandboxDoesNotRequireDockerImageBackend(t *testing.T) {
	testRunAgentMicrosandboxDoesNotRequireDockerImageBackend(t)
}

func TestE2ERunAgentMicrosandboxDoesNotRequireDockerImageBackend(t *testing.T) {
	testRunAgentMicrosandboxDoesNotRequireDockerImageBackend(t)
}

func testRunAgentMicrosandboxDoesNotRequireDockerImageBackend(t *testing.T) {
	t.Helper()
	_, service, projectID := setupRunPreparationProject(t, dockerEnsureProjectSpec("ensure-microsandbox", "microsandbox", "micro:v1"), t.TempDir())
	service.images = noDockerImageBackend(t, "microsandbox run")

	resp, err := service.RunAgent(context.Background(), connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		Prompt:          "run microsandbox image skip",
		ClientRequestId: "microsandbox-image-skip",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned error: %v", err)
	}
	if resp.Msg.GetRun().GetSummary().GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED {
		t.Fatalf("RunAgent response = %#v", resp.Msg.GetRun().GetSummary())
	}
}

func TestRunAgentDockerImageEnsureErrorMarksRunFailed(t *testing.T) {
	testRunAgentDockerImageEnsureErrorMarksRunFailed(t)
}

func TestIntegrationRunAgentDockerImageEnsureErrorMarksRunFailed(t *testing.T) {
	testRunAgentDockerImageEnsureErrorMarksRunFailed(t)
}

func TestE2ERunAgentDockerImageEnsureErrorMarksRunFailed(t *testing.T) {
	testRunAgentDockerImageEnsureErrorMarksRunFailed(t)
}

func testRunAgentDockerImageEnsureErrorMarksRunFailed(t *testing.T) {
	t.Helper()
	store, service, projectID := setupRunPreparationProject(t, dockerEnsureProjectSpec("ensure-run-error", "docker", "agent:missing"), t.TempDir())
	service.images = &fakeImageBackend{
		inspectImage: func(ctx context.Context, req ImageInspectRequest) (ImageInspectResult, error) {
			return ImageInspectResult{}, imageBackendOpError{Op: "inspect image", Endpoint: "tcp://docker.example:2375", ImageRef: req.ImageRef, Err: errors.New("docker daemon unavailable")}
		},
	}

	resp, err := service.RunAgent(context.Background(), connect.NewRequest(&agentcomposev2.RunAgentRequest{
		ProjectId:       projectID,
		AgentName:       "reviewer",
		ClientRequestId: "docker-image-error",
	}))
	if err != nil {
		t.Fatalf("RunAgent returned RPC error: %v", err)
	}
	summary := resp.Msg.GetRun().GetSummary()
	if summary.GetStatus() != agentcomposev2.RunStatus_RUN_STATUS_FAILED {
		t.Fatalf("RunAgent summary = %#v, want failed", summary)
	}
	for _, want := range []string{"session start failed", "driver docker", "agent:missing", "tcp://docker.example:2375"} {
		if !strings.Contains(summary.GetError(), want) {
			t.Fatalf("RunAgent error %q does not contain %q", summary.GetError(), want)
		}
	}
	stored, err := store.GetProjectRun(context.Background(), summary.GetRunId())
	if err != nil {
		t.Fatalf("GetProjectRun returned error: %v", err)
	}
	if stored.Status != ProjectRunStatusFailed || strings.TrimSpace(stored.SessionID) != "" {
		t.Fatalf("stored failed run = %#v", stored)
	}
}

func noDockerImageBackend(t *testing.T, label string) *fakeImageBackend {
	t.Helper()
	return &fakeImageBackend{
		inspectImage: func(context.Context, ImageInspectRequest) (ImageInspectResult, error) {
			t.Fatalf("%s should not inspect Docker images", label)
			return ImageInspectResult{}, nil
		},
		pullImage: func(context.Context, ImagePullRequest) (ImagePullResult, error) {
			t.Fatalf("%s should not pull Docker images", label)
			return ImagePullResult{}, nil
		},
	}
}

func dockerEnsureProjectSpec(name, driver, image string) *agentcomposev2.ProjectSpec {
	return &agentcomposev2.ProjectSpec{
		Name: name,
		Agents: []*agentcomposev2.AgentSpec{{
			Name:     "reviewer",
			Provider: "codex",
			Model:    "gpt-test",
			Image:    image,
			Driver:   &agentcomposev2.DriverSpec{Name: driver},
		}},
	}
}
