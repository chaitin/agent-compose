package runs

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestProjectAgentTerminalEventsOnlyPersistFinalMessage(t *testing.T) {
	run := domain.ProjectRunRecord{RunID: "run-agent-events", AgentName: "worker"}
	cell := domain.NotebookCell{
		Agent:      "codex",
		Output:     "checking weather\n$ curl https://weather.test\n{\"temperature\":26}\nBeijing is 26°C today.",
		Success:    true,
		StopReason: "completed",
	}
	events := projectAgentTerminalEvents(run, cell, domain.SandboxEvent{Message: "Beijing is 26°C today."}, nil)
	if len(events) != 1 {
		t.Fatalf("terminal events = %#v", events)
	}
	if events[0].Kind != domain.ProjectRunEventKindAgentMessage || events[0].Text != "Beijing is 26°C today." {
		t.Fatalf("agent message event = %#v", events[0])
	}
}

func TestFailedAgentTerminalEventsDoNotPersistTranscript(t *testing.T) {
	run := domain.ProjectRunRecord{RunID: "run-agent-failure", AgentName: "worker"}
	cell := domain.NotebookCell{Agent: "codex", Output: "$ curl https://weather.test\nrequest failed", ExitCode: 1}
	events := projectAgentTerminalEvents(run, cell, domain.SandboxEvent{Message: "codex run failed"}, nil)
	if len(events) != 0 {
		t.Fatalf("failed agent events = %#v, want none", events)
	}
}

func TestTranscriptFallbackDoesNotPersistRunEvent(t *testing.T) {
	transcript := "$ curl https://weather.test\n{\"temperature\":26}"
	events := attachedAgentTurnEvents(
		domain.ProjectRunRecord{RunID: "run-fallback", AgentName: "worker"},
		7,
		[]byte("fallback-frame"),
		agentTurnProjection{
			Transcript:      transcript,
			FinalText:       transcript,
			FinalTextSource: domain.AgentFinalTextSourceTranscriptFallback,
			Provider:        "codex",
		},
	)
	if len(events) != 0 {
		t.Fatalf("fallback events = %#v, want none", events)
	}
}

func TestProviderFinalTextPersistsAgentMessage(t *testing.T) {
	events := terminalPromptTurnEvents(
		domain.ProjectRunRecord{RunID: "run-provider-message", AgentName: "worker"},
		agentTurnProjection{
			Transcript:      "$ command\noutput\nfinal answer",
			FinalText:       "final answer",
			FinalTextSource: domain.AgentFinalTextSourceProviderMessage,
			Provider:        "codex",
		},
	)
	if len(events) != 1 || events[0].Kind != domain.ProjectRunEventKindAgentMessage || events[0].Text != "final answer" {
		t.Fatalf("provider message events = %#v", events)
	}
}
