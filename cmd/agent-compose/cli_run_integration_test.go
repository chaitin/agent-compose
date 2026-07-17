package main

import (
	"agent-compose/pkg/identity"
	domain "agent-compose/pkg/model"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestIntegrationCLIRunDetachStartsBackgroundRun(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detach
agents:
  reviewer:
    provider: codex
`)
	var sawStart bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			startRun: func(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				sawStart = true
				runReq := req.Msg.GetRun()
				if runReq.GetAgentName() != "reviewer" || runReq.GetCommand() != "echo detached" || runReq.GetSandboxId() != "" || runReq.GetDriver() != "microsandbox" {
					t.Fatalf("StartRun request = %#v", runReq)
				}
				if runReq.GetSource() != agentcomposev2.RunSource_RUN_SOURCE_MANUAL {
					t.Fatalf("StartRun source = %#v", runReq.GetSource())
				}
				return connect.NewResponse(&agentcomposev2.StartRunResponse{
					Run: &agentcomposev2.RunSummary{
						RunId:       "run-detached",
						ProjectId:   runReq.GetProjectId(),
						ProjectName: "cli-run-detach",
						AgentName:   "reviewer",
						Status:      agentcomposev2.RunStatus_RUN_STATUS_PENDING,
						SandboxId:   "sandbox-detached",
						Source:      agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
					},
					Warnings: []string{"detached warning"},
					Started:  true,
				}), nil
			},
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for detached run")
				return nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "-d", "--host", server.URL, "--file", composePath, "--driver", "msb", "reviewer", "--command", "echo detached")
	if exitCode != 0 {
		t.Fatalf("run -d exit code = %d, stderr=%q", exitCode, stderr)
	}
	for _, want := range []string{
		"Run: run-detached",
		"Sandbox: sandbox-detached",
		"Status: pending",
		"Logs: agent-compose --host " + server.URL + " --file " + composePath + " logs --run run-detached --follow",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run -d stdout %q does not contain %q", stdout, want)
		}
	}
	if !strings.Contains(stderr, "warning: detached warning") {
		t.Fatalf("run -d stderr = %q", stderr)
	}
	if !sawStart {
		t.Fatal("StartRun was not called")
	}
}

func TestIntegrationCLIRunDetachJupyterExposePrintsURL(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detach-jupyter
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			startRun: func(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				runReq := req.Msg.GetRun()
				if runReq.GetJupyter() == nil || !runReq.GetJupyter().GetEnabled() || !runReq.GetJupyter().GetExpose() {
					t.Fatalf("StartRun jupyter request = %#v", runReq)
				}
				return connect.NewResponse(&agentcomposev2.StartRunResponse{
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-detached-jupyter",
						ProjectId: runReq.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_PENDING,
						Source:    agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
					},
					Started: true,
				}), nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				if req.Msg.GetRunId() != "run-detached-jupyter" {
					t.Fatalf("GetRun id = %q", req.Msg.GetRunId())
				}
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-detached-jupyter", "reviewer", "sandbox-detached-jupyter", agentcomposev2.RunStatus_RUN_STATUS_RUNNING, 0, "")}), nil
			},
		},
		session: sessionServiceStub{
			getSessionProxy: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: testCLISandboxProxy(req.Msg.GetSandboxId(), "detached-token")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "-d", "--host", server.URL, "--file", composePath, "reviewer", "--jupyter-expose", "--prompt", "inspect")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -d --jupyter-expose code/stderr = %d / %q", exitCode, stderr)
	}
	if want := "Jupyter: " + server.URL + "/agent-compose/session/sandbox-detached-jupyter/lab?token=detached-token"; !strings.Contains(stdout, want) {
		t.Fatalf("run -d --jupyter-expose stdout %q does not contain %q", stdout, want)
	}
}

func TestIntegrationCLIRunDetachJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-detach-json
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			startRun: func(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
				runReq := req.Msg.GetRun()
				return connect.NewResponse(&agentcomposev2.StartRunResponse{
					Run: &agentcomposev2.RunSummary{
						RunId:       "run-detached-json",
						ProjectId:   runReq.GetProjectId(),
						ProjectName: "cli-run-detach-json",
						AgentName:   "reviewer",
						Status:      agentcomposev2.RunStatus_RUN_STATUS_RUNNING,
						SandboxId:   "sandbox-json",
						Source:      agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
						Warnings:    []string{"summary warning"},
					},
					Warnings: []string{"response warning"},
					Started:  true,
				}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "-d", "--json", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "inspect")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run -d --json code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeRunOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode run -d JSON: %v\n%s", err, stdout)
	}
	if decoded.ID != "run-detached-json" || decoded.SandboxID != "sandbox-json" || decoded.Status != "running" {
		t.Fatalf("run -d JSON decoded = %#v", decoded)
	}
	if !strings.Contains(decoded.LogsCommand, "logs --run run-detached-json --follow") || len(decoded.Warnings) != 2 {
		t.Fatalf("run -d JSON logs/warnings = %#v", decoded)
	}
}

func TestIntegrationCLIRunSendsJupyterExpose(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-jupyter
agents:
  reviewer:
    provider: codex
`)
	var sawRequest bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				sawRequest = true
				if req.Msg.GetJupyter() == nil || !req.Msg.GetJupyter().GetEnabled() || !req.Msg.GetJupyter().GetExpose() {
					t.Fatalf("RunAgentStream jupyter request = %#v", req.Msg)
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-jupyter",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-jupyter",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-jupyter",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-jupyter", "reviewer", "sandbox-jupyter", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		session: sessionServiceStub{
			getSessionProxy: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				if req.Msg.GetSandboxId() != "sandbox-jupyter" {
					t.Fatalf("GetSandbox id = %q", req.Msg.GetSandboxId())
				}
				return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: testCLISandboxProxy("sandbox-jupyter", "sync-token")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--jupyter-expose", "--keep-running", "--prompt", "inspect")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --jupyter-expose code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if want := "Jupyter: " + server.URL + "/agent-compose/session/sandbox-jupyter/lab?token=sync-token\n"; stdout != want {
		t.Fatalf("run --jupyter-expose stdout = %q, want %q", stdout, want)
	}
	if !sawRequest {
		t.Fatal("RunAgentStream was not called")
	}
}

func TestIntegrationCLIRunJupyterWithoutExposePrintsTokenRedirectEntry(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-jupyter-private
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetJupyter() == nil || !req.Msg.GetJupyter().GetEnabled() || req.Msg.GetJupyter().GetExpose() {
					t.Fatalf("RunAgentStream jupyter request = %#v", req.Msg)
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-jupyter-private",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-jupyter-private",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-jupyter-private",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-jupyter-private", "reviewer", "sandbox-jupyter-private", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		session: sessionServiceStub{
			getSessionProxy: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: &agentcomposev2.Sandbox{
					SandboxId: req.Msg.GetSandboxId(),
					ProxyPath: "/agent-compose/session/" + req.Msg.GetSandboxId() + "/lab",
					Status:    domain.VMStatusRunning,
				}}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--jupyter", "--keep-running", "--prompt", "inspect")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --jupyter code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	want := "Jupyter: " + server.URL + "/agent-compose/session/sandbox-jupyter-private\n"
	if stdout != want {
		t.Fatalf("run --jupyter stdout = %q, want %q", stdout, want)
	}
}

func TestIntegrationCLIRunJupyterExposeDefaultCleanupDoesNotPrintURL(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-jupyter-stopped
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetJupyter() == nil || !req.Msg.GetJupyter().GetEnabled() || !req.Msg.GetJupyter().GetExpose() {
					t.Fatalf("RunAgentStream jupyter request = %#v", req.Msg)
				}
				if req.Msg.GetCleanupPolicy() != agentcomposev2.RunSandboxCleanupPolicy_RUN_SANDBOX_CLEANUP_POLICY_STOP_ON_COMPLETION {
					t.Fatalf("RunAgentStream cleanup policy = %#v", req.Msg.GetCleanupPolicy())
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-jupyter-stopped",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-jupyter-stopped",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-jupyter-stopped",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-jupyter-stopped", "reviewer", "sandbox-jupyter-stopped", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		session: sessionServiceStub{
			getSessionProxy: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				t.Fatalf("GetSessionProxy should not be called for stopped jupyter run")
				return nil, nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--jupyter-expose", "--prompt", "inspect")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run --jupyter-expose default cleanup code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
}

func TestIntegrationCLIRunJupyterExposeJSONIncludesURL(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-jupyter-json
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				if req.Msg.GetJupyter() == nil || !req.Msg.GetJupyter().GetEnabled() || !req.Msg.GetJupyter().GetExpose() {
					t.Fatalf("RunAgentStream jupyter request = %#v", req.Msg)
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-jupyter-json",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-jupyter-json",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-jupyter-json",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-jupyter-json", "reviewer", "sandbox-jupyter-json", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		session: sessionServiceStub{
			getSessionProxy: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetSandboxResponse{Sandbox: testCLISandboxProxy(req.Msg.GetSandboxId(), "json-token")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--json", "--host", server.URL, "--file", composePath, "reviewer", "--jupyter-expose", "--keep-running", "--prompt", "inspect")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --json --jupyter-expose code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeRunOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode jupyter run JSON: %v\n%s", err, stdout)
	}
	if decoded.JupyterPath != "/agent-compose/session/sandbox-jupyter-json/lab" || decoded.JupyterURL != server.URL+"/agent-compose/session/sandbox-jupyter-json/lab?token=json-token" {
		t.Fatalf("jupyter JSON fields = %q / %q", decoded.JupyterPath, decoded.JupyterURL)
	}
}

func TestIntegrationCLIRunJupyterExposeJSONDefaultCleanupOmitsURL(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-jupyter-json-stopped
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-jupyter-json-stopped",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-jupyter-json-stopped",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-jupyter-json-stopped",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-jupyter-json-stopped", "reviewer", "sandbox-jupyter-json-stopped", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
		session: sessionServiceStub{
			getSessionProxy: func(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
				t.Fatalf("GetSessionProxy should not be called for stopped jupyter JSON run")
				return nil, nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--json", "--host", server.URL, "--file", composePath, "reviewer", "--jupyter-expose", "--prompt", "inspect")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --json --jupyter-expose default cleanup code/stderr = %d / %q", exitCode, stderr)
	}
	var decoded composeRunOutput
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("decode stopped jupyter run JSON: %v\n%s", err, stdout)
	}
	if decoded.JupyterURL != "" || decoded.JupyterPath != "" {
		t.Fatalf("stopped jupyter JSON fields = %q / %q", decoded.JupyterPath, decoded.JupyterURL)
	}
}

func TestIntegrationCLIRunCommandSendsCommandAndStreamsOutput(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-command
agents:
  reviewer:
    provider: codex
`)
	var sawRequest bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				sawRequest = true
				if req.Msg.GetAgentName() != "reviewer" || req.Msg.GetCommand() != "echo command" || req.Msg.GetPrompt() != "" || req.Msg.GetTriggerId() != "" {
					t.Fatalf("RunAgentStream command request = %#v", req.Msg)
				}
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-command",
					Chunk:     "command stdout",
				}); err != nil {
					return err
				}
				if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
					RunId:     "run-command",
					Chunk:     "command stderr",
					Stream:    agentcomposev2.StdioStream_STDIO_STREAM_STDERR,
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-command",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-command",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-command",
					},
				})
			},
			runAttach: func(context.Context, *connect.BidiStream[agentcomposev2.RunAttachRequest, agentcomposev2.RunAttachResponse]) error {
				t.Fatalf("RunAttach should not be called for non-interactive run --command")
				return nil
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-command", "reviewer", "sandbox-command", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "command stdout\n")}), nil
			},
		},
	})
	defer server.Close()

	projectID, err := domain.StableProjectID("cli-run-command", composePath)
	if err != nil {
		t.Fatalf("StableProjectID returned error: %v", err)
	}
	agentID, err := domain.StableManagedAgentID(projectID, "reviewer")
	if err != nil {
		t.Fatalf("StableManagedAgentID returned error: %v", err)
	}
	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, identity.ShortID(agentID), "--command", "echo command")
	if exitCode != 0 || stderr != "command stderr\n" || stdout != "command stdout\n" {
		t.Fatalf("run --command code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if !sawRequest {
		t.Fatal("RunAgentStream was not called")
	}
}

func TestIntegrationCLIRunInteractivePromptDefaultProviderAllowed(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive-default-provider
agents:
  reviewer: {}
`)
	var sawRun bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				sawRun = true
				return stream.Send(&agentcomposev2.RunAgentStreamResponse{
					EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
					RunId:     "run-default-provider",
					Run: &agentcomposev2.RunSummary{
						RunId:     "run-default-provider",
						ProjectId: req.Msg.GetProjectId(),
						AgentName: "reviewer",
						Status:    agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED,
						SandboxId: "sandbox-default-provider",
					},
				})
			},
			getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
				return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), req.Msg.GetRunId(), "reviewer", "sandbox-default-provider", agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED, 0, "")}), nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommandWithInput("", "run", "--host", server.URL, "--file", composePath, "reviewer", "-i", "--prompt", "hello")
	if exitCode != 0 || stdout != "" || stderr != "" {
		t.Fatalf("run -i --prompt default provider code/stdout/stderr = %d / %q / %q", exitCode, stdout, stderr)
	}
	if !sawRun {
		t.Fatal("RunAgentStream was not called")
	}
}

