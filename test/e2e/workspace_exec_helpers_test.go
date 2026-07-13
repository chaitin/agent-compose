package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

const (
	e2eGuestWorkspacePath = "/workspace"
	e2eExecTimeout        = 30 * time.Second
	e2eExecRPCTimeout     = 45 * time.Second
	e2eExecMaxOutputBytes = 64 << 10
)

func TestWorkspaceExecHelpersUseFormalConnectContract(t *testing.T) {
	capture := &e2eExecContractCapture{}
	mux := http.NewServeMux()
	servicePath, serviceHandler := agentcomposev2connect.NewExecServiceHandler(capture)
	mux.Handle(servicePath, serviceHandler)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	ctx := context.Background()
	client := newE2EExecClient(server.Client(), server.URL)
	const (
		sandboxID = "sandbox-exec-contract"
		fileName  = "nested/file name.txt"
		content   = `agent value with ' " $() characters`
	)
	writeE2EWorkspaceFile(t, ctx, client, sandboxID, fileName, content)
	removeE2EWorkspaceFile(t, ctx, client, sandboxID, fileName)
	assertE2EWorkspaceFileContent(t, ctx, client, sandboxID, fileName, content)
	assertE2EWorkspaceFilePresent(t, ctx, client, sandboxID, fileName)
	assertE2EWorkspaceFileAbsent(t, ctx, client, sandboxID, fileName)

	failure := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "false")
	if failure.GetSuccess() || failure.GetExitCode() != 7 || failure.GetStderr() == "" {
		t.Fatalf("workspace exec did not preserve non-zero result metadata: %s", e2eExecResultSummary(failure))
	}

	if len(capture.requests) != 6 {
		t.Fatalf("workspace Exec request count = %d, want 6", len(capture.requests))
	}
	writeRequest := capture.requests[0]
	writeArgs := writeRequest.GetCommand().GetArgs()
	if writeRequest.GetSandboxId() != sandboxID || writeRequest.GetCwd() != e2eGuestWorkspacePath ||
		writeRequest.GetCommand().GetCommand() != "sh" || len(writeArgs) != 6 ||
		writeArgs[3] != "workspace-write" || writeArgs[4] != fileName || writeArgs[5] != content {
		t.Fatal("workspace write did not preserve the formal Exec target, cwd, command, and positional arguments")
	}
	if strings.Contains(writeArgs[2], fileName) || strings.Contains(writeArgs[2], content) {
		t.Fatal("workspace write interpolated a dynamic value into shell source")
	}
	if summary := e2eExecResultSummary(failure); strings.Contains(summary, failure.GetStderr()) {
		t.Fatal("workspace exec diagnostic summary exposed captured output")
	}
}

type e2eExecContractCapture struct {
	agentcomposev2connect.UnimplementedExecServiceHandler
	requests []*agentcomposev2.ExecRequest
}

func (c *e2eExecContractCapture) Exec(
	_ context.Context,
	req *connect.Request[agentcomposev2.ExecRequest],
) (*connect.Response[agentcomposev2.ExecResponse], error) {
	c.requests = append(c.requests, req.Msg)
	result := &agentcomposev2.ExecResult{
		ExecId:    "exec-contract",
		SandboxId: req.Msg.GetSandboxId(),
		Command:   req.Msg.GetCommand(),
		Cwd:       req.Msg.GetCwd(),
		Success:   true,
	}
	switch req.Msg.GetCommand().GetCommand() {
	case "cat":
		result.Stdout = `agent value with ' " $() characters`
		result.Output = result.Stdout
	case "false":
		result.Success = false
		result.ExitCode = 7
		result.Stderr = "sensitive stderr must stay out of diagnostics"
		result.Output = result.Stderr
	}
	return connect.NewResponse(&agentcomposev2.ExecResponse{Result: result}), nil
}

func newE2EExecClient(httpClient *http.Client, baseURL string) agentcomposev2connect.ExecServiceClient {
	return agentcomposev2connect.NewExecServiceClient(httpClient, baseURL)
}

// runE2EWorkspaceCommand executes through the public v2 Connect API. A non-zero
// exit status is returned to the caller so tests can assert negative commands.
func runE2EWorkspaceCommand(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ExecServiceClient,
	sandboxID string,
	command string,
	args ...string,
) *agentcomposev2.ExecResult {
	t.Helper()
	sandboxID = strings.TrimSpace(sandboxID)
	command = strings.TrimSpace(command)
	if client == nil || sandboxID == "" || command == "" {
		t.Fatal("workspace exec requires a client, sandbox ID, and command")
	}

	requestCtx, cancel := context.WithTimeout(ctx, e2eExecRPCTimeout)
	defer cancel()
	response, err := client.Exec(requestCtx, connect.NewRequest(&agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: sandboxID},
		Command: &agentcomposev2.ExecCommand{
			Command: command,
			Args:    append([]string(nil), args...),
		},
		Cwd:            e2eGuestWorkspacePath,
		TimeoutMs:      uint32(e2eExecTimeout.Milliseconds()),
		MaxOutputBytes: e2eExecMaxOutputBytes,
	}))
	if err != nil {
		t.Fatalf("workspace Exec RPC failed for sandbox %s: code=%s error_type=%T", sandboxID, connect.CodeOf(err), err)
	}
	if response == nil || response.Msg == nil {
		t.Fatalf("workspace Exec RPC returned an empty response for sandbox %s", sandboxID)
	}
	result := response.Msg.GetResult()
	if result == nil {
		t.Fatalf("workspace Exec RPC returned no result for sandbox %s", sandboxID)
	}
	if result.GetExecId() == "" || result.GetSandboxId() != sandboxID || result.GetCwd() != e2eGuestWorkspacePath {
		t.Fatalf("workspace Exec RPC returned inconsistent identity for sandbox %s: %s", sandboxID, e2eExecResultSummary(result))
	}
	if result.GetCommand().GetCommand() != command || !slices.Equal(result.GetCommand().GetArgs(), args) {
		t.Fatalf("workspace Exec RPC returned inconsistent command metadata for sandbox %s: %s", sandboxID, e2eExecResultSummary(result))
	}
	return result
}

