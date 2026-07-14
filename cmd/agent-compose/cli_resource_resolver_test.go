package main

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	agentcomposeapi "agent-compose/pkg/agentcompose/api"
	"agent-compose/pkg/identity"
	"agent-compose/pkg/images"
	domain "agent-compose/pkg/model"
	"agent-compose/pkg/resources"
	"agent-compose/pkg/runtimecache"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestIntegrationCLIInspectResolvesUntypedResourceRefs(t *testing.T) {
	testCLIInspectResolvesUntypedResourceRefs(t)
}

func TestE2ECLIInspectResolvesUntypedResourceRefs(t *testing.T) {
	testCLIInspectResolvesUntypedResourceRefs(t)
}

func testCLIInspectResolvesUntypedResourceRefs(t *testing.T) {
	t.Helper()
	projectID := identity.NewID(identity.ResourceProject, "resolved-project")
	runID := identity.NewID(identity.ResourceRun, "resolved-run")
	sandboxID := identity.NewID(identity.ResourceSandbox, "resolved-sandbox")
	cacheID := identity.NewID(identity.ResourceCache, "resolved-cache")
	imageID := identity.NewID(identity.ResourceKind("image"), "resolved-image")
	project := testCLIProject(projectID, "resolved-project", "/tmp/resolved-project/agent-compose.yml")
	resourceResolver := resources.NewResolver(
		&cliResolverStoredSource{resourcesByRef: map[string][]domain.ResolvedResource{
			"resolved-project":      {{Kind: domain.ResourceKindProject, MatchType: domain.ResourceMatchName, ID: projectID, ShortID: identity.ShortID(projectID), Name: "resolved-project", ProjectID: projectID, ProjectName: "resolved-project", InspectRef: projectID}},
			"reviewer":              {{Kind: domain.ResourceKindAgent, MatchType: domain.ResourceMatchName, ID: project.GetAgents()[0].GetManagedAgentId(), ShortID: identity.ShortID(project.GetAgents()[0].GetManagedAgentId()), Name: "reviewer", ProjectID: projectID, ProjectName: "resolved-project", InspectRef: "reviewer"}},
			identity.ShortID(runID): {{Kind: domain.ResourceKindRun, MatchType: domain.ResourceMatchIDPrefix, ID: runID, ShortID: identity.ShortID(runID), ProjectID: projectID, ProjectName: "resolved-project", InspectRef: runID}},
			"resolved-volume":       {{Kind: domain.ResourceKindVolume, MatchType: domain.ResourceMatchName, Name: "resolved-volume", InspectRef: "resolved-volume"}},
		}},
		&cliResolverSandboxSource{sandbox: &domain.Sandbox{Summary: domain.SandboxSummary{ID: sandboxID, ShortID: identity.ShortID(sandboxID)}}},
		cliResolverImageBackend{image: &agentcomposev2.Image{ImageId: imageID, ImageRef: "resolved:latest"}},
		&cliResolverCacheSource{cacheID: cacheID},
	)

	server := newComposeServiceStubServer(t, composeServiceStubs{
		resourceHandler: agentcomposeapi.NewResourceHandler(resourceResolver),
		project: projectServiceStub{getProject: func(context.Context, *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
		}},
		run: runServiceStub{
			getRun: func(context.Context, *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: &agentcomposev2.RunDetail{Summary: &agentcomposev2.RunSummary{
					RunId: runID, ProjectId: projectID, ProjectName: "resolved-project", AgentName: "reviewer",
				}}}), nil
			},
			listRuns: func(context.Context, *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{}), nil
			},
		},
		session: sessionServiceStub{
			getSession: func(context.Context, *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: &agentcomposev2.Sandbox{SandboxId: sandboxID}}), nil
			},
			listSessions: func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{}), nil
			},
		},
		image: imageServiceStub{inspectImage: func(context.Context, *connect.Request[agentcomposev2.InspectImageRequest]) (*connect.Response[agentcomposev2.InspectImageResponse], error) {
			return connect.NewResponse(&agentcomposev2.InspectImageResponse{Image: &agentcomposev2.Image{ImageId: imageID, ImageRef: "resolved:latest"}}), nil
		}},
		cache: cacheServiceStub{inspectCache: func(context.Context, *connect.Request[agentcomposev2.InspectCacheRequest]) (*connect.Response[agentcomposev2.InspectCacheResponse], error) {
			return connect.NewResponse(&agentcomposev2.InspectCacheResponse{Cache: &agentcomposev2.CacheItem{CacheId: cacheID}}), nil
		}},
		volume: volumeServiceStub{inspectVolume: func(context.Context, *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error) {
			return connect.NewResponse(&agentcomposev2.InspectVolumeResponse{Volume: &agentcomposev2.Volume{Name: "resolved-volume"}}), nil
		}},
	})
	defer server.Close()

	for _, tc := range []struct {
		ref  string
		want string
	}{
		{ref: "resolved-project", want: "resolved-project"},
		{ref: "reviewer", want: "reviewer"},
		{ref: identity.ShortID(runID), want: displayOpaqueID(runID)},
		{ref: identity.ShortID(sandboxID), want: displayOpaqueID(sandboxID)},
		{ref: "resolved:latest", want: "resolved:latest"},
		{ref: identity.ShortID(cacheID), want: shortOpaqueID(cacheID)},
		{ref: "resolved-volume", want: "resolved-volume"},
	} {
		t.Run(tc.ref, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, tc.ref)
			if exitCode != 0 || stderr != "" || !strings.Contains(stdout, tc.want) {
				t.Fatalf("inspect %s code/stdout/stderr = %d/%q/%q, want output containing %q", tc.ref, exitCode, stdout, stderr, tc.want)
			}
		})
	}
}

