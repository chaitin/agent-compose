package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/spf13/cobra"
)

func TestIntegrationCLIRunPromptTTYUsesRunAttach(t *testing.T) {
	stream := newFakeRunAttachStream([]*agentcomposev2.RunAttachResponse{
		{Frame: &agentcomposev2.RunAttachResponse_AgentEvent{AgentEvent: &agentcomposev2.AttachAgentEvent{Text: "first output\n"}}},
		{Frame: &agentcomposev2.RunAttachResponse_AgentTurnCompleted{AgentTurnCompleted: &agentcomposev2.AttachAgentTurnCompleted{RunId: "run-prompt-attach"}}},
		{Frame: &agentcomposev2.RunAttachResponse_AgentEvent{AgentEvent: &agentcomposev2.AttachAgentEvent{Text: "second output\n"}}},
		{Frame: &agentcomposev2.RunAttachResponse_AgentTurnCompleted{AgentTurnCompleted: &agentcomposev2.AttachAgentTurnCompleted{RunId: "run-prompt-attach"}}},
		{Frame: &agentcomposev2.RunAttachResponse_Result{Result: &agentcomposev2.AttachResult{
			Success: true,
			Run:     &agentcomposev2.RunSummary{RunId: "run-prompt-attach", Status: agentcomposev2.RunStatus_RUN_STATUS_SUCCEEDED},
		}}},
	})
	client := &fakeRunAttachClient{stream: stream}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := &cobra.Command{Use: "run"}
	cmd.SetContext(context.Background())
	cmd.SetIn(strings.NewReader(strings.Repeat("\n", 1024) + "second prompt\n/exit\n"))
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	err := runComposeRunPromptAttachCommand(cmd, "cli-run-prompt-attach", client, &agentcomposev2.RunAgentRequest{
		ProjectId: "project-1",
		AgentName: "reviewer",
		Prompt:    "first prompt",
	})
	if err != nil {
		t.Fatalf("run prompt attach returned error: %v", err)
	}
	if stdout.String() != "first output\nsecond output\n" || stderr.String() != "" {
		t.Fatalf("run prompt attach stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
	sent := stream.sentFrames()
	if len(sent) != 3 {
		t.Fatalf("RunAttach sent %d frames, want start/human/eof: %#v", len(sent), sent)
	}
	if start := sent[0].GetStart(); start == nil || start.GetMode() != agentcomposev2.AttachRunMode_ATTACH_RUN_MODE_PROMPT || start.GetTty() || start.GetRequest().GetPrompt() != "first prompt" {
		t.Fatalf("RunAttach prompt start = %#v", sent[0])
	}
	if sent[1].GetHumanMessage().GetText() != "second prompt" {
		t.Fatalf("RunAttach human message = %#v", sent[1])
	}
	if sent[2].GetStdinEof() == nil || !stream.closedRequest() {
		t.Fatalf("RunAttach eof/close eof=%#v closed=%v", sent[2], stream.closedRequest())
	}
}

func TestIntegrationCLIExecStreamsAndSupportsJSON(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-exec-demo
agents:
  reviewer:
    provider: codex
`)
	var sawSandbox bool
	var sawCommand bool
	server := newComposeServiceStubServer(t, composeServiceStubs{
		exec: execServiceStub{
			execStream: func(ctx context.Context, req *connect.Request[agentcomposev2.ExecRequest], stream *connect.ServerStream[agentcomposev2.ExecStreamResponse]) error {
				if req.Msg.GetSandboxId() == "sandbox-exec" {
					sawSandbox = true
					if req.Msg.GetCommand().GetCommand() != "bash" || req.Msg.GetCommand().GetArgs()[0] != "-lc" {
						t.Fatalf("ExecStream sandbox request = %#v", req.Msg)
					}
				}
				if req.Msg.GetSandboxId() == "sandbox-command" {
					sawCommand = true
					if req.Msg.GetCommand().GetCommand() != "bash" || len(req.Msg.GetCommand().GetArgs()) != 2 || req.Msg.GetCommand().GetArgs()[0] != "-lc" || req.Msg.GetCommand().GetArgs()[1] != "git status --short" {
						t.Fatalf("ExecStream --command request = %#v", req.Msg)
					}
				}
				if err := stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType:  agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT,
					ExecId:     "exec-cli",
					SandboxId:  "session-exec",
					RunId:      "run-exec",
					Transcript: &agentcomposev2.TranscriptEvent{Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDOUT, Text: "exec stdout"},
				}); err != nil {
					return err
				}
				if err := stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType:  agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_OUTPUT,
					ExecId:     "exec-cli",
					SandboxId:  "session-exec",
					RunId:      "run-exec",
					Transcript: &agentcomposev2.TranscriptEvent{Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDERR, Text: "exec stderr"},
				}); err != nil {
					return err
				}
				return stream.Send(&agentcomposev2.ExecStreamResponse{
					EventType: agentcomposev2.ExecStreamEventType_EXEC_STREAM_EVENT_TYPE_COMPLETED,
					ExecId:    "exec-cli",
					SandboxId: "session-exec",
					RunId:     "run-exec",
					Result: &agentcomposev2.ExecResult{
						ExecId:    "exec-cli",
						SandboxId: "session-exec",
						RunId:     "run-exec",
						Command:   req.Msg.GetCommand(),
						Cwd:       req.Msg.GetCwd(),
						ExitCode:  0,
						Success:   true,
						Stdout:    "exec stdout\n",
						Stderr:    "exec stderr\n",
						Output:    "exec stdout\nexec stderr\n",
					},
				})
			},
			execAttach: func(context.Context, *connect.BidiStream[agentcomposev2.ExecAttachRequest, agentcomposev2.ExecAttachResponse]) error {
				t.Fatalf("ExecAttach should not be called for non-interactive exec")
				return nil
			},
		},
	})
	defer server.Close()

	stdout, stderr, _, exitCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--cwd", "/workspace", "sandbox-exec", "--command", "pwd")
	if exitCode != 0 {
		t.Fatalf("exec exit code = %d, stderr=%q", exitCode, stderr)
	}
	if stdout != "exec stdout\n" || stderr != "exec stderr\n" {
		t.Fatalf("exec stdout/stderr = %q / %q", stdout, stderr)
	}
	if !sawSandbox {
		t.Fatal("ExecStream sandbox target was not used")
	}

	commandOut, commandErr, _, commandCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "sandbox-command", "--command", "git status --short")
	if commandCode != 0 {
		t.Fatalf("exec --command exit code = %d, stderr=%q", commandCode, commandErr)
	}
	if commandOut != "exec stdout\n" || commandErr != "exec stderr\n" {
		t.Fatalf("exec --command stdout/stderr = %q / %q", commandOut, commandErr)
	}
	if !sawCommand {
		t.Fatal("ExecStream --command target was not used")
	}

	jsonOut, jsonErr, _, jsonCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--json", "session-exec", "--command", "bash")
	if jsonCode != 0 {
		t.Fatalf("exec --json code/stderr = %d / %q", jsonCode, jsonErr)
	}
	if jsonErr != "" || strings.Contains(jsonOut, "deprecated") {
		t.Fatalf("exec positional json stdout/stderr = %q / %q", jsonOut, jsonErr)
	}
	if strings.Contains(jsonErr, "exec stderr") {
		t.Fatalf("exec --json leaked transcript stdout/stderr = %q / %q", jsonOut, jsonErr)
	}
	var decoded composeExecOutput
	if err := json.Unmarshal([]byte(jsonOut), &decoded); err != nil {
		t.Fatalf("exec JSON decode failed: %v\n%s", err, jsonOut)
	}
	if decoded.ExecID != "exec-cli" || decoded.SandboxID != "session-exec" || decoded.Stdout != "exec stdout\n" || !decoded.Success {
		t.Fatalf("exec JSON = %#v", decoded)
	}

	legacyExecOut, legacyExecErr, _, legacyExecCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "--json", "--session-id", "session-exec", "--command", "printf alias")
	if legacyExecCode != exitCodeUsage || legacyExecOut != "" || !strings.Contains(legacyExecErr, "unknown flag: --session-id") {
		t.Fatalf("exec --session-id code/stdout/stderr = %d / %q / %q", legacyExecCode, legacyExecOut, legacyExecErr)
	}

	ambiguousOut, ambiguousErr, _, ambiguousCode := executeCLICommand("exec", "--host", server.URL, "--file", composePath, "sandbox-command", "--command", "pwd", "whoami")
	if ambiguousCode != exitCodeUsage {
		t.Fatalf("exec --command ambiguous exit code = %d, want %d", ambiguousCode, exitCodeUsage)
	}
	if ambiguousOut != "" || !strings.Contains(ambiguousErr, "positional command cannot be combined with --command") {
		t.Fatalf("exec --command ambiguous stdout/stderr = %q / %q", ambiguousOut, ambiguousErr)
	}
}
