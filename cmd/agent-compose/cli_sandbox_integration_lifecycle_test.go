package main

import (
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLIRunStreamsOutputAndSupportsSandboxReuse(t *testing.T) {
	dir := t.TempDir()
	composePath := writeComposeFile(t, dir, `
name: cli-run-demo
agents:
  reviewer:
    provider: codex
`)
	var sawRequest bool
	server := newRunServiceStubServer(t, runServiceStub{
		runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
			sawRequest = true
			if req.Msg.GetAgentName() != "reviewer" || req.Msg.GetPrompt() != "check this" || req.Msg.GetSandboxId() != "session-reuse" || req.Msg.GetTriggerId() != "" {
				t.Fatalf("RunAgentStream request = %#v", req.Msg)
			}
			if req.Msg.GetSource() != agentcomposev2.RunSource_RUN_SOURCE_MANUAL || req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING {
				t.Fatalf("RunAgentStream source/cleanup = %#v", req.Msg)
			}
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_STARTED,
				RunId:     "run-success",
			}); err != nil {
				return err
			}
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:      "run-success",
				Transcript: &agentcomposev2.TranscriptEvent{Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT, Text: "live output\n"},
			}); err != nil {
				return err
			}
			return stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
				RunId:     "run-success",
				Run: &agentcomposev2.RunSummary{
					RunId:     "run-success",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					SandboxId: "session-reuse",
				},
			})
		},
		runAttach: func(context.Context, *connect.BidiStream[agentcomposev2.RunAttachRequest, agentcomposev2.RunAttachResponse]) error {
			t.Fatalf("RunAttach should not be called for non-interactive run --prompt")
			return nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-success", "reviewer", "session-reuse", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "live output\n")}), nil
		},
	})
	defer server.Close()

	stdout, stderr, runCount, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--sandbox", "session-reuse", "--keep-running", "reviewer", "--prompt", "check this")
	if exitCode != 0 {
		t.Fatalf("run success exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stdout != "live output\n" || stderr != "" {
		t.Fatalf("run success stdout/stderr = %q / %q", stdout, stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	if !sawRequest {
		t.Fatal("RunAgentStream was not called")
	}

	for _, tc := range []struct {
		name string
		flag string
	}{
		{name: "legacy sandbox id flag", flag: "--sandbox-id"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			legacyOut, legacyErr, _, legacyCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, tc.flag, "session-reuse", "--keep-running", "reviewer", "--prompt", "check this")
			if legacyCode != exitCodeUsage {
				t.Fatalf("run %s exit code = %d, want %d; stderr=%q", tc.flag, legacyCode, exitCodeUsage, legacyErr)
			}
			if legacyOut != "" || !strings.Contains(legacyErr, "unknown flag: "+tc.flag) {
				t.Fatalf("run %s stdout/stderr = %q / %q", tc.flag, legacyOut, legacyErr)
			}
		})
	}
}

func TestIntegrationCLIRunInteractivePromptReusesSession(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-prompt
agents:
  reviewer:
    provider: codex
`)
	var prompts []string
	var sessions []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				prompts = append(prompts, req.Msg.GetPrompt())
				sessions = append(sessions, req.Msg.GetSandboxId())
				if req.Msg.GetCommand() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream interactive prompt request = %#v", req.Msg)
				}
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				runID := fmt.Sprintf("run-repl-%d", len(prompts))
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:      runID,
					Transcript: &agentcomposev2.TranscriptEvent{Text: fmt.Sprintf("prompt %d output\n", len(prompts))},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run: &agentcomposev2.RunSummary{
						RunId:     runID,
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-repl",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-repl", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("second prompt\n\n/exit\n", "run", "--host", server.URL, "--file", composePath, "reviewer", "-i", "--prompt", "first prompt")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -i --prompt code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "prompt 1 output\nprompt 2 output\n" {
		t.Fatalf("run -i --prompt stdout = %q", stdout)
	}
	if strings.Join(prompts, "|") != "first prompt|second prompt" {
		t.Fatalf("prompts = %#v", prompts)
	}
	if strings.Join(sessions, "|") != "|sandbox-repl" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestIntegrationCLIRunInteractiveDriverOnlySentForInitialSandbox(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-driver
agents:
  reviewer:
    provider: codex
`)
	var drivers []string
	var prompts []string
	var sessions []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				drivers = append(drivers, req.Msg.GetDriver())
				prompts = append(prompts, req.Msg.GetPrompt())
				sessions = append(sessions, req.Msg.GetSandboxId())
				if req.Msg.GetCommand() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream interactive prompt request = %#v", req.Msg)
				}
				runID := fmt.Sprintf("run-driver-repl-%d", len(prompts))
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:      runID,
					Transcript: &agentcomposev2.TranscriptEvent{Text: fmt.Sprintf("driver %d output\n", len(prompts))},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run: &agentcomposev2.RunSummary{
						RunId:     runID,
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-driver-repl",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-driver-repl", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("second prompt\n/exit\n", "run", "--host", server.URL, "--file", composePath, "--driver", "msb", "reviewer", "-i", "--prompt", "first prompt")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -i --driver --prompt code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "driver 1 output\ndriver 2 output\n" {
		t.Fatalf("run -i --driver --prompt stdout = %q", stdout)
	}
	if strings.Join(prompts, "|") != "first prompt|second prompt" {
		t.Fatalf("prompts = %#v", prompts)
	}
	if strings.Join(sessions, "|") != "|sandbox-driver-repl" {
		t.Fatalf("sessions = %#v", sessions)
	}
	if strings.Join(drivers, "|") != "microsandbox|" {
		t.Fatalf("drivers = %#v", drivers)
	}
}

