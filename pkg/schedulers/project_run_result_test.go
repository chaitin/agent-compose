package schedulers

import (
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestAgentResultFromProjectRunPrefersFinalTextMetadataOverOutput(t *testing.T) {
	run := domain.ProjectRunRecord{
		RunID:      "project-run-1",
		AgentName:  "reviewer",
		Status:     domain.ProjectRunStatusSucceeded,
		Output:     "thinking...\ncat SKILL.md\n{...}",
		ResultJSON: `{"cellId":"cell-1","agent":"codex","finalText":"here is the answer"}`,
	}
	result, err := AgentResultFromProjectRun(run, "")
	if err != nil {
		t.Fatalf("AgentResultFromProjectRun returned error: %v", err)
	}
	if result.FinalText != "here is the answer" || result.Text != "here is the answer" {
		t.Fatalf("FinalText/Text = %q/%q, want the recorded assistant message", result.FinalText, result.Text)
	}
	if result.Output != run.Output {
		t.Fatalf("Output = %q, want the untouched transcript %q", result.Output, run.Output)
	}
	if result.FinalText == result.Output {
		t.Fatalf("FinalText must not equal the transcript Output when finalText metadata exists")
	}
}

func TestAgentResultFromProjectRunFallsBackToOutputWithoutFinalTextMetadata(t *testing.T) {
	run := domain.ProjectRunRecord{
		RunID:      "project-run-2",
		Status:     domain.ProjectRunStatusSucceeded,
		Output:     "plain transcript",
		ResultJSON: `{"cellId":"cell-2"}`,
	}
	result, err := AgentResultFromProjectRun(run, "")
	if err != nil {
		t.Fatalf("AgentResultFromProjectRun returned error: %v", err)
	}
	if result.FinalText != "plain transcript" || result.Output != "plain transcript" {
		t.Fatalf("FinalText/Output = %q/%q, want both to fall back to the transcript", result.FinalText, result.Output)
	}
}

func TestAgentResultFromProjectRunFallsBackToErrorWhenEmpty(t *testing.T) {
	run := domain.ProjectRunRecord{
		RunID:  "project-run-3",
		Status: domain.ProjectRunStatusFailed,
		Error:  "sandbox start failed",
	}
	result, err := AgentResultFromProjectRun(run, "")
	if err != nil {
		t.Fatalf("AgentResultFromProjectRun returned error: %v", err)
	}
	if result.Text != "sandbox start failed" {
		t.Fatalf("Text = %q, want the run error", result.Text)
	}
	if result.FinalText != "" {
		t.Fatalf("FinalText = %q, want empty when there is no output or finalText metadata", result.FinalText)
	}
}
