package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLIVolumeCommands(t *testing.T) {
	var listCalls int
	var createdLabel string
	var removed []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			listProjects: func(context.Context, *connect.Request[agentcomposev2.ListProjectsRequest]) (*connect.Response[agentcomposev2.ListProjectsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListProjectsResponse{Projects: []*agentcomposev2.ProjectSummary{{
					ProjectId: "project-1",
					Name:      "volume-sharing",
				}}}), nil
			},
		},
		volume: volumeServiceStub{
			listVolumes: func(ctx context.Context, req *connect.Request[agentcomposev2.ListVolumesRequest]) (*connect.Response[agentcomposev2.ListVolumesResponse], error) {
				listCalls++
				if req.Msg.GetQuery() != "cac" || req.Msg.GetDriver() != "local" {
					t.Fatalf("ListVolumes request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.ListVolumesResponse{Volumes: []*agentcomposev2.Volume{testCLIVolume("cache")}}), nil
			},
			createVolume: func(ctx context.Context, req *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error) {
				if req.Msg.GetName() != "cache" || req.Msg.GetDriver() != "local" || req.Msg.GetLabels()["purpose"] != "cache" || req.Msg.GetOptions()["quota"] != "1g" {
					t.Fatalf("CreateVolume request = %#v", req.Msg)
				}
				createdLabel = req.Msg.GetLabels()["purpose"]
				return connect.NewResponse(&agentcomposev2.CreateVolumeResponse{Volume: testCLIVolume("cache"), Created: true}), nil
			},
			inspectVolume: func(ctx context.Context, req *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error) {
				if req.Msg.GetName() != "cache" {
					t.Fatalf("InspectVolume request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.InspectVolumeResponse{Volume: testCLIVolume("cache")}), nil
			},
			removeVolume: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveVolumeRequest]) (*connect.Response[agentcomposev2.RemoveVolumeResponse], error) {
				if !req.Msg.GetForce() {
					t.Fatalf("RemoveVolume force = false")
				}
				removed = append(removed, req.Msg.GetName())
				return connect.NewResponse(&agentcomposev2.RemoveVolumeResponse{Name: req.Msg.GetName(), Removed: true}), nil
			},
			pruneVolumes: func(ctx context.Context, req *connect.Request[agentcomposev2.PruneVolumesRequest]) (*connect.Response[agentcomposev2.PruneVolumesResponse], error) {
				if !req.Msg.GetForce() || req.Msg.GetDriver() != "local" {
					t.Fatalf("PruneVolumes request = %#v", req.Msg)
				}
				return connect.NewResponse(&agentcomposev2.PruneVolumesResponse{
					Removed: []*agentcomposev2.Volume{testCLIVolume("cache")},
					Matched: []*agentcomposev2.Volume{testCLIVolume("cache")},
				}), nil
			},
		},
	})
	defer server.Close()

	textOut, textErr, _, textCode := executeCLICommand("volume", "ls", "--host", server.URL, "--query", "cac", "--driver", "local")
	if textCode != 0 || textErr != "" || !strings.Contains(textOut, "volume-sharing") || strings.Contains(textOut, "project-1") {
		t.Fatalf("volume ls text code/stdout/stderr = %d / %q / %q", textCode, textOut, textErr)
	}

	verboseOut, verboseErr, _, verboseCode := executeCLICommand("volume", "ls", "--host", server.URL, "--verbose", "--query", "cac", "--driver", "local")
	if verboseCode != 0 || verboseErr != "" || !strings.Contains(verboseOut, "PROJECT ID") || !strings.Contains(verboseOut, "volume-sharing") || !strings.Contains(verboseOut, "project-1") {
		t.Fatalf("volume ls --verbose code/stdout/stderr = %d / %q / %q", verboseCode, verboseOut, verboseErr)
	}

	listOut, listErr, _, listCode := executeCLICommand("volume", "ls", "--host", server.URL, "--json", "--query", "cac", "--driver", "local")
	if listCode != 0 || listErr != "" {
		t.Fatalf("volume ls code/stderr = %d / %q", listCode, listErr)
	}
	var listDecoded composeVolumeListOutput
	if err := json.Unmarshal([]byte(listOut), &listDecoded); err != nil {
		t.Fatalf("volume ls JSON decode failed: %v\n%s", err, listOut)
	}
	if len(listDecoded.Volumes) != 1 || listDecoded.Volumes[0].Name != "cache" || listDecoded.Volumes[0].ProjectName != "volume-sharing" || listDecoded.Volumes[0].ProjectID != "project-1" {
		t.Fatalf("volume ls JSON = %#v", listDecoded)
	}

	createOut, createErr, _, createCode := executeCLICommand("volume", "create", "--host", server.URL, "--label", "purpose=cache", "--opt", "quota=1g", "cache")
	if createCode != 0 || createErr != "" || strings.TrimSpace(createOut) != "cache" || createdLabel != "cache" {
		t.Fatalf("volume create code/stdout/stderr = %d / %q / %q label=%q", createCode, createOut, createErr, createdLabel)
	}

	inspectOut, inspectErr, _, inspectCode := executeCLICommand("inspect", "volume", "--host", server.URL, "cache")
	if inspectCode != 0 || inspectErr != "" || !strings.Contains(inspectOut, "Name: cache") || strings.Contains(inspectOut, "Volume ID") || !strings.Contains(inspectOut, "Labels") {
		t.Fatalf("inspect volume code/stdout/stderr = %d / %q / %q", inspectCode, inspectOut, inspectErr)
	}

	removeOut, removeErr, _, removeCode := executeCLICommand("volume", "rm", "--host", server.URL, "--force", "cache", "state")
	if removeCode != 0 || removeErr != "" || !strings.Contains(removeOut, "cache") || !strings.Contains(removeOut, "state") || len(removed) != 2 {
		t.Fatalf("volume rm code/stdout/stderr removed = %d / %q / %q / %#v", removeCode, removeOut, removeErr, removed)
	}

	pruneOut, pruneErr, _, pruneCode := executeCLICommand("volume", "prune", "--host", server.URL, "--driver", "local", "--force")
	if pruneCode != 0 || pruneErr != "" || !strings.Contains(pruneOut, "Removed 1 volume") || listCalls != 3 {
		t.Fatalf("volume prune code/stdout/stderr/listCalls = %d / %q / %q / %d", pruneCode, pruneOut, pruneErr, listCalls)
	}
}