func TestIntegrationCLIRunInteractiveCommandReusesSession(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-command
agents:
  reviewer:
    provider: codex
`)
	var commands []string
	var sessions []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				commands = append(commands, req.Msg.GetCommand())
				sessions = append(sessions, req.Msg.GetSandboxId())
				if req.Msg.GetPrompt() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream interactive command request = %#v", req.Msg)
				}
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_KEEP_RUNNING {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				runID := fmt.Sprintf("run-command-repl-%d", len(commands))
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType:  agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:      runID,
					Transcript: &agentcomposev2.TranscriptEvent{Text: fmt.Sprintf("command %d output\n", len(commands))},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     runID,
					Run: &agentcomposev2.RunSummary{
						RunId:     runID,
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-command-repl",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-command-repl", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("pwd\nwhoami\n/exit\n", "run", "--host", server.URL, "--file", composePath, "reviewer", "-i", "--command")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -i --command code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if stdout != "command 1 output\ncommand 2 output\n" {
		t.Fatalf("run -i --command stdout = %q", stdout)
	}
	if strings.Join(commands, "|") != "pwd|whoami" {
		t.Fatalf("commands = %#v", commands)
	}
	if strings.Join(sessions, "|") != "|sandbox-command-repl" {
		t.Fatalf("sessions = %#v", sessions)
	}
}

func TestIntegrationCLIRunInteractiveRemoveCreatedSandboxOnExit(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-rm
agents:
  reviewer:
    provider: codex
`)
	var removed []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-repl-rm",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-repl-rm",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-repl-rm",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-repl-rm", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				removed = append(removed, req.Msg.GetSandboxId())
				if !req.Msg.GetForce() {
					t.Fatalf("RemoveSandbox force = false")
				}
				return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{SandboxId: req.Msg.GetSandboxId(), Removed: true}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("", "run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "-i", "--prompt", "first")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run -i --rm code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if strings.Join(removed, "|") != "sandbox-repl-rm" {
		t.Fatalf("removed = %#v", removed)
	}
}

func TestIntegrationCLIRunRemoveSandboxOnSuccess(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-rm
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-rm",
					Chunk:     "done\n",
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-rm",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-rm",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-rm",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-rm", "reviewer", "sandbox-rm", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "done\n")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "--prompt", "clean")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --rm code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "done\n" {
		t.Fatalf("run --rm stdout = %q", stdout)
	}
}

func TestIntegrationCLIRunRemoveSandboxJSONDoesNotPrintCleanupText(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-rm-detail
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-rm-detail",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-rm-detail",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-rm-detail", "reviewer", "sandbox-from-detail", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--rm", "--json", "reviewer", "--prompt", "clean")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --rm --json code/stderr = %d / %q", exitCode, stderr)
	}
	if strings.Contains(stdout, "removed sandbox") {
		t.Fatalf("run --rm --json stdout contains text cleanup output: %q", stdout)
	}
}