func TestIntegrationCLIRunFailureReturnsStableExitCode(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-failure
agents:
  reviewer:
    provider: codex
`)
	server := newRunServiceStubServer(t, runServiceStub{
		runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
			if err := stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_OUTPUT,
				RunId:     "run-failed",
				Chunk:     "agent failed\n",
				Stream:    agentcomposev2.StdioStream_STDIO_STREAM_STDERR,
			}); err != nil {
				return err
			}
			return stream.Send(&agentcomposev2.RunAgentStreamResponse{
				EventType: agentcomposev2.RunAgentStreamEventType_RUN_AGENT_STREAM_EVENT_TYPE_COMPLETED,
				RunId:     "run-failed",
				Run: &agentcomposev2.RunSummary{
					RunId:     "run-failed",
					ProjectId: req.Msg.GetProjectId(),
					AgentName: "reviewer",
					Status:    agentcomposev2.RunStatus_RUN_STATUS_FAILED,
					SandboxId: "session-failed",
					ExitCode:  7,
					Error:     "agent execution failed",
				},
			})
		},
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			return connect.NewResponse(&agentcomposev2.GetRunResponse{Run: testRunDetail(req.Msg.GetProjectId(), "run-failed", "reviewer", "session-failed", agentcomposev2.RunStatus_RUN_STATUS_FAILED, 7, "agent failed\n")}), nil
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("run", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "fail")
	if exitCode != 7 {
		t.Fatalf("run failure exit code = %d, want 7; stderr=%q", exitCode, stderr)
	}
	if stdout != "" {
		t.Fatalf("run failure stdout = %q, want empty", stdout)
	}
	for _, want := range []string{"agent failed", "run-failed", "agent execution failed"} {
		if !strings.Contains(stderr, want) {
			t.Fatalf("run failure stderr %q does not contain %q", stderr, want)
		}
	}
}
