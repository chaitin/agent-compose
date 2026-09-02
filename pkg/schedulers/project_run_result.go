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
	finalText := firstNonEmpty(metadata.FinalText, run.Output)
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
		Success:       run.Status == domain.ProjectRunStatusSucceeded,
		ExitCode:      run.ExitCode,
	}, jsonErr
}

func ProjectRunResultMetadata(resultJSON string) ProjectRunResultFields {
	var metadata ProjectRunResultFields
	_ = json.Unmarshal([]byte(resultJSON), &metadata)
	return metadata
}