func TestIntegrationCLIRunRemoveSandboxError(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-rm-error
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_REMOVE_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-rm-error",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-rm-error",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-rm-error",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				run := testRunDetail(req.Msg.GetProjectId(), "run-rm-error", "reviewer", "sandbox-rm-error", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")
				run.CleanupError = "remove failed"
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: run}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "--rm", "reviewer", "--prompt", "clean")
	if exitCode == 0 {
		t.Fatalf("run --rm cleanup error exit code = %d, want non-zero", exitCode)
	}
	if stdout != "" || !strings.Contains(stderr, "succeeded but sandbox cleanup failed") || !strings.Contains(stderr, "remove failed") {
		t.Fatalf("run --rm cleanup error stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestIntegrationCLILogsFiltersRunAgentSessionAndJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-logs-demo
agents:
  reviewer:
    provider: codex
`)
	runID := identity.NewID(identity.ResourceRun, "logs", "run")
	sandboxID := identity.NewID(identity.ResourceSandbox, "logs", "sandbox")
	var sawFilteredList bool
	server := newRunServiceStubServer(t, runServiceStub{
		listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
			switch req.Msg.GetLimit() {
			case 200:
				if req.Msg.GetSandboxId() != "" {
					t.Fatalf("ListRuns resolver request = %#v", req.Msg)
				}
			case 20:
				sawFilteredList = true
				if req.Msg.GetAgentName() != "reviewer" || req.Msg.GetSandboxId() != sandboxID {
					t.Fatalf("ListRuns filtered request = %#v", req.Msg)
				}
			default:
				t.Fatalf("ListRuns request = %#v", req.Msg)
			}
			return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
				RunId:     runID,
				ProjectId: req.Msg.GetProjectId(),
				AgentName: "reviewer",
				Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
				SandboxId: sandboxID,
			}}}), nil
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			if req.Msg.GetRunId() != runID {
				t.Fatalf("GetRun request = %#v", req.Msg)
			}
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", sandboxID, agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "stored log output\n")}), nil
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "reviewer", "--sandbox", identity.ShortID(sandboxID), "--json")
	if exitCode != 0 {
		t.Fatalf("logs exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stderr != "" {
		t.Fatalf("logs stderr = %q, want empty", stderr)
	}
	var decoded composeLogsOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("logs JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Runs) != 1 || decoded.Runs[0].RunID != displayOpaqueID(runID) || decoded.Runs[0].Prompt != "test prompt" || decoded.Runs[0].Content != "stored log output\n" {
		t.Fatalf("logs JSON = %#v", decoded)
	}
	if !sawFilteredList {
		t.Fatal("ListRuns was not called")
	}

	legacyOut, legacyErr, _, legacyCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--agent", "reviewer", "--session-id", sandboxID, "--json")
	if legacyCode != exitCodeUsage || legacyOut != "" || !strings.Contains(legacyErr, "unknown flag: --session-id") {
		t.Fatalf("logs --session-id code/stdout/stderr = %d / %q / %q", legacyCode, legacyOut, legacyErr)
	}

	runOut, runErr, _, runCode := executeCLICommand("logs", "--host", server.URL, "--file", composePath, "--run", identity.ShortID(runID))
	runPrefix := "reviewer-run-" + identity.ShortID(runID) + " | "
	wantRunOut := expectedLogSeparator(runPrefix, ">") +
		"reviewer-run-" + identity.ShortID(runID) + " | test prompt\n" +
		expectedLogSeparator(runPrefix, "<") +
		"reviewer-run-" + identity.ShortID(runID) + " | stored log output\n"
	if runCode != 0 || runErr != "" || runOut != wantRunOut {
		t.Fatalf("logs --run code/stdout/stderr = %d / %q / %q", runCode, runOut, runErr)
	}
}

func TestIntegrationCLISandboxCommandGroupHelp(t *testing.T) {
	stdout, stderr, runCount, exitCode := executeCLICommand("sandbox", "--help")
	if exitCode != 0 || stderr != "" || runCount != 0 {
		t.Fatalf("sandbox --help code/stderr/runCount = %d / %q / %d", exitCode, stderr, runCount)
	}
	for _, want := range []string{"Manage project sandboxes", "ls", "stop", "resume", "rm", "prune"} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("sandbox --help output %q does not contain %q", stdout, want)
		}
	}
}

func TestIntegrationCLIResumeSandboxesJSON(t *testing.T) {
	var resumed []string
	server := newComposeServiceStubServer(t, composeServiceStubs{
		session: sessionServiceStub{
			resumeSession: func(ctx context.Context, req *connect.Request[agentcomposev2.ResumeSandboxRequest]) (*connect.Response[agentcomposev2.ResumeSandboxResponse], error) {
				resumed = append(resumed, req.Msg.GetSandboxId())
				return connect.NewResponse(&agentcomposev2.ResumeSandboxResponse{}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("resume", "--host", server.URL, "--json", "sandbox-a", "sandbox-b")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("resume --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeSandboxActionOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("resume JSON decode failed: %v\n%s", err, stdout)
	}
	if len(decoded.Results) != 2 ||
		decoded.Results[0].SandboxID != "sandbox-a" ||
		decoded.Results[0].Status != "resumed" ||
		decoded.Results[1].SandboxID != "sandbox-b" ||
		decoded.Results[1].Status != "resumed" {
		t.Fatalf("resume JSON = %#v", decoded)
	}
	if len(resumed) != 2 || resumed[0] != "sandbox-a" || resumed[1] != "sandbox-b" {
		t.Fatalf("resumed sandboxes = %#v", resumed)
	}
}

func TestIntegrationCLIRemoveSandboxes(t *testing.T) {
	type removedRequest struct {
		sandbox string
		force   bool
	}
	var removed []removedRequest
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				removed = append(removed, removedRequest{sandbox: req.Msg.GetSandboxId(), force: req.Msg.GetForce()})
				return connect.NewResponse(&agentcomposev2.RemoveSandboxResponse{
					SandboxId: req.Msg.GetSandboxId(),
					Removed:   true,
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rm", "--host", server.URL, "--force", "sandbox-a")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("rm --force code/stderr = %d / %q", exitCode, stderr)
	}
	if stdout != "removed sandbox sandbox-a\n" {
		t.Fatalf("rm --force stdout = %q", stdout)
	}
	if len(removed) != 1 || removed[0].sandbox != "sandbox-a" || !removed[0].force {
		t.Fatalf("removed requests = %#v", removed)
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("rm", "--host", server.URL, "--json", "sandbox-b", "sandbox-c")
	if jsonCode != 0 || jsonErr != "" {
		t.Fatalf("rm --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	var decoded composeSandboxActionOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("rm JSON decode failed: %v\n%s", err, jsonOut)
	}
	if len(decoded.Results) != 2 || decoded.Results[0].SandboxID != "sandbox-b" || decoded.Results[0].Status != "removed" || decoded.Results[1].SandboxID != "sandbox-c" {
		t.Fatalf("rm JSON = %#v", decoded)
	}
	if len(removed) != 3 || removed[1].force || removed[2].force {
		t.Fatalf("removed requests after json = %#v", removed)
	}

	sandboxOut, sandboxErr, _, sandboxCode := executeCLICommand("sandbox", "rm", "--host", server.URL, "--force", "sandbox-d")
	if sandboxCode != 0 || sandboxErr != "" {
		t.Fatalf("sandbox rm --force code/stderr = %d / %q", sandboxCode, sandboxErr)
	}
	if sandboxOut != "removed sandbox sandbox-d\n" {
		t.Fatalf("sandbox rm --force stdout = %q", sandboxOut)
	}
	if len(removed) != 4 || removed[3].sandbox != "sandbox-d" || !removed[3].force {
		t.Fatalf("removed requests after sandbox rm = %#v", removed)
	}
}

func TestIntegrationCLIInspectProjectAgentRunSandboxSessionJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-inspect-demo
agents:
  reviewer:
    provider: codex
`)
	project := testCLIProject("project-inspect", "cli-inspect-demo", composePath)
	reviewerID, err := domain.StableManagedAgentID(project.GetSummary().GetProjectId(), "reviewer")
	if err != nil {
		t.Fatalf("StableManagedAgentID reviewer returned error: %v", err)
	}
	workerID, err := domain.StableManagedAgentID(project.GetSummary().GetProjectId(), "worker")
	if err != nil {
		t.Fatalf("StableManagedAgentID worker returned error: %v", err)
	}
	project.Agents[0].ManagedAgentId = reviewerID
	project.Agents[1].ManagedAgentId = workerID
	server := newComposeServiceStubServer(t, composeServiceStubs{
		project: projectServiceStub{
			getProject: func(ctx context.Context, req *connect.Request[agentcomposev2.GetProjectRequest]) (*connect.Response[agentcomposev2.GetProjectResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetProjectResponse{Project: project}), nil
			},
		},
		run: runServiceStub{
			listRuns: func(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
				return connect.NewResponse(&agentcomposev2.ListRunsResponse{Runs: []*agentcomposev2.RunSummary{{
					RunId:     "run-inspect",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
					SandboxId: "session-inspect",
				}}}), nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "session-inspect", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "inspect output\n")}), nil
			},
		},
		session: sessionServiceStub{
			getSession: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: testCLISessionDetail(req.Msg.GetSandboxId(), "RUNNING")}), nil
			},
		},
	})
	defer server.Close()

	projectOut, projectErr, _, projectCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "project")
	if projectCode != 0 || projectErr != "" {
		t.Fatalf("inspect project code/stderr = %d / %q", projectCode, projectErr)
	}
	var projectDecoded composeProjectOutput
	if err := json.Unmarshal([]byte(projectOut), &projectDecoded); err != nil {
		t.Fatalf("inspect project JSON decode failed: %v\n%s", err, projectOut)
	}
	if projectDecoded.Project.Name != "cli-inspect-demo" || len(projectDecoded.Agents) != 2 || len(projectDecoded.Schedulers) != 1 {
		t.Fatalf("inspect project JSON = %#v", projectDecoded)
	}
	if strings.Contains(projectOut, "managed_loader") {
		t.Fatalf("inspect project JSON exposes internal loader identity: %s", projectOut)
	}

	agentOut, agentErr, _, agentCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "agent", identity.ShortID(reviewerID))
	if agentCode != 0 || agentErr != "" {
		t.Fatalf("inspect agent code/stderr = %d / %q", agentCode, agentErr)
	}
	var agentDecoded composeAgentInspectOutput
	if err := json.Unmarshal([]byte(agentOut), &agentDecoded); err != nil {
		t.Fatalf("inspect agent JSON decode failed: %v\n%s", err, agentOut)
	}
	if agentDecoded.Agent.Name != "reviewer" || agentDecoded.LatestRun.ID != "run-inspect" || len(agentDecoded.RunningSandboxes) != 1 {
		t.Fatalf("inspect agent JSON = %#v", agentDecoded)
	}

	runOut, runErr, _, runCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "run", "run-inspect")
	if runCode != 0 || runErr != "" {
		t.Fatalf("inspect run code/stderr = %d / %q", runCode, runErr)
	}
	var runDecoded composeRunOutput
	if err := json.Unmarshal([]byte(runOut), &runDecoded); err != nil {
		t.Fatalf("inspect run JSON decode failed: %v\n%s", err, runOut)
	}
	if runDecoded.ID != "run-inspect" || runDecoded.Status != "running" || runDecoded.SandboxID != "session-inspect" {
		t.Fatalf("inspect run JSON = %#v", runDecoded)
	}

	sandboxOut, sandboxErr, _, sandboxCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "sandbox", "session-inspect")
	if sandboxCode != 0 || sandboxErr != "" {
		t.Fatalf("inspect sandbox code/stderr = %d / %q", sandboxCode, sandboxErr)
	}
	var sandboxDecoded composeSandboxOutput
	if err := json.Unmarshal([]byte(sandboxOut), &sandboxDecoded); err != nil {
		t.Fatalf("inspect sandbox JSON decode failed: %v\n%s", err, sandboxOut)
	}
	if sandboxDecoded.SandboxID != "session-inspect" || sandboxDecoded.VMStatus != "running" || sandboxDecoded.Tags["project"] == "" {
		t.Fatalf("inspect sandbox JSON = %#v", sandboxDecoded)
	}

	sessionOut, sessionErr, _, sessionCode := executeCLICommand("inspect", "--host", server.URL, "--file", composePath, "--json", "session", "session-inspect")
	if sessionCode != 0 {
		t.Fatalf("inspect session code = %d; stderr = %q", sessionCode, sessionErr)
	}
	if !strings.Contains(sessionErr, "deprecated") || !strings.Contains(sessionErr, "will be removed") || !strings.Contains(sessionErr, "agent-compose inspect sandbox") {
		t.Fatalf("inspect session stderr missing deprecated warning: %q", sessionErr)
	}
	var sessionDecoded composeSandboxOutput
	if err := json.Unmarshal([]byte(sessionOut), &sessionDecoded); err != nil {
		t.Fatalf("inspect session JSON decode failed: %v\n%s", err, sessionOut)
	}
	if sessionDecoded.SandboxID != "session-inspect" || sessionDecoded.VMStatus != "running" || sessionDecoded.Tags["project"] == "" {
		t.Fatalf("inspect session JSON = %#v", sessionDecoded)
	}
	if !reflect.DeepEqual(sessionDecoded, sandboxDecoded) {
		t.Fatalf("inspect session alias JSON differs from sandbox JSON:\nsession=%#v\nsandbox=%#v", sessionDecoded, sandboxDecoded)
	}
}