type volumeServiceStub struct {
	listVolumes   func(context.Context, *connect.Request[agentcomposev2.ListVolumesRequest]) (*connect.Response[agentcomposev2.ListVolumesResponse], error)
	createVolume  func(context.Context, *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error)
	inspectVolume func(context.Context, *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error)
	removeVolume  func(context.Context, *connect.Request[agentcomposev2.RemoveVolumeRequest]) (*connect.Response[agentcomposev2.RemoveVolumeResponse], error)
	pruneVolumes  func(context.Context, *connect.Request[agentcomposev2.PruneVolumesRequest]) (*connect.Response[agentcomposev2.PruneVolumesResponse], error)

	agentcomposev2connect.UnimplementedVolumeServiceHandler
}

func (s volumeServiceStub) ListVolumes(ctx context.Context, req *connect.Request[agentcomposev2.ListVolumesRequest]) (*connect.Response[agentcomposev2.ListVolumesResponse], error) {
	if s.listVolumes == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListVolumes stub is not configured"))
	}
	return s.listVolumes(ctx, req)
}

func (s volumeServiceStub) CreateVolume(ctx context.Context, req *connect.Request[agentcomposev2.CreateVolumeRequest]) (*connect.Response[agentcomposev2.CreateVolumeResponse], error) {
	if s.createVolume == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("CreateVolume stub is not configured"))
	}
	return s.createVolume(ctx, req)
}

func (s volumeServiceStub) InspectVolume(ctx context.Context, req *connect.Request[agentcomposev2.InspectVolumeRequest]) (*connect.Response[agentcomposev2.InspectVolumeResponse], error) {
	if s.inspectVolume == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("InspectVolume stub is not configured"))
	}
	return s.inspectVolume(ctx, req)
}

func (s volumeServiceStub) RemoveVolume(ctx context.Context, req *connect.Request[agentcomposev2.RemoveVolumeRequest]) (*connect.Response[agentcomposev2.RemoveVolumeResponse], error) {
	if s.removeVolume == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveVolume stub is not configured"))
	}
	return s.removeVolume(ctx, req)
}

func (s volumeServiceStub) PruneVolumes(ctx context.Context, req *connect.Request[agentcomposev2.PruneVolumesRequest]) (*connect.Response[agentcomposev2.PruneVolumesResponse], error) {
	if s.pruneVolumes == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("PruneVolumes stub is not configured"))
	}
	return s.pruneVolumes(ctx, req)
}

func testCLIVolume(name string) *agentcomposev2.Volume {
	return &agentcomposev2.Volume{
		Name:      name,
		Driver:    "local",
		Path:      "/tmp/agent-compose/volumes/local/11111111-1111-4111-8111-111111111111/data",
		Labels:    map[string]string{"purpose": "cache"},
		ProjectId: "project-1",
		CreatedAt: "2026-07-07T12:00:00Z",
		UpdatedAt: "2026-07-07T12:00:00Z",
	}
}
