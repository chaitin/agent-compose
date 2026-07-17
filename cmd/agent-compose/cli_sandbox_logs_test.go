package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

func TestResolveProjectSandboxIDRefSupportsSchedulerProjectTag(t *testing.T) {
	const projectID = "project-1"
	sandboxID := strings.Repeat("a", 64)
	server := newComposeServiceStubServer(t, composeServiceStubs{sandbox: sandboxServiceStub{
		listSandboxes: func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: []*agentcomposev2.Sandbox{
				{SandboxId: sandboxID, Tags: []*agentcomposev2.SandboxTag{{Name: "project_id", Value: projectID}, {Name: "agent", Value: "worker"}}},
			}}), nil
		},
	}})
	defer server.Close()

	client := agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)
	got, err := resolveProjectSandboxIDRef(context.Background(), client, projectID, sandboxID[:12])
	if err != nil {
		t.Fatalf("resolveProjectSandboxIDRef returned error: %v", err)
	}
	if got != sandboxID {
		t.Fatalf("sandbox id = %q, want %q", got, sandboxID)
	}
}

func TestWriteSandboxHistoryLogsPrintsSchedulerCommandOutput(t *testing.T) {
	const projectID = "project-1"
	sandboxID := strings.Repeat("b", 64)
	server := newComposeServiceStubServer(t, composeServiceStubs{sandbox: sandboxServiceStub{
		listSandboxes: func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: []*agentcomposev2.Sandbox{
				{SandboxId: sandboxID, Tags: []*agentcomposev2.SandboxTag{{Name: "project", Value: projectID}}},
			}}), nil
		},
		listHistory: func(_ context.Context, req *connect.Request[agentcomposev2.ListSandboxHistoryRequest]) (*connect.Response[agentcomposev2.ListSandboxHistoryResponse], error) {
			if req.Msg.GetSandboxId() != sandboxID {
				t.Fatalf("sandbox id = %q, want %q", req.Msg.GetSandboxId(), sandboxID)
			}
			return connect.NewResponse(&agentcomposev2.ListSandboxHistoryResponse{Cells: []*agentcomposev2.SandboxHistoryCell{{Id: "cell-1", Output: "scheduler command output\n", Success: true}}}), nil
		},
	}})
	defer server.Close()

	var out bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	cmd.SetOut(&out)
	client := agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)
	err := writeSandboxHistoryLogs(cmd, cliOptions{}, client, projectID, composeLogsOptions{SandboxID: sandboxID, TailLines: -1})
	if err != nil {
		t.Fatalf("writeSandboxHistoryLogs returned error: %v", err)
	}
	if got := out.String(); got != "scheduler command output\n" {
		t.Fatalf("output = %q", got)
	}
}
