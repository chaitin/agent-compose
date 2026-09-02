package schedulers

import (
	"encoding/json"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

type ProjectRunResultFields struct {
	CellID        string `json:"cellId"`
	Agent         string `json:"agent"`
	AgentThreadID string `json:"agentThreadId"`
	StopReason    string `json:"stopReason"`
	FinalText     string `json:"finalText"`
}

func AgentResultFromProjectRun(run domain.ProjectRunRecord, outputSchemaJSON string) (domain.SchedulerAgentResult, error) {
	metadata := ProjectRunResultMetadata(run.ResultJSON)
	succeeded := run.Status == domain.ProjectRunStatusSucceeded
	// metadata.FinalText may hold a synthesized failure summary (see
	// agentAssistantMessage) rather than provider text; trust it only once
	// the run actually succeeded, or a failed run's real transcript/error
	// gets displaced by that placeholder.
	finalText := run.Output
	if succeeded {
		finalText = firstNonEmpty(metadata.FinalText, run.Output)
	}
	text := firstNonEmpty(finalText, run.Error)
	jsonValue, jsonErr := JSONResult(text, outputSchemaJSON, "project run output")
	return domain.SchedulerAgentResult{
		Text:          text,
		Output:        run.Output,
		FinalText:     finalText,
		JSON:          jsonValue,
		SandboxID:     run.SandboxID,
		CellID:        metadata.CellID,
		Agent:         firstNonEmpty(metadata.Agent, run.AgentName),
		AgentThreadID: metadata.AgentThreadID,
		StopReason:    metadata.StopReason,
		Success:       succeeded,
		ExitCode:      run.ExitCode,
	}, jsonErr
}

func ProjectRunResultMetadata(resultJSON string) ProjectRunResultFields {
	var metadata ProjectRunResultFields
	_ = json.Unmarshal([]byte(resultJSON), &metadata)
	return metadata
}
