package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"agent-compose/proto/agentcompose/v2/agentcomposev2connect"
	"bytes"
	"context"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

func executeCommand(args ...string) (string, string, int, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runCount := 0
	cmd := newRootCommand(&stdout, &stderr, func(context.Context) error {
		runCount++
		return nil
	})
	if args == nil {
		args = []string{}
	}
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(context.Background())
	return stdout.String(), stderr.String(), runCount, err
}

func executeCLICommand(args ...string) (string, string, int, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runCount := 0
	exitCode := executeCLI(context.Background(), &stdout, &stderr, args, func(context.Context) error {
		runCount++
		return nil
	})
	return stdout.String(), stderr.String(), runCount, exitCode
}

func executeCLICommandWithInput(input string, args ...string) (string, string, int, int) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	runCount := 0
	cmd := newRootCommand(&stdout, &stderr, func(context.Context) error {
		runCount++
		return nil
	})
	cmd.SetIn(strings.NewReader(input))
	cmd.SetArgs(args)
	exitCode := 0
	if err := cmd.ExecuteContext(context.Background()); err != nil {
		_, _ = fmt.Fprintln(&stderr, err)
		exitCode = commandExitCode(err)
	}
	return stdout.String(), stderr.String(), runCount, exitCode
}

func TestComposeExecCommandFromPositionalArgs(t *testing.T) {
	command, err := composeExecCommandFromArgs(composeExecOptions{}, []string{"ps", "axu", "--sort=-pid"})
	if err != nil {
		t.Fatalf("composeExecCommandFromArgs returned error: %v", err)
	}
	if command.GetCommand() != "ps" || !reflect.DeepEqual(command.GetArgs(), []string{"axu", "--sort=-pid"}) {
		t.Fatalf("positional command = %#v", command)
	}
}

func TestCLIExecInteractiveReservedUnsupported(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("exec", "-t", "sandbox-1")
	if exitCode != exitCodeUsage {
		t.Fatalf("exec -t exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "exec -t/--tty requires -i/--interactive") {
		t.Fatalf("exec -t stdout/stderr = %q / %q", stdout, stderr)
	}
	stdout, stderr, _, exitCode = executeCLICommand("exec", "--json", "-i", "sandbox-1")
	if exitCode != exitCodeUsage {
		t.Fatalf("exec --json -i exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "exec --json cannot be used with -i/--interactive or -t/--tty") {
		t.Fatalf("exec --json -i stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestCLIExecInteractiveUnsupportedUsesUnsupportedExitCode(t *testing.T) {
	client := &fakeExecAttachClient{stream: &fakeExecAttachStream{
		closedCh: make(chan struct{}),
		recvErr:  connect.NewError(connect.CodeUnimplemented, fmt.Errorf("exec attach unsupported")),
	}}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{Use: "exec"}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runComposeExecAttachCommand(cmd, "cli-exec-attach", client, &agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-attach"},
		Command: &agentcomposev2.ExecCommand{Command: "sh"},
	}, composeExecOptions{Interactive: true})
	if commandExitCode(err) != exitCodeUnsupported {
		t.Fatalf("exec attach unsupported err=%v code=%d, want %d", err, commandExitCode(err), exitCodeUnsupported)
	}
	if client.calls != 1 {
		t.Fatalf("ExecAttach calls = %d, want 1", client.calls)
	}
}

func TestCLIExecTTYRequiresLocalTerminal(t *testing.T) {
	client := &fakeExecAttachClient{stream: newFakeExecAttachStream(nil)}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{Use: "exec"}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(""))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runComposeExecAttachCommand(cmd, "cli-exec-attach", client, &agentcomposev2.ExecRequest{
		Target:  &agentcomposev2.ExecRequest_SandboxId{SandboxId: "sandbox-attach"},
		Command: &agentcomposev2.ExecCommand{Command: "sh"},
	}, composeExecOptions{Interactive: true, TTY: true})
	if commandExitCode(err) != exitCodeUsage || !strings.Contains(err.Error(), "exec -t/--tty requires terminal stdin") {
		t.Fatalf("exec -it non-terminal err=%v code=%d", err, commandExitCode(err))
	}
	if client.calls != 0 {
		t.Fatalf("ExecAttach calls = %d, want 0", client.calls)
	}
}

func (s *fakeRunAttachStream) Send(req *agentcomposev2.RunAttachRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, req)
	return nil
}

func (s *fakeRunAttachStream) Receive() (*agentcomposev2.RunAttachResponse, error) {
	for {
		s.mu.Lock()
		if s.recvIndex < len(s.responses) {
			resp := s.responses[s.recvIndex]
			s.recvIndex++
			s.mu.Unlock()
			return resp, nil
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil, io.EOF
		}
		<-s.closedCh
	}
}

func (s *fakeRunAttachStream) CloseRequest() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.closedCh)
	}
	return nil
}

func (s *fakeRunAttachStream) sentFrames() []*agentcomposev2.RunAttachRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentcomposev2.RunAttachRequest(nil), s.sent...)
}

func (s *fakeRunAttachStream) closedRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *fakeExecAttachStream) Send(req *agentcomposev2.ExecAttachRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sent = append(s.sent, req)
	return nil
}

func (s *fakeExecAttachStream) Receive() (*agentcomposev2.ExecAttachResponse, error) {
	if s.recvErr != nil {
		return nil, s.recvErr
	}
	for {
		s.mu.Lock()
		if s.recvIndex < len(s.responses) {
			resp := s.responses[s.recvIndex]
			s.recvIndex++
			s.mu.Unlock()
			return resp, nil
		}
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return nil, io.EOF
		}
		<-s.closedCh
	}
}

func (s *fakeExecAttachStream) CloseRequest() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.closedCh)
	}
	return nil
}

func (s *fakeExecAttachStream) sentFrames() []*agentcomposev2.ExecAttachRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*agentcomposev2.ExecAttachRequest(nil), s.sent...)
}

func (s *fakeExecAttachStream) closedRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func TestCLIExecAgentFlagIsRemoved(t *testing.T) {
	stdout, stderr, _, exitCode := executeCLICommand("exec", "--agent", "reviewer", "bash")
	if exitCode != exitCodeUsage {
		t.Fatalf("exec --agent exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "unknown flag: --agent") {
		t.Fatalf("exec --agent stdout/stderr = %q / %q", stdout, stderr)
	}
}

type execServiceStub struct {
	exec       func(context.Context, *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error)
	execStream func(context.Context, *connect.Request[agentcomposev2.ExecRequest], *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error
	execAttach func(context.Context, *connect.BidiStream[agentcomposev2.ExecAttachRequest, agentcomposev2.ExecAttachResponse]) error

	agentcomposev2connect.UnimplementedExecServiceHandler
}

func (s execServiceStub) Exec(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest]) (*connect.Response[agentcomposev2.ExecResponse], error) {
	if s.exec == nil {
		return nil, connect.NewError(connect.CodeUnimplemented, fmt.Errorf("Exec stub is not configured"))
	}
	return s.exec(ctx, req)
}

func (s execServiceStub) ExecStream(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
	if s.execStream == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("ExecStream stub is not configured"))
	}
	return s.execStream(ctx, req, stream)
}
