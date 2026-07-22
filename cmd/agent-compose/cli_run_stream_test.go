package main

import (
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
	"context"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"
)

func TestCLIRunInteractivePromptProviderUnsupported(t *testing.T) {
	for _, provider := range []string{"gemini"} {
		t.Run(provider, func(t *testing.T) {
			composePath := writeComposeFile(t, t.TempDir(), fmt.Sprintf(`
name: cli-run-interactive-%s
agents:
  reviewer:
    provider: %s
`, provider, provider))
			stdout, stderr, _, exitCode := executeCLICommandWithInput("hello\n", "run", "--file", composePath, "reviewer", "-i", "-t", "--prompt", "hello")
			if exitCode != exitCodeUnsupported {
				t.Fatalf("run --prompt -it %s exit code = %d, want %d; stderr=%q", provider, exitCode, exitCodeUnsupported, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, "run --prompt -it is unsupported for provider "+provider) || !strings.Contains(stderr, "supported providers: codex, claude, opencode, pi") {
				t.Fatalf("run --prompt -it %s stdout/stderr = %q / %q", provider, stdout, stderr)
			}
		})
	}
}

func TestCLIRunInteractiveUsageErrors(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-interactive
agents:
  reviewer:
    provider: codex
`)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "no mode",
			args: []string{"run", "--file", composePath, "reviewer", "-i"},
			want: "requires exactly one of --prompt or --command",
		},
		{
			name: "json",
			args: []string{"run", "--file", composePath, "--json", "reviewer", "-i", "--prompt"},
			want: "cannot be combined with --json",
		},
		{
			name: "prompt and command",
			args: []string{"run", "--file", composePath, "reviewer", "-i", "--prompt", "--command"},
			want: "requires exactly one of --prompt or --command",
		},
		{
			name: "additional positional argument",
			args: []string{"run", "--file", composePath, "reviewer", "-i", "--prompt", "first", "legacy"},
			want: "does not accept additional positional arguments",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdout, stderr, _, exitCode := executeCLICommand(tc.args...)
			if exitCode != exitCodeUsage {
				t.Fatalf("run -i exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
			}
			if stdout != "" || !strings.Contains(stderr, tc.want) {
				t.Fatalf("run -i stdout/stderr = %q / %q, want %q", stdout, stderr, tc.want)
			}
		})
	}
}

func TestCLIRunTTYRequiresInteractive(t *testing.T) {
	composePath := writeComposeFile(t, t.TempDir(), `
name: cli-run-tty
agents:
  reviewer:
    provider: codex
`)
	stdout, stderr, _, exitCode := executeCLICommand("run", "--file", composePath, "reviewer", "--command", "echo hi", "-t")
	if exitCode != exitCodeUsage {
		t.Fatalf("run -t exit code = %d, want %d; stderr=%q", exitCode, exitCodeUsage, stderr)
	}
	if stdout != "" || !strings.Contains(stderr, "requires -i/--interactive") {
		t.Fatalf("run -t stdout/stderr = %q / %q", stdout, stderr)
	}
}

func TestWriteTranscriptOrChunkRoutesTranscriptAndChunks(t *testing.T) {
	var stdout strings.Builder
	var stderr strings.Builder
	if err := writeTranscriptOrChunk(&stdout, &stderr, &agentcomposev2.TranscriptEvent{Text: "err\n", Stream: agentcomposev2.StdioStream_STDIO_STREAM_STDERR}, "ignored\n", agentcomposev2.StdioStream_STDIO_STREAM_STDOUT); err != nil {
		t.Fatalf("write transcript returned error: %v", err)
	}
	if err := writeTranscriptOrChunk(&stdout, &stderr, nil, "out\n", agentcomposev2.StdioStream_STDIO_STREAM_UNSPECIFIED); err != nil {
		t.Fatalf("write unspecified chunk returned error: %v", err)
	}
	if stdout.String() != "out\n" || stderr.String() != "err\n" {
		t.Fatalf("stdout/stderr = %q / %q", stdout.String(), stderr.String())
	}
}

func (s runServiceStub) RunAgentStream(ctx context.Context, req *connect.Request[agentcomposev2.RunAgentRequest], stream *connect.ServerStream[agentcomposev2.RunAgentStreamResponse]) error {
	if s.runAgentStream == nil {
		return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("RunAgentStream stub is not configured"))
	}
	return s.runAgentStream(ctx, req, stream)
}
