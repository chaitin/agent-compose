package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
	"google.golang.org/protobuf/types/known/timestamppb"

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
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

func TestWriteSandboxHistoryLogsPrintsOldestFirstAndTailsTheNewest(t *testing.T) {
	const projectID = "project-1"
	sandboxID := strings.Repeat("c", 64)
	at := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	server := newComposeServiceStubServer(t, composeServiceStubs{sandbox: sandboxServiceStub{
		listSandboxes: func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListSandboxesResponse{Sandboxes: []*agentcomposev2.Sandbox{
				{SandboxId: sandboxID, Tags: []*agentcomposev2.SandboxTag{{Name: "project", Value: projectID}}},
			}}), nil
		},
		// The server pages history newest first.
		listHistory: func(context.Context, *connect.Request[agentcomposev2.ListSandboxHistoryRequest]) (*connect.Response[agentcomposev2.ListSandboxHistoryResponse], error) {
			return connect.NewResponse(&agentcomposev2.ListSandboxHistoryResponse{Cells: []*agentcomposev2.SandboxHistoryCell{
				{Id: "newer", Output: "step2 line1\nstep2 line2\n", CreatedAt: timestamppb.New(at.Add(time.Minute))},
				{Id: "older", Output: "step1 line1\nstep1 line2\n", CreatedAt: timestamppb.New(at)},
			}}), nil
		},
	}})
	defer server.Close()
	client := agentcomposev2connect.NewSandboxServiceClient(server.Client(), server.URL)
	for tail, want := range map[int]string{
		-1: "step1 line1\nstep1 line2\nstep2 line1\nstep2 line2\n",
		2:  "step2 line1\nstep2 line2\n",
	} {
		var out bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetContext(context.Background())
		cmd.SetOut(&out)
		if err := writeSandboxHistoryLogs(cmd, cliOptions{}, client, projectID, composeLogsOptions{SandboxID: sandboxID, TailLines: tail}); err != nil {
			t.Fatal(err)
		}
		if got := out.String(); got != want {
			t.Fatalf("tail %d: output = %q, want %q", tail, got, want)
		}
	}
}

func TestSandboxLogUnitsInterleaveRunsAndStepsByStart(t *testing.T) {
	at := time.Date(2026, 11, 27, 8, 0, 0, 0, time.UTC)
	runs := []*agentcomposev2.RunSummary{{RunId: "run-late", AgentName: "worker", StartedAt: timestamppb.New(at.Add(2 * time.Minute))}}
	steps := []*agentcomposev2.SchedulerEvent{
		{LinkedCellId: "step-early", AgentName: "worker", CreatedAt: timestamppb.New(at)},
		{LinkedCellId: "step-last", AgentName: "worker", CreatedAt: timestamppb.New(at.Add(3 * time.Minute))},
	}
	var order []string
	for _, unit := range sandboxLogUnits(runs, steps) {
		if unit.run != nil {
			order = append(order, unit.run.GetRunId())
		} else {
			order = append(order, unit.step.GetLinkedCellId())
		}
	}
	if got := strings.Join(order, ","); got != "step-early,run-late,step-last" {
		t.Fatalf("order = %s", got)
	}
	if prefix := runLogPrefix(&agentcomposev2.RunSummary{RunId: stepLogRunIDPrefix + "0123456789abcdef", AgentName: "worker"}); !strings.HasPrefix(prefix, "worker-step-") {
		t.Fatalf("step prefix = %q", prefix)
	}
}
