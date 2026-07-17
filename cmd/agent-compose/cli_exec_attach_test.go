package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

func TestCommandExitErrorForConnectExplainsHTTP2AttachMismatch(t *testing.T) {
	err := commandExitErrorForConnect(fmt.Errorf("run project demo attach: unavailable: http2: failed reading the frame payload: http2: frame too large, note that the frame header looked like an HTTP/1.1 header"))
	if got := commandExitCode(err); got != exitCodeUnavailable {
		t.Fatalf("exit code = %d, want %d; err=%v", got, exitCodeUnavailable, err)
	}
	for _, want := range []string{"attach RPCs require HTTP/2 h2c", "restart the agent-compose daemon", "HTTP/1 proxy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestPromptAttachInputPromptUpdatesFromAttachMetadata(t *testing.T) {
	prompt := promptAttachInputPrompt{}
	if got, want := prompt.String(), "agent@sandbox:> "; got != want {
		t.Fatalf("empty prompt = %q, want %q", got, want)
	}

	prompt.UpdateFromStarted(&agentcomposev2.AttachStarted{
		SandboxId: "sandbox-abcdef1234567890",
		Run:       &agentcomposev2.RunSummary{AgentName: "reviewer", SandboxId: "ignored-session"},
	})
	if got, want := prompt.String(), "reviewer@sandbox-abcd:> "; got != want {
		t.Fatalf("started prompt = %q, want %q", got, want)
	}

	prompt.UpdateFromRun(&agentcomposev2.RunSummary{
		AgentName: "writer",
		SandboxId: "sandbox-fedcba9876543210",
	})
	if got, want := prompt.String(), "writer@sandbox-fedc:> "; got != want {
		t.Fatalf("run prompt = %q, want %q", got, want)
	}
}

func TestRunComposeExecPromptOnceCommand(t *testing.T) {
	stream := newFakeExecAttachStream([]*agentcomposev2.ExecAttachResponse{
		{Frame: &agentcomposev2.ExecAttachResponse_AgentEvent{AgentEvent: &agentcomposev2.AttachAgentEvent{Text: "prompt reply\n"}}},
		{Frame: &agentcomposev2.ExecAttachResponse_Result{Result: &agentcomposev2.AttachResult{Success: true, Run: &agentcomposev2.RunSummary{RunId: "run-prompt", SandboxId: "sandbox-prompt"}}}},
	})
	client := &fakeExecAttachClient{stream: stream}
	var stdout bytes.Buffer
	cmd := &cobra.Command{Use: "exec"}
	cmd.SetContext(context.Background())
	cmd.SetOut(&stdout)
	cmd.SetErr(io.Discard)
	err := runComposeExecPromptOnceCommand(cmd, "project", client, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-prompt"},
	}, composeExecOptions{Prompt: "hello"}, false)
	if err != nil {
		t.Fatalf("prompt once returned error: %v", err)
	}
	if stdout.String() != "prompt reply\n" {
		t.Fatalf("prompt once stdout = %q", stdout.String())
	}
	sent := stream.sentFrames()
	if len(sent) != 1 || sent[0].GetStart().GetPrompt() != "hello" || sent[0].GetStart().GetAttachStdin() {
		t.Fatalf("prompt once start = %#v", sent)
	}
	if !stream.closedRequest() {
		t.Fatal("prompt once request was not closed")
	}
}

