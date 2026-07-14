package main

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"agent-compose/pkg/identity"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestIntegrationCLILogsResolvesResourceRefsWithoutComposeFile(t *testing.T) {
	testCLILogsResolvesResourceRefsWithoutComposeFile(t)
}

func TestE2ECLILogsResolvesResourceRefsWithoutComposeFile(t *testing.T) {
	testCLILogsResolvesResourceRefsWithoutComposeFile(t)
}

func testCLILogsResolvesResourceRefsWithoutComposeFile(t *testing.T) {
	t.Helper()
	projectID := identity.NewID(identity.ResourceProject, "resolved-logs")
	agentID := identity.NewID(identity.ResourceAgent, "resolved-logs", "reviewer")
	runID := identity.NewID(identity.ResourceRun, "resolved-logs", "run")
	sandboxID := identity.NewID(identity.ResourceSandbox, "resolved-logs", "sandbox")
	resolved := map[string]*agentcomposev2.ResolvedResource{
		"resolved-logs": {
			Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT, Id: projectID, Name: "resolved-logs", ProjectId: projectID, ProjectName: "resolved-logs", InspectRef: projectID,
		},
		"reviewer": {
			Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT, Id: agentID, Name: "reviewer", ProjectId: projectID, ProjectName: "resolved-logs", InspectRef: "reviewer",
		},
		identity.ShortID(runID): {
			Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_RUN, Id: runID, ProjectId: projectID, ProjectName: "resolved-logs", InspectRef: runID,
		},
		identity.ShortID(sandboxID): {
			Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_SANDBOX, Id: sandboxID, InspectRef: sandboxID,
		},
	}
	wantKinds := []agentcomposev2.ResourceKind{
		agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT,
		agentcomposev2.ResourceKind_RESOURCE_KIND_AGENT,
		agentcomposev2.ResourceKind_RESOURCE_KIND_RUN,
		agentcomposev2.ResourceKind_RESOURCE_KIND_SANDBOX,
	}
	var listRequest *agentcomposev2.ListRunsRequest
	var getRequest *agentcomposev2.GetRunRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{
		resource: resourceServiceStub{resolveResource: func(_ context.Context, req *connect.Request[agentcomposev2.ResolveResourceRequest]) (*connect.Response[agentcomposev2.ResolveResourceResponse], error) {
			if !reflect.DeepEqual(req.Msg.GetKinds(), wantKinds) {
				t.Fatalf("ResolveResource kinds = %v, want %v", req.Msg.GetKinds(), wantKinds)
			}
			if req.Msg.GetRef() == "ambiguous" {
				return connect.NewResponse(&agentcomposev2.ResolveResourceResponse{Resources: []*agentcomposev2.ResolvedResource{
					resolved["resolved-logs"], resolved["reviewer"],
				}}), nil
			}
			resource := resolved[req.Msg.GetRef()]
			if resource == nil {
				return connect.NewResponse(&agentcomposev2.ResolveResourceResponse{}), nil
			}
			return connect.NewResponse(&agentcomposev2.ResolveResourceResponse{Resources: []*agentcomposev2.ResolvedResource{resource}}), nil
		}},
		run: runServiceStub{
			listRuns: func(_ context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				listRequest = req.Msg
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
					RunId: runID, ProjectId: projectID, ProjectName: "resolved-logs", AgentName: "reviewer", SandboxId: sandboxID, Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
				}}}), nil
			},
			getRun: func(_ context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				getRequest = req.Msg
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), runID, "reviewer", sandboxID, agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "resolved log output\n")}), nil
			},
		},
	})
	defer server.Close()

	for _, tc := range []struct {
		name         string
		ref          string
		wantList     bool
		projectID    string
		agentName    string
		sandboxID    string
		getProjectID string
	}{
		{name: "project name", ref: "resolved-logs", wantList: true, projectID: projectID, getProjectID: projectID},
		{name: "agent name", ref: "reviewer", wantList: true, projectID: projectID, agentName: "reviewer", getProjectID: projectID},
		{name: "run short id", ref: identity.ShortID(runID), projectID: projectID, getProjectID: projectID},
		{name: "sandbox short id", ref: identity.ShortID(sandboxID), wantList: true, sandboxID: sandboxID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listRequest = nil
			getRequest = nil
			stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--json", tc.ref)
			if exitCode != 0 || stderr != "" || !strings.Contains(stdout, "resolved log output") {
				t.Fatalf("logs %q code/stdout/stderr = %d/%q/%q", tc.ref, exitCode, stdout, stderr)
			}
			if (listRequest != nil) != tc.wantList {
				t.Fatalf("logs %q ListRuns request = %#v, wantList=%t", tc.ref, listRequest, tc.wantList)
			}
			if listRequest != nil && (listRequest.GetProjectId() != tc.projectID || listRequest.GetAgentName() != tc.agentName || listRequest.GetSandboxId() != tc.sandboxID) {
				t.Fatalf("logs %q ListRuns request = %#v", tc.ref, listRequest)
			}
			if getRequest == nil || getRequest.GetRunId() != runID || getRequest.GetProjectId() != tc.getProjectID {
				t.Fatalf("logs %q GetRun request = %#v", tc.ref, getRequest)
			}
		})
	}

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "not found", args: []string{"missing"}, want: `resource "missing" not found`},
		{name: "ambiguous", args: []string{"ambiguous"}, want: "resource ref \"ambiguous\" is ambiguous"},
		{name: "run and sandbox conflict", args: []string{identity.ShortID(runID), "--sandbox", sandboxID}, want: "logs run ref cannot be combined"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"logs", "--host", server.URL}
			args = append(args, tc.args...)
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("logs %v code/stdout/stderr = %d/%q/%q, want %q", tc.args, exitCode, stdout, stderr, tc.want)
			}
		})
	}
}
