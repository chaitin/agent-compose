package runs

import (
	"encoding/json"
	"testing"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

func TestTransitionFromAgentCellRecordsFinalTextMetadata(t *testing.T) {
	run := domain.ProjectRunRecord{RunID: "run-final-text"}
	cell := domain.NotebookCell{ID: "cell-1", Output: "thinking...\ncat SKILL.md\n{...}", Success: true}

	transition := TransitionFromAgentCell(run, nil, cell, "here is the answer", nil)

	var metadata struct {
		FinalText string `json:"finalText"`
	}
	if err := json.Unmarshal([]byte(transition.ResultJSON), &metadata); err != nil {
		t.Fatalf("unmarshal ResultJSON: %v", err)
	}
	if metadata.FinalText != "here is the answer" {
		t.Fatalf("ResultJSON finalText = %q, want the assistant message", metadata.FinalText)
	}
	if transition.Output != cell.Output {
		t.Fatalf("Output = %q, want the untouched transcript %q", transition.Output, cell.Output)
	}
}