func TestCLIExecInteractiveUsesExecAttachClient(t *testing.T) {
	stream := newFakeExecAttachStream([]*agentcomposev2.ExecAttachResponse{
		{Frame: &agentcomposev2.ExecAttachResponse_Output{Output: &agentcomposev2.AttachOutput{
			Data:   []byte("attach stdout\n"),
			Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT,
		}}},
		{Frame: &agentcomposev2.ExecAttachResponse_Output{Output: &agentcomposev2.AttachOutput{
			Data:   []byte("attach stderr\n"),
			Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDERR,
		}}},
		{Frame: &agentcomposev2.ExecAttachResponse_Result{Result: &agentcomposev2.AttachResult{
			Success:  true,
			ExitCode: 0,
			ExecResult: &agentcomposev2.ExecResult{
				ExecId:    "exec-attach",
				SandboxId: "sandbox-attach",
				ExitCode:  0,
				Success:   true,
			},
		}}},
	})
	client := &fakeExecAttachClient{stream: stream}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{Use: "exec"}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader("hello attach\n"))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runComposeExecAttachCommand(cmd, "cli-exec-attach", client, &agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-attach"},
		Command: &agentcomposev2.ExecCommand{Command: "cat"},
	}, composeExecOptions{Interactive: true})
	if err != nil {
		t.Fatalf("exec attach returned error: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("ExecAttach calls = %d, want 1", client.calls)
	}
	if stdout.String() != "attach stdout\n" || stderr.String() != "attach stderr\n" {
		t.Fatalf("exec attach stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
	sent := stream.sentFrames()
	if len(sent) != 3 {
		t.Fatalf("ExecAttach sent %d frames, want start/stdin/eof: %#v", len(sent), sent)
	}
	if start := sent[0].GetStart(); start == nil || !start.GetAttachStdin() || start.GetTty() || start.GetRequest().GetSandboxId() != "sandbox-attach" {
		t.Fatalf("ExecAttach start = %#v", sent[0])
	}
	if sent[0].GetStart().GetMode() != agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND {
		t.Fatalf("ExecAttach command mode = %s", sent[0].GetStart().GetMode())
	}
	if string(sent[1].GetStdin().GetData()) != "hello attach\n" {
		t.Fatalf("ExecAttach stdin = %#v", sent[1])
	}
	if sent[2].GetStdinEof() == nil || !stream.closedRequest() {
		t.Fatalf("ExecAttach eof/close eof=%#v closed=%v", sent[2], stream.closedRequest())
	}
}

func TestCLIExecPromptAttachUsesExecAttachClient(t *testing.T) {
	stream := newFakeExecAttachStream([]*agentcomposev2.ExecAttachResponse{
		{Frame: &agentcomposev2.ExecAttachResponse_AgentEvent{AgentEvent: &agentcomposev2.AttachAgentEvent{Text: "hello agent"}}},
		{Frame: &agentcomposev2.ExecAttachResponse_AgentTurnCompleted{AgentTurnCompleted: &agentcomposev2.AttachAgentTurnCompleted{RunId: "run-1"}}},
		{Frame: &agentcomposev2.ExecAttachResponse_AgentTurnCompleted{AgentTurnCompleted: &agentcomposev2.AttachAgentTurnCompleted{RunId: "run-2"}}},
		{Frame: &agentcomposev2.ExecAttachResponse_Result{Result: &agentcomposev2.AttachResult{
			Success: true,
			Run:     &agentcomposev2.RunSummary{RunId: "run-1", SandboxId: "sandbox-attach"},
		}}},
	})
	client := &fakeExecAttachClient{stream: stream}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{Use: "exec"}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(strings.Repeat("\n", 1024) + "next message\n/exit\n"))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runComposeExecPromptAttachCommand(cmd, "cli-exec-prompt", client, &agentcomposev2.ExecRequest{
		Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-attach"},
	}, composeExecOptions{Interactive: true, Prompt: "hi"})
	if err != nil {
		t.Fatalf("exec prompt attach returned error: %v", err)
	}
	if stdout.String() != "hello agent" || stderr.String() != "" {
		t.Fatalf("exec prompt stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
	sent := stream.sentFrames()
	if len(sent) != 3 {
		t.Fatalf("ExecPromptAttach sent %d frames, want start/human/eof: %#v", len(sent), sent)
	}
	start := sent[0].GetStart()
	if start == nil || start.GetMode() != agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT || start.GetPrompt() != "hi" || start.GetRequest().GetSandboxId() != "sandbox-attach" {
		t.Fatalf("ExecPromptAttach start = %#v", sent[0])
	}
	if got := sent[1].GetHumanMessage().GetText(); got != "next message" {
		t.Fatalf("ExecPromptAttach human message = %q", got)
	}
	if sent[2].GetStdinEof() == nil || !stream.closedRequest() {
		t.Fatalf("ExecPromptAttach eof/close eof=%#v closed=%v", sent[2], stream.closedRequest())
	}
}

func TestCLIExecPromptAttachDoesNotWaitForOpenStdin(t *testing.T) {
	stream := newFakeExecAttachStream(nil)
	stream.recvErr = io.EOF
	stdin, stdinWriter := io.Pipe()
	defer func() { _ = stdin.Close() }()
	defer func() { _ = stdinWriter.Close() }()

	cmd := &cobra.Command{Use: "exec"}
	cmd.SetContext(context.Background())
	cmd.SetIn(stdin)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	done := make(chan error, 1)
	go func() {
		done <- runComposeExecPromptAttachCommand(cmd, "cli-exec-prompt", &fakeExecAttachClient{stream: stream}, &agentcomposev2.ExecRequest{
			Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-attach"},
		}, composeExecOptions{Interactive: true, Prompt: "hi"})
	}()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "completed without result") {
			t.Fatalf("exec prompt attach error = %v, want missing result", err)
		}
		if err := stdinWriter.Close(); err != nil {
			t.Fatalf("close caller-owned stdin writer: %v", err)
		}
		select {
		case <-stream.closedCh:
		case <-time.After(time.Second):
			t.Fatal("prompt input pump did not close the request stream after stdin ended")
		}
	case <-time.After(time.Second):
		t.Fatal("exec prompt attach waited for stdin after the response stream completed")
	}
}

func TestCLIExecPromptAttachReceiveErrorDoesNotCloseCallerStdin(t *testing.T) {
	receiveErr := connect.NewError(connect.CodeUnavailable, errors.New("stream lost"))
	stream := newFakeExecAttachStream(nil)
	stream.recvErr = receiveErr
	stdin, stdinWriter := io.Pipe()
	defer func() { _ = stdin.Close() }()
	defer func() { _ = stdinWriter.Close() }()

	cmd := &cobra.Command{Use: "exec"}
	cmd.SetContext(context.Background())
	cmd.SetIn(stdin)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	done := make(chan error, 1)
	go func() {
		done <- runComposeExecPromptAttachCommand(cmd, "cli-exec-prompt", &fakeExecAttachClient{stream: stream}, &agentcomposev2.ExecRequest{
			Target: &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-attach"},
		}, composeExecOptions{Interactive: true, Prompt: "hi"})
	}()

	select {
	case err := <-done:
		if connect.CodeOf(err) != connect.CodeUnavailable {
			t.Fatalf("exec prompt attach error = %v, want unavailable", err)
		}
	case <-time.After(time.Second):
		t.Fatal("exec prompt attach waited for stdin after receive failed")
	}
	if _, err := stdinWriter.Write([]byte("caller still owns stdin\n")); err != nil {
		t.Fatalf("write caller-owned stdin after command returned: %v", err)
	}
	if err := stdinWriter.Close(); err != nil {
		t.Fatalf("close caller-owned stdin writer: %v", err)
	}
	select {
	case <-stream.closedCh:
	case <-time.After(time.Second):
		t.Fatal("prompt input pump did not close the request stream after stdin ended")
	}
}

func TestCLIRunInteractiveCommandUsesRunAttachClient(t *testing.T) {
	stream := newFakeRunAttachStream([]*agentcomposev2.RunAttachResponse{
		{Frame: &agentcomposev2.RunAttachResponse_Output{Output: &agentcomposev2.AttachOutput{
			Data:   []byte("attach stdout\n"),
			Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT,
		}}},
		{Frame: &agentcomposev2.RunAttachResponse_Output{Output: &agentcomposev2.AttachOutput{
			Data:   []byte("attach stderr\n"),
			Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDERR,
		}}},
		{Frame: &agentcomposev2.RunAttachResponse_Result{Result: &agentcomposev2.AttachResult{
			Success:  true,
			ExitCode: 0,
			Run:      &agentcomposev2.RunSummary{RunId: "run-attach", SandboxId: "sandbox-attach"},
		}}},
	})
	client := &fakeRunAttachClient{stream: stream}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{Use: "run"}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader("hello attach\n"))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runComposeRunAttachCommand(cmd, "cli-run-attach", client, &agentcomposev2.RunAgentRequest{
		ProjectId: "project-1",
		AgentName: "reviewer",
		Command:   "cat",
		SandboxId: "sandbox-attach",
	}, composeRunOptions{Interactive: true})
	if err != nil {
		t.Fatalf("run attach returned error: %v", err)
	}
	if client.calls != 1 {
		t.Fatalf("RunAttach calls = %d, want 1", client.calls)
	}
	if stdout.String() != "attach stdout\n" || stderr.String() != "attach stderr\n" {
		t.Fatalf("run attach stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
	sent := stream.sentFrames()
	if len(sent) != 3 {
		t.Fatalf("RunAttach sent %d frames, want start/stdin/eof: %#v", len(sent), sent)
	}
	if start := sent[0].GetStart(); start == nil || start.GetMode() != agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_COMMAND || !start.GetAttachStdin() || start.GetTty() || start.GetRequest().GetCommand() != "cat" {
		t.Fatalf("RunAttach start = %#v", sent[0])
	}
	if string(sent[1].GetStdin().GetData()) != "hello attach\n" {
		t.Fatalf("RunAttach stdin = %#v", sent[1])
	}
	if sent[2].GetStdinEof() == nil || !stream.closedRequest() {
		t.Fatalf("RunAttach eof/close eof=%#v closed=%v", sent[2], stream.closedRequest())
	}
}

func TestExecAttachResultProjectionWithoutExecResult(t *testing.T) {
	result := execResultFromAttachResult(&agentcomposev2.AttachResult{
		ExitCode: 7,
		Success:  false,
		Output:   "combined",
		Error:    "failed",
	})
	if result.GetExitCode() != 7 || result.GetSuccess() || result.GetOutput() != "combined" || result.GetError() != "failed" {
		t.Fatalf("projected exec result = %#v", result)
	}
}

type fakeRunAttachClient struct {
	stream *fakeRunAttachStream
	calls  int
}

func (c *fakeRunAttachClient) RunAttach(context.Context) runAttachStream {
	c.calls++
	return c.stream
}

type fakeRunAttachStream struct {
	mu        sync.Mutex
	sent      []*agentcomposev2.RunAttachRequest
	responses []*agentcomposev2.RunAttachResponse
	recvIndex int
	closed    bool
	closedCh  chan struct{}
}

func newFakeRunAttachStream(responses []*agentcomposev2.RunAttachResponse) *fakeRunAttachStream {
	return &fakeRunAttachStream{
		responses: responses,
		closedCh:  make(chan struct{}),
	}
}

type fakeExecAttachClient struct {
	stream *fakeExecAttachStream
	calls  int
}

func (c *fakeExecAttachClient) ExecAttach(context.Context) execAttachStream {
	c.calls++
	return c.stream
}

type fakeExecAttachStream struct {
	mu        sync.Mutex
	sent      []*agentcomposev2.ExecAttachRequest
	responses []*agentcomposev2.ExecAttachResponse
	recvErr   error
	recvIndex int
	closed    bool
	closedCh  chan struct{}
}

func newFakeExecAttachStream(responses []*agentcomposev2.ExecAttachResponse) *fakeExecAttachStream {
	return &fakeExecAttachStream{
		responses: responses,
		closedCh:  make(chan struct{}),
	}
}

func (s runServiceStub) RunAttach(ctx context.Context, stream *connect.BidiStream[agentcomposev2.RunAttachRequest, agentcomposev2.RunAttachResponse]) error {
	if s.runAttach == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RunAttach stub is not configured"))
	}
	return s.runAttach(ctx, stream)
}

func (s execServiceStub) ExecAttach(ctx context.Context, stream *connect.BidiStream[agentcomposev2.ExecAttachRequest, agentcomposev2.ExecAttachResponse]) error {
	if s.execAttach == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ExecAttach stub is not configured"))
	}
	return s.execAttach(ctx, stream)
}