func TestIntegrationCLIInspectUntypedRefReportsAmbiguityAndNotFound(t *testing.T) {
	testCLIInspectUntypedRefReportsAmbiguityAndNotFound(t)
}

func TestE2ECLIInspectUntypedRefReportsAmbiguityAndNotFound(t *testing.T) {
	testCLIInspectUntypedRefReportsAmbiguityAndNotFound(t)
}

func testCLIInspectUntypedRefReportsAmbiguityAndNotFound(t *testing.T) {
	t.Helper()
	server := newComposeServiceStubServer(t, composeServiceStubs{resource: resourceServiceStub{
		resolveResource: func(_ context.Context, req *connect.Request[agentcomposev2.ResolveResourceRequest]) (*connect.Response[agentcomposev2.ResolveResourceResponse], error) {
			if req.Msg.GetRef() == "ambiguous" {
				return connect.NewResponse(&agentcomposev2.ResolveResourceResponse{Resources: []*agentcomposev2.ResolvedResource{
					{Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT, Name: "ambiguous", Id: strings.Repeat("a", 64)},
					{Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_VOLUME, Name: "ambiguous", InspectRef: "ambiguous"},
				}}), nil
			}
			return connect.NewResponse(&agentcomposev2.ResolveResourceResponse{}), nil
		},
	}})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "ambiguous")
	if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, "project ambiguous") || !strings.Contains(stderr, "volume ambiguous") {
		t.Fatalf("ambiguous inspect code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
	}
	stdout, stderr, _, exitCode = executeCLICommand("inspect", "--host", server.URL, "missing")
	if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, `resource "missing" not found`) {
		t.Fatalf("missing inspect code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
	}
}

type cliResolverStoredSource struct {
	resourcesByRef map[string][]domain.ResolvedResource
}

func (s *cliResolverStoredSource) ResolveStoredResources(_ context.Context, options domain.ResourceResolveOptions) ([]domain.ResolvedResource, error) {
	return append([]domain.ResolvedResource(nil), s.resourcesByRef[options.Ref]...), nil
}

type cliResolverSandboxSource struct {
	sandbox *domain.Sandbox
}

func (s *cliResolverSandboxSource) GetSandbox(context.Context, string) (*domain.Sandbox, error) {
	return s.sandbox, nil
}

func (s *cliResolverSandboxSource) ListSandboxes(context.Context, domain.SandboxListOptions) (domain.SandboxListResult, error) {
	return domain.SandboxListResult{Sandboxes: []*domain.Sandbox{s.sandbox}}, nil
}

type cliResolverCacheSource struct {
	cacheID string
}

func (s *cliResolverCacheSource) ListCaches(context.Context, runtimecache.ListRequest) (runtimecache.ListResult, error) {
	return runtimecache.ListResult{Items: []runtimecache.Item{{CacheID: s.cacheID}}}, nil
}

type cliResolverImageBackend struct {
	image *agentcomposev2.Image
}

func (b cliResolverImageBackend) ListImages(context.Context, images.ListRequest) (images.ListResult, error) {
	return images.ListResult{Images: []*agentcomposev2.Image{b.image}}, nil
}

func (cliResolverImageBackend) PullImage(context.Context, images.PullRequest) (images.PullResult, error) {
	return images.PullResult{}, nil
}

func (b cliResolverImageBackend) InspectImage(_ context.Context, request images.InspectRequest) (images.InspectResult, error) {
	if request.ImageRef == b.image.GetImageRef() || request.ImageRef == b.image.GetImageId() {
		return images.InspectResult{Image: b.image}, nil
	}
	return images.InspectResult{}, nil
}

func (cliResolverImageBackend) RemoveImage(context.Context, images.RemoveRequest) (images.RemoveResult, error) {
	return images.RemoveResult{}, nil
}