func writeE2EWorkspaceFile(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ExecServiceClient,
	sandboxID string,
	name string,
	content string,
) {
	t.Helper()
	name = cleanE2EWorkspaceRelativePath(t, name)
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "sh", "-eu", "-c", `
parent=${1%/*}
if [ "$parent" != "$1" ]; then
  mkdir -p -- "$parent"
fi
printf '%s' "$2" > "$1"
`, "workspace-write", name, content)
	requireE2EExecSuccess(t, "write workspace file", result)
}

func removeE2EWorkspaceFile(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ExecServiceClient,
	sandboxID string,
	name string,
) {
	t.Helper()
	name = cleanE2EWorkspaceRelativePath(t, name)
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "rm", "-f", "--", name)
	requireE2EExecSuccess(t, "remove workspace file", result)
}

func assertE2EWorkspaceFileContent(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ExecServiceClient,
	sandboxID string,
	name string,
	want string,
) {
	t.Helper()
	name = cleanE2EWorkspaceRelativePath(t, name)
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "cat", "--", name)
	requireE2EExecSuccess(t, "read workspace file", result)
	if result.GetStdoutTruncated() || result.GetStdout() != want {
		t.Fatalf(
			"workspace file content mismatch for sandbox %s: got_bytes=%d want_bytes=%d stdout_truncated=%t",
			sandboxID,
			len(result.GetStdout()),
			len(want),
			result.GetStdoutTruncated(),
		)
	}
}

func assertE2EWorkspaceFilePresent(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ExecServiceClient,
	sandboxID string,
	name string,
) {
	t.Helper()
	name = cleanE2EWorkspaceRelativePath(t, name)
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "test", "-f", name)
	requireE2EExecSuccess(t, "assert workspace file present", result)
}

func assertE2EWorkspaceFileAbsent(
	t *testing.T,
	ctx context.Context,
	client agentcomposev2connect.ExecServiceClient,
	sandboxID string,
	name string,
) {
	t.Helper()
	name = cleanE2EWorkspaceRelativePath(t, name)
	result := runE2EWorkspaceCommand(t, ctx, client, sandboxID, "sh", "-eu", "-c", `
if [ -e "$1" ] || [ -L "$1" ]; then
  exit 1
fi
`, "workspace-absent", name)
	requireE2EExecSuccess(t, "assert workspace file absent", result)
}

func requireE2EExecSuccess(t *testing.T, operation string, result *agentcomposev2.ExecResult) {
	t.Helper()
	if result.GetSuccess() && result.GetExitCode() == 0 && result.GetError() == "" {
		return
	}
	t.Fatalf("%s failed: %s", operation, e2eExecResultSummary(result))
}

func cleanE2EWorkspaceRelativePath(t *testing.T, name string) string {
	t.Helper()
	clean := path.Clean(name)
	if strings.TrimSpace(name) == "" || clean == "." || path.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		t.Fatal("workspace file helper requires a relative path contained by the guest workspace")
	}
	return clean
}

// e2eExecResultSummary intentionally excludes command arguments and captured
// text. File templates and daemon environments may contain credentials; byte
// counts and protocol status are enough to diagnose this deterministic helper.
func e2eExecResultSummary(result *agentcomposev2.ExecResult) string {
	if result == nil {
		return "result=nil"
	}
	return strings.Join([]string{
		"exec_id_set=" + strconv.FormatBool(result.GetExecId() != ""),
		"success=" + strconv.FormatBool(result.GetSuccess()),
		"exit_code=" + strconv.FormatInt(int64(result.GetExitCode()), 10),
		"stdout_bytes=" + strconv.Itoa(len(result.GetStdout())),
		"stderr_bytes=" + strconv.Itoa(len(result.GetStderr())),
		"output_bytes=" + strconv.Itoa(len(result.GetOutput())),
		"stdout_truncated=" + strconv.FormatBool(result.GetStdoutTruncated()),
		"stderr_truncated=" + strconv.FormatBool(result.GetStderrTruncated()),
		"output_truncated=" + strconv.FormatBool(result.GetOutputTruncated()),
		"error_set=" + strconv.FormatBool(result.GetError() != ""),
	}, " ")
}
