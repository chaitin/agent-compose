package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c" //nolint:staticcheck // h2c is required for unencrypted HTTP/2 compatibility with Connect bidi streams.

	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
	"github.com/chaitin/agent-compose/proto/agentcompose/v2/agentcomposev2connect"
)

// newAttachExecH2CServer serves ExecService over h2c, which is what the CLI
// dials for attach RPCs; Connect bidi streams need real HTTP/2.
func newAttachExecH2CServer(t *testing.T, attach func(context.Context, *connect.BidiStream[agentcomposev2.AttachExecRequest, agentcomposev2.AttachExecResponse]) error) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	path, handler := agentcomposev2connect.NewExecServiceHandler(execServiceStub{execAttach: attach})
	mux.Handle(path, handler)
	server := httptest.NewServer(h2c.NewHandler(mux, &http2.Server{})) //nolint:staticcheck // see import comment.
	t.Cleanup(server.Close)
	return server
}

// TestCLIExecPromptOnceSignalsEndOfInputOverH2C runs the real `exec --prompt`
// command against a daemon that behaves like the runtime wrapper: it answers
// the prompt and then reports its result only once the client says input has
// ended. Issue #659 is exactly this handshake going missing — the CLI used to
// half-close the request and wait forever for a result that the runtime could
// not produce.
func TestCLIExecPromptOnceSignalsEndOfInputOverH2C(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-prompt-once
agents:
  reviewer:
    provider: codex
`)
	server := newAttachExecH2CServer(t, func(_ context.Context, stream *connect.BidiStream[agentcomposev2.AttachExecRequest, agentcomposev2.AttachExecResponse]) error {
		first, err := stream.Receive()
		if err != nil {
			return err
		}
		start := first.GetStart()
		if start == nil || start.GetMode() != agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT {
			return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("first frame must be a prompt start"))
		}
		if err := stream.Send(&agentcomposev2.AttachExecResponse{Frame: &agentcomposev2.AttachExecResponse_AgentEvent{
			AgentEvent: &agentcomposev2.AttachAgentEvent{Text: "已记住验证码 KUNPENG-920。\n"},
		}}); err != nil {
			return err
		}
		// The wrapper keeps the turn open until its input ends, so the run
		// cannot be completed before the client says it is done sending.
		for {
			req, err := stream.Receive()
			if err != nil {
				if errors.Is(err, io.EOF) {
					return connect.NewError(connect.CodeDeadlineExceeded, fmt.Errorf("prompt input never ended"))
				}
				return err
			}
			if req.GetStdinEof() != nil {
				break
			}
		}
		return stream.Send(&agentcomposev2.AttachExecResponse{Frame: &agentcomposev2.AttachExecResponse_Result{
			Result: &agentcomposev2.AttachResult{
				Success: true,
				Run:     &agentcomposev2.RunSummary{RunId: "run-prompt", SandboxId: "sandbox-prompt"},
			},
		}})
	})

	stdout, stderr, _, exitCode := executeCLICommand(
		"exec", "--host", server.URL, "--file", composePath,
		"sandbox-prompt", "--prompt", "请记住验证码 KUNPENG-920，并回复已记住。",
	)

	if exitCode != 0 {
		t.Fatalf("exec --prompt exit code = %d, stderr=%q (issue #659)", exitCode, stderr)
	}
	if !strings.Contains(stdout, "已记住验证码 KUNPENG-920") {
		t.Fatalf("exec --prompt stdout = %q", stdout)
	}
}
