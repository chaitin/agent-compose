package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func TestIntegrationCLIInspectProjectExplicitRefOverridesImplicitSelectors(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: compose-project
agents: {}
`)
	targetID := strings.Repeat("b", 64)
	targetProject := testCLIProject(targetID, "target-project", "/projects/target/agent-compose.yml")
	targetProject.Spec = &agentcomposev2.ProjectSpec{Variables: []*agentcomposev2.EnvVarSpec{{Name: "TOKEN", Value: "stored-secret", Secret: true}}}
	shortID := identity.ShortID(targetID)

	var requestedNames []string
	var requestedIDs []string
	resolveRequests := 0
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(_ context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				switch selector := req.Msg.GetProject().GetSelector().(type) {
				case *agentcomposev2.ProjectRef_Name:
					requestedNames = append(requestedNames, selector.Name)
					if selector.Name == targetProject.GetSummary().GetName() {
						return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: targetProject}), nil
					}
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project name %s not found", selector.Name))
				case *agentcomposev2.ProjectRef_ProjectId:
					requestedIDs = append(requestedIDs, selector.ProjectId)
					if selector.ProjectId == targetID {
						return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: targetProject}), nil
					}
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project id %s not found", selector.ProjectId))
				default:
					return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unexpected project selector %T", selector))
				}
			},
		},
		resource: resourceServiceStub{
			resolveID: func(_ context.Context, req *connect.Request[agentcomposev2.ResolveResourceIDRequest]) (*connect.Response[agentcomposev2.ResolveResourceIDResponse], error) {
				resolveRequests++
				if req.Msg.GetId() != shortID {
					t.Fatalf("ResolveID id = %q, want %q", req.Msg.GetId(), shortID)
				}
				if len(req.Msg.GetKinds()) != 1 || req.Msg.GetKinds()[0] != agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
					t.Fatalf("ResolveID kinds = %v, want project only", req.Msg.GetKinds())
				}
				return connect.NewResponse(&agentcomposev2.ResolveResourceIDResponse{Targets: []*agentcomposev2.ResourceTarget{{
					Kind:      agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT,
					Id:        targetID,
					ShortId:   shortID,
					ProjectId: targetID,
				}}}), nil
			},
		},
	})
	defer server.Close()

	tests := []struct {
		name      string
		ref       string
		selectors []string
	}{
		{name: "name overrides file", ref: targetProject.GetSummary().GetName(), selectors: []string{"--file", composePath}},
		{name: "full id overrides project name", ref: targetID, selectors: []string{"--project-name", "selected-project"}},
		{name: "short id overrides file and project name", ref: shortID, selectors: []string{"--file", composePath, "--project-name", "selected-project"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"inspect", "--host", server.URL}
			args = append(args, test.selectors...)
			args = append(args, "--json", "project", test.ref)
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || stderr != "" {
				t.Fatalf("inspect project %s code/stderr = %d/%q", test.ref, exitCode, stderr)
			}
			var output composeProjectOutput
			if err := json.Unmarshal([]byte(stdout), &output); err != nil {
				t.Fatalf("decode inspect project %s output: %v\n%s", test.ref, err, stdout)
			}
			if output.Project.ID != targetID || output.Project.Name != "target-project" {
				t.Fatalf("inspect project %s output = %#v", test.ref, output.Project)
			}
			if got := output.Spec.GetVariables()[0].GetValue(); got != "stored-secret" {
				t.Fatalf("inspect project %s spec secret = %q", test.ref, got)
			}
		})
	}

	if strings.Contains(strings.Join(requestedNames, "\x00"), "selected-project") {
		t.Fatalf("explicit refs fell back to --project-name: requested names = %v", requestedNames)
	}
	if resolveRequests != 1 {
		t.Fatalf("ResolveID requests = %d, want 1 for the short ID only", resolveRequests)
	}
	if len(requestedIDs) != 2 || requestedIDs[0] != targetID || requestedIDs[1] != targetID {
		t.Fatalf("project ID requests = %v, want the target full ID twice", requestedIDs)
	}
}

func TestIntegrationCLIInspectProjectExplicitMissingRefDoesNotFallback(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: compose-project
agents: {}
`)
	selectedProject := testCLIProject(strings.Repeat("a", 64), "selected-project", composePath)
	composeProjectID, err := domain.StableProjectID("compose-project", domain.NormalizeProjectSourcePath(composePath))
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	composeProject := testCLIProject(composeProjectID, "compose-project", composePath)
	missingID := strings.Repeat("c", 64)

	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(_ context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				switch selector := req.Msg.GetProject().GetSelector().(type) {
				case *agentcomposev2.ProjectRef_Name:
					if selector.Name == selectedProject.GetSummary().GetName() {
						return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: selectedProject}), nil
					}
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project name %s not found", selector.Name))
				case *agentcomposev2.ProjectRef_ProjectId:
					if selector.ProjectId == composeProjectID {
						return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: composeProject}), nil
					}
					return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project id %s not found", selector.ProjectId))
				default:
					return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("unexpected project selector %T", selector))
				}
			},
		},
	})
	defer server.Close()

	tests := []struct {
		name      string
		ref       string
		selectors []string
	}{
		{name: "missing name with file", ref: "missing-project", selectors: []string{"--file", composePath}},
		{name: "missing full id with project name", ref: missingID, selectors: []string{"--project-name", selectedProject.GetSummary().GetName()}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args := []string{"inspect", "--host", server.URL}
			args = append(args, test.selectors...)
			args = append(args, "--json", "project", test.ref)
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("inspect missing project %s exit code = %d, want %d; stderr=%q", test.ref, exitCode, exitCodeUsage, stderr)
			}
			if stdout != "" {
				t.Fatalf("inspect missing project %s stdout = %q, want empty", test.ref, stdout)
			}
			if !strings.Contains(stderr, test.ref) || !strings.Contains(strings.ToLower(stderr), "not found") {
				t.Fatalf("inspect missing project %s stderr = %q, want ref and not found", test.ref, stderr)
			}
			if strings.Contains(stderr, selectedProject.GetSummary().GetProjectId()) {
				t.Fatalf("inspect missing project %s fell back to selected project: %q", test.ref, stderr)
			}
		})
	}
}

func TestIntegrationCLIInspectProjectRejectsAmbiguousShortID(t *testing.T) {
	shortID := strings.Repeat("d", 12)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(_ context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("project name %s not found", req.Msg.GetProject().GetName()))
			},
		},
		resource: resourceServiceStub{
			resolveID: func(_ context.Context, req *connect.Request[agentcomposev2.ResolveResourceIDRequest]) (*connect.Response[agentcomposev2.ResolveResourceIDResponse], error) {
				if req.Msg.GetId() != shortID || len(req.Msg.GetKinds()) != 1 || req.Msg.GetKinds()[0] != agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT {
					t.Fatalf("ResolveID request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ResolveResourceIDResponse{Targets: []*agentcomposev2.ResourceTarget{
					{Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT, Id: shortID + strings.Repeat("1", 52), ShortId: shortID},
					{Kind: agentcomposev2.ResourceKind_RESOURCE_KIND_PROJECT, Id: shortID + strings.Repeat("2", 52), ShortId: shortID},
				}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("inspect", "--host", server.URL, "--project-name", "selected-project", "project", shortID)
	if exitCode != exitCodeUsage || stdout != "" || !strings.Contains(strings.ToLower(stderr), "ambiguous") {
		t.Fatalf("inspect ambiguous project code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
	}
}
