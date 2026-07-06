package execution

import (
	"strings"
	"testing"

	domain "agent-compose/pkg/model"
)

func TestParseAgentAndCommandExecResultWorkflows(t *testing.T) {
	agentPayload := AgentResultPrefix + `{"provider":"codex","sessionId":"agent-session","stopReason":"done","finalText":"final","transcript":"transcript"}`
	agent, err := ParseAgentExecResult("codex", domain.ExecResult{Stdout: "logs\n" + agentPayload, ExitCode: 0, Success: true})
	if err != nil {
		t.Fatalf("ParseAgentExecResult returned error: %v", err)
	}
	if agent.Agent != "codex" || agent.SessionID != "agent-session" || agent.DisplayOutput != "transcript" {
		t.Fatalf("agent result = %#v", agent)
	}
	if _, err := ParseAgentExecResult("codex", domain.ExecResult{Stderr: strings.Repeat("x", 300)}); err == nil || !strings.Contains(err.Error(), "...") {
		t.Fatalf("expected summarized failure, got %v", err)
	}
	if stripped := StripAgentResultPayload("hello\n" + agentPayload); stripped != "hello\n" {
		t.Fatalf("stripped = %q", stripped)
	}
	sanitized := SanitizeAgentExecResult(domain.ExecResult{Stdout: "stdout\n" + agentPayload, Output: "output\n" + agentPayload})
	if strings.Contains(sanitized.Stdout, AgentResultPrefix) || strings.Contains(sanitized.Output, AgentResultPrefix) {
		t.Fatalf("sanitized = %#v", sanitized)
	}

	commandPayload := CommandResultPrefix + `{"stdout":"out","stderr":"err","output":"out","exitCode":7,"success":false}`
	if stripped := StripCommandResultPayload("out\n" + commandPayload); stripped != "out\n" {
		t.Fatalf("command stripped = %q", stripped)
	}
	if stripped := StripCommandResultPayload(commandPayload); stripped != "" {
		t.Fatalf("command payload stripped = %q", stripped)
	}
	command, err := ParseCommandExecResult(domain.ExecResult{Stdout: "noise\n" + commandPayload})
	if err != nil {
		t.Fatalf("ParseCommandExecResult returned error: %v", err)
	}
	if command.ExitCode != 7 || command.Stdout != "out" || command.Success {
		t.Fatalf("command result = %#v", command)
	}
	joinedCommand, err := ParseCommandExecResult(domain.ExecResult{Stdout: "no-newline" + commandPayload})
	if err != nil {
		t.Fatalf("ParseCommandExecResult joined payload returned error: %v", err)
	}
	if joinedCommand.ExitCode != 7 || joinedCommand.Stdout != "out" || joinedCommand.Success {
		t.Fatalf("joined command result = %#v", joinedCommand)
	}
	if _, err := ParseCommandExecResult(domain.ExecResult{Stdout: "noise"}); err == nil {
		t.Fatalf("expected missing command payload error")
	}
}

func TestFilterCommandStreamChunk(t *testing.T) {
	commandPayload := CommandResultPrefix + `{"stdout":"out","stderr":"","output":"out","exitCode":0,"success":true}`
	filtered, visible := FilterCommandStreamChunk(domain.ExecChunk{
		Text:   "visible\n" + commandPayload,
		Stream: domain.StdioStdout,
	})
	if !visible {
		t.Fatalf("expected command chunk to remain visible")
	}
	if filtered.Text != "visible\n" || filtered.Stream != domain.StdioStdout {
		t.Fatalf("filtered command chunk = %#v", filtered)
	}

	filtered, visible = FilterCommandStreamChunk(domain.ExecChunk{
		Text:   commandPayload,
		Stream: domain.StdioStderr,
	})
	if visible {
		t.Fatalf("payload-only command chunk should be hidden: %#v", filtered)
	}
	if filtered.Text != "" || filtered.Stream != domain.StdioStderr {
		t.Fatalf("payload-only command chunk = %#v", filtered)
	}

	filtered, visible = FilterCommandStreamChunk(domain.ExecChunk{
		Text:   "stderr transcript",
		Stream: domain.StdioStderr,
	})
	if !visible || filtered.Text != "stderr transcript" || filtered.Stream != domain.StdioStderr {
		t.Fatalf("stderr transcript command chunk = %#v visible=%v", filtered, visible)
	}

	unknownStream := domain.StdioStream("future")
	filtered, visible = FilterCommandStreamChunk(domain.ExecChunk{
		Text:   "unknown stream transcript",
		Stream: unknownStream,
	})
	if !visible || filtered.Text != "unknown stream transcript" || filtered.Stream != unknownStream {
		t.Fatalf("unknown stream command chunk = %#v visible=%v", filtered, visible)
	}
}

func TestFilterAgentStreamChunk(t *testing.T) {
	agentPayload := AgentResultPrefix + `{"provider":"codex","finalText":"done"}`
	filtered, visible := FilterAgentStreamChunk(domain.ExecChunk{
		Text:   "stdout transcript\n" + agentPayload,
		Stream: domain.StdioStdout,
	})
	if !visible {
		t.Fatalf("expected agent stdout prefix to remain visible")
	}
	if filtered.Text != "stdout transcript\n" || filtered.Stream != domain.StdioStdout {
		t.Fatalf("filtered agent stdout chunk = %#v", filtered)
	}

	filtered, visible = FilterAgentStreamChunk(domain.ExecChunk{
		Text:   agentPayload,
		Stream: domain.StdioStdout,
	})
	if visible {
		t.Fatalf("payload-only agent chunk should be hidden: %#v", filtered)
	}
	if filtered.Text != "" || filtered.Stream != domain.StdioStdout {
		t.Fatalf("payload-only agent chunk = %#v", filtered)
	}

	filtered, visible = FilterAgentStreamChunk(domain.ExecChunk{
		Text:   "stderr transcript",
		Stream: domain.StdioStderr,
	})
	if !visible || filtered.Text != "stderr transcript" || filtered.Stream != domain.StdioStderr {
		t.Fatalf("stderr transcript agent chunk = %#v visible=%v", filtered, visible)
	}

	unknownStream := domain.StdioStream("future")
	filtered, visible = FilterAgentStreamChunk(domain.ExecChunk{
		Text:   "unknown stream transcript",
		Stream: unknownStream,
	})
	if !visible || filtered.Text != "unknown stream transcript" || filtered.Stream != unknownStream {
		t.Fatalf("unknown stream agent chunk = %#v visible=%v", filtered, visible)
	}
}

func TestIntegrationParseAgentAndCommandExecResultWorkflows(t *testing.T) {
	TestParseAgentAndCommandExecResultWorkflows(t)
}

func TestE2EParseAgentAndCommandExecResultWorkflows(t *testing.T) {
	TestParseAgentAndCommandExecResultWorkflows(t)
}
