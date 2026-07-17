package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestWritePrefixedRunOutputHonorsTimestampFlag(t *testing.T) {
	summary := &agentcomposev2.RunSummary{
		RunId:       "run-123456789abc",
		AgentName:   "reviewer",
		CompletedAt: "2026-06-11T00:00:03Z",
	}
	var out strings.Builder
	if err := writePrefixedRunOutput(&out, summary, "line\n", false); err != nil {
		t.Fatalf("writePrefixedRunOutput returned error: %v", err)
	}
	if got, want := out.String(), "reviewer-run-123456789abc | line\n"; got != want {
		t.Fatalf("writePrefixedRunOutput without timestamp = %q, want %q", got, want)
	}
}

func TestCLIRunStreamAndDetailEdgeBranches(t *testing.T) {
	t.Run("stream completes without terminal run", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return nil
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), io.Discard, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "stream completed without terminal run") {
			t.Fatalf("terminal missing error = %v", err)
		}
	})

	t.Run("stream rpc error", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return connect.NewError(connect.CodeUnavailable, fmt.Errorf("runner unavailable"))
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), io.Discard, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "runner unavailable") || commandExitCode(err) != exitCodeUnavailable {
			t.Fatalf("stream rpc error = %v code=%d", err, commandExitCode(err))
		}
	})

	t.Run("warnings aggregate and output can be suppressed", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-warn",
					Warnings:  []string{"event warning"},
					Chunk:     "hidden\n",
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-warn",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-warn",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: req.Msg.GetAgentName(),
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						Warnings:  []string{"summary warning", "event warning"},
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				run := testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-warn", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "stored\n")
				run.Warnings = []string{"detail warning"}
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		var stdout bytes.Buffer
		detail, completed, warnings, err := runComposeRunStreamAndDetail(context.Background(), &stdout, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "reviewer"}, true)
		if err != nil {
			t.Fatalf("run stream returned error: %v", err)
		}
		if stdout.String() != "" || detail.GetSummary().GetRunId() != "run-warn" || completed.GetRunId() != "run-warn" {
			t.Fatalf("stdout/detail/completed = %q/%#v/%#v", stdout.String(), detail, completed)
		}
		if strings.Join(warnings, "|") != "event warning|summary warning" {
			t.Fatalf("warnings = %#v", warnings)
		}
	})

	t.Run("output writer error stops stream", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-write-error",
					Chunk:     "cannot write\n",
				})
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), failingWriter{}, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "write failed") {
			t.Fatalf("writer error = %v", err)
		}
	})

	t.Run("get detail error is wrapped", func(t *testing.T) {
		server := newRunServiceStubServer(t, runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-detail-missing",
					Run:       &agentcomposev2.RunSummary{RunId: "run-detail-missing", ProjectId: req.Msg.GetProjectId(), AgentName: req.Msg.GetAgentName(), Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("missing run"))
			},
		})
		defer server.Close()
		client := agentcomposev2connect.NewRunServiceClient(server.Client(), server.URL)
		_, _, _, err := runComposeRunStreamAndDetail(context.Background(), io.Discard, io.Discard, client, "project-1", "Project", &agentcomposev2.RunAgentRequest{ProjectId: "project-1", AgentName: "reviewer"}, false)
		if err == nil || !strings.Contains(err.Error(), "get run run-detail-missing") || commandExitCode(err) != exitCodeUsage {
			t.Fatalf("get detail error = %v code=%d", err, commandExitCode(err))
		}
	})
}

func TestCLIRunCommandAdditionalEdgeWorkflows(t *testing.T) {
	t.Run("optional prompt flag consumes positional value", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-optional-prompt
agents:
  reviewer:
    provider: codex
`)
		var sawRequest bool
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
					sawRequest = true
					if req.Msg.GetPrompt() != "positional prompt" || req.Msg.GetCommand() != "" || req.Msg.GetTriggerId() != "" {
						t.Fatalf("RunAgentStream request = %#v", req.Msg)
					}
					return stream.Send(&agentcomposev2.RunAgentStreamResponse{
						EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
						RunId:     "run-optional-prompt",
						Run:       &agentcomposev2.RunSummary{RunId: "run-optional-prompt", ProjectId: req.Msg.GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
					})
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-optional", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "positional prompt")
		if exitCode != 0 || stdout != "" || stderr != "" || !sawRequest {
			t.Fatalf("run optional prompt code/stdout/stderr/saw = %d/%q/%q/%v", exitCode, stdout, stderr, sawRequest)
		}
	})

	t.Run("optional command flag consumes positional value", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-optional-command
agents:
  reviewer:
    provider: codex
`)
		var sawRequest bool
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
					sawRequest = true
					if req.Msg.GetCommand() != "echo positional" || req.Msg.GetPrompt() != "" || req.Msg.GetTriggerId() != "" {
						t.Fatalf("RunAgentStream request = %#v", req.Msg)
					}
					return stream.Send(&agentcomposev2.RunAgentStreamResponse{
						EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
						RunId:     "run-optional-command",
						Run:       &agentcomposev2.RunSummary{RunId: "run-optional-command", ProjectId: req.Msg.GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
					})
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-optional", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--command", "echo positional")
		if exitCode != 0 || stdout != "" || stderr != "" || !sawRequest {
			t.Fatalf("run optional command code/stdout/stderr/saw = %d/%q/%q/%v", exitCode, stdout, stderr, sawRequest)
		}
	})

	t.Run("detached response without run", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detached-empty
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				startRun: func(context.Context, *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.StartRunResponse{Started: true}), nil
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "-d", "reviewer", "--prompt", "detached")
		if exitCode != exitCodeGeneral || stdout != "" || !strings.Contains(stderr, "response did not include run summary") {
			t.Fatalf("run detached empty code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
	})

	t.Run("interactive remove cleanup failure becomes command error", func(t *testing.T) {
		composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-cleanup
agents:
  reviewer:
    provider: codex
`)
		server := newComposeServiceStubServer(t, composeServiceStubs{
			run: runServiceStub{
				runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
					return stream.Send(&agentcomposev2.RunAgentStreamResponse{
						EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
						RunId:     "run-interactive-cleanup",
						Run:       &agentcomposev2.RunSummary{RunId: "run-interactive-cleanup", ProjectId: req.Msg.GetProjectId(), AgentName: "reviewer", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, SandboxId: "sandbox-cleanup"},
					})
				},
				getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
					return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-cleanup", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
				},
			},
			sandbox: sandboxServiceStub{
				removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
					if req.Msg.GetSandboxId() != "sandbox-cleanup" || !req.Msg.GetForce() {
						t.Fatalf("RemoveSandbox request = %#v", req.Msg)
					}
					return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("cleanup unavailable"))
				},
			},
		})
		defer server.Close()

		stdout, stderr, _, exitCode := executeCLICommandWithInput("/exit\n", "run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "-i", "--prompt", "first prompt")
		if exitCode != exitCodeUnavailable || stdout != "" || !strings.Contains(stderr, "remove interactive sandbox sandbox-cleanup") {
			t.Fatalf("run interactive cleanup code/stdout/stderr = %d/%q/%q", exitCode, stdout, stderr)
		}
	})
}
