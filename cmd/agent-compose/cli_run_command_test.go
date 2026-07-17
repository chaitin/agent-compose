package main

import (
	"agent-compose/pkg/config"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // Tests the daemon's required h2c transport compatibility.
)

func TestRunHelpHidesOptionalModeFlagSentinel(t *testing.T) {
	stdout, stderr, runCount, exitCode := executeCLICommand("run", "--help")
	if exitCode != 0 || stderr != "" {
		t.Fatalf("run --help code/stderr = %d / %q", exitCode, stderr)
	}
	if runCount != 0 {
		t.Fatalf("daemon runner called %d times, want 0", runCount)
	}
	for _, unexpected := range []string{
		"agent-compose-run-mode",
		"\x00",
		`[="`,
	} {
		if strings.Contains(stdout, unexpected) {
			t.Fatalf("run --help contains %q:\n%s", unexpected, stdout)
		}
	}
	for _, want := range []string{
		"--prompt string",
		"--command string",
		"Prompt to send to the agent",
		"Bash command to execute in the agent sandbox",
	} {
		if !strings.Contains(stdout, want) {
			t.Fatalf("run --help does not contain %q:\n%s", want, stdout)
		}
	}
}

func TestResolveAgentComposeSocketForCLIFallsBackToVarRun(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")

	socketPath, err := resolveAgentComposeSocketForCLI("")
	if err != nil {
		t.Fatalf("resolveAgentComposeSocketForCLI returned error: %v", err)
	}
	if socketPath != config.DefaultAgentComposeSocketPath {
		t.Fatalf("socketPath = %q, want %q", socketPath, config.DefaultAgentComposeSocketPath)
	}
}

