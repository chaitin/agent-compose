package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
)

func TestCLIHelpUsesSandboxTerminology(t *testing.T) {
	commands := [][]string{
		{"--help"},
		{"run", "--help"},
		{"logs", "--help"},
		{"exec", "--help"},
		{"inspect", "--help"},
		{"cache", "ls", "--help"},
		{"cache", "prune", "--help"},
		{"volume", "ls", "--help"},
		{"volume", "prune", "--help"},
	}
	for _, args := range commands {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(args...)
			if exitCode != 0 || stderr != "" {
				t.Fatalf("%v code/stderr = %d / %q", args, exitCode, stderr)
			}
			if strings.Contains(strings.ToLower(stdout), "session") {
				t.Fatalf("%v help contains session terminology:\n%s", args, stdout)
			}
		})
	}
}

func TestWaitForDetachedRunSandboxSlowGetRunReturnsTimeoutMessage(t *testing.T) {
	var calls int
	client := runServiceStub{
		getRun: func(ctx context.Context, req *connect.Request[agentcomposev2.GetRunRequest]) (*connect.Response[agentcomposev2.GetRunResponse], error) {
			calls++
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	run, err := waitForDetachedRunSandbox(context.Background(), client, "project-detached", "run-slow", 10*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out waiting for run run-slow to report a sandbox") {
		t.Fatalf("waitForDetachedRunSandbox err = %v", err)
	}
	if strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("waitForDetachedRunSandbox leaked context error: %v", err)
	}
	if run != nil {
		t.Fatalf("waitForDetachedRunSandbox run = %#v, want nil", run)
	}
	if calls != 1 {
		t.Fatalf("GetRun calls = %d, want 1", calls)
	}
}

func TestCLIRemoveSandboxRunningRequiresForce(t *testing.T) {
	server := newComposeServiceStubServer(t, composeServiceStubs{
		sandbox: sandboxServiceStub{
			removeSandbox: func(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
				return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("sandbox %s is running", req.Msg.GetSandboxId()))
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("rm", "--host", server.URL, "sandbox-running")
	if exitCode == 0 {
		t.Fatalf("rm running exit code = %d, want non-zero", exitCode)
	}
	if stdout != "" || !strings.Contains(stderr, "is running") {
		t.Fatalf("rm running stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestCLIRemoveSandboxUsageErrors(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("rm")
	if exitCode != exitCodeUsage {
		t.Fatalf("rm without args exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" || !strings.Contains(stderr, "requires at least 1 sandbox") {
		t.Fatalf("rm without args stdout/stderr = %q / %q", stdout, stderr)
	}

	stdout, stderr, _, exitCode = executeCLICommand("rm", " ")
	if exitCode != exitCodeUsage {
		t.Fatalf("rm empty sandbox exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" || !strings.Contains(stderr, "requires non-empty sandbox") {
		t.Fatalf("rm empty stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestCLIStopRequiresSandboxUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("stop")
	if exitCode != exitCodeUsage {
		t.Fatalf("stop without args exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("stop without args stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "requires at least 1 sandbox") {
		t.Fatalf("stop without args stderr = %q", stderr)
	}
}

func TestCLIResumeRejectsEmptySandboxUsageError(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("resume", " ")
	if exitCode != exitCodeUsage {
		t.Fatalf("resume empty sandbox exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("resume empty sandbox stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "requires non-empty sandbox") {
		t.Fatalf("resume empty sandbox stderr = %q", stderr)
	}
}

func TestCLIExecRejectsEmptySandboxUsageError(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-empty
agents:
  reviewer:
    provider: codex
`)
	stdout, stderr, _, exitCode := executeCLICommand("exec", "--file", composePath, " ", "--command", "pwd")
	if exitCode != exitCodeUsage {
		t.Fatalf("exec empty sandbox exit code = %d, want %d", exitCode, exitCodeUsage)
	}
	if stdout != "" {
		t.Fatalf("exec empty sandbox stdout = %q, want empty", stdout)
	}
	if !strings.Contains(stderr, "requires non-empty sandbox") {
		t.Fatalf("exec empty sandbox stderr = %q", stderr)
	}
}

type sandboxServiceStub struct {
	removeSandbox func(context.Context, *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error)
	getStats      func(context.Context, *connect.Request[agentcomposev2.GetSandboxStatsRequest]) (*connect.Response[agentcomposev2.GetSandboxStatsResponse], error)
	getSandbox    func(context.Context, *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error)
	listSandboxes func(context.Context, *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error)
	listHistory   func(context.Context, *connect.Request[agentcomposev2.ListSandboxHistoryRequest]) (*connect.Response[agentcomposev2.ListSandboxHistoryResponse], error)
	stopSandbox   func(context.Context, *connect.Request[agentcomposev2.StopSandboxRequest]) (*connect.Response[agentcomposev2.StopSandboxResponse], error)
	resumeSandbox func(context.Context, *connect.Request[agentcomposev2.ResumeSandboxRequest]) (*connect.Response[agentcomposev2.ResumeSandboxResponse], error)

	agentcomposev2connect.UnimplementedSandboxServiceHandler
}

func (s sandboxServiceStub) ListSandboxHistory(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxHistoryRequest]) (*connect.Response[agentcomposev2.ListSandboxHistoryResponse], error) {
	if s.listHistory == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListSandboxHistory stub is not configured"))
	}
	return s.listHistory(ctx, req)
}

func (s sandboxServiceStub) ResumeSandbox(ctx context.Context, req *connect.Request[agentcomposev2.ResumeSandboxRequest]) (*connect.Response[agentcomposev2.ResumeSandboxResponse], error) {
	if s.resumeSandbox == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ResumeSandbox stub is not configured"))
	}
	return s.resumeSandbox(ctx, req)
}

func (s sandboxServiceStub) GetSandbox(ctx context.Context, req *connect.Request[agentcomposev2.GetSandboxRequest]) (*connect.Response[agentcomposev2.GetSandboxResponse], error) {
	if s.getSandbox == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("GetSandbox stub is not configured"))
	}
	return s.getSandbox(ctx, req)
}

func (s sandboxServiceStub) ListSandboxes(ctx context.Context, req *connect.Request[agentcomposev2.ListSandboxesRequest]) (*connect.Response[agentcomposev2.ListSandboxesResponse], error) {
	if s.listSandboxes == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ListSandboxes stub is not configured"))
	}
	return s.listSandboxes(ctx, req)
}

func (s sandboxServiceStub) RemoveSandbox(ctx context.Context, req *connect.Request[agentcomposev2.RemoveSandboxRequest]) (*connect.Response[agentcomposev2.RemoveSandboxResponse], error) {
	if s.removeSandbox == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RemoveSandbox stub is not configured"))
	}
	return s.removeSandbox(ctx, req)
}

func TestE2ECLIResolveSandboxRefAfterProjectDown(t *testing.T) {
	testResolveComposeSandboxRefFromSessions(t)
}

func testCLISandboxProxy(sessionID, token string) *agentcomposev2.Sandbox {
	proxyPath := "/agent-compose/session/" + sessionID + "/lab"
	return &agentcomposev2.Sandbox{
		SandboxId:   sessionID,
		ProxyPath:   proxyPath,
		NotebookUrl: proxyPath + "?token=" + token,
		Driver:      "boxlite",
		Status:      "RUNNING",
	}
}