func TestCLIRunInputModeUsageErrors(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-input-errors
agents:
  reviewer:
    provider: codex
`)
	server := newComposeServiceStubServer(t, composeServiceStubs{
		run: runServiceStub{
			runAgentStream: func(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
				t.Fatalf("RunAgentStream should not be called for invalid input mode")
				return nil
			},
		},
	})
	defer server.Close()

	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "trigger flag unsupported",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--trigger", "nightly"},
			want: "unknown flag: --trigger",
		},
		{
			name: "legacy sandbox id flag unsupported",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--sandbox-id", "sandbox-1", "--prompt", "check"},
			want: "unknown flag: --sandbox-id",
		},
		{
			name: "legacy session flag unsupported",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--session-id", "sandbox-1", "--prompt", "check"},
			want: "unknown flag: --session-id",
		},
		{
			name: "bad driver",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--driver", "bad", "--prompt", "check"},
			want: "unsupported agent-compose runtime driver",
		},
		{
			name: "driver with sandbox id",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--sandbox", "sandbox-1", "--driver", "docker", "--prompt", "check"},
			want: "run --driver cannot be combined with --sandbox",
		},
		{
			name: "command and prompt flags",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--command", "echo hi", "--prompt", "check"},
			want: "only one of --prompt or --command",
		},
		{
			name: "prompt flag and positional trigger",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--prompt", "check", "legacy"},
			want: "run with --prompt does not accept additional positional arguments",
		},
		{
			name: "command flag and positional trigger",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--command", "echo hi", "legacy"},
			want: "run with --command does not accept additional positional arguments",
		},
		{
			name: "positional trigger rejected",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "nightly"},
			want: "does not accept positional trigger arguments",
		},
		{
			name: "multiple extra positional arguments rejected",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "nightly", "extra"},
			want: "does not accept positional trigger arguments",
		},
		{
			name: "empty command flag",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "--command", " "},
			want: "requires a non-empty command",
		},
		{
			name: "detach and interactive flags",
			args: []string{"run", "--host", server.URL, "--file", composePath, "reviewer", "-d", "-i"},
			want: "cannot be combined",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("stdout/stderr = %q / %q, want stderr containing %q", stdout, stderr, tc.want)
			}
		})
	}
}

func TestCLIRunCompletionErrorBranches(t *testing.T) {
	failed := &agentcomposev2.RunSummary{RunId: "run-fail-cleanup", Status: agentcomposev2.RunStatus_RUN_STATUS_FAILED, ExitCode: 8, Error: "agent failed"}
	detail := &agentcomposev2.RunDetail{CleanupError: "remove failed"}
	err := composeRunCompletionError("Project", "reviewer", failed, detail)
	if err == nil || commandExitCode(err) != 8 || !strings.Contains(err.Error(), "cleanup warning: remove failed") {
		t.Fatalf("failed cleanup completion error = %v code=%d", err, commandExitCode(err))
	}
	if got := runDetailCleanupError(nil); got != "" {
		t.Fatalf("nil cleanup error = %q", got)
	}
}

type runServiceStub struct {
	startRun       func(context.Context, *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error)
	runAgentStream func(context.Context, *connect.Request[agentcomposev2.RunAgentRequest], *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error
	runAttach      func(context.Context, *connect.BidiStream[agentcomposev2.RunAttachRequest, agentcomposev2.RunAttachResponse]) error
	getRun         func(context.Context, *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error)
	listRuns       func(context.Context, *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error)
	listRunEvents  func(context.Context, *connect.Request[agentcomposev2.ListRunEventsRequest]) (*connect.Response[agentcomposev2.ListRunEventsResponse], error)
	followRunLogs  func(context.Context, *connect.Request[agentcomposev2.FollowRunLogsRequest], *connect.ServerStream[agentcomposev2.RunLogChunk]) error

	agentcomposev2connect.UnimplementedRunServiceHandler
}

func (s runServiceStub) StartRun(ctx context.Context, req *connect.Request[agentcomposev2.StartRunRequest]) (*connect.Response[agentcomposev2.StartRunResponse], error) {
	if s.startRun == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("StartRun stub is not configured"))
	}
	return s.startRun(ctx, req)
}

func (s runServiceStub) GetRun(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
	if s.getRun == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetRun stub is not configured"))
	}
	return s.getRun(ctx, req)
}

func (s runServiceStub) ListRuns(ctx context.Context, req *connect.Request[agentcomposev2.ListRunsRequest]) (*connect.Response[agentcomposev2.ListRunsResponse], error) {
	if s.listRuns == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListRuns stub is not configured"))
	}
	return s.listRuns(ctx, req)
}

func (s runServiceStub) ListRunEvents(ctx context.Context, req *connect.Request[agentcomposev2.ListRunEventsRequest]) (*connect.Response[agentcomposev2.ListRunEventsResponse], error) {
	if s.listRunEvents == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListRunEvents stub is not configured"))
	}
	return s.listRunEvents(ctx, req)
}

func newRunServiceStubServer(t *testing.T, stub runServiceStub) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewRunServiceHandler(stub)
	mux.Handle(path, handler)
	return httptest.NewServer(h2c.NewHandler(mux, &http2.Server{})) //nolint:staticcheck // Tests required h2c transport compatibility.
}

func testRunDetail(projectID, runID, agentName, sessionID string, status agentcomposev2.RunStatus, exitCode int32, output string) *agentcomposev2.RunDetail {
	return &agentcomposev2.RunDetail{
		Summary: &agentcomposev2.RunSummary{
			RunId:      runID,
			ProjectId:  projectID,
			AgentName:  agentName,
			Source:     agentcomposev2.RunSource_RUN_SOURCE_MANUAL,
			Status:     status,
			SandboxId:  sessionID,
			ExitCode:   exitCode,
			StartedAt:  "2026-06-11T00:00:00Z",
			UpdatedAt:  "2026-06-11T00:00:01Z",
			DurationMs: 1000,
		},
		Prompt:       "test prompt",
		Output:       output,
		ResultJson:   "{}",
		LogsPath:     "/tmp/output.txt",
		ArtifactsDir: "/tmp/artifacts",
	}
}
